// Package anthropic — stream_support.go holds the one stream-level helper
// that survived the 2026-09-14 dead-bridge retirement (R27 audit §三 #1,
// following the 97aa179ab precedent). The retired copy carried a full
// StreamOpenAIToAnthropicSSE bridge with no production callers — the live
// bridge is domains/streaming/anthropic_stream.go (8-param, commit-gated).
//
// Live package surface (verified by selector scan, 2026-09-14):
//   - IsAnthropicStreamEmpty          ← streaming/responses_bridge.go, streaming/anthropic_bridge.go
//   - ValidateStreamingToolArgs       ← streaming/anthropic_bridge.go (tool_args_assembler.go)
//   - ConvertChatRequestToAnthropic   ← streaming/anthropic_bridge.go (chat_to_anthropic.go)
//   - ConvertAnthropicResponseToChat  ← streaming/anthropic_bridge.go (anthropic_to_chat.go)
package anthropic

// IsAnthropicStreamEmpty reports whether an Anthropic-compatibility stream
// must be surfaced as KindEmptyResponse so the executor fails over to the
// next candidate. Mirrors the raw passthrough detector and the Q3 detector
// (audit-24h-20260828-r3 P1-B parity).
//
// clientDisconnectPending is deliberately distinct from merely having a
// pending capturer. A capturer is also installed for ordinary requests so a
// later client disconnect can be replayed; it must not turn a clean upstream
// empty response into a success. Only a stream that actually observed a
// client write failure may preserve completed-replay semantics.
//
// Usage tokens do not make a response semantically non-empty. Providers can
// report prompt/output accounting while returning no assistant content; the
// candidate loop must receive KindEmptyResponse for that shape as well.
func IsAnthropicStreamEmpty(emittedContent bool, inputTokens, outputTokens int, clientDisconnectPending bool) bool {
	_ = inputTokens
	_ = outputTokens
	return !emittedContent && !clientDisconnectPending
}
