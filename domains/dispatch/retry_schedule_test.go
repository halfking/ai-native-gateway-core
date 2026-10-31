package dispatch

// 会话优化 v4 T3-8 — timed retry scheduling tests (UT-DQ-04).
//
// The HeapRetryScheduler is exercised with an injectable fake clock/sleeper:
// the sleeper advances the fake clock by the requested wait, so due-time
// ordering is deterministic without real sleeping.

import (
	"context"
	"errors"
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
	}, clock, steppedSleeper(clock, step), 0 /* maxItems: unbounded */)
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
	}, clock, steppedSleeper(clock, step), 0 /* maxItems: unbounded */)
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
		clock, steppedSleeper(clock, make(chan struct{})), 0 /* maxItems: unbounded */)

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

// TestHeapRetrySchedulerMaxItemsBound (audit 2026-09-14 R28 #15b): Schedule
// refuses (returns false) once the parked heap reaches maxItems, so callers
// take their existing immediate-path fallback; already-parked items still
// fire normally. maxItems=0 keeps the legacy unbounded behavior.
func TestHeapRetrySchedulerMaxItemsBound(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	step := make(chan struct{})
	var mu sync.Mutex
	fired := 0
	s := NewHeapRetryScheduler(func(*QueuedRequest, time.Time) {
		mu.Lock()
		fired++
		mu.Unlock()
	}, clock, steppedSleeper(clock, step), 2 /* maxItems */)
	defer s.Close()

	if !s.Schedule(parkedRequest("a"), clock.Now().Add(time.Hour)) {
		t.Fatal("Schedule refused below capacity")
	}
	if !s.Schedule(parkedRequest("b"), clock.Now().Add(2*time.Hour)) {
		t.Fatal("Schedule refused at last free slot")
	}
	if s.Schedule(parkedRequest("c"), clock.Now().Add(3*time.Hour)) {
		t.Fatal("Schedule must refuse at maxItems capacity")
	}
	if got := s.Len(); got != 2 {
		t.Fatalf("parked = %d, want 2", got)
	}

	// Unbounded constructor (maxItems=0) keeps accepting.
	unbounded := NewHeapRetryScheduler(func(*QueuedRequest, time.Time) {}, clock, steppedSleeper(clock, make(chan struct{})), 0)
	defer unbounded.Close()
	for i := 0; i < 4; i++ {
		if !unbounded.Schedule(parkedRequest(fmt.Sprintf("u%d", i)), clock.Now().Add(time.Hour)) {
			t.Fatalf("unbounded scheduler refused item %d", i)
		}
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
	s := NewHeapRetryScheduler(p.onRetryDue, clock, advancingSleeper(clock), 0 /* maxItems: unbounded */)
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

// ===== P0-C (audit 2026-10-28 §4): retry budget + backoff jitter =====

// TestApplyRetryJitterBoundaries pins the ±20% envelope under the
// deterministic seam; identical boundaries to the survival layer
// (survival_jitter_test.go), because both delegate to one implementation.
func TestApplyRetryJitterBoundaries(t *testing.T) {
	defer SetRetryJitterEnabled(true)
	SetRetryJitterEnabled(true)
	d := 10 * time.Second
	cases := []struct {
		r    float64
		want time.Duration
	}{
		{0, 8 * time.Second},
		{0.5, 10 * time.Second},
		{1, 12 * time.Second},
	}
	for _, c := range cases {
		if got := ApplyRetryJitter(d, func() float64 { return c.r }); got != c.want {
			t.Fatalf("ApplyRetryJitter(10s, r=%v) = %v, want %v", c.r, got, c.want)
		}
	}
	// Disabled ⇒ pass-through byte-identical to the pre-P0-C timeline.
	SetRetryJitterEnabled(false)
	if got := ApplyRetryJitter(d, func() float64 { return 0 }); got != d {
		t.Fatalf("jitter disabled must pass through unchanged, got %v", got)
	}
	SetRetryJitterEnabled(true)
	// Degenerate inputs never jitter.
	if got := ApplyRetryJitter(0, nil); got != 0 {
		t.Fatalf("0 must pass through, got %v", got)
	}
	if got := ApplyRetryJitter(-time.Second, nil); got != -time.Second {
		t.Fatalf("negative must pass through, got %v", got)
	}
	// Randomized: within [0.8d, 1.2d], and the sample set actually spreads
	// (a constant function would pass the bounds check but defeat the goal).
	low, high := time.Duration(1<<62), time.Duration(0)
	for i := 0; i < 200; i++ {
		j := ApplyRetryJitter(d, nil)
		if j < 8*time.Second || j > 12*time.Second {
			t.Fatalf("jitter out of envelope: %v", j)
		}
		if j < low {
			low = j
		}
		if j > high {
			high = j
		}
	}
	if high-low < time.Second {
		t.Fatalf("jitter did not spread: min=%v max=%v", low, high)
	}
}

// TestApplyRetryJitterParityWithSurvival pins the formula on the dispatch
// side: the ungated core is the only jitter implementation, and the gated
// entry differs from it ONLY by the config gate (the survival layer
// delegates to the same core — see TestApplyBackoffJitterBoundaries).
func TestApplyRetryJitterParityWithSurvival(t *testing.T) {
	defer SetRetryJitterEnabled(true)
	SetRetryJitterEnabled(true)
	for _, r := range []float64{0, 0.25, 0.5, 0.75, 1} {
		seam := func() float64 { return r }
		for _, d := range []time.Duration{5 * time.Second, 37 * time.Second, 120 * time.Second} {
			always := ApplyRetryJitterAlways(d, seam)
			gated := ApplyRetryJitter(d, seam)
			if gated != always {
				t.Fatalf("gated must equal the ungated core while enabled: r=%v d=%v", r, d)
			}
			want := time.Duration(float64(d) * (1 + 0.2*(2*r-1)))
			if always != want {
				t.Fatalf("formula drift: r=%v d=%v got=%v want=%v", r, d, always, want)
			}
		}
	}
}

// TestRetryBudgetShadowVsEnforce drives observeRetryBudget through a real
// pipeline: shadow counts without refusing; enforce refuses and the mover
// falls through to the sibling ladder.
func TestRetryBudgetShadowVsEnforce(t *testing.T) {
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{"gpt4": {cred(1, ModeConcurrency, 5)}},
		forwardFn: func(ctx context.Context, qr *QueuedRequest, c CredentialRef) ForwardOutcome {
			return ForwardOutcome{Err: errors.New("timeout"), ErrorKind: "timeout"}
		},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	sched := p.NewDefaultRetryScheduler()
	defer sched.Close()
	p.SetRetryScheduler(sched)

	// Force the budget to bite: percent 0 would disable; instead put parked
	// items above the floor by shrinking the floor via Reload.
	cfg := DefaultConfig()
	cfg.RetryBudgetPercent = 25
	cfg.RetryBudgetFloor = 0 // budget = active×25% + 0
	p.Reload(cfg)

	// No in-flight, 3 parked ⇒ budget = 0 ⇒ every admission must exceed.
	qr1 := NewQueuedRequest("rb1", "t", "gpt4", context.Background(), "payload")
	qr2 := NewQueuedRequest("rb2", "t", "gpt4", context.Background(), "payload")
	qr3 := NewQueuedRequest("rb3", "t", "gpt4", context.Background(), "payload")
	for _, qr := range []*QueuedRequest{qr1, qr2, qr3} {
		if !sched.Schedule(qr, time.Now().Add(time.Minute)) {
			t.Fatal("precondition: scheduler must accept")
		}
	}
	if p.inFlight.Load() != 0 {
		t.Fatalf("precondition: inFlight = %d, want 0", p.inFlight.Load())
	}

	// Shadow (default config Reload kept enforce=false… wait, Reload above
	// kept cfg.RetryBudgetEnforce=false): count only.
	if !p.observeRetryBudget(qr1) {
		t.Fatal("shadow mode must not refuse")
	}

	// Enforce: refuse while over budget.
	cfg.RetryBudgetEnforce = true
	p.Reload(cfg)
	if p.observeRetryBudget(qr2) {
		t.Fatal("enforce mode must refuse when parked ≥ budget")
	}

	// Under budget ⇒ allow: raise the floor so budget > parked.
	cfg.RetryBudgetFloor = 10
	p.Reload(cfg)
	if !p.observeRetryBudget(qr3) {
		t.Fatal("enforce mode must allow when parked < budget")
	}

	// Percent 0 disables the budget entirely, even in enforce.
	cfg.RetryBudgetPercent = 0
	p.Reload(cfg)
	if !p.observeRetryBudget(qr1) {
		t.Fatal("percent 0 must disable the budget")
	}
}

// TestReloadAppliesJitterGate pins the config→gate wiring: Reload carries
// RetryJitterEnabled into the package-level gate (and DefaultConfig turns
// jitter ON — the P0-C default).
func TestReloadAppliesJitterGate(t *testing.T) {
	defer SetRetryJitterEnabled(true)
	p := NewPipeline(Deps{})
	defer p.Stop()
	if !RetryJitterEnabled() {
		t.Fatal("DefaultConfig must enable jitter")
	}
	cfg := DefaultConfig()
	cfg.RetryJitterEnabled = false
	p.Reload(cfg)
	if RetryJitterEnabled() {
		t.Fatal("Reload must propagate RetryJitterEnabled=false")
	}
}
