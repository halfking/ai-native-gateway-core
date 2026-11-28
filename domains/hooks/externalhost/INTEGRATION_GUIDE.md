# W3 External Hooks - Production Integration Guide

**Status**: Production Wiring Complete ✅ (2026-11-27) | Enable via env, default OFF

> **✅ 生产装配已落地（2026-11-27 修订 —— 本段取代下面 2026-11-25 的现状声明）**
>
> 1. **装配面已接通**：`cmd/gateway/external_hook_init.go` 完成 loader→host→integrator→`chatHandler.SetExternalHooks` 全链装配（main.go 在 plugin canon 路由之后调用）。**默认关**——`LLM_GATEWAY_EXTERNAL_HOOK_ENABLED=1` 显式开启，未开启时零装配、零外部调用。
> 2. **上线前必修项已修**（下面旧声明列出的缺陷逐条处置）：
>    - 来源判定：`isLocalNetwork`（RFC1918 一律 local）已替换为 `isLoopbackAddr`（只认 127/8 与 ::1）——私网客户端按 remote 走隐私路径。★ 反向代理场景（2026-11-28 收口）：新增 XFF 信任链——`LLM_GATEWAY_EXTERNAL_HOOK_TRUSTED_PROXY_HOPS=N`（默认 0=不信任）声明前面有 N 层受信代理时，来源按 `X-Forwarded-For` 右锚定第 N 项判类；链缺失/短于声明层数 fail-closed 按 remote。
>    - 凭据头脱敏：`BuildRequestPayload` 改为「精确黑名单 + 凭据语义词根（key/token/secret/credential/password/auth）」双保险（`pipeline_integration.go` 的 `isSensitiveHeader`），`x-goog-api-key`、内部凭据头等全部剔除。
>    - 熔断与观测：新增 `metrics.go`——连续 5 次失败开熔断 30s（半开探测恢复）+ Prometheus 指标 `external_hook_calls_total{hook_point,result}` / `external_hook_duration_seconds` / `external_hook_circuit_open`。
>    - 流式空正文：onResponse 对空 `result.ResponseBody` 用 `reassembleStreamBody(streamCapture)` 兜底重组成 assistant 正文。
>    - 改写体校验：`host.go` 送出侧强制 `contract.BoundedCopy`（8MiB 上限兑现，`boundedRequest`/`boundedResponse`）。
>    - reason 截断（2026-11-28 收口）：钩子返回的 `reason`/`error` 经 `contract.TruncateReason` 收敛到 `MaxReasonBytes`（1024 字节、UTF-8 安全）后才进入 403 响应、日志、追踪与库。
> 3. **仍未实现**（照抄旧文会失败的部分保持如实标注）：配置热 reload（需重启进程）、停钩子进程在线摘除（熔断器已兜底：连续失败后自动跳过）。`config.ExternalHooks.Enabled` YAML 配置不存在——装配走环境变量（见下表）。
>
> **环境变量清单**（`cmd/gateway/external_hook_init.go`）：
>
> | 变量 | 默认 | 说明 |
> |---|---|---|
> | `LLM_GATEWAY_EXTERNAL_HOOK_ENABLED` | `false` | 总开关（默认关） |
> | `LLM_GATEWAY_EXTERNAL_HOOK_MANIFEST` | — | 单个 hook manifest 路径 |
> | `LLM_GATEWAY_EXTERNAL_HOOKS_DIR` | — | 目录扫描（*.json，与上一同使用） |
> | `LLM_GATEWAY_EXTERNAL_HOOK_TIMEOUT_MS` | `1000` | 单次调用超时（合同区间 100-10000） |
> | `LLM_GATEWAY_EXTERNAL_HOOK_CIRCUIT_THRESHOLD` | `5` | 连续失败熔断阈值 |
> | `LLM_GATEWAY_EXTERNAL_HOOK_CIRCUIT_WINDOW_MS` | `30000` | 熔断打开时长 |
> | `LLM_GATEWAY_EXTERNAL_HOOK_ALLOW_REMOTE_BODY` | `false` | 允许 remote 来源捕获正文（W3.4，默认禁） |
> | `LLM_GATEWAY_EXTERNAL_HOOK_TRUSTED_PROXY_HOPS` | `0` | 受信反向代理层数（XFF 信任链，2026-11-28；0=不信任 XFF） |
>
> 逐条历史证据（文件:行）见 `docs/operations/2026-11-23-211配置优化执行报告.md` §9.30 ㉓-44。

> **现状声明（2026-11-25 第 28 轮 24h 审计订正 —— 已被上方 2026-11-27 修订取代，留档）**
>
> 1. **本管线目前是休眠的。** `ChatHandler.SetExternalHooks` 在生产代码里**没有任何调用方**（只有定义、测试与 demo）；仓内也不存在任何配置项、环境变量或部署产物能启用它。是否上线属 owner 决策（见 `docs/12小时内修订审计-2026-11-25-0400.md` 待决项 ⑤）。上面那行「Production Integration: Ready for Review」应读作「尚不可接」。
> 2. **接线之前必须先修的缺陷**（一旦接线，它们就在请求主路径上生效）：
>    - **来源判定在反向代理之后失效**：来源取自 TCP 对端地址（`handler.go` 的 `isLocalNetwork(r.RemoteAddr)`，RFC1918 一律视为本地）⇒ 网关在 nginx 之后时，所有租户的请求都被判为 `local` ⇒ 「远端来源不捕获正文」的默认保护形同虚设，提示词全部送往钩子主机。
>    - **凭据头未脱敏**：转发前只跳过 `Authorization` / `Cookie` / `X-Api-Key`（`pipeline_integration.go`），而 `x-goog-api-key` 是本网关认可的凭据头之一 ⇒ 会被原样转发；上游响应头也整体转抄。
>    - **请求路径上的同步依赖没有观测与熔断**：钩子挂起时每个请求都等满超时（默认 1 秒、上限 10 秒）；失败只累加内部计数，不记日志、无指标、无断路器、无并发上限。
>    - **流式响应下响应钩子拿到的是空正文**；客户端断连、上游出错、治理拦截时响应钩子不会触发。
>    - **请求钩子排在副作用之后**（会话分配、限流、GoalRun 落库之后），且被钩子改写后的请求体不经 JSON / 大小校验就下发。
>    - 响应捕获按请求无界起 goroutine、无 panic 恢复；钩子返回的 `reason` 不截断地进入 403 响应、日志、追踪与库。
> 3. **下文与代码不符之处**（照抄会编译不过，或指向不存在的东西）：`config.ExternalHooks.Enabled` 与对应的 YAML 片段不存在；`externalhost.NewLoader()` 现在的签名是 `NewLoader(cfg LoaderConfig)`；manifest 的协议值须为 `"http-unix-socket"` 而不是 `"http+unix"`；「改配置后 reload 即生效」「停掉钩子进程零影响」均未实现（`SetExternalHooks` 要求在开始服务之前调用）；`examples/cmd/server.go`、`/hooks/audit` 路由、`internal/gwtrace/events.go` 路径都不存在。
>
> 逐条证据（文件:行）见 `docs/operations/2026-11-23-211配置优化执行报告.md` §9.30 ㉓-44。

## Overview

The W3 external hooks infrastructure is complete and tested:
- ✅ Contract package (553 lines, `contract/contract.go`)
- ✅ Host implementation (314 lines, `host.go`) 
- ✅ Pipeline integrator (301 lines, `pipeline_integration.go`)
- ✅ First real hook: Compliance checker (370 lines, `examples/compliance_hook.go`)
- ✅ Comprehensive tests: 46 tests total (36 host/loader + 10 compliance)

This guide documents the **exact integration points** needed to wire external hooks into the production request pipeline in `domains/streaming/handler.go`.

---

## Architecture Recap

```
Client Request
     ↓
[Authentication] ← External Hook onRequest (before protocol translation)
     ↓
[Protocol Translation]
     ↓
[Upstream LLM]
     ↓
[Response Streaming]
     ↓
[Response Complete] → External Hook onResponse (fire-and-forget)
     ↓
Client Response
```

**Key semantics (W3.2/W3.3)**:
- onRequest failure/timeout → pass through (fail-open)
- onRequest block → return error to client
- onResponse is fire-and-forget (doesn't block client EOF)
- State flows from onRequest to onResponse via ctx.State

---

## Step 1: Add PipelineIntegrator to ChatHandler

**File**: `domains/streaming/handler.go`

**Location**: Add field to ChatHandler struct (around line 1265)

```go
type ChatHandler struct {
	circuit      *credential.Manager
	limiter      *credential.Limiter
	matrix       *transformation.Matrix
	pools        *pool.PoolManager
	resolver     *resolve.Resolver
	auditor      audit.Sink
	client       *upstreampkg.Client
	normalizer   *Normalizer
	
	// ... existing fields ...
	
	// External hooks integration (W3)
	externalHooks *externalhost.PipelineIntegrator // ← ADD THIS
	
	// ... rest of fields ...
}
```

**Add setter method** (around line 1760):

```go
// SetExternalHooks configures external hook pipeline integration (W3).
func (h *ChatHandler) SetExternalHooks(integrator *externalhost.PipelineIntegrator) {
	h.externalHooks = integrator
}
```

---

## Step 2: Call InterceptRequest Before Protocol Translation

**File**: `domains/streaming/handler.go`

**Location**: In `serveHTTPInner`, after request ID generation but before protocol translation (around line 2500-2600)

**Integration point**: After authentication, before calling upstream

```go
// ── External Hook: onRequest (W3.1) ──────────────────────────────────
// Called BEFORE protocol translation to allow hooks to:
//   - Block requests (compliance/policy checks)
//   - Rewrite request bodies (content moderation)
//   - Set state for onResponse correlation
//
// Failure modes (W3.2):
//   - timeout/crash → pass through (logged but not blocked)
//   - block decision → return error to client
//   - protected field change → pass through (logged)
var reqContext *externalhost.RequestContext
if h.externalHooks != nil {
	// Build hook context from request metadata
	hookCtx := externalhost.BuildHookContext(
		r,
		requestID,
		gwSessionID, // resolved session ID
		tenantID,    // from authentication
	)
	hookCtx.Protocol = upstreamProtocol // e.g. "openai", "anthropic"
	hookCtx.Model = modelChoice          // resolved model
	hookCtx.Origin = determineOrigin(r)  // local/gateway-key/remote
	
	// Build request payload
	reqPayload := externalhost.BuildRequestPayload(
		r,
		bodyBytes, // raw request body
		modelChoice,
		isStreaming,
	)
	
	// Call onRequest hook
	rewrittenReq, reqContext, err := h.externalHooks.InterceptRequest(
		r.Context(),
		hookCtx,
		reqPayload,
	)
	
	// Handle block decision
	if err != nil {
		// Hook blocked request - return error to client
		h.emitTrace(r.Context(), requestID,
			gwtrace.ExternalHookBlock("onRequest blocked", err.Error()))
		writeJSONError(w, 403, "request blocked by policy", "policy_violation", err.Error())
		return
	}
	
	// Apply rewritten request if present
	if rewrittenReq != nil {
		// Hook rewrote request - use modified body
		bodyBytes = rewrittenReq.Body.Body
		h.emitTrace(r.Context(), requestID,
			gwtrace.ExternalHookRewrite("onRequest rewrote body"))
	}
}
// ── End External Hook: onRequest ─────────────────────────────────────
```

**Helper function to add** (around line 1800):

```go
// determineOrigin classifies request origin for external hooks (W3.4).
func determineOrigin(r *http.Request) contract.Origin {
	// Check authentication middleware results
	if isLocalNetwork(r.RemoteAddr) {
		return contract.OriginLocal
	}
	if hasGatewayKey(r) {
		return contract.OriginGatewayKey
	}
	return contract.OriginRemote
}
```

---

## Step 3: Call CaptureResponse After Upstream Returns

**File**: `domains/streaming/handler.go`

**Location**: After streaming response completes (around line 4500-4800)

**Integration point**: After response fully sent to client

```go
// ── External Hook: onResponse (W3.3) ─────────────────────────────────
// Called AFTER response sent to client (fire-and-forget).
// Captures response metadata for audit/analytics without blocking EOF.
if h.externalHooks != nil && reqContext != nil && reqContext.OnRequestSucceeded {
	// Build response payload with bounded body copy
	respPayload := externalhost.BuildResponsePayload(
		responseStatus,
		responseHeaders,
		capturedResponseBody, // bounded copy (8MiB max)
		actualModel,          // model that actually served request
		isStreaming,
	)
	
	// Update hook context with final values
	hookCtx.Abort = clientDisconnected // signal if client hung up
	
	// Capture response in background (doesn't block)
	h.externalHooks.CaptureResponse(reqContext, hookCtx, respPayload)
	
	h.emitTrace(r.Context(), requestID,
		gwtrace.ExternalHookCapture("onResponse captured"))
}
// ── End External Hook: onResponse ────────────────────────────────────
```

**Note**: For streaming responses, use `CaptureResponseFromStream` to handle incremental capture:

```go
if isStreaming {
	capturedBody, err := h.externalHooks.CaptureResponseFromStream(
		reqContext,
		hookCtx,
		respPayload,
		responseBodyReader, // io.Reader
	)
	// capturedBody contains up to 8MiB for processing
}
```

---

## Step 4: Configuration & Initialization

**File**: `cmd/gateway/main.go` (or wherever ChatHandler is initialized)

```go
import (
	"github.com/kaixuan/llm-gateway-go/domains/hooks/externalhost"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/externalhost/examples"
)

// Initialize external hooks if enabled
var externalHooksIntegrator *externalhost.PipelineIntegrator
if config.ExternalHooks.Enabled {
	// Option A: Load from plugin manifest (production path)
	loader := externalhost.NewLoader()
	manifest := loadManifest(config.ExternalHooks.ManifestPath)
	host, err := loader.Load(manifest)
	if err != nil {
		log.Fatal("failed to load external hook:", err)
	}
	externalHooksIntegrator = externalhost.NewPipelineIntegrator(host)
	
	// Option B: Use in-process example hook (testing/demo)
	// complianceHook := examples.NewComplianceHook()
	// mockHost := wrapComplianceHook(complianceHook)
	// externalHooksIntegrator = externalhost.NewPipelineIntegrator(mockHost)
	
	log.Info("external hooks enabled",
		"timeout", host.Timeout(),
		"socket", config.ExternalHooks.SocketPath)
}

// Set on handler
chatHandler.SetExternalHooks(externalHooksIntegrator)
```

**Configuration structure** (add to gateway config):

```yaml
external_hooks:
  enabled: true
  socket_path: /var/run/gateway/hooks/compliance.sock
  timeout: 1s
  allow_remote_body_capture: false  # Privacy: skip remote origin bodies
  manifest_path: /etc/gateway/hooks/compliance-hook.json
```

**Example manifest** (`compliance-hook.json`):

```json
{
  "plugin_id": "compliance-checker",
  "version": "1.0.0",
  "protocol": "http+unix",
  "socket_path": "/var/run/gateway/hooks/compliance.sock",
  "capabilities": ["on_request", "on_response"],
  "timeout_ms": 1000
}
```

---

## Step 5: Monitoring & Observability

### Metrics to Add

```go
// In handler initialization or dedicated metrics package
var (
	externalHookRequestTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "external_hook_request_total",
			Help: "Total external hook onRequest calls",
		},
		[]string{"decision", "failure"},
	)
	
	externalHookRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "external_hook_request_duration_seconds",
			Help: "External hook onRequest latency",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1},
		},
		[]string{"decision"},
	)
	
	externalHookResponseTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "external_hook_response_total",
			Help: "Total external hook onResponse calls",
		},
		[]string{"skipped"},
	)
)
```

### Tracing Events (already referenced in integration code)

Add to `internal/gwtrace/events.go`:

```go
func ExternalHookBlock(stage, reason string) EventBuilder {
	return Event("external_hook_block").
		WithDetails("stage", stage, "reason", reason)
}

func ExternalHookRewrite(stage string) EventBuilder {
	return Event("external_hook_rewrite").
		WithDetails("stage", stage)
}

func ExternalHookCapture(stage string) EventBuilder {
	return Event("external_hook_capture").
		WithDetails("stage", stage)
}
```

### Alerting Rules

```yaml
# Alert if hooks start blocking >5% of requests
- alert: ExternalHookHighBlockRate
  expr: |
    rate(external_hook_request_total{decision="block"}[5m]) /
    rate(external_hook_request_total[5m]) > 0.05
  for: 5m
  annotations:
    summary: "External hooks blocking high rate of requests"

# Alert if hooks are timing out
- alert: ExternalHookTimeouts
  expr: rate(external_hook_request_total{failure="timeout"}[5m]) > 10
  for: 5m
  annotations:
    summary: "External hooks timing out frequently"

# Alert if hooks are crashing
- alert: ExternalHookCrashes
  expr: rate(external_hook_request_total{failure="crash"}[5m]) > 1
  for: 1m
  annotations:
    summary: "External hook process crashing"
```

---

## Step 6: Deployment Checklist

### Pre-deployment
- [ ] Review hook implementation for security (input validation, resource limits)
- [ ] Load test: 1000 req/s with hook enabled, verify <1ms p99 overhead
- [ ] Verify fail-open behavior: kill hook process, requests still succeed
- [ ] Test protected field rollback: hook tries to change model, request continues with original
- [ ] Test block decision: hook blocks request, client gets 403 with reason

### Deployment
- [ ] Deploy hook process first (unix socket available)
- [ ] Enable hooks with `allow_remote_body_capture: false` (privacy)
- [ ] Monitor metrics: hook latency, block rate, timeout rate
- [ ] Gradual rollout: 1% → 10% → 50% → 100% traffic
- [ ] Verify audit logs: onRequest/onResponse correlation working

### Post-deployment
- [ ] Review first 24h of audit logs for unexpected blocks
- [ ] Check performance: p50/p95/p99 latency impact
- [ ] Verify no memory leaks (monitor hook process RSS)
- [ ] Document any edge cases discovered

---

## Testing the Integration

### Unit Test (add to handler_test.go)

```go
func TestChatHandler_ExternalHooks_BlockRequest(t *testing.T) {
	// Setup mock hook that blocks requests
	mockHost := &mockHookHost{
		onRequestFunc: func(ctx context.Context, hookCtx *contract.Context, req *contract.RequestPayload) *Outcome {
			return &Outcome{
				Decision: contract.DecisionBlock,
				Reason:   "test block",
			}
		},
	}
	integrator := externalhost.NewPipelineIntegrator(mockHost)
	
	handler := NewChatHandler(...)
	handler.SetExternalHooks(integrator)
	
	// Make request
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"messages":[]}`))
	w := httptest.NewRecorder()
	
	handler.ServeHTTP(w, req)
	
	// Verify blocked
	if w.Code != 403 {
		t.Errorf("status = %d, want 403", w.Code)
	}
	
	body := w.Body.String()
	if !strings.Contains(body, "test block") {
		t.Errorf("body should contain block reason: %s", body)
	}
}
```

### Integration Test

```bash
# Start compliance hook server
$ go run ./domains/hooks/externalhost/examples/cmd/server.go \
    --socket /tmp/compliance.sock

# Start gateway with hooks enabled
$ ./gateway --config config-with-hooks.yaml

# Test clean request (should succeed)
$ curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"messages":[{"role":"user","content":"Hello"}]}'

# Test banned content (should block)
$ curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"messages":[{"role":"user","content":"How to exploit this?"}]}'
# Expected: 403 Forbidden, reason: "content policy violation"

# Check audit log
$ curl http://localhost:9090/hooks/audit
```

---

## Rollback Plan

If issues arise post-deployment:

1. **Immediate**: Set `external_hooks.enabled: false` in config, reload gateway
   - Requests continue normally, no restart needed
   
2. **Graceful**: Leave hooks enabled but stop hook process
   - Gateway fail-open: requests pass through with timeout logged
   - Zero client impact
   
3. **Diagnosis**: Check metrics dashboard
   - Hook latency spike → investigate hook implementation
   - High block rate → review blocked request patterns
   - Crashes → check hook process logs

---

## Example: Compliance Hook Deployment

The reference compliance hook (`examples/compliance_hook.go`) demonstrates:

✅ **onRequest**: Blocks requests with banned keywords
✅ **onResponse**: Logs token usage for audit
✅ **State correlation**: Input tokens flow from request to response
✅ **10 comprehensive tests**: All passing

**To deploy**:

```bash
# Build hook server (would need to create cmd/server.go wrapper)
$ go build -o /usr/local/bin/compliance-hook \
    ./domains/hooks/externalhost/examples/cmd/server.go

# Start as systemd service
$ sudo systemctl start compliance-hook

# Verify socket exists
$ ls -la /var/run/gateway/hooks/compliance.sock

# Enable in gateway config
$ vim /etc/gateway/config.yaml  # Set external_hooks.enabled: true

# Reload gateway (zero downtime)
$ sudo systemctl reload gateway
```

---

## Summary

**Infrastructure Status**: ✅ Complete (301 lines + 46 tests)

**Integration Status**: 📋 Documented (this guide)

**Next Steps**:
1. Review this guide with infrastructure team
2. Load test in staging environment
3. Deploy to 1% production traffic
4. Monitor for 24h before full rollout

**Safety**:
- Fail-open by default (timeout/crash → pass through)
- Fire-and-forget onResponse (doesn't block client)
- Protected field guards (model/stream/tools can't be changed)
- Privacy-first (remote origins skip body capture by default)

The infrastructure is production-ready. Integration requires careful testing and gradual rollout per this guide.
