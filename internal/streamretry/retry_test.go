package streamretry

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
	"testing"
	"time"
)

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retriable bool
		reason    string
	}{
		{
			name:      "nil error",
			err:       nil,
			retriable: false,
			reason:    "",
		},
		{
			name:      "context canceled",
			err:       context.Canceled,
			retriable: false,
			reason:    "context_canceled",
		},
		{
			name:      "context deadline exceeded",
			err:       context.DeadlineExceeded,
			retriable: false,
			reason:    "context_canceled",
		},
		{
			name:      "EOF",
			err:       io.EOF,
			retriable: true,
			reason:    "premature_eof",
		},
		{
			name:      "unexpected EOF",
			err:       io.ErrUnexpectedEOF,
			retriable: true,
			reason:    "premature_eof",
		},
		{
			name:      "connection refused",
			err:       syscall.ECONNREFUSED,
			retriable: true,
			reason:    "", // Reason varies by OS wrapping
		},
		{
			name:      "connection reset",
			err:       syscall.ECONNRESET,
			retriable: true,
			reason:    "", // Reason varies by OS wrapping
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ClassifyError(tt.err)
			if result.Retriable != tt.retriable {
				t.Errorf("ClassifyError(%v).Retriable = %v, want %v", tt.err, result.Retriable, tt.retriable)
			}
			if tt.reason != "" && result.Reason != tt.reason {
				t.Errorf("ClassifyError(%v).Reason = %v, want %v", tt.err, result.Reason, tt.reason)
			}
		})
	}
}

func TestClassifyHTTPError(t *testing.T) {
	baseErr := errors.New("upstream error")

	tests := []struct {
		name       string
		statusCode int
		retriable  bool
		reason     string
	}{
		{
			name:       "HTTP 408 timeout",
			statusCode: 408,
			retriable:  true,
			reason:     "http_408_timeout",
		},
		{
			name:       "HTTP 425 too early",
			statusCode: 425,
			retriable:  true,
			reason:     "http_425_too_early",
		},
		{
			name:       "HTTP 429 rate limit",
			statusCode: 429,
			retriable:  true,
			reason:     "http_429_rate_limit",
		},
		{
			name:       "HTTP 500 server error",
			statusCode: 500,
			retriable:  true,
			reason:     "http_500_server_error",
		},
		{
			name:       "HTTP 502 bad gateway",
			statusCode: 502,
			retriable:  true,
			reason:     "http_502_server_error",
		},
		{
			name:       "HTTP 503 service unavailable",
			statusCode: 503,
			retriable:  true,
			reason:     "http_503_server_error",
		},
		{
			name:       "HTTP 504 gateway timeout",
			statusCode: 504,
			retriable:  true,
			reason:     "http_504_server_error",
		},
		{
			name:       "HTTP 400 bad request",
			statusCode: 400,
			retriable:  false,
			reason:     "http_400_client_error",
		},
		{
			name:       "HTTP 401 unauthorized",
			statusCode: 401,
			retriable:  false,
			reason:     "http_401_client_error",
		},
		{
			name:       "HTTP 403 forbidden",
			statusCode: 403,
			retriable:  false,
			reason:     "http_403_client_error",
		},
		{
			name:       "HTTP 404 not found",
			statusCode: 404,
			retriable:  false,
			reason:     "http_404_client_error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ClassifyHTTPError(tt.statusCode, baseErr)
			if result.Retriable != tt.retriable {
				t.Errorf("ClassifyHTTPError(%d).Retriable = %v, want %v", tt.statusCode, result.Retriable, tt.retriable)
			}
			if result.Reason != tt.reason {
				t.Errorf("ClassifyHTTPError(%d).Reason = %v, want %v", tt.statusCode, result.Reason, tt.reason)
			}
		})
	}
}

func TestCalculateRetryDelay(t *testing.T) {
	tests := []struct {
		name        string
		attempt     int
		baseDelayMs int
		maxDelayMs  int
		wantMin     time.Duration
		wantMax     time.Duration
	}{
		{
			name:        "attempt 0",
			attempt:     0,
			baseDelayMs: 200,
			maxDelayMs:  5000,
			wantMin:     160 * time.Millisecond, // 200ms - 20%
			wantMax:     240 * time.Millisecond, // 200ms + 20%
		},
		{
			name:        "attempt 1",
			attempt:     1,
			baseDelayMs: 200,
			maxDelayMs:  5000,
			wantMin:     320 * time.Millisecond, // 400ms - 20%
			wantMax:     480 * time.Millisecond, // 400ms + 20%
		},
		{
			name:        "attempt 2",
			attempt:     2,
			baseDelayMs: 200,
			maxDelayMs:  5000,
			wantMin:     640 * time.Millisecond, // 800ms - 20%
			wantMax:     960 * time.Millisecond, // 800ms + 20%
		},
		{
			name:        "attempt 5 (capped)",
			attempt:     5,
			baseDelayMs: 200,
			maxDelayMs:  5000,
			wantMin:     4000 * time.Millisecond, // 5000ms - 20%
			wantMax:     6000 * time.Millisecond, // 5000ms + 20%
		},
		{
			name:        "default values",
			attempt:     0,
			baseDelayMs: 0,
			maxDelayMs:  0,
			wantMin:     160 * time.Millisecond, // 200ms default - 20%
			wantMax:     240 * time.Millisecond, // 200ms default + 20%
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Run multiple times to test jitter range
			for i := 0; i < 100; i++ {
				delay := CalculateRetryDelay(tt.attempt, tt.baseDelayMs, tt.maxDelayMs)
				if delay < tt.wantMin || delay > tt.wantMax {
					t.Errorf("CalculateRetryDelay(%d, %d, %d) = %v, want between %v and %v",
						tt.attempt, tt.baseDelayMs, tt.maxDelayMs, delay, tt.wantMin, tt.wantMax)
					break
				}
			}
		})
	}
}

func TestRetryContext_ShouldRetry(t *testing.T) {
	tests := []struct {
		name      string
		config    Config
		attempt   int
		err       error
		wantRetry bool
	}{
		{
			name:      "disabled globally",
			config:    Config{Enabled: false, MaxRetries: 3},
			attempt:   0,
			err:       io.EOF,
			wantRetry: false,
		},
		{
			name:      "max retries exceeded",
			config:    Config{Enabled: true, MaxRetries: 3},
			attempt:   3,
			err:       io.EOF,
			wantRetry: false,
		},
		{
			name:      "retriable error within limit",
			config:    Config{Enabled: true, MaxRetries: 3},
			attempt:   1,
			err:       io.EOF,
			wantRetry: true,
		},
		{
			name:      "non-retriable error",
			config:    Config{Enabled: true, MaxRetries: 3},
			attempt:   0,
			err:       context.Canceled,
			wantRetry: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc := &RetryContext{
				Config:  tt.config,
				Attempt: tt.attempt,
			}
			got := rc.ShouldRetry(tt.err)
			if got != tt.wantRetry {
				t.Errorf("RetryContext.ShouldRetry() = %v, want %v", got, tt.wantRetry)
			}
		})
	}
}

func TestRetryContext_Sleep(t *testing.T) {
	t.Run("sleep completes normally", func(t *testing.T) {
		rc := &RetryContext{
			Config: Config{
				BaseDelayMs: 10, // 10ms for fast test
				MaxDelayMs:  50,
			},
			Attempt: 0,
		}

		ctx := context.Background()
		start := time.Now()
		err := rc.Sleep(ctx)
		elapsed := time.Since(start)

		if err != nil {
			t.Errorf("Sleep() error = %v, want nil", err)
		}
		// Should sleep at least base delay (with jitter, might be less)
		if elapsed < 5*time.Millisecond {
			t.Errorf("Sleep() elapsed = %v, want >= 5ms", elapsed)
		}
	})

	t.Run("sleep canceled by context", func(t *testing.T) {
		rc := &RetryContext{
			Config: Config{
				BaseDelayMs: 1000, // 1s
				MaxDelayMs:  5000,
			},
			Attempt: 0,
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		err := rc.Sleep(ctx)
		if err != context.Canceled {
			t.Errorf("Sleep() error = %v, want context.Canceled", err)
		}
	})
}

// TestNetworkError verifies network error classification
func TestNetworkError(t *testing.T) {
	// Simulate a network timeout error
	timeoutErr := &net.DNSError{
		Err:         "timeout",
		IsTimeout:   true,
		IsTemporary: true,
	}

	result := ClassifyError(timeoutErr)
	if !result.Retriable {
		t.Errorf("Network timeout should be retriable, got %v", result.Retriable)
	}
	if result.Reason != "network_timeout" {
		t.Errorf("Network timeout reason = %v, want network_timeout", result.Reason)
	}
}
