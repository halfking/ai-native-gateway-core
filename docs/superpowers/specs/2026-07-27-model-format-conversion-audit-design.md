# Model Format Conversion, Streaming & Session Audit — Design

**Date:** 2026-07-27
**Status:** Approved (recommended plan)
**Scope:** Request data fidelity, tool message preservation, format anomaly
detection, raw audit capture, session/request correlation, concurrency.

## 1. Context

Production feedback between 2026-07-22 and 2026-07-26 surfaced two
recurring failures:

1. **"First request empty, second request fine"** with minimax-m3 (and
   less often other models). The first outbound request to the upstream
   either produces no body content (empty stream / failover) or the
   client never sees the body. The second request recovers.
2. **"Tool-related response missing"** — the model returns successfully
   but the gateway drops `tool_calls` / `tool_use` / `tool_result` data
   before reaching the client. minimax-m3 is the worst observed case.

Read-only audit (`docs/` agent reports) found:

- `Chat` writes a provisional `X-Gw-Session-Id` header *before* reading
  the body, so a body-supplied `session_id` is shadowed on the first
  request. The second request echoes the gateway session header and
  therefore matches the correct session.
- `/v1/messages` always sets `ToolsRequested: false` even when the
  request body carries `tools`, which disables XML→structured tool-call
  coercion and corrupts downstream diagnostics.
- `IR.ParseAnthropicResponse` does not copy `tool_use.input` into
  `ResponseToolCall.Arguments`; non-stream Anthropic→OpenAI conversion
  emits empty `tool_calls.arguments`.
- Several conversion paths silently replace invalid tool arguments with
  `{}`, collapse array/object tool results into text, or replace missing
  tool IDs with `unknown_tool` / `__unknown_tool_<i>__`.
- Stream bridges silently drop unknown event types and unmarshal
  failures (`continue` with `slog.Warn`); no anomaly is recorded.
- Streaming telemetry conflates `KindStreamTimeout`, `KindConcurrent`,
  `KindEmptyResponse` into a single `error_kind = "stream_error"` even
  though the executor distinguishes them.
- Raw-body capture is opt-in (`LLM_GATEWAY_RAW_LOG_ENABLED=true`), the
  JSONL has no correlation envelope, and the async queue drops entries
  on overflow without surfacing per-request diagnostics.
- `Executor.asyncDepth` is a process-wide atomic counter that can
  wrongly suppress async fallback for unrelated requests.
- `SessionFollowUpCounts` in `response_interceptor_helpers.go` uses a
  global `sync.Map` and `cleanupSessionFollowUps` is dead code.

This design closes the data-fidelity / observability gap and aligns
session, error, and concurrency semantics across the three transport
paths (`chat`, `messages`, `responses`).

## 2. Goals

- Default-on, rotating raw body capture (client inbound, upstream
  outbound, upstream response, client outbound) with full correlation
  envelope.
- Faithful round-trip of `tool_use` / `tool_calls` / `tool_result` data
  across `Anthropic ↔ OpenAI ↔ Minimax` for both streaming and
  non-streaming.
- Chat session precedence matches `messages` / `responses`; the body
  session always wins over the provisional header.
- All silent message/tool/SSE drops become first-class anomalies with
  preserved raw evidence.
- Streaming error classification is preserved end-to-end.
- `go test -race` covers the touched packages.

## 3. Non-Goals

- Building a generic agent loop inside the gateway. `tool_calls` are
  still surfaced to the client; the gateway does not execute tools or
  fan-out follow-up requests in-band.
- Persisting tool execution transcripts — that work lives in
  `toolexecution` and is untouched.
- Renaming `request_logs_bodies_hot` or replacing the WAL.

## 4. High-Level Architecture

We keep the existing canonical path (`domains/streaming` + `internal/ir`)
as the only production path. `adapter/unified/*` is marked deprecated
and guarded by a build tag so it cannot be re-wired.

A new `AuditEnvelope` is introduced and propagated through every logging
boundary so raw JSONL, request log, anomaly record, and
`response_format_anomalies` share the same correlation keys.

```
                 +---------------------+
client  ───HTTP──▶  ChatHandler        │
messages ───HTTP──▶  MessagesHandler   │─── applyResolvedGatewaySession (all paths)
responses ─HTTP──▶  ResponsesHandler  │
                 +---------------------+
                              │
                              ▼
                    Executor (USRM v2)
                              │
                              ▼
   ┌──────────────────────────┴──────────────────────────┐
   │  IRTransport / Streaming bridges                   │
   │  (Parse / Serialize / Stream)                      │
   └──────────────────────────┬──────────────────────────┘
                              │ RawDataLogger (default-on)
                              ▼
               raw_data_*.jsonl (200MB × 5 files)
                              │
                              ▼
       format_anomaly_recorder → response_format_anomalies
                              │
                              ▼
       LockFreeAnomalyReporter → https://llmgo.kxpms.cn/format-anomalies
       (summary + sha256 + local log reference, NO payload)
```

## 5. Component Design

### 5.1 Session precedence (Phase 1)

New helper `domains/session/resolve.go::ResolveSession`:

1. header `X-Gw-Session-Id` (highest)
2. body `session_id` (preferred over header in chat path)
3. sessionGetter lookup / assignment
4. provisional (`gw_<uuid>`) only emitted for early-failure branches

Chat path is rewritten to:

- Read body first.
- Call `ResolveSession`.
- Call `applyResolvedGatewaySession` last (after `assignGatewaySession`).

Provisional header is only emitted inside the deferred safety net for
early-failure branches.

### 5.2 ToolsRequested fix (Phase 1)

`/v1/messages` `ExecParams.ToolsRequested` becomes:

```go
ToolsRequested: len(reqBody.Tools) > 0,
```

`/v1/responses` and the streaming bridge receive the same value.

### 5.3 IR tool fidelity (Phase 1)

`internal/ir/response.go` changes:

- `ResponseToolCall` gains `InputRaw json.RawMessage` (raw JSON of
  `tool_use.input` or `tool_calls.function.arguments`).
- `ParseAnthropicResponse` copies `block.Input` into `InputRaw` and
  populates `Arguments` via `json.Marshal` for backward compatibility.
- `ParseOpenAIResponse` parses `function.arguments` first as JSON, and
  if that fails leaves `InputRaw` with the original string for the
  anomaly recorder.
- `SerializeOpenAIResponse` / `SerializeAnthropicResponse` emit
  `InputRaw` when present, otherwise `Arguments` (legacy callers).

`internal/ir/validate_and_fix.go` changes:

- `SanitizeToolMessages` keeps the rule "drop orphan tool" but emits
  `response_format_anomalies` rows with `removed_ids`, `request_id`,
  `client_request_id`, and `gw_session_id`.
- `ValidateAndFixRequest` no longer injects `__unknown_tool__`; missing
  IDs are reported as `tool_id_missing` anomalies and the original
  `id` is preserved (empty allowed, never falsified).
- `validateAnthropicToolCallIntegrity` runs for `len(messages) > 0`
  instead of `> 2`.

### 5.4 Conversion bridges (Phase 1)

`domains/streaming/anthropic_bridge.go` and `anthropic_stream.go`:

- `ConvertAnthropicResponseToChat` copies `InputRaw` into
  `tool_calls.function.arguments` instead of marshalling a generic
  `map[string]any`. If `input` is not an object, it is preserved as
  a JSON string and a `field_shape_warning` anomaly is recorded.
- `convertBridgeChatMessageToAnthropic` keeps `tool_call_id` /
  `tool_use_id` from the original message; rejects unknown fields with
  a `passthrough` block AND records a `message_field_dropped` anomaly.
- `StreamAnthropicSSEToOpenAI` stops dropping unknown events: it
  forwards them as SSE comments to the client and records an anomaly
  with the raw frame.
- `StreamOpenAIToAnthropicSSE` stops dropping `isOpenAIFormatData`
  frames; it surfaces them as `stream_unexpected_payload` anomalies
  and continues after a warning.

### 5.5 Raw audit capture (Phase 2)

`internal/logging/raw_data_logger.go::RawDataEntry` gains:

```go
type RawDataEntry struct {
    Timestamp       time.Time
    RequestID       string
    ClientRequestID string
    GWSessionID     string
    GWTaskID        string
    ParentRequestID string
    TenantID        string
    ApplicationID   string
    APIKeyID        int
    ProviderID      int
    CredentialID    int
    AttemptNo       int
    Direction       string
    Protocol        string
    ConversionStep  string
    ChunkIndex      int
    TraceID         string
    SpanID          string
    DataSize        int
    SHA256          string
    RawData         string
    RawDataEncoding string
    Headers         map[string]string
    Error           string
}
```

`AsyncRawDataLogger` becomes:

- No drop on overflow. When the queue is full, it writes a stub
  `direction=overflow` entry with `data_size=0` and `error=
  "queue_full"`, incrementing `DropCount` *and* recording a
  `raw_log_overflow` anomaly with the request envelope.
- `Close` drains until empty or 5×`flushDelay` (5s) timeout, then
  emits a `raw_log_close_drained` audit row with totals.
- `MaxSize` is 200 MB; `KeepCount` is 5; rotation is handoff-safe via
  `rotateMu` (added) so concurrent writes cannot lose data.

`LogUpstreamResponse` and a new `LogClientResponse` are wired through
the streaming bridges so the actual wire bytes written to the client
are captured (under the existing `RawDataLogger` interface).

The default flavor:

```go
// cmd/gateway/main.go
asyncRawLogger, _ := logging.NewAsyncRawDataLogger(
    logDir, 200 * 1024 * 1024, true, 10000)
routingExec.RawDataLogger = executors.NewRawDataLoggerAdapter(asyncRawLogger)
```

Removal of `LLM_GATEWAY_RAW_LOG_ENABLED` opt-in — always on; the path
can be overridden but never disabled.

### 5.6 Format anomaly recorder (Phase 3)

`domains/streaming/format_anomaly_recorder.go` receives a new payload:

```go
type FormatAnomaly struct {
    RequestID       string
    ClientRequestID string
    GWSessionID     string
    GWTaskID        string
    ParentRequestID string
    TenantID        string
    ProviderID      int
    CredentialID    int
    AttemptNo       int
    TraceID         string
    SourceProtocol  string
    TargetProtocol  string
    ConversionStep  string
    AnomalyType     string
    RawInputHash    string
    RawInputSize    int
    RawOutputHash   string
    RawOutputSize   int
    RawLogFile      string
    RawLogOffset    int64
    Analysis        map[string]string
    Confidence      float64
    RemovedIDs      []string
    DroppedCount    int
    Severity        string
    DetectedAt      time.Time
}
```

Sampling switches to deterministic hash sampling:

```go
func shouldSample(requestID string, rate int) bool {
    h := sha256.Sum256([]byte(requestID))
    return binary.BigEndian.Uint32(h[:4])%uint32(rate) == 0
}
```

Unrecorded anomalies still bump `format_anomaly_total` (Prometheus).

### 5.7 External anomaly reporter (Phase 3)

`internal/logging/lockfree_anomaly_reporter.go`:

- `AnomalyReport` adds the same correlation keys.
- The HTTP payload omits `RawDataSample` entirely; only sends
  `RawInputHash`, `RawInputSize`, `RawOutputHash`, `RawOutputSize`,
  `RawLogFile`, `RawLogOffset`.
- The legacy `anomaly_reporter.go` (mutex-based) is removed.
- `AnomalyReporter` retries with bounded backoff; `enqueue` returns
  `false` only when the queue is permanently closed, never on transient
  failure.

### 5.8 Concurrency & error classification (Phase 4)

- `domains/streaming/request_log_pipeline.go::RequestLogContext`
  embeds a `sync.Mutex`; counters migrated to `atomic.Int64`.
- `domains/streaming/executors/executor.go` `asyncDepth` moves into
  `ExecParams` as `requestLocal semaphore` (a `chan struct{}` of size
  1 reserved on `ExecParams` and held for the executor's lifetime).
- `domains/hooks/audit/audit.go::StreamCapture` exposes `QualityFlags`
  via mutex-guarded methods.
- `domains/streaming/handler.go::emitTelemetry` writes the executor's
  `Kind` to `request_logs.error_kind` directly:
  - `stream_timeout`
  - `concurrent_overload`
  - `empty_response`
  - `client_cancel`
  - `upstream_error`
  - `conversion_error`
  - existing legacy kinds (`http_500`, etc.) preserved.
- `response_interceptor_helpers.go` adds `parent_request_id` to the
  follow-up synthetic request header and clears the per-session counter
  in `updateSessionLastSeen` (replaces the dead `cleanupSessionFollowUps`).
- `domains/streaming/keepalive_sender.go::Stop` becomes idempotent
  via `sync.Once`.

### 5.9 Tool-id mapping (Phase 1)

`internal/ir/provider_field_mapping.go`:

- Adds `GetProviderFieldConfig(providerID int)`. The executor passes
  `params.Candidates[i].ProviderID` (when available) so we don't have
  to special-case Minimax strings.
- Adds `nvidia` alias guidance: `minimaxai/*`, `minimax-*`, `minimax/*`
  and the *non-aggregator* catalog code `volcano-tokenplan` (provider
  34) all use `tool_call_id`.

### 5.10 End-to-end tests & documentation

- `internal/ir/response_test.go`: add Anthropic→OpenAI round-trip with
  non-empty `input`; add malformed `input` preserved.
- `domains/streaming/anthropic_bridge_test.go`: add multi-choice,
  parse-failure, unknown event, half-tool-drop end-to-end fixtures.
- `domains/streaming/executors/executor_anthropic_test.go`: add
  `__unknown_tool` regression (assertion that ID is preserved).
- `domains/streaming/format_anomaly_recorder_test.go`: deterministic
  sampling, hash fields, no payload.
- `internal/logging/raw_data_logger_test.go`: rotation, drop, drain.
- `domains/streaming/concurrent_session_test.go`: chat session
  shadowing regression.
- `docs/2026-07-25-format-detection-*-implementation.md` updated to
  reflect the canonical IR path.

### 5.11 Removed / guarded code

- `internal/logging/anomaly_reporter.go` (legacy mutex) deleted.
- `adapter/unified/*` annotated with `// Deprecated: production uses
  canonical IR only. May not be wired.` A build tag `no_adapter_unified`
  is added that omits the package from the production build.
- `BUGFIX_MINIMAX_REQUEST_MESSAGE_EMPTY.md`'s verification SQL is
  corrected to match `request_id`-only JOIN.

## 6. Migration / Rollout

1. Schema delta is not required; `response_format_anomalies` already
   exists with a `metadata` JSONB column. New fields are stored in
   `metadata`.
2. Deploy canary at 5% traffic. Compare `format_anomaly_total` rate
   before/after.
3. Watch `raw_log_overflow` (must be ≤ 0.01% of requests). If positive,
   raise queue size or shrink keep-count.
4. Roll out to 50%, then 100%.
5. After 7 days, delete legacy `AnomalyReporter` and `adapter/unified`.

## 7. Open Questions

- Should the gateway proactively convert `tool_calls` to in-band
  follow-up requests (true agent loop)? Out of scope for this design.
- Visual editor for anomaly playback? Out of scope.
