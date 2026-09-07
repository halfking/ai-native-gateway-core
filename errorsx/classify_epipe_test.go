package errorsx

import (
	"errors"
	"testing"
)

// TestClassifyError_EPIPE locks the direction-agnostic EPIPE policy: broken
// pipe / epipe always classifies as KindNetwork (retryable). The error text
// alone cannot tell a client-bound write from an upstream-bound write —
// client.Do uploading a request body to a dying upstream produces the exact
// same "write tcp ...: write: broken pipe" shape — and classifying that as
// KindCanceled made request-phase upstream failures terminal (no failover,
// no credential state write). Client disconnects are handled via
// errors.Is(err, context.Canceled) and stream_recovery's own message
// matching, not here.
func TestClassifyError_EPIPE(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"upstream read broken pipe", errors.New("read tcp 10.0.0.1:443: read: broken pipe")},
		{"upstream write broken pipe (request-phase)", errors.New(`Post "https://upstream/v1/chat": write tcp 10.0.0.5:52111->10.0.0.9:443: write: broken pipe`)},
		{"client-shaped write broken pipe (ambiguous text)", errors.New("write tcp 127.0.0.1:8781->10.0.0.2:443: write: broken pipe")},
		{"bare write epipe", errors.New("write: broken pipe")},
		{"bare epipe", errors.New("EPIPE")},
		{"lowercase epipe", errors.New("epipe")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyError(tt.err, nil)
			if got != KindNetwork {
				t.Fatalf("ClassifyError(%q) = %q, want %q", tt.err, got, KindNetwork)
			}
			if !IsRetryable(got) {
				t.Fatalf("EPIPE must stay retryable, kind=%q", got)
			}
		})
	}
}
