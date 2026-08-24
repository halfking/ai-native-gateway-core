package dispatch

// 会话优化 v4 T3-8 — timed retry scheduling tests (UT-DQ-04).
//
// The HeapRetryScheduler is exercised with an injectable fake clock/sleeper:
// the sleeper advances the fake clock by the requested wait, so due-time
// ordering is deterministic without real sleeping.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeClock is a mutable clock advanced only by an explicit test pulse.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// steppedSleeper blocks until the test pulses the step channel, then
// advances the fake clock by exactly the requested wait. Deterministic:
// without a pulse, time never moves, so nothing can fire early.
func steppedSleeper(clock *fakeClock, step chan struct{}) RetrySleeper {
	return func(ctx context.Context, d time.Duration, wake <-chan struct{}) {
		select {
		case <-step:
			clock.advance(d)
		case <-wake:
			// A newly scheduled item became the earliest one — recompute
			// without advancing time.
		case <-ctx.Done():
		}
	}
}

// advancingSleeper instantly advances the fake clock by the requested wait.
// Used by flow tests that only need fake time to keep moving (no mid-flight
// parked-count assertions — those need the stepped sleeper).
func advancingSleeper(clock *fakeClock) RetrySleeper {
	return func(ctx context.Context, d time.Duration, _ <-chan struct{}) {
		clock.advance(d)
	}
}

// parkedRequest is a minimal QueuedRequest for scheduler-only tests.
func parkedRequest(id string) *QueuedRequest {
	return NewQueuedRequest(id, "t", "m", context.Background(), nil)
}

// TestNextRetryDelayLadder pins the R2.4 backoff ladder: 5s base, ×2 growth,
// 120s cap; upstream Retry-After wins but is clamped into the same window.
func TestNextRetryDelayLadder(t *testing.T) {
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second,
		40 * time.Second, 80 * time.Second, 120 * time.Second, 120 * time.Second}
	for i, d := range want {
		if got := NextRetryDelay(i+1, 0); got != d {
			t.Fatalf("NextRetryDelay(%d) = %v, want %v", i+1, got, d)
		}
	}
	if got := NextRetryDelay(50, 0); got != 120*time.Second {
		t.Fatalf("ladder must clamp at 120s, got %v", got)
	}
	if got := NextRetryDelay(1, 30*time.Second); got != 30*time.Second {
		t.Fatalf("upstream Retry-After must take priority, got %v", got)
	}
	if got := NextRetryDelay(9, 500*time.Millisecond); got != 5*time.Second {
		t.Fatalf("Retry-After below base must clamp to 5s, got %v", got)
	}
	if got := NextRetryDelay(1, time.Hour); got != 120*time.Second {
		t.Fatalf("Retry-After above cap must clamp to 120s, got %v", got)
	}
	if got := NextRetryDelay(0, 0); got != 5*time.Second {
		t.Fatalf("seq 0 must fall back to the base delay, got %v", got)
	}
}

// TestHeapRetrySchedulerFiresInDueTimeOrder (UT-DQ-04 core): items fire at
// their retry_at in due order regardless of scheduling order. The stepped
// sleeper keeps fake time frozen between pulses, so nothing fires early and
// the parked-count check is deterministic.
func TestHeapRetrySchedulerFiresInDueTimeOrder(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1700000000, 0)}
	step := make(chan struct{})
	var mu sync.Mutex
	fired := make([]string, 0, 4)
	s := NewHeapRetryScheduler(func(qr *QueuedRequest, retryAt time.Time) {
		mu.Lock()
		fired = append(fired, fmt.Sprintf("%s@%d", qr.ID, retryAt.Unix()))
		mu.Unlock()
	}, clock, steppedSleeper(clock, step))
	defer s.Close()

	base := clock.Now()
	if !s.Schedule(parkedRequest("late"), base.Add(10*time.Second)) {
		t.Fatal("Schedule refused")
	}
	if !s.Schedule(parkedRequest("early"), base.Add(1*time.Second)) {
		t.Fatal("Schedule refused")
	}
	if !s.Schedule(parkedRequest("tied-a"), base.Add(1*time.Second)) {
		t.Fatal("Schedule refused")
	}
	// No pulse yet: fake time is frozen, nothing can be due.
	if got := s.Len(); got != 3 {
		t.Fatalf("parked = %d, want 3", got)
	}

	// Pulse time forward until everything fired. Each pulse advances by the
	// picker's current head wait (1s, then 9s); wake-ups reschedule without
	// advancing, so ordering stays (retry_at, seq).
	pulse := func() {
		select {
		case step <- struct{}{}:
		default:
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := len(fired) == 3
		mu.Unlock()
		if done {
			break
		}
		pulse()
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(fired) != 3 {
		t.Fatalf("fired = %v", fired)
	}
	if fired[0] != "early@1700000001" {
		t.Fatalf("earliest retry must fire first, got %v", fired)
	}
	if fired[1] != "tied-a@1700000001" {
		t.Fatalf("equal retry_at must keep FIFO order, got %v", fired)
	}
	if fired[2] != "late@1700000010" {
		t.Fatalf("later retry must fire last, got %v", fired)
	}
}

// TestHeapRetrySchedulerCloseDropsPending: Close stops the picker; pending
// items are dropped and Schedule refuses.
func TestHeapRetrySchedulerCloseDropsPending(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	step := make(chan struct{})
	var fired int
	var mu sync.Mutex
	s := NewHeapRetryScheduler(func(*QueuedRequest, time.Time) {
		mu.Lock()
		fired++
		mu.Unlock()
	}, clock, steppedSleeper(clock, step))
	if !s.Schedule(parkedRequest("r"), clock.Now().Add(time.Hour)) {
		t.Fatal("Schedule refused before Close")
	}
	s.Close()
	s.Close() // idempotent
	if s.Schedule(parkedRequest("r2"), clock.Now().Add(time.Minute)) {
		t.Fatal("Schedule must refuse after Close")
	}
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if fired != 0 {
		t.Fatalf("closed scheduler must not fire pending items, fired %d", fired)
	}
}

// TestHeapRetrySchedulerCloseCompletesParkedItems: with a close handler
// wired, Close hands every still-parked request to the handler instead of
// silently dropping it — Submit callers otherwise wait on ResultCh forever
// during graceful shutdown. Nil handler keeps the legacy drop behaviour.
func TestHeapRetrySchedulerCloseCompletesParkedItems(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	var mu sync.Mutex
	var closed []*QueuedRequest
	s := NewHeapRetrySchedulerWithCloseHandler(
		func(*QueuedRequest, time.Time) {},
		func(qr *QueuedRequest) {
			mu.Lock()
			defer mu.Unlock()
			closed = append(closed, qr)
		},
		clock, steppedSleeper(clock, make(chan struct{})))

	if !s.Schedule(parkedRequest("a"), clock.Now().Add(time.Hour)) {
		t.Fatal("Schedule refused")
	}
	if !s.Schedule(parkedRequest("b"), clock.Now().Add(2*time.Hour)) {
		t.Fatal("Schedule refused")
	}
	s.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(closed) != 2 {
		t.Fatalf("close handler saw %d parked requests, want 2", len(closed))
	}
	seen := map[string]bool{}
	for _, qr := range closed {
		seen[qr.ID] = true
	}
	if !seen["a"] || !seen["b"] {
		t.Fatalf("close handler saw %v, want both parked requests", seen)
	}
	if got := s.Len(); got != 0 {
		t.Fatalf("heap retained %d items after Close, want 0", got)
	}
}

// TestPipelineTimedRetryFlow (UT-DQ-04, pipeline): a pre-firstbyte failure
// with a RetryScheduler wired parks the request as pending+retry_at, the
// retry_scheduled observation carries retry_at, and the due pickup re-enters
// in-flight and completes successfully.
func TestPipelineTimedRetryFlow(t *testing.T) {
	clock := &fakeClock{now: time.Now()}

	f := &fakeDeps{
		// ProviderID must be set: the retry_scheduled observation carries an
		// AttemptRef whose validation requires a positive provider id.
		refsByModel: map[string][]CredentialRef{"m": {credWithProvider(1, 10, 4)}},
		forwardFn: func(_ context.Context, qr *QueuedRequest, _ CredentialRef) ForwardOutcome {
			if qr.AttemptCount == 1 {
				return ForwardOutcome{Err: context.DeadlineExceeded, ErrorKind: "deadline_exceeded"}
			}
			return ForwardOutcome{}
		},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	events := collectObservations(p)
	s := NewHeapRetryScheduler(p.onRetryDue, clock, advancingSleeper(clock))
	defer s.Close()
	p.SetRetryScheduler(s)
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("retry-1", "tenant", "m", context.Background(), nil)
	qr.GatewayInstanceID = "gw-test" // required for observation emission
	done := make(chan error, 1)
	go func() {
		res, err := p.Submit(context.Background(), qr)
		if err == nil && res != "ok:cred1:call2" {
			done <- fmt.Errorf("unexpected result %v", res)
			return
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("timed retry flow failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed retry never completed")
	}

	// Registry must have parked the request with retry_at and completed it.
	entry, ok := p.registry.Get("retry-1")
	if !ok || entry.State != LifecycleCompleted {
		t.Fatalf("terminal registry state = %#v (ok=%v)", entry, ok)
	}

	// The retry_scheduled observation must carry retry_at (async pump — poll).
	deadline := time.Now().Add(time.Second)
	for {
		found, bad := false, false
		for _, observation := range events() {
			if observation.Type != ObservationRetryScheduled {
				continue
			}
			if observation.RetryAt == nil {
				bad = true
			}
			found = true
		}
		if found && !bad {
			break
		}
		if bad {
			t.Fatal("retry_scheduled must carry retry_at when scheduled")
		}
		if time.Now().After(deadline) {
			t.Fatal("no retry_scheduled observation recorded")
		}
		time.Sleep(time.Millisecond)
	}
	if got := f.forwardCalls[1]; got != 2 {
		t.Fatalf("forward calls = %d, want 2 (fail then timed retry)", got)
	}
}
