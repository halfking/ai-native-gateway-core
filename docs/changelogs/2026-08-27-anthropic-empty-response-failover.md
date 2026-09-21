# 2026-08-27: Anthropic non-stream empty-response failover

## Context

User reported two failure modes when calling Claude models through the gateway:

1. Terminal 502 with "模型未返回任何内容 / TraceID: hydrate-trace"
2. Client-side schema validation failure: `Turn execution failed ... Type validation failed ... invalid_union`

Investigation revealed that native Anthropic Messages non-stream responses with semantically empty
content (HTTP 200, but `content: []` or all empty blocks) were being returned directly to clients
instead of triggering candidate failover like the OpenAI Chat path does.

## Changes

### Round 1: Initial implementation (`626a4118a`)

- Added `isEmptyAnthropicMessagesResponse` classifier in `executors/empty_response.go`
- Integrated empty-response detection into `executeAnthropicOnce` non-stream path
- Initial version wrapped error in `retryableError` and gated on `ClientProtocol == "anthropic-messages"`

### Round 2: Audit corrections (`dffe3f99f`)

Fixed three semantic issues discovered in first-round review:

1. **Retry semantics violation**: Changed from `retryableError` wrapper to bare `*upstream.Error`.
   `KindEmptyResponse` is deliberately outside `errorsx.IsRetryable`, so an empty 2xx body must
   fail over to the next candidate immediately rather than burning same-credential retries.

2. **Classifier drift from authority**: Aligned block semantics with handler-side
   `isEmptyAnthropicContent`:
   - `thinking` blocks with a `signature` but no text count as output
   - `redacted_thinking`, `server_tool_use`, `web_search_tool_result` count as output

3. **Q3 coverage gap**: Removed `ClientProtocol` gate. The raw upstream body is Anthropic-shaped
   on every client protocol at that point (Q3 conversion happens inside `WriteNonStreamResponse`),
   so OpenAI/Responses clients via Anthropic upstream get the same protection.

### Round 3: Resource leak (`da945d9d3`)

Non-stream path read the upstream body with `io.ReadAll` but never closed the original `resp.Body`.
`resp.Body` is replaced with a reconstructed `NopCloser` before `WriteNonStreamResponse`, and that
method's defer only closes the replacement (receiver binds at the defer statement), so the real
transport body leaked on success, empty-response failover, and read-error paths.

Fixed by adding `defer resp.Body.Close()` before `ReadAll`, mirroring `executor_chat.go`.

### Round 4: Conditional retry + passthrough leak (`722d29ca9`)

1. **Read-error retry condition**: `executeAnthropicOnce` wrapped every non-stream body read
   failure in `retryableError` unconditionally, but the retry ladder only inspects the wrapper type
   (not the kind). A mid-read client cancellation (`KindCanceled`, outside `IsRetryable`) was
   retried with backoff on a dead request. Extracted `anthropicReadBodyError` to classify first and
   wrap only `IsRetryable` kinds, matching the 4xx/5xx branch convention.

2. **Passthrough body leak**: `defaultAnthropicPassthrough` never closed `resp.Body` after
   `io.Copy`. It is the sole consumer (only reached when the `PassthroughStream` hook is unwired),
   so a deferred close is safe and required for connection reuse.

## Testing

New regression tests cover:

- Empty-response classifier: missing `content`, empty array, empty blocks, signature-bearing
  `thinking`, tool blocks
- Positive cases: text, thinking, `tool_use`, `redacted_thinking`, `server_tool_use`
- Executor boundary: empty response triggers bare `upstream.Error`, no client bytes written,
  upstream called exactly once (no same-credential retry with `maxRetries=2`)
- Read-error classification: canceled read stays unwrapped (`KindCanceled`), network read stays
  retryable (`KindNetwork`)
- Passthrough: body forwarded and closed

All tests in `domains/streaming/executors` and `domains/streaming` pass (84s combined).

## Deployment notes

- No schema or config changes
- No new environment variables
- Backward compatible: empty responses previously surfaced as terminal 502 now fail over
- Circuit/URSM behavior unchanged: empty-response failover does not call
  `recordProtocolCircuitSuccess` (matches Chat path)

## Documentation updates

- Updated `docs/runbooks/empty-response-routing.md` to document non-stream detection semantics for
  both OpenAI Chat and Anthropic Messages paths

## Related

- Chat path equivalent: `executor_chat.go:1270` (2026-07-15)
- Handler-side authority: `streaming/empty_response.go:isEmptyAnthropicContent`
- Error taxonomy: `errorsx/classify.go` `KindEmptyResponse` rationale
