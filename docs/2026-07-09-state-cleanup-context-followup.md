# Follow-up: request ctx 与善后写回 / 资源释放 解耦

## 背景

在 `GLM-5.2` 故障排查中，我们确认 `FP slot` 泄漏的直接根因是：

- 资源释放使用了请求自身的 `context`
- 客户端断开后 `ctx` 立即取消
- Redis / DB 善后写回随之失败
- 状态残留或资源泄漏逐步积累

初次修复只覆盖了 `credentialfpslot` 主释放路径。随后我们对仓库中同类模式做了横向审计，并完成第一批统一治理。

## 本轮修复范围

### 1. 新增统一 helper

新增：`internal/runctx/runctx.go`

提供两类统一模式：
- `BackgroundTimeout(d)`：纯 cleanup/release
- `DetachedTimeout(parent, d)`：保留上下文 values，但脱离 cancel

### 2. executor

以下请求结束后仍必须完成的操作，统一改为 detached ctx：
- success:
  - `restoreCredentialState`
  - `Recorder.RecordSuccess`
  - `HealthTracker.OnSuccess`
  - `UnifiedProbeScheduler.OnRealRequest(success)`
  - `StateObserver.UpdateOnSuccess`
- failure:
  - `Recorder.RecordFailure`
  - `StateObserver.UpdateOnFailure`
  - `writeCredentialStateOnError`
  - `forceUnpinOnFatalKind`
- `model_not_found` 路径：
  - `recordModelNotFound`
  - `coolBindingOnMnfStreak`

### 3. credentialstate

异步 Redis / probe 提交统一改为 detached ctx：
- `UpdateOnSuccess`
- `UpdateOnFailure`
- `UpdateFromProbe`
- `TriggerPing`
- `RegisterNode` / `EnableNode` 的立即探测

同时给 `setToRedis()` 增加了 warn 日志，避免静默丢写。

### 4. admin discover

`admin/models.go` 中 `triggerDiscover` 改为 detached ctx，避免后台 discover 因 HTTP 请求结束而被取消。

### 5. URSM

同源风险收敛：
- `fp_slot_manager.Release`
- `conc_slot_manager.Release`
- `api_record.go` 中 probe 提交
- `batch_writer.go` 中 invalidate callback
- `manager.ReleaseResources()` 从空实现变为可用实现

### 6. URSM 规划阶段资源持有问题

本轮采取的策略不是把 `RouteNode` 资源句柄一路透传到 executor，而是：

- `GetAvailableNodes()` 不再在“路由规划阶段”真实占用 FP / concurrency 资源
- URSM 负责状态过滤 + 排序
- 实际资源门控继续复用 executor 中已经成熟的 `FpSlots + Limiter` 路径

这样可以：
- 避免双重资源管理
- 直接消除“未选中候选也持有 slot”的结构性泄漏
- 不引入更大范围的接口改造

## 统一治理原则

1. 请求内业务 I/O 可以使用 request ctx
2. 请求结束后仍必须完成的善后动作，必须使用：
   - `DetachedTimeout(...)` 或
   - `BackgroundTimeout(...)`
3. 任何 `go func()` 后还要做 DB/Redis/外部状态写入的逻辑，禁止直接复用 `r.Context()`
4. 影响可用性的状态写回失败，至少记录 `warn`；无补偿路径则应 `error`

## 尚未纳入本轮的后续项

仍建议后续继续跟进：
- 为状态写回失败增加指标（metrics）
- 对 URSM `RecordRequest` 的状态机占位实现做完整修正
- 若将来 URSM 要真正承担资源管理，则需再做一轮 `RouteNode -> executor` 资源句柄透传改造
