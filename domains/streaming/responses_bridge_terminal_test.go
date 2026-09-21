package streaming

// R36 (2026-09-17 audit) — the streaming Responses bridge must classify
// upstream finish_reason="refusal" as an abnormal terminal, matching the
// non-streaming mapping (internal/ir mapFinishReasonToResponsesStatus →
// incomplete + content_filter). Previously a refusal-only streaming response
// rendered as a successful "completed" envelope.

import "testing"

func TestOpenaiFinishReasonIsError_IncludesRefusal(t *testing.T) {
	for _, fr := range []string{"content_filter", "refusal", "network_error", "sensitive", "error"} {
		if !openaiFinishReasonIsError(fr) {
			t.Errorf("finish_reason %q must be classified as terminal error", fr)
		}
	}
	for _, fr := range []string{"", "stop", "length", "tool_calls"} {
		if openaiFinishReasonIsError(fr) {
			t.Errorf("finish_reason %q must not be classified as terminal error", fr)
		}
	}
}
