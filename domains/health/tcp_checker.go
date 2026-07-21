package health

import (
	"context"
	"net"
	"time"
)

// TCPCheckResult represents the result of a TCP connectivity check.
type TCPCheckResult struct {
	Success   bool
	Latency   time.Duration
	Error     error
	CheckedAt time.Time
}

// TCPChecker performs TCP connectivity checks.
type TCPChecker struct {
	timeout time.Duration
}

// NewTCPChecker creates a new TCP checker with the specified timeout.
func NewTCPChecker(timeout time.Duration) *TCPChecker {
	if timeout == 0 {
		timeout = 1 * time.Second // Default 1s timeout
	}
	return &TCPChecker{timeout: timeout}
}

// Check performs a TCP connectivity check to the specified address.
// addr should be in the format "host:port" (e.g., "api.openai.com:443")
func (c *TCPChecker) Check(ctx context.Context, addr string) TCPCheckResult {
	start := time.Now()
	result := TCPCheckResult{
		CheckedAt: start,
	}

	// Create dialer with timeout
	dialer := &net.Dialer{
		Timeout: c.timeout,
	}

	// Attempt TCP connection
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	result.Latency = time.Since(start)

	if err != nil {
		result.Success = false
		result.Error = err
		return result
	}

	// Close connection immediately
	conn.Close()

	result.Success = true
	return result
}

// CheckWithRetry performs a TCP check with retry logic.
func (c *TCPChecker) CheckWithRetry(ctx context.Context, addr string, maxRetries int) TCPCheckResult {
	var lastResult TCPCheckResult

	for attempt := 0; attempt <= maxRetries; attempt++ {
		lastResult = c.Check(ctx, addr)
		if lastResult.Success {
			return lastResult
		}

		// Don't sleep after the last attempt
		if attempt < maxRetries {
			select {
			case <-ctx.Done():
				lastResult.Error = ctx.Err()
				return lastResult
			case <-time.After(100 * time.Millisecond):
				// Short delay between retries
			}
		}
	}

	return lastResult
}

// Ping is a convenience function for quick TCP connectivity checks.
func Ping(addr string, timeout time.Duration) (latency time.Duration, err error) {
	checker := NewTCPChecker(timeout)
	result := checker.Check(context.Background(), addr)
	return result.Latency, result.Error
}
