# Phase E: Responses API Protocol Slot in IR Matrix (2026-07-01)

## Problem

ZCode UI's `/v1/responses` endpoint failed schema validation when routed to an
`anthropic-messages` upstream (apiclaude provider 587). The client expected
OpenAI Responses API SSE events (`response.output_text.delta` etc.) but
received raw `chat.completion.chunk` (`data: {"choices":[...]}`) — the
Q3 OpenAI translator was wired instead of a Responses translator.

```
$ curl -N -X POST .../v1/responses -d '{"model":"claude-sonnet-5","input":"hi","stream":true}'
data: {"choices":[{"delta":{"role":"assistant"},"finish_reason":null,"index":0}],
       "id":"chatcmpl-...","model":"claude-sonnet-5","object":"chat.completion.chunk"}

[ZCode] Type validation failed: Value: {...chat.completion.chunk...} ... expected response.output_text.delta
```

### Root Cause

Two layered bugs:

**Bug 1 — Responses path completely missing from IR matrix.**
`domains/streaming/responses.go:425` hard-coded `ClientProtocol: "openai-completions"`
for the `/v1/responses` handler. Combined with the hand-written
`StreamResponsesSSE` (which expected OpenAI `chat.completion.chunk` upstream
format), the result was:
- /v1/responses request → routed to apiclaude (anthropic upstream)
- Anthropic SSE → executor picks `OpenAITranslator` (because ClientProtocol="openai-completions")
- OpenAITranslator emits `chat.completion.chunk` → Responses API client rejects

**Bug 2 (discovered during Step 6 verification).**
`internal/ir/detect.go` had a model-hint branch that mis-classified minimal
Chat Completions bodies (e.g. `{model:"claude-sonnet-5", messages:[...]}`)
as `anthropic-messages`. With ClientProtocol wrongly set to "anthropic-messages",
`executor_anthropic.go:535` skipped the OpenAI translator wiring, causing
**/v1/chat/completions → apiclaude** to also leak raw Anthropic SSE.

The model-name "claude" tipped the result in the ambiguity branch whenever
openAIScore (from `messages[]` field, normalized 0.1875) was below the
0.2 threshold — a classic boundary bug in score normalization.

## Fix

### IR-First Design (Phase E roadmap step)

Per `executor.go:163-175` design contract:

> Complexity reduced from O(N²) to O(N): adding a new protocol only
> requires one Parser + one Serializer.

Adding the Responses API client target to the IR matrix required:

| File | Change |
|---|---|
| `internal/ir/stream.go` | +`StreamChunk.SerializeResponses(itemID)` — per-chunk SSE event emitter |
| `internal/ir/response.go` | +`SerializeResponsesResponse(ir, clientModel)` — non-stream complete body |
| `internal/ir/detect.go` | Fix model-hint ambiguity threshold (Bug 2 fix) |
| `domains/streaming/executors/executor.go` | +`AnthropicToResponsesSSEFunc`, `OpenAIToResponsesSSEFunc` types; +`ResponsesTranslator` hook on `AnthropicExecutor`; extend `IRConverter` interface |
| `domains/streaming/executors/executor_anthropic.go` | Wire `ae.ResponsesTranslator` for anthropic upstream → Responses client; dispatch priority `ResponsesTranslator > OpenAITranslator > Passthrough`; non-stream path uses `IR.SerializeResponsesResponse` when ClientProtocol="openai-responses" |
| `domains/streaming/executors/executor_chat.go` | OpenAIToResponses bridge branch for OpenAI upstream → Responses client |
| `domains/streaming/responses_bridge.go` | NEW — `StreamAnthropicSSEToResponses` + `StreamOpenAIToResponsesSSE` orchestrators; `responsesScaffold` helper for shared initial/final event scaffolding; `toolCallIDs` state map to track tool_use id across Anthropic input_json_delta chunks |
| `domains/streaming/responses.go` | Change `ClientProtocol` from "openai-completions" to "openai-responses"; remove now-redundant `StreamWrapper` |
| `cmd/gateway/main.go` | Wire `routingExec.AnthropicToResponsesStream` and `routingExec.OpenAIToResponsesStream` |
| `domains/transformation/ir_converter.go` | `IRConverterAdapter` + `TransportIRConverter` implement `SerializeResponses`/`SerializeResponsesResponse` |
| `domains/transformation/ir_transport.go` | `serializeClientChunk` and `serializeResponse` switch on clientProtocol → "openai-responses" branch |

### `SerializeResponses` Wire Format

Per chunk, emits ONE event (or more if multiple sub-events in one delta):

| StreamChunk type | Event emitted |
|---|---|
| `ChunkTypeDelta` (text) | `event: response.output_text.delta` |
| `ChunkTypeDelta` (reasoning) | `event: response.reasoning_text.delta` |
| `ChunkTypeDelta` (tool call start) | `event: response.output_item.added` (with type=function_call) |
| `ChunkTypeDelta` (tool call args) | `event: response.function_call_arguments.delta` |
| `ChunkTypeError` | `event: error` |
| `ChunkTypeDone`/`ChunkTypeUsage` | (empty — orchestrator aggregates into `response.completed`) |

Orchestrator (`responses_bridge.go`) emits opening scaffold:
```
event: response.created
event: response.output_item.added
event: response.content_part.added
```

And closing scaffold:
```
event: response.output_text.done
event: response.output_item.done
event: response.completed
  (with accumulated usage: input_tokens, output_tokens, total_tokens)
```

### Tool Call ID Tracking

Both Anthropic's `input_json_delta` and OpenAI's subsequent tool_calls chunks
only carry the `index` of the tool, not the `id`. The Responses API client
requires every `response.function_call_arguments.delta` event to carry
the correct `item_id` so it can correlate partial arguments to the right
function call.

The bridge maintains a `toolCallIDs map[int]string` (index → tool_use id)
populated when a `tool_use` is first seen (with id) and consulted on every
subsequent delta for that index. Without this state, item_id would be
empty on all deltas after the first one.

### ID Convention Stability

To preserve client-side dedup / replay tokens across the bridge rollout:
- `response.id` = `"resp_" + requestID[:24]`
- `message.id` = `"msg_" + requestID[8:24]`
- `function_call.id` = `"{messageID}_fc_{i}"`

This matches the pre-bridge convention used by `StreamResponsesSSE`, so existing
client bookmarks/replays continue to work after upgrade.

## Verification

### Unit Tests (28+ new tests, all passing)

| File | Tests |
|---|---|
| `internal/ir/stream_test.go` | 12× `TestStreamChunk_SerializeResponses_*` |
| `internal/ir/response_test.go` | 12× `TestSerializeResponsesResponse_*` |
| `domains/streaming/responses_bridge_test.go` | 12× bridge tests (full-flow / multi-arg-chunk / second-tool-call / OpenAI-format-detection / ID-derivation / IR-mapping / scaffold-round-trip / no-usage / length→incomplete / etc.) |
| `domains/streaming/executors/executor_anthropic_protocol_fix_test.go` | 4× dispatch tests |
| `domains/transformation/legacy_transport_test.go` | Updated to assert correct post-fix detect behavior |
| `internal/ir/detect_test.go` | `TestDetectProtocol_MinimalClaudeChatCompletion` regression test + rewritten `TestDetectProtocol_AnthropicBody` + new `TestDetectProtocol_ChatCompletionWithAnthropicFields` |

### Live Deployment on 184 (2026-07-01)

Image: `127.0.0.1:5000/kx-llm-gateway-go:gitsha-bdc48509-fix2`
Deployed via `k3s kubectl set image ...` then waited for `1/1 Running`.

`/v1/responses` end-to-end (curl):
```
event: response.created
event: response.output_item.added
event: response.content_part.added
event: response.output_text.delta (×3 chunks)
event: response.output_text.done
event: response.output_item.done
event: response.completed (status: completed, usage: input=14, output=16, total=30)
```

DB row verification:
```
request_mode = responses
success = true
stream_chunk_count = 3
```

`/v1/chat/completions` end-to-end (bonus fix verification):
```
data: {"choices":[{"delta":{"role":"assistant"}}], "object":"chat.completion.chunk", ...}
data: {"choices":[{"delta":{"content":"Hey! How can I help you today?"}}], "object":"chat.completion.chunk", ...}
data: {"choices":[{"delta":{},"finish_reason":"stop"}], "object":"chat.completion.chunk", ...}
data: [DONE]
```

DB row verification:
```
request_mode = chat
success = true
stream_chunk_count = 5
```

## Known Issues (Out of Scope for Phase E)

### Pre-existing Panic Blocking HEAD Build

A duplicate `log.archive_days` settings spec is registered in both
`settings/spec_logs.go:83` and `settings/spec_storage.go:91`. This causes
`MustRegisterSpec` to panic at startup with the message
`settings: spec "log.archive_days" already registered`. The panic is
intermittent — the production binary (gitsha-bdc48509-fix2) starts
successfully because of build/init ordering, but newer builds from HEAD
trigger it.

This is **not introduced by Phase E** (the duplicate registration predates
the work — see commit 1ca45635 which added storage settings). It needs to
be fixed in a separate PR by either:
- removing the duplicate from `spec_logs.go`, OR
- changing the key on `spec_storage.go` to a storage-specific name like
  `storage.archive_days`

Until that's done, deployments from HEAD will CrashLoopBackOff and need
to be pinned to gitsha-bdc48509-fix2 (the last known-good image).

### Tool Call ID Fix in HEAD But Not in Production

The `toolCallIDs` bridge state was committed in 2e3e26e7 (Round 44 lint)
but the running binary (fix2) was built before that commit. Edge case
tests in commit 0c1a31bc pin the contract but the production fix is
blocked by the duplicate-settings panic above.

## Lessons Learned

1. **`ir.DetectProtocol` model-hint branch** is fragile. The score normalization
   thresholds (0.1875 for messages-only, 0.119 for single anthropic extension)
   leave a narrow band where one protocol's score can dominate only after the
   model hint tip. Future additions to the scoring function should:
   - Always favor **body shape** (`messages[]`, `system`+`messages`+`content[]`-blocks)
     over model name.
   - Only use model hint as tiebreaker for **truly empty bodies**.

2. **`ClientProtocol` must match the handler's actual protocol target.**
   Hard-coding `"openai-completions"` for `/v1/responses` was wrong from day one
   and was only caught because the Responses client got a `chat.completion.chunk`
   instead of `response.output_text.delta`. Future handlers should use distinct
   `ClientProtocol` values (`"openai-completions"`, `"openai-responses"`,
   `"anthropic-messages"`) so the executor's translator dispatch can pick
   correctly.

3. **Tool call ids require bridge state.** Both Anthropic and OpenAI streaming
   protocols only send the tool id in the FIRST chunk; subsequent chunks
   only carry the index. The IR StreamChunk is stateless by design (good
   for testability), but bridge orchestrators need per-stream state to
   correctly attribute partial arguments to the right function call.

4. **Adding a new IR protocol slot is cheap when the IR is set up well.**
   Total LOC for Phase E: ~3,000 lines (mostly tests + scaffolding). The
   contract "one Parser + one Serializer per protocol" held: we added 2
   Serializers and 0 Parsers (no upstream speaks Responses yet).

5. **Settings spec deduplication is fragile.** Two commits adding the same
   setting key (under different categories) is a setup-time landmine.
   Consider a CI check that greps for duplicate `Key:` fields in
   `settings/spec_*.go`.