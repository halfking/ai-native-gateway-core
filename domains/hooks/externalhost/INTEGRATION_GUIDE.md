# W3 External Hooks - Production Integration Guide

**Status**: Infrastructure Complete ✅ | Production Integration: Ready for Review

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
