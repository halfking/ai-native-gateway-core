package requestdetail

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// flakyBodies returns ErrNotFound for the first missCount calls to
// ReadRequestLogsBodies, then returns a populated record. Used to simulate
// the cross-replica read-your-writes race window that retry is designed to
// cover.
type flakyBodies struct {
	missCount int32 // remaining ErrNotFound responses
	calls     int32 // total call count
	bodies    Bodies
	meta      Meta
}

func (f *flakyBodies) ReadRequestLogsBodies(_ context.Context, requestID string, _ bool) (Bodies, Meta, error) {
	atomic.AddInt32(&f.calls, 1)
	if atomic.LoadInt32(&f.missCount) > 0 {
		atomic.AddInt32(&f.missCount, -1)
		return Bodies{}, Meta{}, ErrNotFound
	}
	return f.bodies, f.meta, nil
}

func (f *flakyBodies) ReadSessionTurnsBodies(_ context.Context, _ string, _ bool) (Bodies, Meta, error) {
	return Bodies{}, Meta{}, ErrNotFound
}

func TestLocator_DBRetry_RecoversOnSecondAttempt(t *testing.T) {
	flaky := &flakyBodies{
		missCount: 1,
		bodies:    Bodies{RequestBody: []byte(`{"prompt":"hi"}`)},
		meta:      Meta{RequestID: "req-retry-hit-001", TenantID: "t1"},
	}
	loc := &Locator{
		Bodies:      flaky,
		DBRetryCount: 2,
		DBRetryDelay: 10 * time.Millisecond,
	}
	d, err := loc.Get(context.Background(), "req-retry-hit-001", false)
	if err != nil {
		t.Fatalf("expected retry to recover, got err: %v", err)
	}
	if d.Source != SourceRequestLogs || d.Bodies == nil {
		t.Fatalf("expected request_logs source with bodies, got source=%s bodies=%+v", d.Source, d.Bodies)
	}
	if got := atomic.LoadInt32(&flaky.calls); got != 2 {
		t.Fatalf("expected 2 calls (1 miss + 1 hit), got %d", got)
	}
}

func TestLocator_DBRetry_MissAfterExhaustion(t *testing.T) {
	flaky := &flakyBodies{
		missCount: 5, // more misses than maxAttempts → still miss
	}
	loc := &Locator{
		Bodies:      flaky,
		DBRetryCount: 2,
		DBRetryDelay: 5 * time.Millisecond,
	}
	_, err := loc.Get(context.Background(), "req-retry-miss-001", false)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after retry exhaustion, got %v", err)
	}
	if got := atomic.LoadInt32(&flaky.calls); got != 2 {
		t.Fatalf("expected 2 total attempts (no extra), got %d", got)
	}
}

func TestLocator_DBRetry_DisabledByZeroCount(t *testing.T) {
	flaky := &flakyBodies{
		missCount: 5,
	}
	loc := &Locator{
		Bodies:      flaky,
		DBRetryCount: 0, // explicit disable
	}
	_, err := loc.Get(context.Background(), "req-retry-disabled-001", false)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound with retry disabled, got %v", err)
	}
	if got := atomic.LoadInt32(&flaky.calls); got != 1 {
		t.Fatalf("expected exactly 1 call (no retry), got %d", got)
	}
}

// failingBodies returns a non-ErrNotFound error immediately. Retry MUST NOT
// loop on infra failures — that would amplify outages. This test pins the
// "only retry on the read-your-writes race" contract.
type failingBodies struct {
	calls int32
}

func (f *failingBodies) ReadRequestLogsBodies(_ context.Context, _ string, _ bool) (Bodies, Meta, error) {
	atomic.AddInt32(&f.calls, 1)
	return Bodies{}, Meta{}, errors.New("connection refused")
}
func (f *failingBodies) ReadSessionTurnsBodies(_ context.Context, _ string, _ bool) (Bodies, Meta, error) {
	atomic.AddInt32(&f.calls, 1)
	return Bodies{}, Meta{}, errors.New("connection refused")
}

func TestLocator_DBRetry_NonNotFoundErrorNotRetried(t *testing.T) {
	failing := &failingBodies{}
	loc := &Locator{
		Bodies:      failing,
		DBRetryCount: 3,
		DBRetryDelay: 5 * time.Millisecond,
	}
	_, err := loc.Get(context.Background(), "req-retry-noop-001", false)
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("expected infra error to propagate, got %v", err)
	}
	if got := atomic.LoadInt32(&failing.calls); got != 1 {
		t.Fatalf("expected 1 call (no retry on infra error), got %d", got)
	}
}

func TestLocator_DBRetry_ContextCancellationStopsDelay(t *testing.T) {
	flaky := &flakyBodies{
		missCount: 5,
	}
	loc := &Locator{
		Bodies:      flaky,
		DBRetryCount: 5,
		DBRetryDelay: 1 * time.Second, // long delay → cancellation kicks in
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := loc.Get(ctx, "req-retry-cancel-001", false)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected ctx error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("retry loop did not honor ctx cancel; took %s", elapsed)
	}
}

func TestLocator_DBRetry_DefaultsAppliedWhenUnset(t *testing.T) {
	flaky := &flakyBodies{missCount: 5}
	loc := &Locator{Bodies: flaky} // zero DBRetryCount + zero Delay
	// 0 is treated as "use default 1 attempt" (no retry). The call count
	// proves the default kicks in.
	_, err := loc.Get(context.Background(), "req-retry-default-001", false)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if got := atomic.LoadInt32(&flaky.calls); got != 1 {
		t.Fatalf("expected 1 call (default = 1 attempt), got %d", got)
	}
}
