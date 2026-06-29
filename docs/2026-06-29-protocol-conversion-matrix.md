# Protocol Conversion Matrix — 8 Directions

Date: 2026-06-29
Scope: `llm-gateway-go` Q1–Q4 protocol conversion surface
Author: opencode-agent
Ticket: Q3 stream fix `bdd982ef` follow-up audit; Q2 response fix in this branch

This report audits every place a request or response body is converted
between **OpenAI Chat Completions** and **Anthropic Messages** in the
gateway, with focus on whether the **default production wiring**
(`LLM_GATEWAY_IR_CONVERTER` not set) actually performs the conversion
for each direction, or whether the body is forwarded unconverted.

## Quadrant taxonomy

| Quadrant | Client speaks | Upstream speaks | Direction |
| -------- | ------------- | --------------- | --------- |
| **Q1**   | OpenAI        | OpenAI          | passthrough |
| **Q2**   | Anthropic     | OpenAI          | convert |
| **Q3**   | OpenAI        | Anthropic       | convert |
| **Q4**   | Anthropic     | Anthropic       | passthrough |

Each quadrant needs two conversions (request and response). Plus there
is a **streaming** sibling for each, so the matrix has **8 base
directions × 2 (stream/non-stream) = 16 cells**. This audit covers all
8 base directions and the streaming split for each.

## IR reference layer (canonical 8-direction implementation)

`internal/ir/` provides the single source of truth for the 8 base
directions (request + response, both protocols):

| Direction                                  | Function                                                                 | Location                              |
| ------------------------------------------ | ------------------------------------------------------------------------ | ------------------------------------- |
| OpenAI request → IR                        | `ir.ParseOpenAI(body []byte) (*InternalRequest, error)`                  | `internal/ir/parse_openai.go`         |
| Anthropic request → IR                     | `ir.ParseAnthropic(body []byte) (*InternalRequest, error)`               | `internal/ir/parse_anthropic.go`      |
| IR → OpenAI request                        | `ir.SerializeOpenAI(req *InternalRequest) ([]byte, error)`                | `internal/ir/serialize_openai.go`     |
| IR → Anthropic request                     | `ir.SerializeAnthropic(req *InternalRequest) ([]byte, error)`             | `internal/ir/serialize_anthropic.go`  |
| OpenAI response → IR                       | `ir.ParseOpenAIResponse(body []byte) (*InternalResponse, error)`         | `internal/ir/response.go:172`         |
| Anthropic response → IR                    | `ir.ParseAnthropicResponse(body []byte) (*InternalResponse, error)`      | `internal/ir/response.go:95`          |
| IR → OpenAI response                       | `ir.SerializeOpenAIResponse(ir, clientModel) ([]byte, error)`            | `internal/ir/response.go:273`         |
| IR → Anthropic response                    | `ir.SerializeAnthropicResponse(ir, clientModel) ([]byte, error)`         | `internal/ir/response.go:385`         |

Each function has unit-test coverage in `internal/ir/*_test.go`. The
end-to-end round-trip tests in `internal/ir/response_test.go` confirm
the IR layer is internally consistent.

## Production wiring audit (default flags)

The production wiring happens in `cmd/gateway/main.go`. Feature flags:

- `LLM_GATEWAY_IR_CONVERTER=true`  → `routingExec.IR = &irAdapter{}`
  (8-direction IR path)
- default → legacy callbacks

### Wire-up points (post-Q2-fix)

| Field on `routingExec`        | Wired to                                                    | Site                              |
| ----------------------------- | ----------------------------------------------------------- | --------------------------------- |
| `ChatToAnthropic`             | `streaming.ConvertChatRequestToAnthropic`                   | `cmd/gateway/main.go:372`         |
| `AnthropicToOpenAI`           | `streaming.ConvertAnthropicBodyToOpenAI`                    | `cmd/gateway/main.go:373`         |
| `AnthropicToChatResponse`     | `streaming.ConvertAnthropicResponseToChat`                  | `cmd/gateway/main.go:401`         |
| `ChatResponseToAnthropic`     | `streaming.ConvertChatResponseToAnthropic` (Q2 non-stream)   | `cmd/gateway/main.go:402` (NEW)   |
| `OpenAIToAnthropicStream`     | `streaming.StreamOpenAIToAnthropicSSE` (Q2 stream)          | `cmd/gateway/main.go:414` (NEW)   |
| `AnthropicPassthroughStream`  | `streaming.StreamAnthropicPassthrough`                      | `cmd/gateway/main.go:357`         |
| `AnthropicToOpenAIStream`     | `streaming.StreamAnthropicSSEToOpenAI`                      | `cmd/gateway/main.go:386`         |

### Default-path matrix (post-Q2-fix)

| # | Quadrant | Direction | Stream | Production conversion                                              | Status |
|---|----------|-----------|--------|--------------------------------------------------------------------|--------|
| 1 | Q3       | request   | —      | `e.IR.ParseOpenAI + SerializeAnthropic` (IR), or `e.ChatToAnthropic` (legacy) | ✅ |
| 2 | Q3       | response  | non-stream | `e.IR.ParseAnthropicResponse + SerializeOpenAIResponse` (IR), or `a.ChatResponseConverter` (legacy) | ✅ |
| 3 | Q3       | response  | stream | `e.AnthropicToOpenAIStream` (fixed in `bdd982ef`)                 | ✅ |
| 4 | Q2       | request   | —      | `e.IR.ParseAnthropic + SerializeOpenAI` (IR), or `e.AnthropicToOpenAI` (legacy) | ✅ |
| 5 | Q2       | response  | non-stream | `e.IR.ParseOpenAIResponse + SerializeAnthropicResponse` (IR), or `e.ChatResponseToAnthropic` (legacy → `ConvertChatResponseToAnthropic`) | ✅ **NEW** |
| 6 | Q2       | response  | stream | `e.OpenAIToAnthropicStream` (legacy → `StreamOpenAIToAnthropicSSE`) | ✅ **NEW** |
| 7 | Q4       | request   | —      | passthrough via `AnthropicExecutor.BuildRequest`                  | ✅ |
| 8 | Q4       | response  | non-stream | passthrough via `AnthropicExecutor.WriteNonStreamResponse`        | ✅ |
| 9 | Q4       | response  | stream | passthrough via `AnthropicPassthroughStream`                       | ✅ |
| 10 | Q1      | request   | —      | passthrough via OpenAI ChatExecutor                                | ✅ |
| 11 | Q1      | response  | non-stream | `e.Normalize` only (finish_reason normalisation)                  | ✅ |
| 12 | Q1      | response  | stream | `StreamChatWithPendingCapture` (passthrough)                      | ✅ |

## Findings

### Finding 1 — Q3 stream response (resolved in `bdd982ef`)

`domains/streaming/anthropic_bridge.go`'s Q3 SSE implementation was a
byte-level passthrough. Replaced with the same IR-driven conversion
used in `domains/transformation/anthropic`. Regression test
`TestStreamAnthropicSSEToOpenAI_Opus48ReportedPayload` covers the
exact payload reported in production.

### Finding 2 — Q2 non-stream response (resolved in this branch)

`executor_chat.go` previously only called `e.Normalize(respBody, false)`
which remaps `finish_reason` strings but does not reshape the body.
The raw OpenAI `chat.completion` JSON was forwarded to the Anthropic
client.

**Fix:** before `w.Write(respBody)`, the executor now calls
`e.IR.ParseOpenAIResponse + e.IR.SerializeAnthropicResponse` when the
IR feature flag is on, or `e.ChatResponseToAnthropic` (which delegates
to `streaming.ConvertChatResponseToAnthropic`) when off. The legacy
helper mirrors `_to-be-deprecated/relay.convertChatResponseToAnthropic`
byte-for-byte and is pinned by
`TestConvertChatResponseToAnthropic_*` in
`domains/streaming/chat_to_anthropic_response_test.go`.

### Finding 3 — Q2 stream response (resolved in this branch)

`StreamChatWithPendingCapture` previously did not branch on
`ClientProtocol`. The OpenAI SSE bytes were forwarded verbatim to the
Anthropic client.

**Fix:** the executor now dispatches to
`e.OpenAIToAnthropicStream` (which delegates to
`streaming.StreamOpenAIToAnthropicSSE`) when the client is Anthropic
and the upstream is OpenAI-shaped. The relay-stream re-export at
`domains/streaming/anthropic_stream.go:30` is reused.

### Finding 4 — Q3 non-stream response (skew, not a defect)

Direction 2 has both an IR path and a legacy path. Both pass their
test suites, but the duplicated logic is the maintenance risk that
motivated Phase B/C/D. Future work should remove the legacy path once
`LLM_GATEWAY_IR_CONVERTER` is GA.

## IR matrix verification

```bash
go test ./internal/ir                # IR reference layer (8 directions)
go test ./domains/streaming          # bridge, normaliser, chat_to_anthropic, streaming helpers
go test ./domains/streaming/executors  # executor wiring (Q1–Q4)
go test ./domains/transformation/...   # transport-layer converters
```

All four packages pass on this branch.

## Recommended follow-up

| Priority | Action |
| -------- | ------ |
| P2 | Remove `streaming.ConvertAnthropicBodyToOpenAI`, `ConvertAnthropicResponseToChat`, `ConvertChatResponseToAnthropic` from `domains/streaming/` once `LLM_GATEWAY_IR_CONVERTER` is GA. They will then live only in `internal/ir/` and `_to-be-deprecated/relay/`. |
| P2 | Add an end-to-end executor test that exercises `executeOpenAI` with `ClientProtocol="anthropic-messages"`, an OpenAI-shaped upstream response, and asserts the wire output is Anthropic-shaped (the legacy + IR regression tests above cover the helper layer but not the dispatch in `executor_chat.go`). |
| P3 | Audit `_to-be-deprecated/relay/` callers and migrate them to the live package; the deprecated package can then be deleted. |