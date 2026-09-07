package errorsx

import (
	"errors"
	"testing"
)

func TestClassifyError_EPIPE(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want ErrorKind
	}{
		{"client write broken pipe", errors.New("write tcp 127.0.0.1:8781->10.0.0.2:443: write: broken pipe"), KindCanceled},
		{"bare write epipe", errors.New("write: broken pipe"), KindCanceled},
		{"upstream read broken pipe", errors.New("read tcp 10.0.0.1:443: read: broken pipe"), KindNetwork},
		{"bare epipe", errors.New("EPIPE"), KindNetwork},
		{"lowercase epipe", errors.New("epipe"), KindNetwork},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyError(tt.err, nil)
			if got != tt.want {
				t.Fatalf("ClassifyError(%q) = %q, want %q", tt.err, got, tt.want)
			}
			if tt.want == KindNetwork && !IsRetryable(got) {
				t.Fatalf("upstream EPIPE must stay retryable")
			}
			if tt.want == KindCanceled && IsRetryable(got) {
				t.Fatalf("client write EPIPE must not be retryable")
			}
		})
	}
}
