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

// dialTCP is the dial function used by Check. It is a package-level variable
// so tests can inject deterministic fakes without relying on the host network
// (which makes the documentation-IP / invalid-hostname tests flaky in
// environments with transparent proxies or unusual routing). Production code
// leaves the default, which uses net.Dialer.DialContext.
var dialTCP = func(ctx context.Context, network, addr string, timeout time.Duration) (net.Conn, error) {
	return (&net.Dialer{Timeout: timeout}).DialContext(ctx, network, addr)
}

// Check performs a TCP connectivity check to the specified address.
// addr should be in the format "host:port" (e.g., "api.openai.com:443")
func (c *TCPChecker) Check(ctx context.Context, addr string) TCPCheckResult {
	start := time.Now()
	result := TCPCheckResult{
		CheckedAt: start,
	}

	// Attempt TCP connection (dialTCP is injectable for tests).
	conn, err := dialTCP(ctx, "tcp", addr, c.timeout)
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
