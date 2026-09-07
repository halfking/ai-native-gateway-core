# ML 路由模型回滚运行手册

**文档版本**: 1.0  
**生效日期**: 2026-09-07  
**适用范围**: llm-gateway-go P2.5 ONNX 推理集成

---

## 一、概述

本手册涵盖 ML 路由优化器（MLSelector + ONNX 推理）的灰度放量、故障诊断与回滚操作。

**关键组件**:
- Go 端 ONNX 推理：`routingopt/ml_selector.go`
- 模型清单契约：`routingopt/testdata/ml_fixture/manifest.json`
- 训练管道：`ml-training/`
- 特征开关：`ROUTING_ML_*` 环境变量

**默认状态**: 所有 ML 特性默认关闭（保守降级），必须显式启用。

---

##二、灰度放量操作

### 2.1 前置检查清单

在启用 ML 路由前，确认以下条件：

- [ ] ONNX Runtime 库已安装并可被 Go 进程加载
- [ ] 模型文件（`.onnx` + `manifest.json`）已部署到目标路径
- [ ] `ROUTING_ML_MODEL_PATH` 环境变量指向正确的模型目录
- [ ] 监控系统已配置 `ml_selector_metric` 日志采集
- [ ] 回滚窗口内有足够的流量日志用于 A/B 对比

### 2.2 分阶段放量

#### 阶段 1: 影子模式（0% 实际流量）

```bash
# 启用 ML 但不影响实际路由决策
export ROUTING_ML_ENABLE=true
export ROUTING_ML_SHADOW_MODE=true
export ROUTING_ML_MODEL_PATH=/data/ml-models/v1.0.0
```

**观察指标**:
- `ml_selector_metric` 日志中 `status=success` 的比率（目标 >99%）
- `inference_duration_ms` 延迟分布（目标 P95 <50ms）
- 无 ONNX 加载/推理错误日志

**持续时间**: 24小时

---

#### 阶段 2: 小流量验证（10% 流量）

```bash
export ROUTING_ML_ENABLE=true
export ROUTING_ML_SHADOW_MODE=false
export ROUTING_ML_TRAFFIC_PERCENT=10
export ROUTING_ML_MODEL_PATH=/data/ml-models/v1.0.0
```

**观察指标**:
- 对比 ML 路由 vs 规则引擎的成功率、延迟、成本
- 监控 `routing_decision_log` 表中 `ml_score` 分布
- 检查异常模型选择（如冷门模型突然高流量）

**持续时间**: 48小时

---

#### 阶段 3: 全量放开（100% 流量）

```bash
export ROUTING_ML_ENABLE=true
export ROUTING_ML_SHADOW_MODE=false
export ROUTING_ML_TRAFFIC_PERCENT=100
export ROUTING_ML_MODEL_PATH=/data/ml-models/v1.0.0
```

**观察指标**: 同阶段 2，持续监控 7 天。

---

### 2.3 灰度配置参数说明

| 环境变量 | 默认值 | 说明 |
|---------|--------|------|
| `ROUTING_ML_ENABLE` | `false` | 主开关：启用 ONNX 推理 |
| `ROUTING_ML_SHADOW_MODE` | `false` | 影子模式：推理但不影响路由 |
| `ROUTING_ML_TRAFFIC_PERCENT` | `0` | 实际使用 ML 的流量百分比 (0-100) |
| `ROUTING_ML_MODEL_PATH` | `""` | 模型目录（含 manifest.json） |
| `ROUTING_ML_FALLBACK_TO_RULES` | `true` | 推理失败时回退到规则引擎 |
| `ROUTING_ML_MAX_INFERENCE_MS` | `100` | 推理超时（毫秒），超时则降级 |

---

## 三、故障诊断

### 3.1 常见故障模式

#### 故障 A: ONNX 模型加载失败

**症状**:
```
ERROR ml_selector initialization failed error="onnx: LoadModel failed"
```

**诊断步骤**:
1. 检查 `ROUTING_ML_MODEL_PATH` 是否指向有效目录
2. 验证 `manifest.json` 格式与 `MLManifest` 结构体匹配
3. 确认 `.onnx` 文件路径正确且可读
4. 检查 ONNX Runtime 库版本兼容性（要求 ≥1.29.0）

**临时缓解**: 设置 `ROUTING_ML_ENABLE=false` 回退到规则引擎。

---

#### 故障 B: 推理延迟过高

**症状**: `inference_duration_ms` P95 >100ms，影响整体请求延迟。

**诊断步骤**:
1. 检查模型复杂度（`manifest.json` 中的 `model_type`）
2. 查看 CPU 资源是否充足（ONNX 推理为 CPU 密集型）
3. 分析特征工程耗时（`PreprocessFeatures` 日志）

**临时缓解**: 降低 `ROUTING_ML_TRAFFIC_PERCENT` 或设置 `ROUTING_ML_MAX_INFERENCE_MS=50` 强制降级。

---

#### 故障 C: 模型选择异常

**症状**: 特定模型突然流量激增或冷门模型被频繁选中。

**诊断步骤**:
1. 对比 `routing_decision_log` 表中 ML vs 规则引擎的选择分布
2. 检查训练数据标签分布是否偏斜
3. 查看 `ml_score` 置信度（低置信度应触发降级）

**临时缓解**: 启用影子模式（`ROUTING_ML_SHADOW_MODE=true`）进行诊断。

---

### 3.2 关键日志位置

- **ML 推理日志**: `grep "ml_selector_metric" /var/log/gateway.log`
- **路由决策日志**: `SELECT * FROM routing_decision_log WHERE ml_score IS NOT NULL ORDER BY ts DESC LIMIT 100`
- **降级触发日志**: `grep "ml inference fallback" /var/log/gateway.log`

---

## 四、回滚操作

### 4.1 快速回滚（紧急情况）

**适用场景**: ML 推理导致严重故障（如高错误率、服务不可用）。

**操作步骤**:

```bash
# 1. 立即禁用 ML 路由
export ROUTING_ML_ENABLE=false

# 2. 重启网关实例（或等待配置热重载）
systemctl restart llm-gateway

# 3. 验证回滚生效
curl -s http://localhost:8080/health | jq '.ml_routing_enabled'
# 应返回 false
```

**预期效果**: 所有流量立即回退到规则引擎路由，延迟 <1s。

---

### 4.2 分阶段回滚（非紧急情况）

**适用场景**: ML 性能不符合预期，但无紧急故障。

**操作步骤**:

```bash
# 阶段 1: 降低流量比例到 10%
export ROUTING_ML_TRAFFIC_PERCENT=10

# 阶段 2: 切换到影子模式
export ROUTING_ML_SHADOW_MODE=true

# 阶段 3: 完全禁用
export ROUTING_ML_ENABLE=false
```

每个阶段观察 1-2 小时后再进行下一步。

---

### 4.3 模型版本回滚

**适用场景**: 新版本模型表现劣化，需回退到上一版本。

**操作步骤**:

```bash
# 1. 切换模型路径到上一版本
export ROUTING_ML_MODEL_PATH=/data/ml-models/v0.9.0

# 2. 热重载配置（如支持）或重启
systemctl reload llm-gateway

# 3. 验证模型版本
grep "ml_model_version" /var/log/gateway.log | tail -1
```

**模型版本管理建议**:
- 保留最近 3 个生产版本的模型文件
- 模型目录命名包含版本号和训练日期（如 `v1.0.0-20260907`）
- 通过符号链接管理当前生产版本

---

### 4.4 回滚验证清单

回滚完成后，验证以下指标恢复正常：

- [ ] 路由决策延迟 P95 恢复到 <50ms（无 ML 推理开销）
- [ ] 错误率恢复到基线水平
- [ ] `routing_decision_log` 表中 `ml_score` 列为 NULL
- [ ] 日志中无 ONNX 相关错误

---

## 五、模型版本管理

### 5.1 版本命名规范

```
/data/ml-models/
├── v1.0.0-20260907/          # 生产当前版本
│   ├── manifest.json
│   ├── model.onnx
│   └── label_encoder.json
├── v0.9.0-20260825/          # 上一版本（保留）
└── current -> v1.0.0-20260907  # 符号链接
```

### 5.2 部署新模型版本

```bash
# 1. 上传新模型到独立目录
scp -r ml-models/v1.1.0-20260915/ gateway-prod:/data/ml-models/

# 2. 验证模型文件完整性
cd /data/ml-models/v1.1.0-20260915
sha256sum -c checksums.txt

# 3. 在测试环境验证
export ROUTING_ML_MODEL_PATH=/data/ml-models/v1.1.0-20260915
./test-ml-inference.sh

# 4. 更新符号链接
ln -sfn v1.1.0-20260915 /data/ml-models/current

# 5. 按灰度流程放量（见§二）
```

---

## 六、监控与告警

### 6.1 关键指标

| 指标 | 告警阈值 | 说明 |
|------|---------|------|
| ML 推理成功率 | <95% | ONNX 推理失败率过高 |
| 推理延迟 P95 | >100ms | 可能拖慢整体响应 |
| 降级触发率 | >10% | 频繁回退到规则引擎 |
| 模型加载失败 | >0 | 启动时加载失败 |

### 6.2 推荐告警规则

```yaml
# Prometheus 规则示例
groups:
  - name: ml_routing
    rules:
      - alert: MLInferenceHighFailureRate
        expr: rate(ml_selector_metric{status="failed"}[5m]) > 0.05
        for: 5m
        annotations:
          summary: "ML 推理失败率过高"
          
      - alert: MLInferenceHighLatency
        expr: histogram_quantile(0.95, ml_inference_duration_ms) > 100
        for: 10m
        annotations:
          summary: "ML 推理延迟过高"
```

---

## 七、已知限制与注意事项

1. **特征工程同步**: Go 端特征处理必须与训练管道 `data_loader.py` 的 `normalize_features` 保持一致，否则推理结果不可靠。

2. **标签空间管理**: 模型只能预测训练时见过的标签。新增模型需重新训练并部署新版本。

3. **无自动更新**: 模型不会自动更新，需手动部署新版本。考虑在 CI/CD 中集成定期重训练。

4. **CPU 密集型**: ONNX 推理为 CPU 操作，高 QPS 场景需确保 CPU 资源充足。

5. **无 GPU 加速**: 当前实现未启用 GPU 后端，复杂模型可能延迟较高。

---

## 八、联系人与升级路径

- **一线支持**: 运维团队（查看日志、执行回滚）
- **二线支持**: 后端团队（代码级问题、配置调整）
- **ML 模型问题**: ML 工程团队（重新训练、特征工程调整）

**升级决策树**:
- 推理失败率 >50% → 立即回滚
- 延迟 P95 >200ms → 降低流量比例
- 模型选择异常但可用 → 切换影子模式诊断

---

## 附录 A: 快速命令参考

```bash
# 查看当前 ML 配置
env | grep ROUTING_ML

# 禁用 ML 路由
export ROUTING_ML_ENABLE=false && systemctl restart llm-gateway

# 查看最近 100 条 ML 推理日志
grep "ml_selector_metric" /var/log/gateway.log | tail -100

# 验证模型文件
ls -lh /data/ml-models/current/

# 对比 ML vs 规则引擎路由分布
psql -c "SELECT chosen_model, COUNT(*) FROM routing_decision_log WHERE ts > NOW() - INTERVAL '1 hour' GROUP BY chosen_model ORDER BY COUNT(*) DESC LIMIT 20"
```

---

**文档变更历史**:
- 2026-09-07: 初版，覆盖 P2.5 ONNX 集成（0a92c36bb 提交后）
