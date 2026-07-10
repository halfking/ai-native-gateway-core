# 2026-07-09 credentialfpslot Metrics 补充

## 背景
在修复 GLM-5.2 FP slot 泄漏后，`credentialfpslot` 模块虽然已经修复了关键的释放路径问题，但缺少运行时观测能力。生产环境无法实时监控：
- slot 使用率是否接近上限
- 释放失败率是否异常
- 是否频繁出现饱和/抢占事件

本次补充运行时 metrics，为生产环境提供完整的观测能力。

## 新增 Metrics

### 1. 基础计数指标
- `llmgw_fpslot_acquire_total{outcome}` - slot 获取尝试总数
  - `outcome="success"` - 成功获取
  - `outcome="saturated"` - 因饱和失败
  - `outcome="redis_error"` - 因 Redis 错误失败

- `llmgw_fpslot_acquire_failures_total{reason}` - slot 获取失败次数
  - `reason="saturated"` - 饱和失败
  - `reason="redis_error"` - Redis 错误

- `llmgw_fpslot_release_total` - slot 释放调用总数
- `llmgw_fpslot_release_failures_total` - slot 释放失败次数

### 2. 使用率指标
- `llmgw_fpslot_utilization_ratio{credential_id}` - slot 使用率（0-1）
  - 计算公式：`used / limit`
  - 可按 credential 维度观测

### 3. 事件指标
- `llmgw_fpslot_saturation_events_total` - slot 饱和事件（全占用）
- `llmgw_fpslot_preempt_events_total` - slot 抢占事件（LRU 淘汰）
- `llmgw_fpslot_reclaim_events_total` - 后台回收事件

## 代码接入点

### Acquire 路径
```go
func (m *Manager) Acquire(...) (*Lease, bool) {
    if m.client == nil {
        recordAcquireRedisError()  // Redis 不可用
        return nil, false
    }
    if lease, ok := m.acquireRedis(...); ok {
        recordAcquireSuccess()      // 成功获取
        return lease, true
    }
    recordAcquireSaturated()        // 饱和失败
    return nil, false
}
```

### Release 路径
```go
func (m *Manager) Release(lease *Lease) {
    // ... Redis 释放逻辑 ...
    if err != nil {
        recordReleaseFailure()      // 释放失败
        slog.Error(...)
        return
    }
    recordReleaseSuccess()          // 释放成功
}
```

### Reclaim 路径
```go
func (m *Manager) reclaimLoopRun(...) {
    // ... 扫描并回收空闲 slot ...
    if res == 1 {
        totalReclaimed++
        recordReclaim()             // 后台回收事件
    }
}
```

## 使用场景

### 1. 容量规划
观测 `llmgw_fpslot_utilization_ratio`：
- 持续 > 0.8：考虑增加 `fp_slot_limit`
- 持续 < 0.3：考虑降低 `fp_slot_limit` 节省资源

### 2. 故障排查
观测 `llmgw_fpslot_release_failures_total`：
- 突增：Redis 可能故障或网络抖动
- 持续非零：需要审计释放路径代码

### 3. 性能优化
观测 `llmgw_fpslot_saturation_events_total`：
- 频繁触发：说明 slot pool 太小，影响并发能力
- 结合 `utilization_ratio` 判断是否需要扩容

### 4. 告警配置
推荐告警规则：
```yaml
- alert: FpSlotHighUtilization
  expr: llmgw_fpslot_utilization_ratio > 0.9
  for: 5m
  annotations:
    summary: "FP slot 使用率 > 90%"

- alert: FpSlotReleaseFailureRate
  expr: rate(llmgw_fpslot_release_failures_total[5m]) > 0.01
  for: 2m
  annotations:
    summary: "FP slot 释放失败率 > 1%"

- alert: FpSlotSaturationHigh
  expr: rate(llmgw_fpslot_saturation_events_total[5m]) > 10
  for: 5m
  annotations:
    summary: "FP slot 饱和事件频繁（>10/5min）"
```

## 测试验证
```bash
go test ./credentialfpslot/ -count=1 -timeout 180s
# ok  	github.com/kaixuan/llm-gateway-go/credentialfpslot	0.829s
```

## 提交记录
- `10313226` - feat(credentialfpslot): 补充运行时 metrics 观测能力

## 后续建议
1. 在 Grafana 中创建 FP slot 监控面板
2. 设置上述告警规则
3. 定期审查 metrics 数据，优化 slot pool 配置
4. 若 `preempt_events` 频繁，考虑实现更智能的淘汰策略

---

完成时间：2026-07-09  
相关文档：[docs/2026-07-09-glm52-fp-slot-fix-summary.md](/Users/xutaohuang/workspace/llm-gateway-go-3/docs/2026-07-09-glm52-fp-slot-fix-summary.md)
