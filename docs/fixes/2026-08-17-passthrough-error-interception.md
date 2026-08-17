# Fix: Q4 Anthropic passthrough upstream error interception (2026-08-17)

## Problem

Clients (Claude-Code-style, anthropic-messages protocol) received the relay's
internal diagnostic blob verbatim during provider node switch / retry:

```
Network connection failed for the provider request.
Turn execution failed
provider=ef7bed64-… model=glm-5.2 request=3117711a-… reason=network_error retryable=true
terminated
other side closed
```

Three defects in `StreamAnthropicPassthroughWithDiagnostics`
(`domains/streaming/anthropic_bridge.go`, the Q4 anthropic↔anthropic path
wired from `cmd/gateway/main.go`):

1. **Raw error-frame forwarding.** Upstream `event: error` frames were
   forwarded byte-for-byte (documented passthrough contract), surfacing the
   relay's multi-line internal diagnostics to the client as assistant-visible
   stream content.
2. **False success on error + clean EOF.** When the relay sent the error
   event and then closed the connection cleanly, the read loop hit EOF and
   returned a non-interrupted outcome — the failed turn was recorded as a
   success (`capture.MarkStreamError` was audit-only).
3. **Duplicate-content failover.** `RecordChunkSent` was only wired on the
   OpenAI→OpenAI bridge (`domains/streaming/stream.go`). The passthrough
   never advanced `capture.chunksSent`, so the executor's
   `mayRetryInterruptedStream` (which blocks retry when
   `ChunkCountersSnapshot() > 0`) always saw 0 and approved a candidate
   failover **after content frames were already client-visible** — the next
   candidate re-streamed from `message_start`, duplicating content and
   breaking client stream parsers.

## Fix

- **Interception before forwarding.** An `event: error` declaration (held
  until its data payload arrives) or a data payload shaped
  `{"type":"error",…}` / `{"error":{…}}` is intercepted instead of
  forwarded. The raw payload stays on the audit side channels
  (`pendingCapturer` + raw diagnostics log) only.
- **Classification.** `classifyAnthropicStreamError` maps known Anthropic
  error.type tokens (`overloaded_error`, `rate_limit_error`,
  `authentication_error`/`permission_error`, `timeout`) to errorsx kinds and
  falls back to body-pattern classification, defaulting to
  `KindUpstreamDown` so in-stream terminal errors stay retryable-classified.
- **Transparent retry preserved.** While the attempt commit gate holds the
  frames uncommitted (buffered mode, nothing client-visible), the
  interception renders nothing and leaves `Resumable=true` — the executor
  fails over to the next candidate and the client never sees the blob.
- **Structured terminal error.** When frames of the attempt are already
  client-visible (gate committed / immediate mode), the gateway renders its
  own envelope — `event: error` +
  `{"type":"error","error":{"type":"upstream_error","message":"upstream
  stream error: <type>"}}` — instead of the relay blob, sets
  `Resumable=false`, and pins `capture.RecordChunkSent()` so the retry gate
  cannot approve a duplicate-content retry. The same rendering applies to
  mid-stream read failures ("other side closed" etc.) after commit, which
  previously left the client stream dangling with no terminal event.
- **Ordering note.** `finalizePassthroughInterruption` must run before
  `capture.MarkInterruptedWithReason`: the mark pins the chunk counters
  (2026-07-28 §5.5) and would no-op the `RecordChunkSent` pin.

## Not changed

- Non-error frames remain byte-for-byte passthrough (see
  `TestStreamAnthropicPassthrough_BytesForPassThrough` and the
  `TestGateWriterEndToEndAnthropicPassthroughByteIdentity` gate identity
  test).
- The legacy `domains/transformation/anthropic/anthropic_passthrough_stream.go`
  copy is not production-wired (main.go uses the streaming bridge; the
  transport-layer `LegacyTransport.ConvertStream` has no production caller)
  and was left untouched.
- Diagnostic blobs embedded as `text_delta` *content* (rather than error
  events) still pass through; the observed production shape is the error
  event, which is now intercepted.

## Tests

`domains/streaming/anthropic_passthrough_error_intercept_test.go` pins:
blob suppression + transparent retryability (nothing client-visible),
structured error rendering + non-resumability after commit, error + clean
EOF no longer records success, standalone error payloads, sent-chunk pinning
on read failure, terminal semantics of a bare `event: error` declaration,
malformed-payload classification, and payload-shape detection.
