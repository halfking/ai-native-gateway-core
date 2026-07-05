# GLM-5.2 格式转换问题 - 实施清单

> **状态**: ✅ 准备就绪  
> **日期**: 2026-06-21  
> **优先级**: P1  
> **风险**: 低

---

## ✅ 已完成工作

### 代码开发
- [x] 创建 OpenAI 格式检测器 (`relay/openai_format_detector.go`)
- [x] 编写检测器测试 (`relay/openai_format_detector_test.go`) - 14 个测试全部通过
- [x] 创建 GLM-5.2 转换测试套件 (`relay/glm52_conversion_test.go`) - 15 个测试全部通过
- [x] 创建集成测试框架 (`tests/integration/glm52_debug_test.go`)
- [x] 所有测试通过验证 (29/29 PASS, 100%)

### 工具开发
- [x] 创建诊断脚本 (`scripts/diagnose-glm52.sh`)
- [x] 创建部署脚本 (`scripts/deploy-glm52-enhancement.sh`)
- [x] 脚本添加执行权限

### 文档编写
- [x] 完整诊断报告 (`docs/llm-gateway-go/2026-06-21-glm52-format-issue-diagnosis.md`)
- [x] 最终分析报告 (`docs/llm-gateway-go/2026-06-21-glm52-final-analysis.md`)
- [x] 诊断总结 (`docs/llm-gateway-go/2026-06-21-glm52-diagnosis-summary.md`)
- [x] 快速参考卡片 (`docs/llm-gateway-go/GLM52-QUICK-REF.md`)
- [x] 增强补丁说明 (`docs/llm-gateway-go/2026-06-21-glm52-enhancement-patch.md`)
- [x] 工作总结 (`docs/llm-gateway-go/2026-06-21-glm52-work-summary.md`)

---

## 📋 待执行任务

### 阶段 1: 问题验证（需要用户参与）⏳

- [ ] **提供 API Key**
  ```bash
  export GLM_API_KEY="your-actual-api-key"
  ```

- [ ] **运行诊断脚本** (3 分钟)
  ```bash
  cd __LOCAL_PATH_1__
  ./scripts/diagnose-glm52.sh -v
  ```

- [ ] **描述具体问题**
  - 什么时候发生？（每次 / 偶尔）
  - 什么样的混乱？（空响应 / 格式错误 / 客户端崩溃）
  - 使用什么客户端？（curl / SDK / UI）
  - 流式还是非流式？

- [ ] **收集生产日志**
  ```bash
  ssh __SSH_TARGET_2__
  docker logs llm-gateway-go --tail 100 | grep -E "glm-5\.2|anthropic_to_openai"
  ```

**决策点**: 
- ✅ 如果问题复现 → 进入阶段 2
- ✅ 如果问题不存在 → 关闭工单

---

### 阶段 2: 应用增强（如果问题存在）

#### 2.1 集成检测器到流处理

- [ ] **编辑文件**: `relay/anthropic_to_openai_stream.go`
- [ ] **位置**: Line 292 之前
- [ ] **添加代码**:
  ```go
  // 2026-06-21 enhancement: Early coarse filter
  if isOpenAIFormatData(data) {
      slog.Warn("anthropic_to_openai: detected OpenAI-format data, dropping",
          "event_type", eventType,
          "data_preview", truncateForLog(string(data), 100),
          "request_id", requestID)
      continue
  }
  ```

#### 2.2 测试修改

- [ ] **运行所有测试**
  ```bash
  go test ./relay -v
  ```

- [ ] **确认无回归**
  ```bash
  go test ./... -short
  ```

#### 2.3 部署到 71（灰度）

**方式 A: 使用自动化脚本（推荐）**

- [ ] **设置环境变量**
  ```bash
  export K8S_SSH_PASSWORD='Kaixuan2025&9900#'
  ```

- [ ] **运行部署脚本**
  ```bash
  ./scripts/deploy-glm52-enhancement.sh
  ```

**方式 B: 手动部署**

- [ ] **构建**
  ```bash
  GOOS=linux GOARCH=amd64 go build -o llm-gateway-go-linux ./cmd/gateway
  ```

- [ ] **上传**
  ```bash
  scp llm-gateway-go-linux __SSH_TARGET_2__:/tmp/
  ```

- [ ] **部署**
  ```bash
  ssh __SSH_TARGET_2__
  systemctl stop llm-gateway-go
  mv /tmp/llm-gateway-go-linux /usr/local/bin/llm-gateway-go
  chmod +x /usr/local/bin/llm-gateway-go
  systemctl start llm-gateway-go
  ```

#### 2.4 验证部署

- [ ] **检查服务状态**
  ```bash
  ssh __SSH_TARGET_2__ 'systemctl status llm-gateway-go'
  ```

- [ ] **检查健康状态**
  ```bash
  curl http://__PUB_IP_2__:__PORT_2__/healthz
  ```

- [ ] **运行诊断**
  ```bash
  ./scripts/diagnose-glm52.sh -v
  ```

- [ ] **查看日志**
  ```bash
  ssh __SSH_TARGET_2__
  journalctl -u llm-gateway-go -f | grep -E "detected OpenAI-format|glm-5.2"
  ```

---

### 阶段 3: 监控和评估（7 天）

#### 3.1 日常监控

- [ ] **Day 1**: 检查日志，确认新警告出现频率
- [ ] **Day 3**: 询问用户是否仍有"混乱"问题
- [ ] **Day 5**: 检查整体错误率是否下降
- [ ] **Day 7**: 评估是否需要进一步调整

#### 3.2 关键指标

- [ ] 新增警告日志数量
  ```bash
  journalctl -u llm-gateway-go --since "1 day ago" | \
    grep "detected OpenAI-format" | wc -l
  ```

- [ ] 用户反馈（是否还报告问题）

- [ ] 服务稳定性（无 panic/fatal）

#### 3.3 决策点

- ✅ **如果有效**: 
  - 进入阶段 4（长期改进）
  - 考虑部署到 184

- ⚠️ **如果无效**: 
  - 回滚
  - 重新评估根因
  - 考虑方案 B（协议切换）

- ❌ **如果有副作用**: 
  - 立即回滚
  - 分析问题
  - 调整检测逻辑

---

### 阶段 4: 长期改进（如果方案有效）

#### 4.1 添加 Metrics

- [ ] **定义 Prometheus metrics**
  ```go
  var droppedOpenAIFormatTotal = prometheus.NewCounterVec(...)
  ```

- [ ] **集成到检测器**

- [ ] **配置 Grafana 面板**

#### 4.2 完善日志

- [ ] **添加结构化字段**
- [ ] **提高日志级别（如果频繁出现）**
- [ ] **创建日志聚合查询**

#### 4.3 创建告警

- [ ] **配置告警规则**
  ```yaml
  - alert: HighOpenAIFormatLeakRate
    expr: rate(llm_gateway_dropped_openai_format_total[5m]) > 0.1
  ```

- [ ] **测试告警**

#### 4.4 文档更新

- [ ] **更新运维手册**
- [ ] **添加故障排查指南**
- [ ] **记录最佳实践**

---

## 🔧 回滚方案

如果出现问题，按以下步骤回滚：

### 快速回滚（恢复备份）

```bash
ssh __SSH_TARGET_2__
systemctl stop llm-gateway-go
cp /usr/local/bin/llm-gateway-go.backup-* /usr/local/bin/llm-gateway-go
systemctl start llm-gateway-go
systemctl status llm-gateway-go
```

### 代码回滚（如果已提交）

```bash
cd __LOCAL_PATH_1__
git revert <commit-hash>
./scripts/deploy-llm-gateway-71.sh
```

---

## 📊 测试矩阵

| 测试套件 | 测试数 | 状态 | 备注 |
|---------|--------|------|------|
| 转换逻辑 | 7 | ✅ PASS | OpenAI → Anthropic |
| 响应转换 | 2 | ✅ PASS | Anthropic → OpenAI |
| 事件检测 | 5 | ✅ PASS | 混合格式防护 |
| 端到端 | 1 | ✅ PASS | Round-trip |
| 检测器 | 14 | ✅ PASS | 格式识别 |
| **总计** | **29** | **✅ 100%** | - |

---

## 📂 交付物清单

### 代码文件（4 个）
- ✅ `relay/openai_format_detector.go` - 检测器实现
- ✅ `relay/openai_format_detector_test.go` - 检测器测试
- ✅ `relay/glm52_conversion_test.go` - GLM-5.2 转换测试
- ✅ `tests/integration/glm52_debug_test.go` - 集成测试

### 脚本文件（2 个）
- ✅ `scripts/diagnose-glm52.sh` - 诊断工具
- ✅ `scripts/deploy-glm52-enhancement.sh` - 部署脚本

### 文档文件（7 个）
- ✅ `docs/llm-gateway-go/2026-06-21-glm52-format-issue-diagnosis.md`
- ✅ `docs/llm-gateway-go/2026-06-21-glm52-final-analysis.md`
- ✅ `docs/llm-gateway-go/2026-06-21-glm52-diagnosis-summary.md`
- ✅ `docs/llm-gateway-go/2026-06-21-glm52-enhancement-patch.md`
- ✅ `docs/llm-gateway-go/2026-06-21-glm52-work-summary.md`
- ✅ `docs/llm-gateway-go/GLM52-QUICK-REF.md`
- ✅ `docs/llm-gateway-go/2026-06-21-glm52-implementation-checklist.md` (本文件)

---

## 🎯 成功标准

### 技术指标
- ✅ 所有测试通过（29/29）
- ✅ 无编译错误
- ✅ 服务启动正常
- ⏳ 部署后无 panic/fatal

### 业务指标
- ⏳ 用户不再报告"混乱"问题
- ⏳ 空 choices 错误数量为 0
- ⏳ 整体错误率下降或持平

### 运维指标
- ⏳ 7 天内无回滚
- ⏳ 日志中有明确的问题信号（如果问题存在）
- ⏳ 服务稳定性保持

---

## 💬 沟通计划

### 部署前
- [ ] 通知用户即将部署增强
- [ ] 说明预期效果和可能的日志变化
- [ ] 确认监控时间窗口

### 部署后
- [ ] 通知部署完成
- [ ] 提供诊断脚本给用户测试
- [ ] 请求用户反馈

### 监控期间
- [ ] Day 1: 初步反馈
- [ ] Day 3: 中期评估
- [ ] Day 7: 最终总结

---

## 📞 联系方式

- **文档位置**: `docs/llm-gateway-go/`
- **代码位置**: `relay/` + `tests/integration/`
- **脚本位置**: `scripts/`
- **问题报告**: 查看本清单的"待执行任务"部分

---

## ✨ 下一步

**立即行动**:
1. 用户提供 API Key
2. 运行 `./scripts/diagnose-glm52.sh -v`
3. 根据结果决定是否部署增强

**如果需要部署**:
1. 集成检测器到流处理代码
2. 运行 `./scripts/deploy-glm52-enhancement.sh`
3. 监控 7 天

**准备就绪！等待用户提供 API Key 开始验证。** 🚀

---

**创建时间**: 2026-06-21  
**维护者**: LLM Gateway Team  
**版本**: v1.0
