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
		name    string
		kind    errorsx.ErrorKind
		detail  string
		want    string
	}{
		// Executor-classified Kind takes precedence.
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
		// Fallback by detail code when Kind is empty.
		{"detail-stream_panic", "", "stream_panic", "stream_panic"},
		{"detail-first_byte_timeout", "", "first_byte_timeout", "stream_timeout"},
		{"detail-stream_chunk_timeout", "", "stream_chunk_timeout", "stream_timeout"},
		{"detail-json_error_in_stream", "", "json_error_in_stream", "upstream_error"},
		{"detail-client_cancel", "", "client_cancel", "client_cancel"},
		{"detail-client_disconnected", "", "client_disconnected", "client_cancel"},
		{"detail-concurrent_overload", "", "concurrent_overload", "concurrent_overload"},
		{"detail-empty_stream_no_content", "", "empty_stream_no_content", "empty_response"},
		{"detail-anthropic_to_openai_read_error", "", "anthropic_to_openai_read_error", "stream_read_error"},
		{"detail-read_error", "", "read_error", "stream_read_error"},
		{"detail-eof_without_done", "", "eof_without_done", "stream_read_error"},
		{"detail-empty", "", "", "stream_error"},
		{"detail-unknown", "", "unknown_thing", "stream_error"},
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