package streaming

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// TestStreamErrorKindForDetailCode covers the 2026-07-28 §5.6
// rewrite: the executor-classified Kind (StreamOutcome.Kind) takes
// precedence over the legacy detail-code mapping.
func TestStreamErrorKindForDetailCode(t *testing.T) {
	cases := []struct {
		name   string
		kind   errorsx.ErrorKind
		detail string
		want   string
	}{
		// 2026-07-29 (decomposed) — see handler.go streamErrorKindForDetailCode.
		// Detail-code-only paths (legacy callers / tests not going through executor).
		{"stream_chunk_timeout", "", "stream_chunk_timeout", "stream_timeout"},
		{"stream_timeout", "", "stream_timeout", "stream_timeout"},
		{"chunk_timeout", "", "chunk_timeout", "stream_timeout"},
		{"first_byte_timeout", "", "first_byte_timeout", "stream_timeout"},
		{"concurrent_overload", "", "concurrent_overload", "concurrent_overload"},
		{"concurrent", "", "concurrent", "concurrent_overload"},
		{"empty_stream_no_content", "", "empty_stream_no_content", "empty_response"},
		// 2026-07-29: Decomposed from stream_read_error.
		{"eof_without_done", "", "eof_without_done", "eof_without_done"},
		{"read_error", "", "read_error", "stream_read_error"},
		{"stream_read_error", "", "stream_read_error", "stream_read_error"},
		{"stream_panic_detail", "", "stream_panic", "stream_panic"},
		{"stream_panic_recover", "", "stream_panic_recover", "stream_panic"},
		{"json_error_in_stream", "", "json_error_in_stream", "upstream_error"},
		{"client_cancel_detail", "", "client_cancel", "client_cancel"},
		{"client_disconnected", "", "client_disconnected", "client_cancel"},
		{"anthropic_to_openai_read_error", "", "anthropic_to_openai_read_error", "stream_read_error"},
		{"anthropic_to_responses_read_error", "", "anthropic_to_responses_read_error", "stream_read_error"},
		{"empty_detail", "", "", "stream_error"},
		{"unknown_detail", "", "something_unknown", "stream_error"},
		// Executor-classified Kind takes precedence (2026-07-28 §5.6 / Task 11).
		{"kind-timeout", errorsx.KindStreamTimeout, "", "stream_timeout"},
		{"kind-timeout-generic", errorsx.KindTimeout, "", "stream_timeout"},
		{"kind-concurrent", errorsx.KindConcurrent, "", "concurrent_overload"},
		{"kind-rate-limit", errorsx.KindRateLimit, "", "concurrent_overload"},
		{"kind-empty", errorsx.KindEmptyResponse, "", "empty_response"},
		{"kind-cancel", errorsx.KindCanceled, "", "client_cancel"},
		{"kind-client-bug", errorsx.KindClientBug, "", "client_cancel"},
		{"kind-upstream", errorsx.KindUpstreamDown, "", "upstream_error"},
		{"kind-network", errorsx.KindNetwork, "", "upstream_error"},
		{"kind-conversion", errorsx.KindConversion, "", "conversion_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome := &StreamOutcome{Kind: tc.kind, Reason: tc.detail}
			got := streamErrorKindForDetailCode(outcome, tc.detail)
			if got != tc.want {
				t.Errorf("got %q want %q (kind=%q detail=%q)", got, tc.want, tc.kind, tc.detail)
			}
		})
	}
}

// TestStreamErrorKindForDetailCode_Nil covers the defensive
// guarantee for callers that pass a nil outcome.
func TestStreamErrorKindForDetailCode_Nil(t *testing.T) {
	if got := streamErrorKindForDetailCode(nil, "stream_panic"); got != "stream_panic" {
		t.Errorf("nil outcome detail=stream_panic got %q want stream_panic", got)
	}
	if got := streamErrorKindForDetailCode(nil, ""); got != "stream_error" {
		t.Errorf("nil outcome detail=\"\" got %q want stream_error", got)
	}
}
