# 2026-07-09 GLM-5.2 FP slot 泄漏横向审计补充

在初次修复 `credentialfpslot` 主释放路径后，我们对仓库中同类“资源获取/释放”和“请求结束后状态写回”模式做了二次横向审计，并完成了第一批统一治理。

## 本轮新增修复

### 1. 统一 detached context helper
新增：`internal/runctx/runctx.go`

- `BackgroundTimeout(d)`：完全脱离请求生命周期，适合 release / cleanup
- `DetachedTimeout(parent, d)`：保留 request values，但脱离 cancel，适合状态写回 / 异步补偿

目的：避免各模块重复手写 `context.Background()+timeout`，统一上下文治理方式。

### 2. executor 关键善后写回改为 detached ctx
覆盖：
- success 路径：
  - `restoreCredentialState`
  - `Recorder.RecordSuccess`
  - `HealthTracker.OnSuccess`
  - `UnifiedProbeScheduler.OnRealRequest(success)`
  - `StateObserver.UpdateOnSuccess`
- failure 路径：
  - `Recorder.RecordFailure`
  - `StateObserver.UpdateOnFailure`
  - `writeCredentialStateOnError`
  - `forceUnpinOnFatalKind`
- `model_not_found` 路径：
  - `recordModelNotFound`
  - `coolBindingOnMnfStreak`

现在这些都不再直接使用 `params.R.Context()`，客户端断开不会再中断必须完成的 DB / Redis 写回。

### 3. credentialstate 异步 Redis / probe 提交改为 detached ctx
覆盖：
- `UpdateOnSuccess`
- `UpdateOnFailure`
- `UpdateFromProbe`
- `TriggerPing`
- `RegisterNode(trigger probe)`
- `EnableNode(trigger probe)`

同时给 `setToRedis()` 补了 warn 级别日志，避免 Redis 丢写静默失败。

### 4. admin 手动触发 discover 改为 detached ctx
`admin/models.go:triggerDiscover` 不再把后台 discover 任务直接绑在 `r.Context()` 上，避免管理员点了“开始发现”后页面一关任务就取消。

### 5. URSM 同源 request-ctx 风险收敛
覆盖：
- `fp_slot_manager.Release`：ctx 已取消时回退到独立 background timeout
- `conc_slot_manager.Release`：同上
- `manager.ReleaseResources`：从空实现变为可用实现（释放并发槽 + FP 槽）
- `routing.go` 并发槽获取失败后的 FP 槽回滚：改成独立 cleanup ctx
- `api_record.go` 异步 probe 提交：改成 detached ctx
- `batch_writer.go` 异步 invalidateCallback：改成 detached ctx

## 本轮未完成项（明确保留）

### URSM 完整资源闭环的结构性问题

虽然 `ReleaseResources` 已经实现，但当前 `planWithURSM()` 仍然只把 `RouteNode` 映射回普通 `provider.Candidate`，没有把：
- `sessionID`
- `FpSlotIndex`
- `ConcurrencyHeld`

完整透传到 executor。因此，**“GetAvailableNodes 成功拿到多个节点资源后，未选中节点立即释放”** 这个结构性闭环，在本轮没有彻底打通。

原因：这需要扩大到 `router -> candidate -> executor` 的接口层改造，影响范围明显大于“同源 request ctx 误用修复”，不适合夹带在本次事故修复中半做半留。

### 建议后续单独处理

后续独立补丁建议：
1. 给 `provider.Candidate` 增加 URSM 资源句柄字段，或引入单独的 request-scoped resource token
2. `planWithURSM()` 透传 `sessionID` 与资源元数据
3. executor 在真正选中候选后：
   - 释放未选中节点资源
   - 请求结束时释放选中节点资源

## 测试

已验证：

```bash
go test ./domains/streaming/executors/ ./domains/credentialstate/ ./domains/ursm/ ./credentialfpslot/ ./admin/ ./internal/runctx/
```

相关包测试通过。

## 结论

这轮修复的核心价值不是再补一个点，而是把本次 `FP slot` 泄漏暴露出的**同源上下文治理问题**，系统性收敛到：

- 资源释放：用 `BackgroundTimeout`
- 状态写回：用 `DetachedTimeout`
- 异步补偿：禁止直接继承 request ctx

这样做可以显著降低后续在 executor、credentialstate、URSM、admin 操作里再次出现“客户端断开 → 善后失败 → 状态残留/资源泄漏”的概率。
