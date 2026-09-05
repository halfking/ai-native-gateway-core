---
archived_from: (legacy) docs/archive/2026-07/HANDOFF_DIAGNOSTIC_PHASE2.md
archived_at: 2026-08-17
archived_by: docs-archive remediate v1.0
backup_ts: 20260817-190919
status: archived
note: legacy archive, frontmatter retroactively added
---

# Handoff Document: Diagnostic Integration Phase 2

**Session ID:** sess_668d8405-64bc-401a-81c2-51180fe853ee  
**Handoff Date:** 2026-07-26 04:10:00 UTC+8  
**From Agent:** Kiro (autonomous)  
**Project:** llm-gateway-go-3  
**Repository:** https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git

---

## Current State (§4)

### Git Status
- **Branch:** main
- **Commit:** 9089f54d (after version bump)
- **Parent:** 40606961 (diagnostic integration)
- **Status:** All changes committed and pushed
- **Remote:** origin/main synced

### Deployment
- **Server:** 47.97.111.154:8781
- **Version:** 1393-40606961
- **Status:** Running and healthy
- **Deployed:** 2026-07-26 04:00:03

### Environment
- **Working Dir:** `/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-3`
- **Credentials:** Not injected (handoff does not require device access)
- **Build:** ✅ `go build ./cmd/gateway` passes
- **Tests:** ✅ `go test ./domains/streaming/executors` passes

---

## What Was Completed (§5)

### Phase 1: Diagnostic Infrastructure Integration ✅

**Architecture Analysis:**
- Identified that production uses `domains/streaming/executors/Executor` (not `IRTransport`)
- `Executor` is the actual USRM v2 request handling path
- Streaming functions like `StreamAnthropicSSEToOpenAI()` are called via Executor

**Implementation:**

1. **Added Diagnostic Interfaces to Executor** (`domains/streaming/executors/executor.go`)
   ```go
   type RawDataLogger interface {
       LogRequest(requestID string, protocol string, body []byte) error
       LogResponse(requestID string, protocol string, body []byte, isStream bool) error
   }
   
   type AnomalyReporter interface {
       ReportAnomaly(requestID string, anomalyType string, details map[string]interface{}) error
   }
   
   type SemanticAnalyzer interface {
       AnalyzeRequest(requestID string, irReq interface{}) error
       AnalyzeResponse(requestID string, irResp interface{}) error
   }
   ```

2. **Created Adapter Layer** (`domains/streaming/executors/diagnostic_adapters.go`)
   - `RawDataLoggerAdapter` wraps `internal/logging.RawDataLogger`
   - `AnomalyReporterAdapter` wraps `internal/logging.AnomalyReporter` (placeholder)
   - `SemanticAnalyzerAdapter` wraps `internal/ir.SemanticAnalyzer` (placeholder)
   - Bridges internal concrete types to Executor interfaces

3. **Wired Initialization in main.go** (`cmd/gateway/main.go:876-924`)
   - Reads environment variables on startup
   - Creates diagnostic components conditionally
   - Wraps them with adapters
   - Injects into `routingExec.RawDataLogger`, etc.

4. **Verification on Server 154**
   ```
   ✅ raw_data_logger: initialized (dir=/var/log/llm-gateway/raw_data, max_size=200MB)
   ✅ anomaly_reporter: initialized (endpoint=https://llm.kxpms.cn/api/diagnostics/anomalies)
   ✅ semantic_analyzer: initialized
   ✅ Log file created: raw_data_20260725_200004.096830776_1785009604096832493.jsonl
   ```

**Environment Variables (Server 154):**
```bash
LLM_GATEWAY_RAW_LOG_ENABLED=true
LLM_GATEWAY_RAW_LOG_DIR=/var/log/llm-gateway/raw_data
LLM_GATEWAY_RAW_LOG_MAX_SIZE=209715200
LLM_GATEWAY_ANOMALY_REPORTER_ENABLED=true
LLM_GATEWAY_ANOMALY_ENDPOINT=https://llm.kxpms.cn/api/diagnostics/anomalies
LLM_GATEWAY_SEMANTIC_ANALYSIS_ENABLED=true
```

**Commits:**
- `aa09ef3d` - Initial attempt (integrated into IRTransport, not used in production)
- `40606961` - **Active commit**: Integrated diagnostics into Executor (USRM v2)
- `9089f54d` - Version bump after integration

**Documentation:**
- `DIAGNOSTIC_INTEGRATION_SUMMARY.md` - Full technical summary
- `DEPLOYMENT_STATUS_154.md` - Deployment report and architecture gaps

---

## What Needs to Be Done (§6)

### Phase 2: Data Capture Implementation

**Current Gap:**

Diagnostic components are **initialized in Executor** but **not yet called** by streaming functions. The infrastructure is ready, but no data is being captured.

**Why:**
- Streaming functions (`StreamAnthropicSSEToOpenAI`, `StreamOpenAIToAnthropicSSE`, etc.) are defined in `domains/streaming/`
- They are called via function pointers set in main.go (e.g., `routingExec.AnthropicToOpenAIStream = func(...) { ... }`)
- These functions don't receive the Executor instance or its diagnostic fields
- They only receive specific parameters like `requestID`, `resp`, `capture`, etc.

**Solution: Pass Diagnostic Context to Streaming Functions**

### Recommended Approach

**Step 1: Define DiagnosticContext struct** in `domains/streaming/` or `domains/streaming/executors/`

```go
// domains/streaming/diagnostic_context.go (NEW FILE)
package streaming

import "github.com/kaixuan/llm-gateway-go/domains/streaming/executors"

// DiagnosticContext bundles optional diagnostic components for streaming functions.
// All fields are optional (nil-safe). When non-nil, streaming functions can log
// raw data, report anomalies, or run semantic analysis.
type DiagnosticContext struct {
    RawLogger    executors.RawDataLogger
    Anomaly      executors.AnomalyReporter
    Semantic     executors.SemanticAnalyzer
}
```

**Step 2: Modify streaming function signatures** to accept `*DiagnosticContext`

Example for `domains/streaming/anthropic_bridge.go:StreamAnthropicSSEToOpenAI`:

```go
// Current signature (line ~241)
func StreamAnthropicSSEToOpenAI(
    w http.ResponseWriter,
    resp *http.Response,
    clientModel, outboundModel, requestID string,
    capture *audit.StreamCapture,
    pc *pendingCapturer,
) (outcome StreamOutcome)

// NEW signature
func StreamAnthropicSSEToOpenAI(
    w http.ResponseWriter,
    resp *http.Response,
    clientModel, outboundModel, requestID string,
    capture *audit.StreamCapture,
    pc *pendingCapturer,
    diagnostics *DiagnosticContext, // NEW
) (outcome StreamOutcome)
```

**Step 3: Add diagnostic calls inside streaming functions**

Example locations in `StreamAnthropicSSEToOpenAI`:

```go
// After reading upstream response body
if diagnostics != nil && diagnostics.RawLogger != nil {
    diagnostics.RawLogger.LogResponse(requestID, "anthropic", upstreamBody, true)
}

// After protocol conversion
if diagnostics != nil && diagnostics.Semantic != nil {
    diagnostics.Semantic.AnalyzeResponse(requestID, irResponse)
}

// On conversion errors
if diagnostics != nil && diagnostics.Anomaly != nil {
    diagnostics.Anomaly.ReportAnomaly(requestID, "conversion_error", details)
}
```

**Step 4: Update call sites in main.go** to pass DiagnosticContext

```go
// cmd/gateway/main.go (~line 896)
routingExec.AnthropicToOpenAIStream = func(
    w http.ResponseWriter,
    resp *http.Response,
    clientModel, outboundModel, requestID string,
    capture *audit.StreamCapture,
    pc any,
) executors.StreamOutcome {
    pCast, _ := pc.(*streaming.PendingCapturer)
    
    // NEW: Create diagnostic context from Executor fields
    diagnostics := &streaming.DiagnosticContext{
        RawLogger: routingExec.RawDataLogger,
        Anomaly:   routingExec.AnomalyReporter,
        Semantic:  routingExec.SemanticAnalyzer,
    }
    
    return streaming.StreamAnthropicSSEToOpenAI(
        w, resp, clientModel, outboundModel, requestID,
        capture, pCast,
        diagnostics, // NEW
    )
}
```

**Step 5: Apply to all streaming functions**

Functions to modify (in `domains/streaming/`):
- `StreamAnthropicSSEToOpenAI` (anthropic_bridge.go:241)
- `StreamOpenAIToAnthropicSSE` (anthropic_bridge.go ~line 400+)
- `StreamAnthropicPassthrough` (anthropic_bridge.go:70)
- `StreamOpenAIToResponsesSSE` (responses_bridge.go)
- `StreamChatWithPendingCapture` (if applicable)

**Step 6: Test data capture**

1. Deploy to 154
2. Send test request:
   ```bash
   curl -X POST http://154/v1/chat/completions \
     -H "Authorization: Bearer <valid-key>" \
     -H "Content-Type: application/json" \
     -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"test"}],"stream":true}'
   ```
3. Verify log file contains data:
   ```bash
   ssh 154 'tail -f /var/log/llm-gateway/raw_data/*.jsonl'
   ```

---

## Key Files (§7)

### Modified in Phase 1
- `cmd/gateway/main.go` - Diagnostic initialization (lines 876-924)
- `domains/streaming/executors/executor.go` - Interface definitions, Executor fields
- `domains/streaming/executors/diagnostic_adapters.go` (NEW) - Adapter implementations

### To Modify in Phase 2
- `domains/streaming/diagnostic_context.go` (NEW) - DiagnosticContext struct
- `domains/streaming/anthropic_bridge.go` - Add diagnostics parameter to streaming functions
- `domains/streaming/responses_bridge.go` - Add diagnostics parameter
- `cmd/gateway/main.go` - Update function pointer assignments to pass DiagnosticContext

### Existing Diagnostic Components (Don't Modify)
- `internal/logging/raw_data_logger.go` - Raw data logger implementation
- `internal/logging/anomaly_reporter.go` - Anomaly reporter implementation
- `internal/ir/semantic_analyzer.go` - Semantic analyzer implementation

### Documentation
- `DIAGNOSTIC_INTEGRATION_SUMMARY.md` - Current state documentation
- `DEPLOYMENT_STATUS_154.md` - Deployment status
- `AUDIT_CONCURRENCY_DIAGNOSTICS.md` - Original audit document

---

## Context & Constraints (§8)

### Architecture Context

**USRM v2:** Unified Request State Manager version 2 is the current production architecture. The `Executor` struct in `domains/streaming/executors/` is the central request handler.

**Streaming Path:**
```
HTTP Request → Router → Executor.Execute()
                          ↓
                     Executor.executeStreaming()
                          ↓
                     Calls function pointers:
                     - AnthropicToOpenAIStream
                     - OpenAIToAnthropicStream
                     - etc.
                          ↓
                     domains/streaming/ functions
                     (StreamAnthropicSSEToOpenAI, etc.)
```

**Why We Chose Executor Over IRTransport:**
- `IRTransport` exists but is not used in production (dead code prepared for future refactor)
- `TransportFactory` is never instantiated
- Actual streaming uses `domains/streaming/` functions directly
- Executor is the active integration point for all production requests

### Performance Constraints

- **Async I/O Required:** Raw logging must not block request handling
  - `RawDataLogger` already uses async writes (✅)
  - `AnomalyReporter` batches reports (✅)
  
- **Memory Budget:** ~5MB total overhead
  - RawDataLogger: ~2MB buffer
  - AnomalyReporter: ~1MB queue
  - SemanticAnalyzer: stateless, minimal overhead

- **Disk Space:** Monitor `/var/log/llm-gateway/raw_data/`
  - Current: 200MB per file, auto-rotation
  - TODO: Add cleanup policy for old files

### Testing Requirements

1. **Unit Tests:**
   - Test streaming functions with nil diagnostics (backward compat)
   - Test streaming functions with mocked diagnostics
   - Verify diagnostic calls don't panic on errors

2. **Integration Tests:**
   - Deploy to staging/154
   - Send streaming requests
   - Verify log files contain expected data
   - Verify no performance degradation

3. **Regression:**
   - Ensure all existing tests pass: `go test ./...`
   - Verify no changes to request handling behavior when diagnostics disabled

### Security Constraints

- **No Sensitive Data in Logs:** Raw logs may contain user content
  - Files created with 0600 permissions (✅)
  - Only root can read
  - Consider PII scrubbing in future

- **Anomaly Endpoint:** `https://llm.kxpms.cn/api/diagnostics/anomalies`
  - Ensure endpoint is trusted
  - Batch reports to limit traffic
  - Include authentication if needed

---

## Risks & Mitigations (§9)

### Risk 1: Performance Degradation from Logging
**Impact:** Medium  
**Probability:** Low  
**Mitigation:**
- RawDataLogger uses buffered async writes
- Only active when env var enabled
- Can disable without redeployment: set `LLM_GATEWAY_RAW_LOG_ENABLED=false`

### Risk 2: Signature Changes Break Existing Code
**Impact:** High  
**Probability:** Medium  
**Mitigation:**
- Add diagnostics parameter as **last** parameter (optional)
- Make DiagnosticContext nil-safe (all checks `if diagnostics != nil`)
- Test with nil diagnostics to ensure backward compatibility
- Run full test suite before deployment

### Risk 3: Log Files Fill Disk
**Impact:** High  
**Probability:** Medium  
**Mitigation:**
- File rotation at 200MB (active)
- Monitor disk usage: `df -h /var/log`
- **TODO Phase 3:** Add cleanup policy (delete files >7 days old)

### Risk 4: Anomaly Endpoint Unavailable
**Impact:** Low  
**Probability:** Medium  
**Mitigation:**
- AnomalyReporter already has timeout and retry logic
- Failures don't block request handling
- Reports are batched and dropped on persistent failure

---

## Decision Points (§10)

### Decided (Phase 1)
✅ Use Executor as integration point (not IRTransport)  
✅ Use adapter pattern to bridge internal types  
✅ Initialize via environment variables  
✅ Deploy infrastructure without data capture first (safe rollout)

### Pending (Phase 2)
❓ **DiagnosticContext location:** `domains/streaming/` or `domains/streaming/executors/`?
   - Recommendation: `domains/streaming/` (closer to streaming functions)

❓ **Capture granularity:** Log every chunk or full stream?
   - Recommendation: Full stream (combine chunks before logging)

❓ **Error handling:** Panic on diagnostic errors or log and continue?
   - Recommendation: Log and continue (diagnostics should not break requests)

❓ **Semantic analysis frequency:** Every request or sampled?
   - Recommendation: Every request when enabled (can add sampling later)

---

## Environment & Credentials (§11)

### Server 154 Configuration

**Access:**
```bash
# SSH (via alias)
ssh 154

# Service control
ssh 154 'systemctl status llm-gateway-go'
ssh 154 'systemctl restart llm-gateway-go'
ssh 154 'journalctl -u llm-gateway-go -f'
```

**Environment File:** `/etc/llm-gateway-go/env`

**Logs:**
- Service logs: `journalctl -u llm-gateway-go`
- Raw data: `/var/log/llm-gateway/raw_data/*.jsonl`

**Deployment:**
```bash
# From local machine
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-3
bash scripts/deploy-seamless.sh deploy 154 --seq <NEXT_SEQ>
```

### Required Credentials (Not Loaded)

This handoff does not require credentials to be injected. Phase 2 implementation can be done locally and tested with `go test`. Deployment to 154 will require:

```bash
env-injector inject aliyun-gateway-154
```

---

## Next Session Instructions (§12)

### Immediate Tasks

1. **Create DiagnosticContext struct**
   - File: `domains/streaming/diagnostic_context.go`
   - Export `DiagnosticContext` with 3 optional fields

2. **Modify streaming function signatures** in `domains/streaming/anthropic_bridge.go`
   - Add `diagnostics *DiagnosticContext` as last parameter
   - Update 3-4 streaming functions

3. **Add diagnostic calls inside streaming functions**
   - Log upstream responses with RawLogger
   - Report conversion errors with AnomalyReporter
   - Run semantic analysis on IR objects with SemanticAnalyzer

4. **Update call sites in main.go**
   - Construct `DiagnosticContext` from `routingExec` fields
   - Pass to streaming function calls

5. **Test locally**
   ```bash
   go build ./cmd/gateway
   go test ./domains/streaming -run TestStream
   go test ./domains/streaming/executors
   ```

6. **Deploy to 154 and verify**
   ```bash
   bash scripts/deploy-seamless.sh deploy 154 --seq 1394
   ssh 154 'tail -f /var/log/llm-gateway/raw_data/*.jsonl'
   ```

### Estimated Effort

- Implementation: **2-3 hours**
- Testing: **1 hour**
- Deployment & verification: **30 minutes**
- **Total: ~4 hours**

### Success Criteria

✅ Streaming functions accept DiagnosticContext parameter  
✅ Diagnostic calls are nil-safe  
✅ All tests pass  
✅ Deployed to 154  
✅ Log files contain captured request/response data  
✅ No performance degradation observed

---

## References (§13)

### Documentation
- Phase 1 summary: `DIAGNOSTIC_INTEGRATION_SUMMARY.md`
- Deployment status: `DEPLOYMENT_STATUS_154.md`
- Original audit: `AUDIT_CONCURRENCY_DIAGNOSTICS.md`

### Code References
- Executor interfaces: `domains/streaming/executors/executor.go:117-135`
- Adapters: `domains/streaming/executors/diagnostic_adapters.go`
- Initialization: `cmd/gateway/main.go:876-924`
- Streaming functions: `domains/streaming/anthropic_bridge.go`

### Related Commits
- Infrastructure: `40606961` feat(diagnostics): integrate diagnostic components into Executor (USRM v2)
- Version bump: `9089f54d` chore: bump version to 1393-40606961 after diagnostic integration
- Previous attempt: `aa09ef3d` feat(diagnostics): auto-enable diagnostics via environment variables

### External Resources
- USRM v2 architecture: (internal docs)
- Go testing best practices: https://go.dev/doc/tutorial/add-a-test
- Streaming SSE protocol: https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events

---

**Handoff Complete**  
**Next Agent:** Continue with Phase 2 implementation as outlined in §6 and §12.
