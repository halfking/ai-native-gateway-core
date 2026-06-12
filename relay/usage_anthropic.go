package relay

import "github.com/kaixuan/llm-gateway-go/audit"

// extractAnthropicStreamUsage reads the input/output token counts that
// StreamAnthropicPassthrough accumulates in the side-channel audit
// capture (via observeAnthropicPayload's message_start + message_delta
// handlers) and returns them as pointers so the caller can pass them
// straight to the audit row.
//
// Behavior:
//   - nil capture → (nil, nil) so the caller can fall back to body
//     parsing (the non-stream path that does not go through
//     StreamAnthropicPassthrough).
//   - non-nil capture → returns the pointer fields verbatim. They are
//     nil until the first message_start (input) / message_delta
//     (output) event arrives, which is the expected state for very
//     short or interrupted streams.
//
// This is a thin convenience wrapper; the heavy lifting happens in
// audit/stream_capture.go and relay/anthropic_passthrough_stream.go.
func extractAnthropicStreamUsage(capture *audit.StreamCapture) (input *int, output *int) {
	if capture == nil {
		return nil, nil
	}
	return capture.InputTokens, capture.OutputTokens
}
