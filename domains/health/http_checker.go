package health

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPCheckResult represents the result of an HTTP health check.
type HTTPCheckResult struct {
	Success    bool
	StatusCode int
	Latency    time.Duration
	Error      error
	CheckedAt  time.Time
	Body       string // Optional: response body (up to 1KB)
}

// HTTPChecker performs HTTP health checks.
type HTTPChecker struct {
	client  *http.Client
	timeout time.Duration
}

// NewHTTPChecker creates a new HTTP checker with the specified timeout.
func NewHTTPChecker(timeout time.Duration) *HTTPChecker {
	if timeout == 0 {
		timeout = 3 * time.Second // Default 3s timeout
	}

	return &HTTPChecker{
		client: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// Allow up to 3 redirects
				if len(via) >= 3 {
					return fmt.Errorf("stopped after 3 redirects")
				}
				return nil
			},
		},
		timeout: timeout,
	}
}

// Check performs an HTTP GET request to the health endpoint.
func (c *HTTPChecker) Check(ctx context.Context, url string) HTTPCheckResult {
	start := time.Now()
	result := HTTPCheckResult{
		CheckedAt: start,
	}

	// Create request with context
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		result.Error = err
		result.Latency = time.Since(start)
		return result
	}

	// Set User-Agent
	req.Header.Set("User-Agent", "LLM-Gateway-HealthChecker/1.0")

	// Execute request
	resp, err := c.client.Do(req)
	result.Latency = time.Since(start)

	if err != nil {
		result.Error = err
		return result
	}
	defer resp.Body.Close()

	result.StatusCode = resp.StatusCode

	// Read response body (up to 1KB)
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err == nil {
		result.Body = string(bodyBytes)
	}

	// Consider 2xx as success
	result.Success = resp.StatusCode >= 200 && resp.StatusCode < 300

	return result
}

// CheckWithExpectedStatus checks HTTP endpoint and expects specific status code.
func (c *HTTPChecker) CheckWithExpectedStatus(ctx context.Context, url string, expectedStatus int) HTTPCheckResult {
	result := c.Check(ctx, url)

	// Override success based on expected status
	if result.StatusCode != 0 {
		result.Success = result.StatusCode == expectedStatus
		if !result.Success && result.Error == nil {
			result.Error = fmt.Errorf("expected status %d, got %d", expectedStatus, result.StatusCode)
		}
	}

	return result
}

// QuickCheck is a convenience function for quick HTTP health checks.
func QuickCheck(url string) (statusCode int, latency time.Duration, err error) {
	checker := NewHTTPChecker(3 * time.Second)
	result := checker.Check(context.Background(), url)
	return result.StatusCode, result.Latency, result.Error
}
