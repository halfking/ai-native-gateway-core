---
archived_from: docs/2026-07-25-ab-test-quick-start.md
archived_at: 2026-08-17
archived_by: docs-archive v1.0
backup_ts: 20260817-190606
status: archived
---

> 本文档已归档，原文保持不变。

# A/B 测试快速启动指南

**日期**: 2026-07-25  
**目标**: 一键启动压力感知路由 A/B 测试

---

## 一、可用脚本

项目已经提供了两个自动化脚本：

### 1.1 `scripts/ab-test-pressure.sh` - 控制脚本

```bash
# 启用 Feature flag（开始 A/B 测试）
bash scripts/ab-test-pressure.sh enable

# 查看状态
bash scripts/ab-test-pressure.sh status

# 查看 Prometheus 指标
bash scripts/ab-test-pressure.sh metrics

# 禁用 Feature flag（停止 A/B 测试）
bash scripts/ab-test-pressure.sh disable

# 一键回滚
bash scripts/ab-test-pressure.sh rollback
```

### 1.2 `scripts/ab-test-snapshot.sh` - 数据快照

```bash
# 生成基线快照（Feature flag 关闭时）
bash scripts/ab-test-snapshot.sh baseline > baseline.json

# 生成实验快照（Feature flag 开启时）
bash scripts/ab-test-snapshot.sh experiment > experiment.json

# 对比两个快照
diff baseline.json experiment.json
```

---

## 二、A/B 测试完整流程

### 阶段 1: 基线观察（1-2 天）

```bash
# 1. 确认 Feature flag 已关闭
bash scripts/ab-test-pressure.sh status

# 2. 采集基线数据
bash scripts/ab-test-snapshot.sh baseline > baseline-day1.json

# 3. 收集 24-48 小时的数据
# （建议每小时一次）
bash scripts/ab-test-snapshot.sh baseline > baseline-day2.json
```

### 阶段 2: 启用压力感知（实验期）

```bash
# 1. 启用 Feature flag
bash scripts/ab-test-pressure.sh enable

# 2. 验证启用成功
bash scripts/ab-test-pressure.sh status

# 3. 立即采集第一份实验数据
bash scripts/ab-test-snapshot.sh experiment > experiment-start.json

# 4. 持续观察 24-48 小时
bash scripts/ab-test-snapshot.sh experiment > experiment-day2.json
```

### 阶段 3: 数据对比

```bash
# 1. 提取关键指标对比
echo "=== Baseline vs Experiment ==="
echo ""
echo "Feature Flag:"
grep pressure_aware_routing_enabled baseline.json experiment.json

echo ""
echo "Penalty Applied:"
grep penalty_applied_total baseline.json experiment.json

echo ""
echo "Pressure Distribution:"
grep pressure_penalty_value baseline.json experiment.json | head -10
```

### 阶段 4: 决策

**根据结果选择：**

```bash
# 选项 A: 效果好，保留启用
# （不需要操作，继续观察）

# 选项 B: 效果不佳，关闭 Feature flag
bash scripts/ab-test-pressure.sh disable

# 选项 C: 出现问题，立即回滚
bash scripts/ab-test-pressure.sh rollback
```

---

## 三、关键判断标准

### 3.1 必须达成

- ✅ **请求成功率**: 无下降（允许 ±0.05% 波动）
- ✅ **P95 延迟**: 增长 < 10ms
- ✅ **服务稳定性**: 无新增错误日志

### 3.2 期望达成

- ✅ **节点选择均衡度**: 提升 20%+
- ✅ **高压力节点流量**: 降低 20%+

### 3.3 一票否决

- ❌ 请求成功率下降 > 0.5%
- ❌ P95 延迟增长 > 50ms
- ❌ 新增严重错误（5xx 错误率上升）

---

## 四、监控命令速查

### 4.1 Prometheus 指标

```bash
# 直接查询
ssh root@8.136.114.245 "curl -s http://localhost:8781/metrics" | grep pressure

# 关键指标：
# - llmgw_pressure_aware_routing_enabled (Gauge): Feature flag 状态
# - llmgw_pressure_penalty_applied_total (Counter): 惩罚应用次数
# - llmgw_pressure_penalty_value (Histogram): 惩罚分布
# - llmgw_pressure_signal (Gauge): 压力信号采样
# - llmgw_weight_adjustment_total (Counter): 权重调整次数
```

### 4.2 实时日志

```bash
# 查看压力惩罚日志
ssh root@8.136.114.245 "journalctl -u llm-gateway -f | grep 'pressure penalty'"

# 典型日志：
# router: applied pressure penalty 
#   credential_id=123 
#   fp_pressure=0.85 
#   limiter_pressure=0.75 
#   penalty=0.40
```

### 4.3 Redis 状态

```bash
# 查看 FpSlots 槽位占用
ssh root@8.136.114.245 "redis-cli keys 'llmgw:cred_fp_slot:*' | wc -l"

# 查看 URSM v2 节点状态
ssh root@8.136.114.245 "redis-cli keys 'ursm:v2:*' | wc -l"
```

---

## 五、问题排查

### 5.1 Feature flag 未生效

```bash
# 检查 .env 配置
ssh root@8.136.114.245 "grep PRESSURE_AWARE_ROUTING /opt/llm-gateway-go/.env"

# 预期: PRESSURE_AWARE_ROUTING=true

# 检查启动日志
ssh root@8.136.114.245 "journalctl -u llm-gateway -n 100 | grep -E 'pressure|URSM'"

# 预期: 应看到 "pressure-aware routing enabled"
```

### 5.2 指标无数据

```bash
# 检查 Prometheus 是否正常暴露指标
ssh root@8.136.114.245 "curl -s http://localhost:8781/metrics | head -20"

# 检查所有压力相关指标
ssh root@8.136.114.245 "curl -s http://localhost:8781/metrics | grep llmgw_pressure"
```

### 5.3 紧急回滚

```bash
# 一键回滚（关闭 Feature flag）
bash scripts/ab-test-pressure.sh rollback

# 或手动回滚
ssh root@8.136.114.245 "
    sed -i '/^PRESSURE_AWARE_ROUTING=/d' /opt/llm-gateway-go/.env
    systemctl restart llm-gateway
"
```

---

## 六、文件清单

### 6.1 已创建的文件

| 文件 | 类型 | 用途 |
|------|------|------|
| `scripts/ab-test-pressure.sh` | 控制脚本 | 启用/禁用/查看/回滚 |
| `scripts/ab-test-snapshot.sh` | 采集脚本 | 生成基线/实验数据 |
| `docs/2026-07-25-phase2-ab-test-runbook.md` | 详细手册 | 完整 A/B 测试流程 |
| `docs/2026-07-25-ab-test-quick-start.md` | 本文档 | 快速启动 |

### 6.2 一键命令

```bash
# 🚀 启用 A/B 测试
bash scripts/ab-test-pressure.sh enable

# 📊 查看指标
bash scripts/ab-test-pressure.sh metrics

# 📈 采集数据快照
bash scripts/ab-test-snapshot.sh experiment > snapshot.json

# 🔍 查看状态
bash scripts/ab-test-pressure.sh status

# ⏹️ 停止 A/B 测试
bash scripts/ab-test-pressure.sh disable

# 🚨 紧急回滚
bash scripts/ab-test-pressure.sh rollback
```

---

## 七、注意事项

1. **环境要求**: 脚本默认 SSH 免密登录到 245 服务器
2. **数据采集**: 建议每小时采集一次快照
3. **持续时间**: 至少观察 24-48 小时才能得出可靠结论
4. **决策原则**: 出现任何一票否决情况，立即回滚
5. **文档同步**: 测试完成后及时更新 ARCHITECTURE.md

---

**指南状态**: ✅ 可用  
**下一步**: SSH 测试或观察 A/B 测试结果
