# Audit Correlation & Error-Kind Remediation — Design

**Date:** 2026-07-28
**Status:** Approved (remediation of Phase 2 / Phase 4 defects found during the 2026-07-28 independent review of `7ef154e3...a2278773`)
**Scope:** Wire the correlation envelope through every `RawDataLogger` producer, fix stream counter / error-kind classification to the level design §5.8 required, and make raw-log overflow/close observable via anomalies.
**Supersedes:** portions of `2026-07-27-model-format-conversion-audit-design.md` §5.5, §5.7, §5.8 where the implementation drifted from the spec (specifically: `client_request` envelope, streaming `upstream_resp` correlation, anomaly HTTP context, raw-log locator precision, stream counter atomic wiring, `error_kind` mapping).

## 1. Background

Independent review of the five phase commits plus the two corrective commits (`e6494c09`, `a2278773`) found:

1. `Executor.Execute` calls `RawDataLogger.LogRequest` (the legacy interface) instead of an envelope-aware path, so every production `client_request` raw entry has an empty correlation envelope.
2. Streaming bridges (`logRawUpstreamFrame`) call `RawDataLogger.LogResponse` which routes through the envelope-blind `RawDataLoggerAdapter.LogResponse`, so every production SSE `upstream_response` frame lacks the correlation envelope even though non-stream calls use `LogUpstreamResponseWithEnvelope`.
3. `envelopeFromParams` only fills `GWSessionID`, `GWTaskID` (= `params.Model` ❌), `TenantID`, `APIKeyID`, `ApplicationID`. The fields the operator dashboard needs (`client_request_id`, `parent_request_id`, `provider_id`, `credential_id`, `attempt_no`, `upstream_endpoint`, `trace_id`, `span_id`) are never populated for any direction.
4. `AnomalyReporterAdapter.ReportAnomaly` invokes every `Report*` method with `context.Background()`. `WithAnomalyEnvelope` has zero call sites, so the external HTTP payload reports have empty correlation context.
5. `RawLogFile/RawLogOffset` are populated from the global async-queue `CurrentLocation`, which returns the *next* flush position and is racy under concurrent load. `RawLogOffset` is best-effort and may point to another request's entry.
6. `RequestLogContext.IncrementStreamChunksSent/IncrementStreamChunkErrors` are dead in production; only `audit.StreamCapture` counts. Both `BuildSuccessEntry` (handler.go:3651) and `BuildFailureEntry` (request_log_pipeline.go:660) read the log-context atomic, so partial-failure rows show 0 chunks.
7. `streamErrorKindForDetailCode` knows only `stream_timeout`, `concurrent_overload`, `empty_response`, `stream_read_error`, fallback `stream_error`. The interpreter driver emits `first_byte_timeout`, `json_error_in_stream`, `client_cancel`, `upstream_error`, and `stream_panic`; all of those collapse to the generic bucket. Design §5.8 explicitly listed `client_cancel`, `upstream_error`, `conversion_error` as required kinds.
8. `AsyncRawDataLogger.Log*WithEnvelope` falls back to a `slog.Warn` only when even the `direction=overflow` stub cannot be enqueued. No `raw_log_overflow` anomaly is raised, and no `raw_log_close_drained` audit row is emitted on shutdown.

This remediation addresses all eight findings in one plan so the audit guarantees in design §6.3 / §6.4 become observable.

## 2. Goals

- Every raw log entry carries the full correlation envelope regardless of which logger path wrote it.
- Every anomaly HTTP report carries the correlation envelope and the file/offset of the raw entry most tightly coupled to the anomaly.
- `StreamChunkErrors` / `StreamChunksSent` written to `request_logs` match `audit.StreamCapture` exactly.
- `error_kind` taxonomy covers every interruption code the stream bridges emit, with executor classified `Kind` taking precedence over the legacy detail-code mapping.
- Raw-log overflow and close emit `raw_log_overflow` / `raw_log_close_drained` audit rows and anomalies with correct envelopes.
- Re-run `go test -race` and the full `go test ./...` matrix without regression.

## 3. Non-Goals

- Replacing USRM v2 or the executor's failover/candidate logic.
- Storing tool call transcripts (lives in `toolexecution`).
- Changing the 200 MB / 5 file rotation policy beyond restoring the `rotateMu` handoff-safety the spec called for in §5.5.
- Redesigning `audit.StreamCapture`; we extend it, not replace it.

## 4. High-Level Architecture

```
                +----------------------+
client ─HTTP──▶│  ChatHandler         │
messages ─────▶│  MessagesHandler     │── auditCtx := AuditContextFromRequest (all three)
responses ────▶│  ResponsesHandler    │── stores per-request baseline correlation
                +----------------------+
                              │
                              ▼
                    Executor (USRM v2)
                              │  attempt := AttemptFromParams  (per candidate)
                              │  frame   := FrameCtx{chunk_index++}
                              ▼
+─────────────────────────┬────────────────────────────┐
│  IRTransport / Streaming bridges                       │
│  RawLogger.Log{ClientRequest,UpstreamRequest,          │
│             UpstreamResponse,ClientResponse}WithEnvelope(  │
│                ctx, attempt, env, body, step)         │
└────────────────────────┬───────────────────────────────┘
                              │
                              ▼
        AsyncRawDataLogger — CurrentLocation() indices raw entries by (requestID, direction, attempt)
                              │
                              ▼
       LockFreeAnomalyReporter — anomaly locator reads raw entry by (requestID, direction=upstream_*)
                                                                                    rather than global offset
```

`AuditContext` is the single source of truth for correlation. It is built in the handler from the resolved body / headers, augmented when the executor picks a candidate, and threaded through every raw/anomaly emission via `context.Context`. Where existing types cannot be re-keyed, we attach `AuditContext` to the relevant `ExecParams` / `DiagnosticContext` field so the legacy fallback paths the spec allowed are preserved, but the production paths use the new context.

## 5. Component Design

### 5.1 AuditContext (new)

```go
type AuditContext struct {
    // base (set in handler after body is parsed)
    RequestID       string
    ClientRequestID string
    GWSessionID     string
    GWTaskID        string        // resolved X-Gw-Task-Id or session.TaskID
    ParentRequestID string        // compression-recovery retry parent
    TenantID        string
    ApplicationID   string
    APIKeyID        int
    TraceID         string
    SpanID          string

    // per-attempt (refreshed by the executor when each candidate is picked)
    ProviderID       int
    CredentialID     int
    AttemptNo        int
    UpstreamEndpoint string

    // stream-only
    ChunkIndex atomic.Int64
}

func AuditContextFromRequest(r *http.Request, sn *session.Session, body []byte, kc *authentication.KeyInfo) *AuditContext
func AuditContextFromParams(p *ExecParams) *AuditContext                 // per attempt
func (c *AuditContext) RawCorrelationEnvelope() RawCorrelationEnvelope   // snapshot
func (c *AuditContext) AnomalyReportEnvelope() AnomalyReportEnvelope
```

We do not reuse `RawCorrelationEnvelope` for the runtime context because the design also expects the entry to be addressable by attempt/chunk; we keep `RawCorrelationEnvelope` and `AnomalyReportEnvelope` as wire-level DTOs that `AuditContext` populates at emit time.

### 5.2 Wire-level raw logger (interface change)

Add a single new interface that the production path uses:

```go
type AuditRawLogger interface {
    LogAudit(ctx context.Context, dir Direction, attempt AuditContext, body []byte, headers map[string]string, step string)
    RegisterAttempt(attempt AuditContext)            // called by executor at candidate pick; resets state for the new attempt
    RegisterFrame(attempt AuditContext, dir Direction, chunk int64)  // called by stream bridges per frame; returns (file, offset) for anomaly correlation
    Close(ctx context.Context)
}
```

The existing `RawDataLogger` interface remains for tests/legacy fakes, but the adapter detects envelope-blind loggers and refuses to silently drop the envelope — it returns `ErrEnvelopeRequired` so a misconfiguration surfaces in build, vet, or smoke tests.

### 5.3 Executor paths

`Execute()`:
- Build `AuditContext` once from the handler-propagated context.
- Call `e.RawDataLogger.RegisterAttempt(auditCtx)` for each `PlanCandidates` round before re-trying on a different credential.
- The first write (client request log) and every `e.logUpstreamRequest/Response/ClientResponse` uses `LogAudit` with the current attempt.

`executeAnthropic`/`executeOpenAI`:
- Look up the per-attempt `AuditContext` from the executor (via `e.RawDataLogger`) and call `LogAudit(ctx, "upstream_request", attempt, bodyBytes, headers, "post_conversion")` instead of `logUpstreamRequest`.
- Stream helpers (`StreamAnthropicSSEToOpenAI`, etc.) keep calling `logRawUpstreamFrame`, but the helper now reads envelope + chunk index from the `AuditContext` and the streaming-bridge-attached `FrameCtx`.

### 5.4 Streaming bridges

Each bridge (OpenAI chat, Anthropic, Responses) passes `*AuditContext` through `DiagnosticContext`:

```go
type DiagnosticContext struct {
    RawLogger AuditRawLogger
    Anomaly   AuditAnomalyReporter
    Semantic  SemanticAnalyzer
    Audit     *AuditContext  // new
}
```

`logRawUpstreamFrame` becomes:

```go
func logRawUpstreamFrame(d *DiagnosticContext, attempt *AuditContext, dir Direction, frame []byte) {
    file, off := d.RawLogger.RegisterFrame(attempt, dir, attempt.ChunkIndex.Add(1))
    attempt.RecordLastFrame(file, off)            // for anomaly lookup
    d.RawLogger.LogAudit(d.Audit, attempt, dir, frame, nil, step)
}
```

On stream completion or interruption (every `StreamOutcome.Interrupted` branch, including the `client_cancel` / `upstream_error` / `first_byte_timeout` / `json_error_in_stream` sites) the bridge emits a final `client_response` entry with `direction="client_response"`, the same envelope, an `end_of_stream` frame marker, and the `outcome.Reason` in a new `Reason` field of `RawDataEntry`. That closes the fourth direction.

### 5.5 Stream counter single-source-of-truth

`audit.StreamCapture` already increments. We:

- Make `LogChunkSent` / `LogChunkError` authoritative.
- `RequestLogContext` no longer tracks `StreamChunkErrors/StreamChunksSent` via the atomic mirror. The atomic-int fields are removed in favour of two pointers populated at telemetry-emit time via a single `StreamCapture.Snapshot() (chunksSent, chunkErrors int)`.
- `streamChunksSentFromLogCtx` and `streamChunkErrorsFromLogCtx` read directly from the `*audit.StreamCapture` carried on the log context; non-stream paths default to 0 as today.
- The `BuildSuccessEntry`/`BuildFailureEntry` paths use the same snapshot, so partial-failure rows now match `StreamCapture`.

### 5.6 error_kind taxonomy

`StreamOutcome` gains a `Kind errorsx.ErrorKind` field populated by the executor pre-fallback (`KindStreamTimeout`, `KindConcurrent`, `KindEmptyResponse`, `KindCanceled`, `KindUpstreamDown`, `KindConversion`, etc.). `streamErrorKindForDetailCode` becomes a *fallback* used only when `Kind == ""`:

```go
func streamErrorKindForDetailCode(outcome *StreamOutcome, detail string) string {
    if outcome != nil && outcome.Kind != "" {
        switch outcome.Kind {
        case errorsx.KindStreamTimeout, errorsx.KindTimeout:  return "stream_timeout"
        case errorsx.KindConcurrent, errorsx.KindRateLimit:     return "concurrent_overload"
        case errorsx.KindEmptyResponse:                        return "empty_response"
        case errorsx.KindCanceled, errorsx.KindClientBug:      return "client_cancel"
        case errorsx.KindUpstreamDown, errorsx.KindNetwork:    return "upstream_error"
        case errorsx.KindConversion:                           return "conversion_error"
        }
    }
    switch detail {
    case "stream_panic", "stream_panic_recover": return "stream_panic"
    case "first_byte_timeout":                   return "stream_timeout"
    case "json_error_in_stream":                 return "upstream_error"
    case "stream_chunk_timeout", "stream_timeout", "chunk_timeout": return "stream_timeout"
    case "client_cancel", "client_disconnected": return "client_cancel"
    case "concurrent_overload", "concurrent":    return "concurrent_overload"
    case "empty_stream_no_content":              return "empty_response"
    case "anthropic_to_openai_read_error", "anthropic_to_responses_read_error", "read_error", "stream_read_error", "eof_without_done":
        return "stream_read_error"
    }
    return "stream_error"
}
```

The existing `stream_error_kind_test.go` is updated to assert full coverage. A new test exercises the executor-classified `Kind` path with table-driven cases.

### 5.7 Anomaly correlation + per-request raw location

`AnomalyReporterAdapter.ReportAnomaly` now receives an `*AuditContext` (from `DiagnosticContext` or `ExecParams`) and uses it to:

- `ctx = logging.WithAnomalyEnvelope(parentCtx, auditCtx.AnomalyReportEnvelope())`
- call `ReportToolCallsMissing(ctx, …)` etc., per the existing `LockFreeAnomalyReporter` signatures
- the locator defaults to **per-request indexed lookup** through `AuditRawLogger.LookupFrame(requestID, direction)` returning the file/offset of the latest `upstream_request`/`upstream_response` for that request. Only the legacy `WithAnomalyEnvelope(nil)` path (when an `AuditContext` truly is unavailable) falls back to the global `CurrentLocation()`.

Producer sites that previously used `context.Background()` (the three branches in `AnomalyReporterAdapter.ReportAnomaly`, plus the convertError/semanticIncomplete sites) replace those calls with the audit context's `Parent()`.

### 5.8 AsyncRawDataLogger overflow / close hooks

- Add `RawDataLogger.LookupFrame(requestID, direction, attempt)` backed by an internal `sync.Map` keyed by `requestID:direction:attempt`. Each successful enqueue records `(file, offset)` under that key.
- On overflow: still emit the `direction=overflow` stub via the queue. If that also fails, call `anomalyReporter.ReportRawLogOverflow(ctx, auditCtx, droppedCount)` — a new method on `AnomalyReportEnvelope.ReportRawLogOverflow`. The anomaly is built with `Direction="overflow"` and a deterministic `anomaly_type="raw_log_overflow"`.
- On `Close(timeout)`: drain for up to `5 × flushDelay` (5 s default), then write one final `direction=close_drained` entry containing `EnqueueCount`, `DequeueCount`, `DropCount`, and `Remaining`. If the queue still has items, call `anomalyReporter.ReportRawLogCloseDrained(ctx, auditCtx, remaining)`.

### 5.9 Migration

- No new migrations; `response_format_anomalies` accepts the new `raw_log_overflow` and `raw_log_close_drained` types via the existing `metadata` JSONB column.
- `StreamChunkErrors` / `StreamChunksSent` columns keep `NOT NULL` (default 0); populate from `StreamCapture.Snapshot()`.
- Deploy with the same canary plan as Phase 4 (§6.2). Watch the new `raw_log_overflow` and `raw_log_close_drained` counts (<= 0.01%).

## 6. Errors and Edge Cases

- **Anomaly triggered before any raw entry exists:** `LookupFrame` returns `("", 0)`. The reporter writes the anomaly without file/offset, matching today's behaviour when `RawLogLocator` is nil. Logged at `INFO` so operators can correlate.
- **`AuditContext` is nil:** loggers return `ErrNoAuditContext`. The adapter emits a `slog.Error` and skips. Handlers that lose context (e.g. early-failure before resolution) mark the log entry as `direction=early_failure` so we can detect, not silently drop.
- **Stream capture snapshot taken twice** (success + failure): the second caller reads from a cached snapshot if `StreamCapture.Finalized()` is true; otherwise returns the latest counter values.
- **Concurrent counter races:** all counter writes go through `StreamCapture` under its own mutex; no double atomics.

## 7. Open Questions

- Do we need to keep the legacy `RawDataLogger` interface exposed at all? Decision: keep it only as a test seam, document as "deprecated for production".
- `KindConversion` does not exist in `errorsx` today; we add it (mapping to the existing `conversion_error` reason) so the new taxonomy lines up with the executor's `errorsx` namespace.

## 8. Verification (post-implementation)

- `go build -mod=vendor ./...`
- `go vet ./internal/ir/... ./internal/logging/... ./domains/transformation/... ./domains/streaming/...`
- New tests:
  - Chat/Messages/Responses envelope on `client_request`.
  - Non-stream + three stream bridges: full envelope on every direction, including `gw_task_id` (not the model name).
  - Concurrent stream test asserting `StreamChunksSent/StreamChunkErrors` on `request_logs` match `StreamCapture`.
  - Table-driven `streamErrorKindForDetailCode` for every emitted `Reason`.
  - Anomaly test capturing httptest endpoint and asserting the envelope + `(file, offset)` from `LookupFrame`.
  - Queue overflow + close tests asserting both stub entries *and* anomaly reports.
- Full `go test -race ./...` and `go test ./...` pass on the affected packages.
