package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestIsRetryableDBError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "ErrNoRows should not retry",
			err:      pgx.ErrNoRows,
			expected: false,
		},
		{
			name:     "context deadline exceeded should retry",
			err:      context.DeadlineExceeded,
			expected: true,
		},
		{
			name:     "connection refused should retry",
			err:      errors.New("connection refused"),
			expected: true,
		},
		{
			name:     "timeout should retry",
			err:      errors.New("i/o timeout"),
			expected: true,
		},
		{
			name:     "connection reset should retry",
			err:      errors.New("connection reset by peer"),
			expected: true,
		},
		{
			name:     "broken pipe should retry",
			err:      errors.New("broken pipe"),
			expected: true,
		},
		{
			name:     "SQL syntax error should not retry",
			err:      errors.New("syntax error at or near"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryableDBError(tt.err)
			if got != tt.expected {
				t.Errorf("isRetryableDBError() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestWaitForRetryReturnsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := waitForRetry(ctx, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForRetry() error = %v, want context.Canceled", err)
	}
}

func BenchmarkIsRetryableDBError(b *testing.B) {
	err := errors.New("connection refused")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = isRetryableDBError(err)
	}
}
