package streamretry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestIntegration_SuccessOnFirstAttempt demonstrates the happy path.
func TestIntegration_SuccessOnFirstAttempt(t *testing.T) {
	config := DefaultConfig()
	wrapper := NewWrapper(config, slog.Default())

	streamFunc := func(ctx context.Context, w http.ResponseWriter) error {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "data: {\"message\": \"success\"}\n\n")
		return nil
	}

	w := httptest.NewRecorder()
	ctx := context.Background()

	err := wrapper.Execute(ctx, w, streamFunc)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}

	metrics := wrapper.Metrics()
	if metrics.TotalAttempts != 1 {
		t.Errorf("TotalAttempts = %d, want 1", metrics.TotalAttempts)
	}
	if metrics.TotalRetries != 0 {
		t.Errorf("TotalRetries = %d, want 0", metrics.TotalRetries)
	}
}

// TestIntegration_RetryAndRecover demonstrates automatic retry on transient errors.
func TestIntegration_RetryAndRecover(t *testing.T) {
	config := DefaultConfig()
	config.BaseDelayMs = 10 // Fast retry for testing
	config.MaxDelayMs = 50
	wrapper := NewWrapper(config, slog.Default())

	var attemptCount int32

	streamFunc := func(ctx context.Context, w http.ResponseWriter) error {
		attempt := atomic.AddInt32(&attemptCount, 1)

		// Fail first 2 attempts, succeed on 3rd
		if attempt < 3 {
			return &HTTPError{
				StatusCode: 503,
				Err:        errors.New("service unavailable"),
			}
		}

		// Success
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "data: {\"message\": \"recovered\"}\n\n")
		return nil
	}

	w := httptest.NewRecorder()
	ctx := context.Background()

	err := wrapper.Execute(ctx, w, streamFunc)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (should recover)", err)
	}

	metrics := wrapper.Metrics()
	if metrics.TotalAttempts != 3 {
		t.Errorf("TotalAttempts = %d, want 3", metrics.TotalAttempts)
	}
	if metrics.TotalRetries != 2 {
		t.Errorf("TotalRetries = %d, want 2", metrics.TotalRetries)
	}
	if metrics.SuccessAttempt != 2 {
		t.Errorf("SuccessAttempt = %d, want 2 (0-indexed)", metrics.SuccessAttempt)
	}
}

// TestIntegration_MaxRetriesExceeded demonstrates failure after max retries.
func TestIntegration_MaxRetriesExceeded(t *testing.T) {
	config := DefaultConfig()
	config.MaxRetries = 2
	config.BaseDelayMs = 10
	wrapper := NewWrapper(config, slog.Default())

	streamFunc := func(ctx context.Context, w http.ResponseWriter) error {
		// Always fail with retriable error
		return &HTTPError{
			StatusCode: 503,
			Err:        errors.New("persistent failure"),
		}
	}

	w := httptest.NewRecorder()
	ctx := context.Background()

	err := wrapper.Execute(ctx, w, streamFunc)
	if err == nil {
		t.Fatal("Execute() error = nil, want error (max retries exceeded)")
	}

	// The error should either be the original error or wrapped with retry info
	errStr := err.Error()
	hasRetryInfo := strings.Contains(errStr, "after") && strings.Contains(errStr, "retries")
	isPersistentFailure := strings.Contains(errStr, "persistent failure")

	if !hasRetryInfo && !isPersistentFailure {
		t.Errorf("Error message = %v, want 'after X retries' or 'persistent failure'", err.Error())
	}

	metrics := wrapper.Metrics()
	if metrics.TotalAttempts != 3 {
		t.Errorf("TotalAttempts = %d, want 3", metrics.TotalAttempts)
	}
	if metrics.SuccessAttempt != -1 {
		t.Errorf("SuccessAttempt = %d, want -1 (failed)", metrics.SuccessAttempt)
	}
}

// TestIntegration_NonRetriableError demonstrates immediate failure on non-retriable errors.
func TestIntegration_NonRetriableError(t *testing.T) {
	config := DefaultConfig()
	wrapper := NewWrapper(config, slog.Default())

	streamFunc := func(ctx context.Context, w http.ResponseWriter) error {
		// 401 is non-retriable
		return &HTTPError{
			StatusCode: 401,
			Err:        errors.New("unauthorized"),
		}
	}

	w := httptest.NewRecorder()
	ctx := context.Background()

	err := wrapper.Execute(ctx, w, streamFunc)
	if err == nil {
		t.Fatal("Execute() error = nil, want error (non-retriable)")
	}

	metrics := wrapper.Metrics()
	if metrics.TotalAttempts != 1 {
		t.Errorf("TotalAttempts = %d, want 1 (no retry)", metrics.TotalAttempts)
	}
	if metrics.TotalRetries != 0 {
		t.Errorf("TotalRetries = %d, want 0 (non-retriable)", metrics.TotalRetries)
	}
}

// TestIntegration_ContextCancellation demonstrates proper context handling.
func TestIntegration_ContextCancellation(t *testing.T) {
	config := DefaultConfig()
	config.BaseDelayMs = 1000 // 1s delay
	wrapper := NewWrapper(config, slog.Default())

	streamFunc := func(ctx context.Context, w http.ResponseWriter) error {
		// Always fail to trigger retry
		return &HTTPError{
			StatusCode: 503,
			Err:        errors.New("service unavailable"),
		}
	}

	w := httptest.NewRecorder()
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after first attempt
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := wrapper.Execute(ctx, w, streamFunc)
	if err == nil {
		t.Fatal("Execute() error = nil, want context.Canceled")
	}

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Execute() error = %v, want context.Canceled", err)
	}

	// Should only have 1 attempt (canceled during retry sleep)
	metrics := wrapper.Metrics()
	if metrics.TotalAttempts != 1 {
		t.Errorf("TotalAttempts = %d, want 1 (canceled)", metrics.TotalAttempts)
	}
}

// TestIntegration_KeepaliveWriter demonstrates keepalive functionality.
func TestIntegration_KeepaliveWriter(t *testing.T) {
	// Use a real ResponseWriter with flushing support
	w := httptest.NewRecorder()

	keepalive := NewKeepaliveWriter(w, 50*time.Millisecond)
	if keepalive == nil {
		t.Skip("ResponseWriter doesn't support flushing")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start keepalive
	go keepalive.Start(ctx)

	// Wait for a few keepalive events
	time.Sleep(150 * time.Millisecond)

	// Stop keepalive
	keepalive.Stop()

	// Check that keepalive events were written
	body := w.Body.String()
	if !strings.Contains(body, ": thinking:") {
		t.Errorf("Response body should contain keepalive comments, got: %s", body)
	}

	// Should have at least 2 keepalive events (50ms interval, 150ms duration)
	count := strings.Count(body, ": thinking:")
	if count < 2 {
		t.Errorf("Keepalive event count = %d, want >= 2", count)
	}
}

func TestKeepaliveWriterStopBeforeStartAndRepeatedStop(t *testing.T) {
	keepalive := NewKeepaliveWriter(httptest.NewRecorder(), time.Second)
	done := make(chan struct{})
	go func() {
		keepalive.Stop()
		keepalive.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Stop blocked for a keepalive writer that was never started")
	}
}

func TestKeepaliveWriterStopEndsPeriodicWrites(t *testing.T) {
	w := httptest.NewRecorder()
	keepalive := NewKeepaliveWriter(w, 5*time.Millisecond)
	go keepalive.Start(context.Background())
	time.Sleep(20 * time.Millisecond)
	keepalive.Stop()
	before := w.Body.String()
	time.Sleep(20 * time.Millisecond)
	if got := w.Body.String(); got != before {
		t.Fatalf("body changed after Stop: before=%q after=%q", before, got)
	}
}

// TestIntegration_RealWorldScenario simulates a real-world streaming scenario.
func TestIntegration_RealWorldScenario(t *testing.T) {
	config := DefaultConfig()
	config.BaseDelayMs = 20
	config.MaxRetries = 3
	wrapper := NewWrapper(config, slog.Default())

	// Simulate upstream that fails intermittently
	var attemptCount int32
	upstreamState := []error{
		&HTTPError{StatusCode: 503, Err: errors.New("overload")},
		io.EOF, // Connection drop
		nil,    // Success
	}

	streamFunc := func(ctx context.Context, w http.ResponseWriter) error {
		attempt := atomic.AddInt32(&attemptCount, 1) - 1
		if int(attempt) < len(upstreamState) {
			return upstreamState[attempt]
		}

		// Success: write streaming response
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "data: success after %d attempts\n\n", attempt+1)
		return nil
	}

	w := httptest.NewRecorder()
	ctx := context.Background()

	err := wrapper.Execute(ctx, w, streamFunc)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (should recover)", err)
	}

	// Verify metrics
	metrics := wrapper.Metrics()
	if metrics.TotalAttempts != 3 {
		t.Errorf("TotalAttempts = %d, want 3", metrics.TotalAttempts)
	}
	if metrics.TotalRetries != 2 {
		t.Errorf("TotalRetries = %d, want 2", metrics.TotalRetries)
	}
	if metrics.SuccessAttempt != 2 {
		t.Errorf("SuccessAttempt = %d, want 2", metrics.SuccessAttempt)
	}
}

// TestIntegration_DisabledRetry demonstrates behavior when retry is disabled.
func TestIntegration_DisabledRetry(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = false
	wrapper := NewWrapper(config, slog.Default())

	var attemptCount int32

	streamFunc := func(ctx context.Context, w http.ResponseWriter) error {
		atomic.AddInt32(&attemptCount, 1)
		return &HTTPError{
			StatusCode: 503,
			Err:        errors.New("service unavailable"),
		}
	}

	w := httptest.NewRecorder()
	ctx := context.Background()

	err := wrapper.Execute(ctx, w, streamFunc)
	if err == nil {
		t.Fatal("Execute() error = nil, want error (retry disabled)")
	}

	// Should only attempt once
	if atomic.LoadInt32(&attemptCount) != 1 {
		t.Errorf("Attempt count = %d, want 1 (retry disabled)", attemptCount)
	}

	metrics := wrapper.Metrics()
	if metrics.TotalAttempts != 1 {
		t.Errorf("TotalAttempts = %d, want 1", metrics.TotalAttempts)
	}
}
