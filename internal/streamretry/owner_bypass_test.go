package streamretry

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/retryowner"
)

// TestWrapperSurvivalOwnerExecutesOnce pins the SR-W0 ownership contract
// (docs/修订0811/19): when the request context freezes
// retryowner.Survival, this wrapper must execute the inner handler exactly
// once and never retry — the SurvivalCoordinator owns every retry decision,
// so nesting the streamretry loop would multiply provider calls and cost.
func TestWrapperSurvivalOwnerExecutesOnce(t *testing.T) {
	var attempts int32

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Every attempt reports a retriable 503; under the legacy owner the
		// wrapper would loop, under the survival owner it must not.
		if n < 3 {
			if rec, ok := w.(*errorRecorder); ok {
				rec.err = &HTTPError{StatusCode: http.StatusServiceUnavailable, Err: errors.New("service unavailable")}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: ok\n\n"))
	})

	cfg := DefaultConfig()
	cfg.MaxRetries = 3
	exec := NewDefaultStreamExecutor(inner, cfg)

	req := newStreamingRequest()
	ctx := retryowner.FreezeOwner(req.Context(), retryowner.Survival)

	rec := httptest.NewRecorder()
	_, _ = exec.ExecuteStreamWithMetrics(ctx, rec, req.WithContext(ctx))

	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("inner handler attempts = %d, want exactly 1 under survival owner", got)
	}
	if !strings.Contains(rec.Body.String(), "data: ok") {
		t.Errorf("body = %q, want the single attempt's payload", rec.Body.String())
	}
}

// TestWrapperLegacyOwnerStillRetries guards the flip side: without a frozen
// survival owner, the wrapper keeps its legacy retry behavior.
func TestWrapperLegacyOwnerStillRetries(t *testing.T) {
	var attempts int32

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n < 2 {
			if rec, ok := w.(*errorRecorder); ok {
				rec.err = &HTTPError{StatusCode: http.StatusServiceUnavailable, Err: errors.New("service unavailable")}
			}
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: ok\n\n"))
	})

	cfg := DefaultConfig()
	cfg.MaxRetries = 3
	exec := NewDefaultStreamExecutor(inner, cfg)

	rec := httptest.NewRecorder()
	req := newStreamingRequest()
	ctx := retryowner.FreezeOwner(req.Context(), retryowner.Legacy)

	_, _ = exec.ExecuteStreamWithMetrics(ctx, rec, req.WithContext(ctx))

	if got := atomic.LoadInt32(&attempts); got < 2 {
		t.Fatalf("inner handler attempts = %d, want >=2 under legacy owner", got)
	}
}
