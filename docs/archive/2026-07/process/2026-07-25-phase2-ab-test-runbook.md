---
archived_from: docs/2026-07-25-phase2-ab-test-runbook.md
archived_at: 2026-08-17
archived_by: docs-archive v1.0
backup_ts: 20260817-190606
status: archived
---

> 本文档已归档，原文保持不变。

# Phase 2 A/B 测试运行手册

**日期**: 2026-07-25  
**测试目标**: 验证压力感知路由的业务价值  
**当前状态**: Phase 2 已部署到 245 (seq=1379)

---

## 一、A/B 测试概述

### 1.1 测试目标

验证压力感知路由能够：
1. 更均衡地分配流量到多个节点
2. 降低高压力节点的负载
3. 不显著影响 P95 延迟
4. 不增加请求失败率

### 1.2 测试架构

```
                  ┌─────────────────┐
                  │  245 测试服务器   │
                  │  (8.136.114.245) │
                  └────────┬────────┘
                           │
              ┌────────────┴────────────┐
              │                         │
        Phase 1 基线              Phase 2 启用
   PRESSURE_AWARE_ROUTING   PRESSURE_AWARE_ROUTING
        = false                 = true
              │                         │
              └────────────┬────────────┘
                           │
                    对比指标变化
```

### 1.3 测试时间线

| 阶段 | 时间 | 操作 |
|------|------|------|
| 基线观察 | Day 1-2 | Feature flag = false，收集基线指标 |
| 启用压力感知 | Day 3 | 设置 PRESSURE_AWARE_ROUTING=true |
| 对比观察 | Day 4-5 | 收集新指标，对比差异 |
| 数据分析 | Day 6 | 分析数据，决定是否推广 |
| 报告输出 | Day 7 | 输出 A/B 测试报告 |

---

## 二、操作步骤

### 2.1 阶段 1: 基线观察（Day 1-2）

#### 步骤 1: 确认当前部署版本

```bash
# 查看 245 服务器当前状态
ssh root@8.136.114.245 "cat /opt/llm-gateway-go/version.json | jq .version"
# 或
bash scripts/deploy-seamless.sh status 245
```

**预期**: 当前应运行 v2.4.8-7b443600 (Phase 2 + 审计修复)

#### 步骤 2: 确认 Feature flag 状态

```bash
# 检查环境变量
ssh root@8.136.114.245 "grep PRESSURE_AWARE_ROUTING /opt/llm-gateway-go/.env"
# 预期: 未设置或 =false
```

#### 步骤 3: 收集基线指标

记录以下指标作为基线（Feature flag = false）：

**关键指标**:
```bash
# 路由决策耗时 P95
watch -n 5 "curl -s http://245:8781/metrics | grep router_decision_duration_seconds"

# 节点选择分布（每个 credential 被选中的次数）
ssh root@8.136.114.245 "redis-cli -n 0 keys 'ursm:v2:*' | xargs redis-cli -n 0 get | head -20"

# 请求成功率
curl -s http://245:8781/api/system/health

# 全局请求统计
curl -s http://245:8781/api/admin/stats
```

**观察期**: 24-48 小时

---

### 2.2 阶段 2: 启用压力感知（Day 3）

#### 步骤 1: 启用 Feature flag

```bash
# 方法 1: 修改环境变量并重启
ssh root@8.136.114.245
echo "PRESSURE_AWARE_ROUTING=true" >> /opt/llm-gateway-go/.env
systemctl restart llm-gateway

# 方法 2: 临时启用（不持久化）
ssh root@8.136.114.245
export PRESSURE_AWARE_ROUTING=true
systemctl restart llm-gateway
```

#### 步骤 2: 验证启用成功

```bash
# 检查服务状态
curl -s http://245:8781/api/system/health

# 查看启动日志
ssh root@8.136.114.245 "journalctl -u llm-gateway -n 50 | grep 'pressure-aware'"
# 预期: 应看到 "pressure-aware routing enabled"
```

#### 步骤 3: 观察日志

```bash
# 实时查看压力惩罚日志
ssh root@8.136.114.245 "journalctl -u llm-gateway -f | grep 'pressure penalty'"
# 应看到类似:
# router: applied pressure penalty 
#   credential_id=123 
#   fp_pressure=0.85 
#   limiter_pressure=0.75 
#   penalty=0.40
```

---

### 2.3 阶段 3: 对比观察（Day 4-5）

收集以下指标并与基线对比：

#### 指标 1: 节点选择分布

**观察内容**: 各 credential 的流量分布是否更均衡

**获取方法**:
```bash
# 通过 Redis 查看 NodeView 选择次数
ssh root@8.136.114.245 "redis-cli -n 0 --scan --pattern 'ursm:v2:*' | head -10"
```

**判断标准**:
- ✅ 优秀: 高压力节点的流量降低 20-30%
- ⚠️ 一般: 流量分布略有改善
- ❌ 失败: 流量分布无变化或恶化

#### 指标 2: 路由决策耗时

**观察内容**: P95 延迟是否显著上升

**获取方法**:
```bash
# 查看 Prometheus 指标
curl -s http://245:8781/metrics | grep router_decision_duration_seconds
```

**判断标准**:
- ✅ 优秀: P95 增加 < 5ms
- ⚠️ 一般: P95 增加 5-10ms
- ❌ 失败: P95 增加 > 10ms

#### 指标 3: 请求成功率

**观察内容**: 是否有节点因压力调整导致请求失败

**获取方法**:
```bash
curl -s http://245:8781/api/admin/stats
# 或查看 request_logs 表
```

**判断标准**:
- ✅ 优秀: 成功率无变化或略有提升
- ⚠️ 一般: 成功率下降 < 0.1%
- ❌ 失败: 成功率下降 > 0.1%

#### 指标 4: 资源利用

**观察内容**: FpSlots/Limiter 是否更均匀使用

**获取方法**:
```bash
# 查看 Limiter 状态
ssh root@8.136.114.245 "cat /tmp/limiter-stats.json"

# 查看 FpSlots 占用
ssh root@8.136.114.245 "redis-cli -n 0 keys 'llmgw:cred_fp_slot:*' | wc -l"
```

**判断标准**:
- ✅ 优秀: 资源利用率提升，无单点饱和
- ⚠️ 一般: 资源利用略有改善
- ❌ 失败: 资源利用无变化

---

### 2.4 阶段 4: 数据分析（Day 6）

#### 分析报告模板

```markdown
## A/B 测试结果报告

### 测试时间
- 基线期: YYYY-MM-DD ~ YYYY-MM-DD
- 实验期: YYYY-MM-DD ~ YYYY-MM-DD

### 关键指标对比

| 指标 | 基线 | 实验期 | 变化 | 结论 |
|------|------|--------|------|------|
| 节点选择均衡度 | X.X | Y.Y | +Z% | ✅/⚠️/❌ |
| 路由决策耗时 P95 | Xms | Yms | +Zms | ✅/⚠️/❌ |
| 请求成功率 | X.X% | Y.Y% | +Z% | ✅/⚠️/❌ |
| 资源利用率 | X% | Y% | +Z% | ✅/⚠️/❌ |

### 总体结论
✅ 建议推广 / ⚠️ 需调整参数 / ❌ 回滚
```

---

## 三、回退方案

### 3.1 紧急回退

如果发现严重问题：

```bash
# 方法 1: 关闭 Feature flag
ssh root@8.136.114.245
export PRESSURE_AWARE_ROUTING=false
systemctl restart llm-gateway

# 方法 2: 通过脚本回滚
bash scripts/deploy-seamless.sh rollback 245
```

**影响**:
- ✅ Feature flag 关闭后，零开销运行
- ✅ 不需要重启业务（仅重启网关服务）
- ✅ 立即生效

### 3.2 问题诊断

如果发现问题，按以下步骤诊断：

```bash
# 1. 查看日志
ssh root@8.136.114.245 "journalctl -u llm-gateway -n 100 | grep -E 'pressure|penalty'"

# 2. 检查 Redis 状态
ssh root@8.136.114.245 "redis-cli ping"

# 3. 检查 FpSlots
ssh root@8.136.114.245 "redis-cli keys 'llmgw:cred_fp_slot:*' | head -10"

# 4. 临时禁用观察
ssh root@8.136.114.245 "export PRESSURE_AWARE_ROUTING=false"
```

---

## 四、监控指标清单

### 4.1 必须监控的指标

| 指标 | 类型 | 监控频率 |
|------|------|----------|
| 路由决策耗时 P95 | 性能 | 实时 |
| 请求成功率 | 业务 | 实时 |
| 节点选择分布 | 业务 | 每小时 |
| FpSlots 占用率 | 资源 | 每小时 |
| Limiter 并发度 | 资源 | 每小时 |

### 4.2 监控命令

```bash
# 1. 路由决策耗时
watch -n 5 "curl -s http://245:8781/metrics | grep router_decision_duration"

# 2. 请求成功率
watch -n 10 "curl -s http://245:8781/api/admin/stats | jq .request_stats"

# 3. 节点选择分布
watch -n 60 "ssh root@245 'redis-cli -n 0 --scan --pattern ursm:v2:* | wc -l'"

# 4. FpSlots 压力
watch -n 60 "ssh root@245 'redis-cli -n 0 keys llmgw:cred_fp_slot:* | wc -l'"

# 5. Limiter 状态
watch -n 30 "ssh root@245 'cat /tmp/limiter-stats.json'"
```

---

## 五、成功标准

### 5.1 必须达成（必须全部满足）

- ✅ **请求成功率**: 无下降（允许 ±0.05% 波动）
- ✅ **P95 延迟**: 增长 < 10ms
- ✅ **服务稳定性**: 无新增错误日志

### 5.2 期望达成（至少 3 项）

- ✅ **节点选择均衡度**: 提升 20%+
- ✅ **高压力节点流量**: 降低 20%+
- ✅ **资源利用率**: 更均匀分布

### 5.3 一票否决

- ❌ 请求成功率下降 > 0.5%
- ❌ P95 延迟增长 > 50ms
- ❌ 新增严重错误（5xx 错误率上升）

---

## 六、下一步

### 6.1 如果 A/B 测试成功

1. 更新 ARCHITECTURE.md 文档
2. 准备部署到 154 生产环境
3. 持续监控 1 周
4. 推广到其他环境

### 6.2 如果 A/B 测试需要调整

1. 调整压力惩罚函数参数
2. 重新部署测试
3. 重复 A/B 测试

### 6.3 如果 A/B 测试失败

1. 回滚到 Phase 2.2 版本
2. 分析失败原因
3. 重新设计或取消 Phase 2.3

---

## 七、Phase 2.4 优化计划（待启动）

根据 A/B 测试结果，可能需要以下优化：

### 7.1 可选优化方向

1. **添加 Prometheus 指标**
   - 压力惩罚应用次数
   - 压力信号分布
   - 节点选择变化

2. **动态调整惩罚参数**
   - 支持配置文件
   - 不需要重新编译

3. **细粒度压力感知**
   - 按模型区分
   - 按租户区分

---

**运行手册状态**: 待执行  
**下一步**: 启动 A/B 测试（基线观察）

---

**报告生成时间**: 2026-07-25  
**报告生成人**: Kiro AI Assistant  
**测试窗口**: 2026-07-25 ~ 2026-08-01
