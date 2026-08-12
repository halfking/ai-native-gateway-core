// Package streamretry implements intelligent retry and client keepalive for streaming requests.
//
// Problem:
// Upstream provider transient failures (network blips, overload, brief outages) cause
// streaming requests to disconnect immediately, forcing the client to restart the entire
// conversation manually.
//
// Solution:
// 1. Smart Retry: Exponential backoff with jitter for retriable errors (5xx, 429, connection drops)
// 2. Client Keepalive: Send thinking events during retry to prevent client timeout
// 3. Transparent Reconnection: Hide upstream failures from client when possible
// 4. Unified Backoff: Consistent with existing rate limit retry policy
//
// Architecture:
//
//	Request → [Executor] → [StreamRetry Wrapper] → Upstream
//	                           ↓ on failure
//	                      [Retry Loop with Keepalive]
//	                           ↓ success
//	                      Resume Stream → Client
//
// 2026-08-11: Initial implementation aligned with rule 11 execution protocol.
package streamretry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"sync"
	"syscall"
	"time"
)

// Config holds retry policy configuration.
type Config struct {
	// MaxRetries is the maximum number of retry attempts (default: 3)
	MaxRetries int

	// BaseDelayMs is the base delay in milliseconds for exponential backoff (default: 200)
	BaseDelayMs int

	// MaxDelayMs caps the retry delay (default: 5000)
	MaxDelayMs int

	// KeepaliveInterval is how often to send thinking events during retry (default: 10s)
	KeepaliveInterval time.Duration

	// Enabled globally enables/disables retry (default: true)
	Enabled bool
}

// DefaultConfig returns the production default configuration.
// Aligned with internal/probeutil/retry.go backoff schedule.
func DefaultConfig() Config {
	return Config{
		MaxRetries:        3,
		BaseDelayMs:       200,  // 200ms base (vs 100ms in probeutil for faster recovery)
		MaxDelayMs:        5000, // 5s cap
		KeepaliveInterval: 10 * time.Second,
		Enabled:           true,
	}
}

// RetryableError classifies errors as retriable or permanent.
type RetryableError struct {
	Err       error
	Retriable bool
	Reason    string
}

func (e *RetryableError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("%s (retriable=%v, reason=%s)", e.Err.Error(), e.Retriable, e.Reason)
	}
	return fmt.Sprintf("%s (retriable=%v)", e.Err.Error(), e.Retriable)
}

func (e *RetryableError) Unwrap() error {
	return e.Err
}

// ClassifyError determines if an error should trigger a retry.
//
// Retriable errors (transient):
//   - Network errors: connection refused, connection reset, timeout, DNS failure
//   - HTTP 5xx: upstream server errors
//   - HTTP 429: rate limit (with backoff)
//   - HTTP 408: request timeout
//   - HTTP 425: too early
//   - HTTP 502, 503, 504: gateway errors
//   - EOF: premature connection close
//
// Non-retriable errors (permanent):
//   - HTTP 4xx (except 408, 425, 429): client errors
//   - Context canceled: user/system initiated stop
//   - Auth failures: invalid credentials
func ClassifyError(err error) *RetryableError {
	if err == nil {
		return &RetryableError{Err: err, Retriable: false}
	}

	// Context cancellation (user/system stop signal)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &RetryableError{
			Err:       err,
			Retriable: false,
			Reason:    "context_canceled",
		}
	}

	// Network errors (transient)
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return &RetryableError{
				Err:       err,
				Retriable: true,
				Reason:    "network_timeout",
			}
		}
		return &RetryableError{
			Err:       err,
			Retriable: true,
			Reason:    "network_error",
		}
	}

	// Premature EOF (connection dropped)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return &RetryableError{
			Err:       err,
			Retriable: true,
			Reason:    "premature_eof",
		}
	}

	// Connection refused/reset (transient)
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) {
		return &RetryableError{
			Err:       err,
			Retriable: true,
			Reason:    "connection_refused_or_reset",
		}
	}

	// HTTP status code based classification
	// Check if it's an HTTPError and classify by status code
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return ClassifyHTTPError(httpErr.StatusCode, httpErr.Err)
	}

	// Unknown error type - conservative: non-retriable
	return &RetryableError{
		Err:       err,
		Retriable: false,
		Reason:    "unknown_error_type",
	}
}

// HTTPError wraps an error with HTTP status code for classification.
type HTTPError struct {
	StatusCode int
	Err        error
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %v", e.StatusCode, e.Err)
}

func (e *HTTPError) Unwrap() error {
	return e.Err
}

// ClassifyHTTPError determines if an HTTP error is retriable based on status code.
func ClassifyHTTPError(statusCode int, err error) *RetryableError {
	httpErr := &HTTPError{StatusCode: statusCode, Err: err}

	// Retriable HTTP status codes
	switch {
	case statusCode == 408: // Request Timeout
		return &RetryableError{Err: httpErr, Retriable: true, Reason: "http_408_timeout"}
	case statusCode == 425: // Too Early
		return &RetryableError{Err: httpErr, Retriable: true, Reason: "http_425_too_early"}
	case statusCode == 429: // Rate Limit
		return &RetryableError{Err: httpErr, Retriable: true, Reason: "http_429_rate_limit"}
	case statusCode >= 500 && statusCode <= 599: // Server errors
		return &RetryableError{Err: httpErr, Retriable: true, Reason: fmt.Sprintf("http_%d_server_error", statusCode)}
	case statusCode >= 400 && statusCode <= 499: // Client errors (non-retriable except above)
		return &RetryableError{Err: httpErr, Retriable: false, Reason: fmt.Sprintf("http_%d_client_error", statusCode)}
	default:
		// 2xx/3xx are not errors; shouldn't reach here
		return &RetryableError{Err: httpErr, Retriable: false, Reason: fmt.Sprintf("http_%d_unexpected", statusCode)}
	}
}

// CalculateRetryDelay computes exponential backoff with jitter.
//
// Formula:
//
//	delay = min(baseDelayMs * 2^attempt, maxDelayMs)
//	jitter = delay * 0.2 * random(-1, 1)  // ±20%
//	finalDelay = delay + jitter
//
// Example progression (baseDelayMs=200, maxDelayMs=5000):
//
//	attempt=0: 200ms ± 20% = 160-240ms
//	attempt=1: 400ms ± 20% = 320-480ms
//	attempt=2: 800ms ± 20% = 640-960ms
//	attempt=3: 1600ms ± 20% = 1280-1920ms
//	attempt=4: 3200ms ± 20% = 2560-3840ms
//	attempt=5+: 5000ms ± 20% = 4000-6000ms (capped)
//
// Aligned with domains/streaming/handler.go calculateRetryDelay.
func CalculateRetryDelay(attempt int, baseDelayMs int, maxDelayMs int) time.Duration {
	if baseDelayMs <= 0 {
		baseDelayMs = 200 // default 200ms
	}
	if maxDelayMs <= 0 {
		maxDelayMs = 5000 // default max 5s
	}

	// Exponential backoff. Clamp the shift exponent so baseDelayMs * 2^shift
	// cannot overflow int before the maxDelayMs cap is applied. The cap
	// dominates well before shiftCap, so clamping is behaviourally a no-op for
	// sane inputs; it only removes the overflow footgun for unbounded /
	// caller-supplied attempt values (see AUDIT_CROSSCUTTING_CONCURRENCY_20260813.md §4-SR1).
	const shiftCap = 30
	shift := attempt
	if shift > shiftCap {
		shift = shiftCap
	}
	delayMs := baseDelayMs * (1 << shift)
	if delayMs > maxDelayMs {
		delayMs = maxDelayMs
	}

	// Add ±20% random jitter to prevent thundering herd
	jitter := float64(delayMs) * 0.2
	jitterMs := int(jitter * (2*rand.Float64() - 1))
	delayMs += jitterMs

	// Ensure non-negative
	if delayMs < 0 {
		delayMs = baseDelayMs
	}

	return time.Duration(delayMs) * time.Millisecond
}

// KeepaliveWriter sends periodic thinking events to prevent client timeout during retry.
type KeepaliveWriter struct {
	w        http.ResponseWriter
	flusher  http.Flusher
	interval time.Duration
	stopCh   chan struct{}
	doneCh   chan struct{}
	writeMu  sync.Mutex
}

type retryBlockedError struct {
	err error
}

func (e *retryBlockedError) Error() string {
	return fmt.Sprintf("retry blocked after response commitment: %v", e.err)
}

// NewKeepaliveWriter creates a new keepalive writer.
// Returns nil if ResponseWriter doesn't support flushing.
func NewKeepaliveWriter(w http.ResponseWriter, interval time.Duration) *KeepaliveWriter {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil
	}

	if interval <= 0 {
		interval = 10 * time.Second // default 10s
	}

	kw := &KeepaliveWriter{
		w:        w,
		flusher:  flusher,
		interval: interval,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}

	return kw
}

// Start begins sending keepalive events.
// Must be called in a goroutine.
func (kw *KeepaliveWriter) Start(ctx context.Context) {
	defer close(kw.doneCh)

	ticker := time.NewTicker(kw.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-kw.stopCh:
			return
		case <-ticker.C:
			kw.sendThinking("Retrying connection to upstream service...")
		}
	}
}

// SendRetryMessage sends a one-time retry notification.
func (kw *KeepaliveWriter) SendRetryMessage(attempt int, delay time.Duration) {
	if kw == nil {
		return
	}
	msg := fmt.Sprintf("Upstream service temporarily unavailable, retrying (attempt %d, waiting %v)...", attempt+1, delay.Round(time.Millisecond))
	kw.sendThinking(msg)
}

// sendThinking writes an SSE comment line (won't trigger client parsing errors).
func (kw *KeepaliveWriter) sendThinking(message string) {
	if kw == nil {
		return
	}
	kw.writeMu.Lock()
	defer kw.writeMu.Unlock()

	// SSE comment format: ": text\n\n"
	// This keeps the connection alive without triggering data parsing.
	fmt.Fprintf(kw.w, ": thinking: %s\n\n", message)
	kw.flusher.Flush()
}

// Stop signals the keepalive loop to stop.
func (kw *KeepaliveWriter) Stop() {
	if kw == nil {
		return
	}
	close(kw.stopCh)
	<-kw.doneCh
}

// RetryContext holds state for a retry loop.
type RetryContext struct {
	Config    Config
	Attempt   int
	LastError error
	Keepalive *KeepaliveWriter
}

// ShouldRetry determines if another retry attempt should be made.
func (rc *RetryContext) ShouldRetry(err error) bool {
	if !rc.Config.Enabled {
		return false
	}

	if rc.Attempt >= rc.Config.MaxRetries {
		return false
	}
	if _, blocked := err.(*retryBlockedError); blocked {
		rc.LastError = ClassifyError(err)
		return false
	}

	classified := ClassifyError(err)
	rc.LastError = classified

	return classified.Retriable
}

// Sleep waits for the calculated retry delay with context cancellation support.
// Sends a retry notification via keepalive if available.
func (rc *RetryContext) Sleep(ctx context.Context) error {
	delay := CalculateRetryDelay(rc.Attempt, rc.Config.BaseDelayMs, rc.Config.MaxDelayMs)

	// Notify client about retry
	if rc.Keepalive != nil {
		rc.Keepalive.SendRetryMessage(rc.Attempt, delay)
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
