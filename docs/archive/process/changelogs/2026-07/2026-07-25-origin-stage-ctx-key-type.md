# 2026-07-25 — origin_stage 始终为空：context key 类型不匹配 + RequestLogContext 未桥接

## 老板反馈（2026-07-25）

> "需要通过自检服务的 api 调用情况反向检查，触发探测的任务有哪些，并分析其合理性。这里还有大量 cred=8 的请求，都是自检服务自己发出的探测，但从请求里面看不出来 origin_stage。"

老板在请求流里看到大量 `credential_id=8` 的请求都是自检服务发出的，但 `request_logs.origin_stage` 全部为 NULL — 无法用 origin 维度识别"哪些是探测 vs 真实业务请求"。

## 现象

| 数据源 | 自检请求应该出现 | 实际出现 |
|---|---|---|
| `request_logs.origin_stage` | `'self_check'` / `'node_probe'` | **NULL**（所有请求） |
| `request_context_attrs.origin_stage` | `'self_check'` / `'node_probe'` | **NULL** |
| `request_context_attrs.is_probe` | `true` | **false** |

完全无法通过 origin 维度识别探测请求。

## 根因调查

### 第一次猜测（错）

第一直觉是 `OriginMiddleware` 没运行 / 鉴权没把系统 key 识别为 trusted owner。但 `OriginMiddleware` 的 4 个单测（`origin_mw_test.go`）全部通过，验证了它确实会把 `X-LLM-Origin-Stage` 写到 ctx。

### 第二次猜测（错）

第二直觉是 `ApplyOriginFromContext` 没被调用。检查 `domains/streaming/handler.go:4277` —— 确认有调用：

```go
reqLog.ApplyOriginFromContext(ctx)
```

### 第三次猜测（命中）— Context key 类型不匹配

逐行对比 `origin_mw.go` 的写入路径与 `telemetry/client.go` 的读取路径：

**写入侧（`middleware/origin_mw.go:79-85`）**：
```go
const (
    originStageKey     = "origin.stage"     // type: string
    originActorKey     = "origin.actor"
    originClientIPKey  = "origin.client_ip"
    originClientXFFKey = "origin.xff"
)
ctx = context.WithValue(ctx, originStageKey, stage)  // key 类型: string
```

**读取侧（`telemetry/client.go:1969-1997`）**：
```go
type originCtxKey string                                  // 不同 Go 类型!
func (e *RequestLogEntry) ApplyOriginFromContext(ctx context.Context) {
    if v, ok := ctx.Value(originCtxKey("origin.stage")).(string); ok && v != "" {
        if e.OriginStage == nil {
            s := v
            e.OriginStage = &s
        }
    }
    ...
}
```

**Go 的 context.Value 用 key 的 dynamic type 做匹配**：

> 来自 Go stdlib 文档（`src/context/context.go`）：
> "The provided key must be comparable and should not be of type string or any other built-in type to avoid collisions between packages."

`string("origin.stage")` 和 `originCtxKey("origin.stage")` 是**两个不同的 dynamic type**，即使底层字符串相同，`context.Value` 也不会匹配。`ApplyOriginFromContext` 从上线以来**从来没读到过任何值**，所以 `request_logs.origin_stage` 一直是 NULL。

**验证**：看 git log，origin_mw 上线（`e192a5ac9 feat(middleware): origin_mw`）和 telemetry 侧写入（`ee2903d25 feat(telemetry): RequestLogEntry origin_stage`）是两次 commit，但当时没人做端到端验证（没有给业务请求写过 origin_stage 真实值的集成测试），bug 就这么混到 main 里了。

### 第四个问题 — 侧表从未被桥接

即便主表 `request_logs.origin_stage` 修了，侧表 `request_context_attrs` 仍然空，因为：

1. `RequestLogContext.SetOriginStage()` 定义了但**从未被调用**。
2. `BuildContextAttrsEntry(autoCtx, keyInfo, &autoCtx.meta, nil)` 的第 4 个参数传了 `nil`，`ApplyAttrsFromContext(nil)` 是 no-op。

检查另外 3 处 `BuildContextAttrsEntry` 调用点（`request_log_pipeline.go:596` / `:625` / `handler.go:3804`），它们都正确传了 `c.Request.Context()` 或 `r.Context()`，**只有 handler.go:4287 这一处传了 `nil`**。

## 修复

### Fix 1: 统一 context key 类型

`telemetry/client.go:ApplyOriginFromContext` 改用纯 `string` key 读取（与 middleware 一致）：

```go
// Before
if v, ok := ctx.Value(originCtxKey("origin.stage")).(string); ok && v != "" {
// After
if v, ok := ctx.Value("origin.stage").(string); ok && v != "" {
```

`originCtxKey` 类型保留（仅作为历史兼容，实际无引用），更新注释说明原因。

### Fix 2: 桥接 origin_stage 到 RequestLogContext

`handler.go:recordInitialRequestLog` 在主表 entry 写入后加桥接：

```go
reqLog.ApplyOriginFromContext(ctx)
// 2026-07-25: bridge origin_stage from main entry to RequestLogContext
// so the side table request_context_attrs also carries origin_stage
// and is_probe (derived from it in fillFromRequestLogContext).
if autoCtx != nil && reqLog.OriginStage != nil && autoCtx.OriginStage == "" {
    autoCtx.SetOriginStage(*reqLog.OriginStage)
}
h.telemetryClient.EmitRequestLogInsert(reqLog)
```

桥接用 `autoCtx.OriginStage == ""` 做 first-write-wins 判断，避免覆盖 `requestAttemptMeta` 中已设置的值。

### Fix 3: BuildContextAttrsEntry 传 ctx 而非 nil

```go
// Before
if attrs := BuildContextAttrsEntry(autoCtx, keyInfo, &autoCtx.meta, nil); attrs != nil {
// After
if attrs := BuildContextAttrsEntry(autoCtx, keyInfo, &autoCtx.meta, ctx); attrs != nil {
```

让 `ApplyAttrsFromContext` 也能兜底运行（即使当前没有 attrs.* key 的写入者，未来加 middleware 时不需要再改这里）。

### Fix 4: 测试同步

`telemetry/client_test.go` 5 处 `originCtxKey(...)` → `"..."`（4 处 populate 测试 + 1 处 first-write-wins 测试）。

## 验证

- `go build ./...` — ✅
- `go vet ./domains/hooks/observability/... ./middleware/... ./domains/streaming/... ./bg/...` — ✅
- `TestRequestLogEntry_ApplyOriginFromContext`（3 子用例） — ✅
- `TestContextAttrsEntry_ApplyAttrsFromContext`（4 子用例） — ✅
- `TestOriginMiddleware_*`（4 个单测） — ✅
- `go test ./domains/streaming/` — ✅
- `go test ./bg/` — ✅

## 修复后的数据预期

| 数据源 | 自检请求将出现 |
|---|---|
| `request_logs.origin_stage` | `'self_check'` |
| `request_logs.origin_actor` | `'credential-selfcheck-worker'` |
| `request_logs.client_ip` | egress IP（worker 设置的 `X-Real-IP`） |
| `request_context_attrs.origin_stage` | `'self_check'` |
| `request_context_attrs.is_probe` | `true` |

可在 dashboard 实时请求流按 `is_probe=true` 筛选出所有探测请求，包括：
- `credential_selfcheck_worker` 24h/cred 周期自检
- `node_probe_worker` 错误触发 5s/30s/60s/5m/1h/2h/24h 退避探测
- 业务请求不会进入 is_probe=true 集合

## 部署后回归验证

部署到 245 → 154 后：
1. 自检请求（cred=8 等）写入 `request_logs.origin_stage='self_check'`
2. `request_context_attrs.is_probe=true` 在侧表索引可查
3. dashboard `/request-logs` 实时流按 `is_probe` 筛选能过滤出探测流量
4. 业务请求不受影响（origin_stage 仍为 'business'）

## 已知未覆盖

- `originCtxKey` 类型本身现已无用（仅注释引用），本 PR 保留以减小 diff 与回归风险；后续可单独清理
- `ActiveProbeEmitter.Emit()` 写入的探测行（`ActiveProbeExecutor.Run()` 发的直探测针）仍未设 `OriginStage`，但这些行的 `task_type_chosen='probe_direct'` + `quality_flags=['probe','direct',...]` 已足够识别探测身份
- `buildClientDisconnectProbeEntry` 在写客户端断连的探测行时 `logCtx.OriginStage` 未被桥接（走另一条 codepath），不在本修复范围