package executors

import "github.com/kaixuan/llm-gateway-go/internal/emptyoutcome"

// Wave4-D2 (2026-09-22): this file used to carry full copies of the
// streaming empty-response classifiers (isNonStreamEmptyResponse ≈
// streaming.isEmptyUpstreamChatResponse, chatContentHasOutput,
// isEmptyAnthropicMessagesResponse), duplicated because the executors
// sub-package cannot import its parent (streaming) package — and we need to
// detect empty non-stream responses BEFORE WriteHeader so the outer candidate
// loop can transparently failover to the next credential instead of returning
// a terminal 502 to the client.
//
// The canonical table now lives in internal/emptyoutcome, importable from
// both sides; these aliases keep the executor call sites stable.
//
// One adjudicated divergence from the old copy: the chat classifier now uses
// the streaming authority verbatim, which (a) checks EVERY choice rather than
// only choices[0] (a multi-choice body whose first choice is empty but second
// carries output is no longer misjudged empty) and (b) classifies foreign
// envelopes (Anthropic/Responses shapes) by their own rules instead of
// "no choices key ⇒ empty" — both in the safe direction: a body carrying
// output is no longer failed over because of a gateway-side classification
// gap (R16 policy). Chat-shaped bodies, the only legal input on the chat
// executor path, behave identically.

func isNonStreamEmptyResponse(body []byte) bool {
	return emptyoutcome.ChatBodyIsEmpty(body)
}

func isEmptyAnthropicMessagesResponse(body []byte) bool {
	return emptyoutcome.AnthropicMessagesBodyIsEmpty(body)
}
