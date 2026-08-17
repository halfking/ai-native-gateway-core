package streamretry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type flushErrorResponseWriter struct {
	http.ResponseWriter
	flushErr error
	flushes  int
}

func (w *flushErrorResponseWriter) Flush() { _ = w.FlushError() }
func (w *flushErrorResponseWriter) FlushError() error {
	w.flushes++
	return w.flushErr
}

// fakeJourneyObserver records retry boundary emissions for assertions.
type fakeJourneyObserver struct {
	mu      sync.Mutex
	reasons []string
	high    int64
}

func (f *fakeJourneyObserver) RetryScheduled(ctx context.Context, reason string) {
	f.mu.Lock()
	f.reasons = append(f.reasons, reason)
	f.mu.Unlock()
}

func (f *fakeJourneyObserver) SequenceHighWater() int64 { return f.high }

func (f *fakeJourneyObserver) retryReasons() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reasons...)
}

func TestJourneyCarrierBinding(t *testing.T) {
	observer := &fakeJourneyObserver{}
	ctx := withRequestCarrier(context.Background())
	if got := JourneyObserverFromCtx(ctx); got != nil {
		t.Fatalf("initial observer = %v, want nil", got)
	}
	BindJourneyObserver(ctx, observer)
	if got := JourneyObserverFromCtx(ctx); got != observer {
		t.Fatalf("observer = %v, want the bound observer", got)
	}
	BindJourneyObserver(context.Background(), observer)
	if got := JourneyObserverFromCtx(context.Background()); got != nil {
		t.Fatalf("context without carrier returned observer %v", got)
	}
}

// TestWrapperRetryEmitsJourneyRetryScheduled drives the retry loop through two
// retriable failures and asserts the journey observer sees one retry boundary
// per retry with the classified error reason, and nothing on the success path.
func TestWrapperRetryEmitsJourneyRetryScheduled(t *testing.T) {
	observer := &fakeJourneyObserver{}
	var attempts int32
	streamFunc := func(ctx context.Context, w http.ResponseWriter) error {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			return &HTTPError{StatusCode: http.StatusServiceUnavailable, Err: errors.New("service unavailable")}
		}
		return nil
	}

	cfg := DefaultConfig()
	cfg.BaseDelayMs = 1
	cfg.MaxDelayMs = 2
	wrapper := NewWrapper(cfg, nil)

	ctx := withRequestCarrier(context.Background())
	BindJourneyObserver(ctx, observer)
	if _, err := wrapper.ExecuteWithMetrics(ctx, httptest.NewRecorder(), streamFunc); err != nil {
		t.Fatalf("ExecuteWithMetrics() error = %v", err)
	}

	reasons := observer.retryReasons()
	if len(reasons) != 2 {
		t.Fatalf("retry emissions = %v, want one per retry (2)", reasons)
	}
	for _, reason := range reasons {
		if reason == "" {
			t.Fatalf("retry emission carried empty reason: %v", reasons)
		}
	}
}

// TestWrapperRetryWithoutObserverIsNoOp verifies requests that never bound a
// journey lifecycle (non-retry entry paths, unit callers) retry unchanged.
func TestWrapperRetryWithoutObserverIsNoOp(t *testing.T) {
	var attempts int32
	streamFunc := func(ctx context.Context, w http.ResponseWriter) error {
		if atomic.AddInt32(&attempts, 1) < 2 {
			return &HTTPError{StatusCode: http.StatusServiceUnavailable, Err: errors.New("service unavailable")}
		}
		return nil
	}

	cfg := DefaultConfig()
	cfg.BaseDelayMs = 1
	cfg.MaxDelayMs = 2
	wrapper := NewWrapper(cfg, nil)

	if _, err := wrapper.ExecuteWithMetrics(context.Background(), httptest.NewRecorder(), streamFunc); err != nil {
		t.Fatalf("ExecuteWithMetrics() error = %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

func TestErrorRecorderTreatsAnyWrittenStatusAsCommitted(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadRequest, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			recorder := &errorRecorder{ResponseWriter: httptest.NewRecorder()}
			recorder.WriteHeader(status)
			if !recorder.committed {
				t.Fatalf("status %d was forwarded but recorder remained retryable", status)
			}
		})
	}
}

func TestErrorRecorderPropagatesFlushErrorAfterCommit(t *testing.T) {
	flushErr := errors.New("connection closed")
	underlying := &flushErrorResponseWriter{ResponseWriter: httptest.NewRecorder(), flushErr: flushErr}
	recorder := &errorRecorder{ResponseWriter: underlying}

	if err := recorder.FlushError(); err != nil || underlying.flushes != 0 {
		t.Fatalf("pre-commit flush = (%v, %d), want nil and zero calls", err, underlying.flushes)
	}
	recorder.WriteHeader(http.StatusOK)
	if err := recorder.FlushError(); !errors.Is(err, flushErr) {
		t.Fatalf("committed FlushError() = %v, want %v", err, flushErr)
	}
	if underlying.flushes != 1 {
		t.Fatalf("underlying flushes = %d, want 1", underlying.flushes)
	}
}

// TestDefaultStreamExecutor_SuccessFirstAttempt wraps a happy-path handler and
// verifies the executor forwards the response without retrying.
func TestDefaultStreamExecutor_SuccessFirstAttempt(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: ok\n\n")
	})

	cfg := DefaultConfig()
	exec := NewDefaultStreamExecutor(inner, cfg)

	rec := httptest.NewRecorder()
	req := newStreamingRequest()

	metrics, err := exec.ExecuteStreamWithMetrics(req.Context(), rec, req)
	if err != nil {
		t.Fatalf("ExecuteStreamWithMetrics() error = %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "data: ok") {
		t.Errorf("body = %q, want contains %q", rec.Body.String(), "data: ok")
	}

	if metrics.TotalAttempts != 1 {
		t.Errorf("TotalAttempts = %d, want 1", metrics.TotalAttempts)
	}
	if metrics.TotalRetries != 0 {
		t.Errorf("TotalRetries = %d, want 0", metrics.TotalRetries)
	}
}

// TestDefaultStreamExecutor_RetryOnTransientFailure verifies retrying a
// transient failure reported before the handler commits a response.
func TestDefaultStreamExecutor_RetryOnTransientFailure(t *testing.T) {
	var attempts int32

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			w.(*errorRecorder).err = &HTTPError{
				StatusCode: http.StatusServiceUnavailable,
				Err:        errors.New("service unavailable"),
			}
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "data: recovered-after-%d\n\n", n)
	})

	cfg := DefaultConfig()
	cfg.BaseDelayMs = 5 // tight loop for tests
	cfg.MaxDelayMs = 20
	exec := NewDefaultStreamExecutor(inner, cfg)

	rec := httptest.NewRecorder()
	req := newStreamingRequest()

	metrics, err := exec.ExecuteStreamWithMetrics(req.Context(), rec, req)
	if err != nil {
		t.Fatalf("ExecuteStreamWithMetrics() error = %v", err)
	}

	// Inner handler ran all 3 attempts — that's what matters.
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Errorf("inner attempts = %d, want 3", got)
	}

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

func TestDefaultStreamExecutor_DoesNotRetryAfterFirstStreamFrame(t *testing.T) {
	var attempts int32
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: first-frame\\n\\n"))
		w.(*errorRecorder).err = &HTTPError{StatusCode: http.StatusServiceUnavailable, Err: errors.New("upstream disconnected")}
	})

	cfg := DefaultConfig()
	cfg.BaseDelayMs = 1
	cfg.MaxRetries = 3
	exec := NewDefaultStreamExecutor(inner, cfg)
	rec := httptest.NewRecorder()
	metrics, err := exec.ExecuteStreamWithMetrics(context.Background(), rec, newStreamingRequest())
	if err == nil {
		t.Fatal("ExecuteStreamWithMetrics() error = nil, want committed stream error")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("attempts = %d, want 1 after first frame", got)
	}
	if metrics.TotalAttempts != 1 || metrics.TotalRetries != 0 {
		t.Fatalf("metrics = %+v, want one committed attempt", metrics)
	}
	if got := rec.Body.String(); got != "data: first-frame\\n\\n" {
		t.Fatalf("response body = %q, want one first frame", got)
	}
}

func TestDefaultStreamExecutor_DoesNotRetryCommittedResponse(t *testing.T) {
	var attempts int32
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	})

	cfg := DefaultConfig()
	cfg.BaseDelayMs = 1
	exec := NewDefaultStreamExecutor(inner, cfg)
	metrics, err := exec.ExecuteStreamWithMetrics(context.Background(), httptest.NewRecorder(), newStreamingRequest())
	if err == nil {
		t.Fatal("ExecuteStreamWithMetrics() error = nil, want committed response error")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("attempts = %d, want 1 after response commitment", got)
	}
	if metrics.TotalAttempts != 1 {
		t.Fatalf("TotalAttempts = %d, want 1", metrics.TotalAttempts)
	}
}

// TestDefaultStreamExecutor_NonRetriableErrorReturns verifies 4xx (non-retriable)
// errors propagate immediately without consuming retry budget.
func TestDefaultStreamExecutor_NonRetriableErrorReturns(t *testing.T) {
	var attempts int32

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		http.Error(w, "bad request", http.StatusBadRequest)
	})

	cfg := DefaultConfig()
	cfg.BaseDelayMs = 5
	exec := NewDefaultStreamExecutor(inner, cfg)

	rec := httptest.NewRecorder()
	req := newStreamingRequest()

	exec.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (pass-through, no retry)", rec.Code)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("attempts = %d, want 1 (no retry on 4xx)", got)
	}
}

// TestDefaultStreamExecutor_DisabledRetryFastPath verifies Enabled=false skips
// the retry loop entirely (used as the off-switch for emergency rollback).
func TestDefaultStreamExecutor_DisabledRetryFastPath(t *testing.T) {
	var attempts int32

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		http.Error(w, "boom", http.StatusServiceUnavailable)
	})

	cfg := DefaultConfig()
	cfg.Enabled = false
	exec := NewDefaultStreamExecutor(inner, cfg)

	rec := httptest.NewRecorder()
	req := newStreamingRequest()

	exec.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("attempts = %d, want 1 (retry disabled)", got)
	}
}

// TestDefaultStreamExecutor_ContextCanceledStopsLoop verifies a canceled
// request context aborts the retry loop instead of hanging on backoff sleeps.
// The context error surfaces as the value returned from ServeHTTP / ExecuteStream
// (the wrapper's LastError is set to the prior attempt's err before sleep).
func TestDefaultStreamExecutor_ContextCanceledStopsLoop(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Pre-flight failure: status code only, no body. Retriable.
		w.(*errorRecorder).err = &HTTPError{
			StatusCode: http.StatusServiceUnavailable,
			Err:        errors.New("fail"),
		}
	})

	cfg := DefaultConfig()
	cfg.BaseDelayMs = 1000 // long enough that cancellation kicks in first
	exec := NewDefaultStreamExecutor(inner, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	rec := httptest.NewRecorder()
	req := newStreamingRequest().WithContext(ctx)

	metrics, err := exec.ExecuteStreamWithMetrics(req.Context(), rec, req)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("ExecuteStream err = %v, want context.Canceled", err)
	}
	// Only the first attempt happened (cancel kicked in during sleep before attempt 2).
	if metrics.TotalAttempts != 1 {
		t.Errorf("TotalAttempts = %d, want 1 (canceled before next attempt)", metrics.TotalAttempts)
	}
}

// TestDefaultStreamExecutor_ExhaustedRetriesReturnsLastError verifies the
// final retriable error propagates back through ExecuteStream after the
// retry budget is consumed.
func TestDefaultStreamExecutor_ExhaustedRetriesReturnsLastError(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Pre-flight failure: status code only, no body. This is
		// retriable — the handler has not committed a response.
		w.(*errorRecorder).err = &HTTPError{
			StatusCode: http.StatusServiceUnavailable,
			Err:        errors.New("service unavailable"),
		}
	})

	cfg := DefaultConfig()
	cfg.MaxRetries = 2
	cfg.BaseDelayMs = 5
	exec := NewDefaultStreamExecutor(inner, cfg)

	metrics, err := exec.ExecuteStreamWithMetrics(context.Background(), httptest.NewRecorder(), newStreamingRequest())
	if err == nil {
		t.Fatalf("ExecuteStream() error = nil, want non-nil after retries exhausted")
	}

	if metrics.TotalAttempts != 3 { // initial + 2 retries
		t.Errorf("TotalAttempts = %d, want 3 (initial + MaxRetries)", metrics.TotalAttempts)
	}
	if metrics.SuccessAttempt != -1 {
		t.Errorf("SuccessAttempt = %d, want -1 (all failed)", metrics.SuccessAttempt)
	}
}

// TestDefaultStreamExecutor_ConcurrentRequestsHaveIndependentMetrics verifies
// that one shared mux handler cannot overwrite another request's metrics.
func TestDefaultStreamExecutor_ConcurrentRequestsHaveIndependentMetrics(t *testing.T) {
	const requestCount = 32
	var attempts sync.Map

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Test-Request")
		current, _ := attempts.LoadOrStore(requestID, new(int32))
		attempt := atomic.AddInt32(current.(*int32), 1)
		failures := int32(0)
		if value, err := strconv.Atoi(r.Header.Get("X-Test-Failures")); err == nil {
			failures = int32(value)
		}
		if attempt <= failures {
			w.(*errorRecorder).err = &HTTPError{
				StatusCode: http.StatusServiceUnavailable,
				Err:        errors.New("service unavailable"),
			}
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	cfg := DefaultConfig()
	cfg.BaseDelayMs = 1
	cfg.MaxDelayMs = 2
	exec := NewDefaultStreamExecutor(inner, cfg)

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	for i := 0; i < requestCount; i++ {
		i := i
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			failures := i % 3
			req := newStreamingRequest()
			req.Header.Set("X-Test-Request", fmt.Sprintf("request-%d", i))
			req.Header.Set("X-Test-Failures", strconv.Itoa(failures))
			metrics, err := exec.ExecuteStreamWithMetrics(req.Context(), httptest.NewRecorder(), req)
			if err != nil {
				t.Errorf("request %d: ExecuteStreamWithMetrics() error = %v", i, err)
				return
			}
			wantAttempts := failures + 1
			if metrics.TotalAttempts != int(wantAttempts) {
				t.Errorf("request %d: TotalAttempts = %d, want %d", i, metrics.TotalAttempts, wantAttempts)
			}
			if metrics.TotalRetries != int(failures) || metrics.SuccessAttempt != int(failures) {
				t.Errorf("request %d: metrics = %+v, want retries/success=%d", i, metrics, failures)
			}
			_ = exec.wrapper.Metrics()
		}()
	}
	start.Done()
	done.Wait()
}

func newStreamingRequest() *http.Request {
	return httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"stream":true}`))
}
