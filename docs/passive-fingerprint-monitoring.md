# 被动指纹采集监控系统

## 概述

本文档描述被动指纹采集功能的持续监控系统，用于确保断链修复后的系统持续稳定运行。

## 监控自动化

### 定时任务配置

- **任务ID**: `automation-bc35235e-371e-45b2-937e-2c4fa7cbd02b`
- **运行频率**: 每20分钟（`*/20 * * * *`）
- **创建时间**: 2026-11-11
- **状态**: 活跃

### 监控范围

#### 1. 实时健康检查（每次执行）

**检查项目**：
- Worker flush 频率（5分钟/次）
- 数据写入量（节点数）
- Pending 积压情况
- 容器健康状态
- 部署版本确认

**健康标准**：
- ✅ 健康：30分钟内 5-6 次 flush，节点数 ≥ 3，pending ≤ 10
- ⚠️ 警告：flush 次数 4 次，节点数 2-3，pending 10-20
- ❌ 异常：无 flush，节点数=0，pending > 20

#### 2. 深度数据质量分析（每小时）

**分析指标**：
- 2小时内总 flush 次数（目标：24次）
- 总写入节点数（目标：≥ 120）
- 平均节点数/flush（目标：≥ 5）
- 写入速率（节点/小时）
- 数据趋势分析

**质量评级**：
- ✅ 优秀：2小时写入 ≥ 120 节点，成功率 100%
- ⚠️ 合格：2小时写入 60-120 节点，成功率 ≥ 95%
- ❌ 不达标：2小时写入 < 60 节点，成功率 < 95%

## 监控数据来源

### 1. Docker 日志
```bash
ssh 211 'docker logs --since 30m llm-gateway-211-a 2>&1'
```

关键日志模式：
- `"passive baseline flushed"` - flush 成功记录
- `"passive baseline worker alive"` - 心跳日志
- `"flush had failures"` - 错误记录

### 2. 容器状态
```bash
ssh 211 'docker ps | grep llm-gateway-211-a'
```

### 3. 系统版本
```bash
curl -s https://llm.itestu.cn/api/system/version
```

## 关键指标说明

### Flush 日志字段

```json
{
  "msg": "passive baseline flushed",
  "why": "tick",
  "nodes": 7
}
```

- **nodes**: 本次 flush 成功写入数据库的模型节点数
- **why**: flush 触发原因（tick=定时，shutdown=关闭）

### 心跳日志字段

```json
{
  "msg": "passive baseline worker alive",
  "ticks": 6,
  "nodes_written_since_last_beat": 38,
  "pending": 0
}
```

- **ticks**: worker 运行的周期数（每5分钟1次）
- **nodes_written_since_last_beat**: 自上次心跳以来写入的总节点数
- **pending**: 待写入的缓存数据量

## 异常处理流程

### 异常场景1：30分钟无 flush 记录

**可能原因**：
- Worker 进程崩溃
- 代码逻辑错误导致采样未触发
- 数据库连接失败

**诊断步骤**：
1. 检查容器是否重启：`ssh 211 'docker ps | grep llm-gateway-211-a'`
2. 查看错误日志：`ssh 211 'docker logs --since 1h llm-gateway-211-a 2>&1 | grep -i error'`
3. 检查版本是否回退：`curl -s https://llm.itestu.cn/api/system/version`

**修复措施**：
- 如果容器重启，检查启动日志找到崩溃原因
- 如果版本回退，重新部署最新版本
- 如果数据库连接失败，检查数据库状态

### 异常场景2：所有 flush 都是 nodes=0

**可能原因**：
- 没有流量（业务低峰）
- 采样逻辑未触发（代码问题）
- PassiveCollector 未接收到数据

**诊断步骤**：
1. 检查是否有请求日志：`ssh 211 'docker logs --since 10m llm-gateway-211-a 2>&1 | grep "request_id" | wc -l'`
2. 检查 samplePassiveTiming 是否被调用（代码审查）
3. 检查 PassiveCollector.Add 是否有数据

**修复措施**：
- 如果有流量但无采样，说明代码逻辑有问题，需要回滚或修复
- 如果无流量，这是正常现象，等待流量恢复

### 异常场景3：Pending 持续 > 20

**可能原因**：
- 数据库写入性能问题
- 采样速率过高，超过写入能力
- 数据库连接池耗尽

**诊断步骤**：
1. 检查数据库负载
2. 查看 flush 是否有错误：`ssh 211 'docker logs --since 1h llm-gateway-211-a 2>&1 | grep "flush had failures"'`
3. 检查数据库连接数

**修复措施**：
- 增加 flush 频率或调整批量大小
- 优化数据库性能
- 如果持续无法消化，考虑临时降低采样率

## 性能基线

基于 2026-11-11 的生产验证数据：

| 指标 | 基线值 | 说明 |
|------|--------|------|
| Flush 频率 | 5 分钟/次 | 严格周期 |
| 节点数/flush | 5-7 个 | 正常流量 |
| 写入速率 | 60-84 节点/小时 | 1-1.4 节点/分钟 |
| Pending | 0 | 无积压 |
| 成功率 | 100% | 无失败 |

## 验证历史

### 初次验证（2026-11-11）

**验证时间段**: 17:07:38 - 17:37:38 UTC（30分钟）

**结果**：
- Flush 次数：6 次 ✅
- 总写入节点：38 个 ✅
- 平均节点数：6.3 个/flush ✅
- Pending：0 ✅
- 成功率：100% ✅

**结论**：✅ 被动指纹采集功能完全恢复，达到设计目标

## 相关文档

- [HANDOFF-passive-fix-audit-2026-11-10.md](../HANDOFF-passive-fix-audit-2026-11-10.md) - 断链修复审计
- [domains/streaming/passive_timing_tracker.go](../domains/streaming/passive_timing_tracker.go) - 被动时序跟踪器
- [bg/passive_baseline_worker.go](../bg/passive_baseline_worker.go) - Worker 实现
- [domains/modelcredibility/passive_collector.go](../domains/modelcredibility/passive_collector.go) - 数据收集器

## 查看监控报告

监控任务会自动运行，报告会在 ZCode 的自动化日志中查看。

查看自动化状态：
```bash
# 列出所有自动化任务
zcode cron list

# 查看特定自动化的执行历史
zcode cron logs automation-bc35235e-371e-45b2-937e-2c4fa7cbd02b
```

## 手动执行健康检查

如需立即检查系统状态，可以手动执行以下命令：

```bash
# 查看最近30分钟的 flush 记录
ssh 211 'docker logs --since 30m llm-gateway-211-a 2>&1 | grep -E "(passive baseline flushed|passive baseline worker alive)"'

# 查看容器状态
ssh 211 'docker ps | grep llm-gateway-211-a'

# 查看当前版本
curl -s https://llm.itestu.cn/api/system/version | python3 -m json.tool
```

## 维护说明

### 修改监控频率

如需调整监控频率，使用：
```bash
zcode cron update automation-bc35235e-371e-45b2-937e-2c4fa7cbd02b --cron "*/15 * * * *"
```

### 暂停监控

如需临时暂停监控（如维护期间）：
```bash
zcode cron pause automation-bc35235e-371e-45b2-937e-2c4fa7cbd02b
```

### 恢复监控

```bash
zcode cron resume automation-bc35235e-371e-45b2-937e-2c4fa7cbd02b
```

### 删除监控

如不再需要监控：
```bash
zcode cron delete automation-bc35235e-371e-45b2-937e-2c4fa7cbd02b
```

---

**最后更新**: 2026-11-11  
**维护人**: 开发团队  
**状态**: 生产运行中
