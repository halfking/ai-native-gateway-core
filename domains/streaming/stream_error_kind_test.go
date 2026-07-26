package streaming

import "testing"

// TestStreamErrorKindForDetailCode ensures the operator-facing
// error_kind column distinguishes the failure modes that used to be
// reported as a generic "stream_error" bucket. The mapping is the
// source of truth for the dashboard filters in
// docs/2026-07-27-...-design.md §5.8.
func TestStreamErrorKindForDetailCode(t *testing.T) {
	cases := []struct {
		detail string
		want   string
	}{
		{"stream_chunk_timeout", "stream_timeout"},
		{"stream_timeout", "stream_timeout"},
		{"chunk_timeout", "stream_timeout"},
		{"concurrent_overload", "concurrent_overload"},
		{"concurrent", "concurrent_overload"},
		{"empty_stream_no_content", "empty_response"},
		{"eof_without_done", "stream_read_error"},
		{"read_error", "stream_read_error"},
		{"stream_read_error", "stream_read_error"},
		{"stream_panic", "stream_read_error"},
		{"", "stream_error"},
		{"something_unknown", "stream_error"},
	}
	for _, tc := range cases {
		t.Run(tc.detail, func(t *testing.T) {
			if got := streamErrorKindForDetailCode(tc.detail); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}
