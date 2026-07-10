# 2026-07-09 横向修复最终总结

## 任务背景
在 GLM-5.2 FP slot 泄漏事故后，对仓库进行横向审计，发现并修复同类"request ctx 误用于善后写回/资源释放"问题。

## 完成的工作

### 第一批：核心同源问题修复（提交 `c5082fb4`）
1. **新增统一 helper**：`internal/runctx/runctx.go`
   - `BackgroundTimeout(d)`: 给 release/cleanup 用
   - `DetachedTimeout(parent, d)`: 给状态写回/异步补偿用

2. **executor**：全部关键善后写回已脱离 request ctx
   - success: `restoreCredentialState`, `Recorder.RecordSuccess`, `HealthTracker.OnSuccess`, `UnifiedProbeScheduler.OnRealRequest`, `StateObserver.UpdateOnSuccess`
   - failure: `Recorder.RecordFailure`, `StateObserver.UpdateOnFailure`, `writeCredentialStateOnError`, `forceUnpinOnFatalKind`
   - model_not_found: `recordModelNotFound`, `coolBindingOnMnfStreak`

3. **credentialstate**：异步 Redis/probe 提交已脱离 request ctx
   - `UpdateOnSuccess`, `UpdateOnFailure`, `UpdateFromProbe`, `TriggerPing`
   - `RegisterNode` / `EnableNode` 的立即探测
   - `setToRedis()` 增加 warn 日志

4. **admin**：后台 discover 已脱离 request ctx

5. **URSM**：同源风险已收敛
   - `fp_slot_manager.Release` / `conc_slot_manager.Release`
   - `manager.ReleaseResources` 从空实现变为可用实现
   - `api_record.go` / `batch_writer.go` 异步操作已脱离 request ctx
   - `routing.go` 不再在规划阶段真实占用资源

### 第二批：审计修复（提交 `21c615eb`）
- 修复 `domains/ursm/batch_writer.go` 异步 Redis 写缺少超时控制
- 新增审计报告：`docs/2026-07-09-context-cleanup-audit-report.md`

### 第三批：可观测性与状态机修正（提交 `15c753ae`）
1. **新增 metrics**：
   - `llmgw_credstate_redis_write_failures_total`
   - `llmgw_ursm_state_write_failures_total{layer}`

2. **修复 URSM 状态机占位实现**：
   - `api_record.go` 的 `getNodeState()` 从假实现改为从 `nodeCache` 真实读取
   - 连续失败计数现在可以正确工作

## 关键设计决策

### URSM 资源管理方案
**没有**把 URSM 改成新的完整资源执行链，而是采用分工协作：
- URSM：负责状态过滤 + 排序
- executor：负责实际资源门控（复用成熟的 `FpSlots + Limiter`）

效果：
- 避免双重资源管理
- 消除"未选中候选持有 slot"的结构性泄漏
- 符合"不要重复造轮子"原则

## 验证
所有关键模块测试通过：
```bash
go test ./domains/streaming/executors/ ./domains/credentialstate/ ./domains/ursm/ ./credentialfpslot/
```

## 提交记录
- `c5082fb4` - 主横向修复：收口 request ctx 导致的善后写回与状态残留问题
- `21c615eb` - 审计修复：修复 batch_writer 异步 Redis 写缺少超时控制
- `15c753ae` - 可观测性：为状态写回失败补充 metrics + 修复 URSM 状态机占位实现

## 核心价值
这次不是只补一个点，而是把整类同源问题系统性收口：
1. ✅ executor / credentialstate / URSM / admin 关键路径已全部修复
2. ✅ 引入统一 helper 避免后续重复造轮子
3. ✅ URSM 不再与 executor 双重管理资源
4. ✅ 审计发现的高风险遗漏点已修复
5. ✅ 补充 metrics 为生产环境故障排查提供观测能力
6. ✅ 修复 URSM 状态机假实现，连续失败判断现在可正确工作

## 后续建议
1. 监控新增的 metrics，设置告警阈值
2. 若 URSM 将来要真正承担资源管理，需再做 `RouteNode -> executor` 资源句柄透传改造
3. 定期审计新增代码，确保遵循"善后写回必须 detached ctx"的统一规则
