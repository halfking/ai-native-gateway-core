package dispatch

// 会话优化 v4 T2 — request lifecycle registry + backpressure tests.
//
// Covers the UT-DQ group of docs/会话优化v4测试/客户端会话保持-测试方案与用例.md:
//
//	UT-DQ-01 full ⇒ immediate overflow (zero wait, Retry-After hint)
//	UT-DQ-02 the 30s queueWaitBudget hard floor is gone
//	UT-DQ-03 registry three-state transitions + events
//	UT-DQ-05 completed 50-soft-watermark FIFO eviction; pending/in-flight never evicted
//	UT-DQ-06 unfinished limit ⇒ immediate overflow + events
//	UT-DQ-07 registry/projection pressure never changes request outcomes
//	UT-DQ-10 ResultCh single delivery + journey terminal CAS

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ── Registry unit semantics ─────────────────────────────────────────────────

// TestRegistryThreeStateTransitions (UT-DQ-03, unit): pending → in_flight →
// completed with the documented timestamps, plus retry parking.
func TestRegistryThreeStateTransitions(t *testing.T) {
	r := NewLifecycleRegistry(10, 50, 0)
	base := time.Now()

	if !r.RegisterPending("req-1", "gpt-4", base) {
		t.Fatal("RegisterPending refused below the unfinished limit")
	}
	entry, ok := r.Get("req-1")
	if !ok || entry.State != LifecyclePending || entry.EnqueuedAt.IsZero() {
		t.Fatalf("pending entry = %#v", entry)
	}
	if entry.RetryAt != nil {
		t.Fatalf("fresh pending entry must not carry retry_at: %#v", entry.RetryAt)
	}

	started := base.Add(time.Second)
	r.MarkInFlight("req-1", started)
	entry, _ = r.Get("req-1")
	if entry.State != LifecycleInFlight || entry.StartedAt == nil || !entry.StartedAt.Equal(started) {
		t.Fatalf("in-flight entry = %#v", entry)
	}

	// Retry parking (spec §6): in-flight + retry_at → pending with RetryAt.
	retryAt := base.Add(2 * time.Second)
	r.MarkRetryScheduled("req-1", retryAt)
	entry, _ = r.Get("req-1")
	if entry.State != LifecyclePending || entry.RetryAt == nil || !entry.RetryAt.Equal(retryAt) {
		t.Fatalf("retry-parked entry = %#v", entry)
	}

	// Pickup: back to in-flight, retry_at cleared.
	r.MarkInFlight("req-1", base.Add(3*time.Second))
	entry, _ = r.Get("req-1")
	if entry.State != LifecycleInFlight || entry.RetryAt != nil {
		t.Fatalf("picked-up entry = %#v", entry)
	}

	completedAt := base.Add(4 * time.Second)
	r.MarkCompleted("req-1", completedAt)
	entry, _ = r.Get("req-1")
	if entry.State != LifecycleCompleted || entry.CompletedAt == nil || !entry.CompletedAt.Equal(completedAt) {
		t.Fatalf("completed entry = %#v", entry)
	}

	// Terminal is idempotent; unknown ids are ignored.
	r.MarkCompleted("req-1", completedAt.Add(time.Second))
	entry, _ = r.Get("req-1")
	if !entry.CompletedAt.Equal(completedAt) {
		t.Fatalf("MarkCompleted must be idempotent: %#v", entry.CompletedAt)
	}
	r.MarkInFlight("missing", base)
	r.MarkCompleted("missing", base)

	snapshot := r.Snapshot()
	if snapshot.Total != 1 || snapshot.Completed != 1 || snapshot.Unfinished != 0 {
		t.Fatalf("snapshot counts = %#v", snapshot)
	}
	if got := r.Snapshot(LifecyclePending); len(got.Entries) != 0 {
		t.Fatalf("pending filter = %#v", got.Entries)
	}
	if got := r.Snapshot(LifecycleCompleted); len(got.Entries) != 1 || got.Entries[0].RequestID != "req-1" {
		t.Fatalf("completed filter = %#v", got.Entries)
	}
}

// TestRegistryCompletedWatermarkEviction (UT-DQ-05): completed entries are
// evicted FIFO by CompletedAt beyond the soft watermark; pending/in-flight
// entries are NEVER evicted — not by the watermark, not by capacity.
func TestRegistryCompletedWatermarkEviction(t *testing.T) {
	const watermark = 3
	var evictedMu sync.Mutex
	var evictedIDs []string
	r := NewLifecycleRegistry(1000, watermark, 0)
	r.SetEvictHook(func(entries []RegistryEntry) {
		evictedMu.Lock()
		for _, entry := range entries {
			evictedIDs = append(evictedIDs, entry.RequestID)
		}
		evictedMu.Unlock()
	})
	base := time.Now()

	// Two protected unfinished entries.
	if !r.RegisterPending("pending-keep", "m", base) {
		t.Fatal("pending registration refused")
	}
	r.RegisterPending("inflight-keep", "m", base)
	r.MarkInFlight("inflight-keep", base)

	// Complete watermark+2 entries; the 2 oldest must be evicted.
	for i := 0; i < watermark+2; i++ {
		id := fmt.Sprintf("done-%02d", i)
		if !r.RegisterPending(id, "m", base.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("RegisterPending(%s) refused", id)
		}
		r.MarkInFlight(id, base.Add(time.Duration(i)*time.Second))
		r.MarkCompleted(id, base.Add(time.Duration(i+1)*time.Second))
	}

	snapshot := r.Snapshot(LifecycleCompleted)
	if got := len(snapshot.Entries); got != watermark {
		t.Fatalf("completed count = %d, want watermark %d", got, watermark)
	}
	for _, entry := range snapshot.Entries {
		if entry.RequestID == "done-00" || entry.RequestID == "done-01" {
			t.Fatalf("oldest completed entries must be evicted first, found %s", entry.RequestID)
		}
	}
	// Pending/in-flight survive every eviction pass.
	if _, ok := r.Get("pending-keep"); !ok {
		t.Fatal("pending entry must never be evicted")
	}
	if _, ok := r.Get("inflight-keep"); !ok {
		t.Fatal("in-flight entry must never be evicted")
	}
	evictedMu.Lock()
	got := strings.Join(evictedIDs, ",")
	evictedMu.Unlock()
	if got != "done-00,done-01" {
		t.Fatalf("eviction hook order = %q, want done-00,done-01", got)
	}
}

// TestRegistryCapacityEvictsOldestCompleted (UT-DQ-07 precondition): the
// 1000-entry capacity is a memory guard, NOT a rejection threshold — pressure
// drops the oldest completed entry and admission still succeeds.
func TestRegistryCapacityEvictsOldestCompleted(t *testing.T) {
	// Capacity 2 with watermark disabled: completed entries are still bound
	// by the hard capacity, evicted FIFO oldest-first.
	r := NewLifecycleRegistry(2, 1000, 0)
	base := time.Now()
	if !r.RegisterPending("p1", "m", base) || !r.RegisterPending("p2", "m", base.Add(time.Second)) {
		t.Fatal("pending registrations refused")
	}
	r.MarkInFlight("p1", base)
	r.MarkCompleted("p1", base.Add(2*time.Second))
	r.MarkInFlight("p2", base.Add(time.Second))
	r.MarkCompleted("p2", base.Add(3*time.Second))
	if snapshot := r.Snapshot(); snapshot.Total != 2 {
		t.Fatalf("capacity must hold at 2, got %#v", snapshot)
	}
	// Admitting a third entry beyond capacity evicts the OLDEST completed
	// (p1) instead of rejecting.
	if !r.RegisterPending("p3", "m", base.Add(4*time.Second)) {
		t.Fatal("capacity pressure must evict completed, never reject")
	}
	snapshot := r.Snapshot()
	if snapshot.Total != 2 || snapshot.Completed != 1 || snapshot.Pending != 1 {
		t.Fatalf("post-pressure snapshot = %#v", snapshot)
	}
	if _, ok := r.Get("p1"); ok {
		t.Fatal("oldest completed entry (p1) should have been evicted")
	}
	if _, ok := r.Get("p2"); !ok {
		t.Fatal("newest completed entry (p2) must be retained")
	}
}

// TestRegistryUnfinishedLimitRefusesAdmission (UT-DQ-06): pending+in-flight
// at the limit ⇒ RegisterPending refuses (pipeline surfaces overflow).
func TestRegistryUnfinishedLimitRefusesAdmission(t *testing.T) {
	r := NewLifecycleRegistry(1000, 50, 2)
	base := time.Now()
	if !r.RegisterPending("a", "m", base) || !r.RegisterPending("b", "m", base) {
		t.Fatal("first two admissions must succeed")
	}
	if r.RegisterPending("c", "m", base) {
		t.Fatal("third admission must be refused at the unfinished limit")
	}
	// Completing one frees a slot.
	r.MarkCompleted("a", base.Add(time.Second))
	if !r.RegisterPending("c", "m", base.Add(2*time.Second)) {
		t.Fatal("admission must recover after a completion")
	}
	// Re-registering an existing unfinished id must not consume a NEW slot.
	if !r.RegisterPending("c", "m", base.Add(3*time.Second)) {
		t.Fatal("re-registration of an unfinished id must succeed")
	}
	if got := r.UnfinishedCount(); got != 2 {
		t.Fatalf("unfinished count = %d, want 2", got)
	}
}

// TestRegistryUpdateLimitsHotReload (hot-update leg of UT-DQ-05/06): limits
// swap live; tightening evicts only completed entries.
func TestRegistryUpdateLimitsHotReload(t *testing.T) {
	r := NewLifecycleRegistry(1000, 50, 0)
	base := time.Now()
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("r%d", i)
		r.RegisterPending(id, "m", base)
		if i < 3 {
			r.MarkCompleted(id, base.Add(time.Second))
		}
	}
	r.UpdateLimits(1000, 1, 300)
	snapshot := r.Snapshot()
	if snapshot.Completed != 1 || snapshot.Unfinished != 2 || snapshot.Total != 3 {
		t.Fatalf("post-reload snapshot = %#v", snapshot)
	}
}

// ── Pipeline-level integration ──────────────────────────────────────────────

// collectObservations wires a sink that records every lifecycle observation.
func collectObservations(p *Pipeline) func() []Observation {
	var mu sync.Mutex
	events := make([]Observation, 0, 64)
	p.SetObservationSink(ObservationSinkFunc(func(_ context.Context, observation Observation) {
		mu.Lock()
		events = append(events, observation)
		mu.Unlock()
	}))
	return func() []Observation {
		mu.Lock()
		defer mu.Unlock()
		return append([]Observation(nil), events...)
	}
}

// TestPipelineRegistryLifecycle (UT-DQ-03, integration): a real request walks
// pending → in-flight → completed in the registry while the journey emits the
// matching transition events.
func TestPipelineRegistryLifecycle(t *testing.T) {
	forwardStarted := make(chan struct{})
	release := make(chan struct{})
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{"m": {cred(1, ModeConcurrency, 4)}},
		forwardFn: func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome {
			select {
			case <-forwardStarted:
			default:
				close(forwardStarted)
			}
			<-release
			return ForwardOutcome{Result: "ok"}
		},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	done := make(chan struct{})
	go func() {
		defer close(done)
		qr := NewQueuedRequest("life-1", "tenant", "m", context.Background(), nil)
		res, err := p.Submit(context.Background(), qr)
		if err != nil || res != "ok:cred1:call1" {
			t.Errorf("unexpected outcome: %v %v", res, err)
		}
	}()

	<-forwardStarted
	// Mid-flight: the registry must show the request in-flight.
	deadline := time.Now().Add(time.Second)
	for {
		snapshot := p.LifecycleSnapshot()
		if len(snapshot.Entries) == 1 && snapshot.Entries[0].State == LifecycleInFlight {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("registry never reached in-flight: %#v", snapshot)
		}
		time.Sleep(time.Millisecond)
	}

	close(release)
	<-done

	snapshot := p.LifecycleSnapshot()
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].State != LifecycleCompleted {
		t.Fatalf("terminal registry state = %#v", snapshot)
	}
	if snapshot.Completed != 1 || snapshot.Unfinished != 0 {
		t.Fatalf("terminal counts = %#v", snapshot)
	}
}

// TestQueueWaitBudgetZeroWhenDisabled (UT-DQ-02): with MaxQueueWaitMS <= 0
// the pacing budget is exactly zero — the removed 30s hard floor must never
// come back.
func TestQueueWaitBudgetZeroWhenDisabled(t *testing.T) {
	p := NewPipeline(Deps{})
	cfg := DefaultConfig()
	cfg.MaxQueueWaitMS = 0
	p.Reload(cfg)
	qr := &QueuedRequest{SelectedCred: CredentialRef{}}
	if got := p.queueWaitBudget(qr); got != 0 {
		t.Fatalf("queueWaitBudget(default zero) = %v, want 0 (30s floor removed)", got)
	}
	cfg.MaxQueueWaitMS = -1 // hotconfig could hand us a negative
	p.Reload(cfg)
	if got := p.queueWaitBudget(qr); got != 0 {
		t.Fatalf("queueWaitBudget(negative) = %v, want 0", got)
	}
	// Explicit budgets (global or credential) still work.
	cfg.MaxQueueWaitMS = 1500
	p.Reload(cfg)
	if got := p.queueWaitBudget(qr); got != 1500*time.Millisecond {
		t.Fatalf("queueWaitBudget(1500) = %v", got)
	}
	qr.SelectedCred.MaxQueueWaitMS = 40
	if got := p.queueWaitBudget(qr); got != 40*time.Millisecond {
		t.Fatalf("credential override budget = %v", got)
	}
}

// TestSubmitModelQueueFullImmediateOverflow (UT-DQ-01): once the Tier-1 model
// queue reaches its depth bound, the next Submit is rejected IMMEDIATELY with
// a retryable overflow error carrying a Retry-After hint (G9) — zero queue
// wait (elapsed well under any plausible 5s/30s wait).
func TestSubmitModelQueueFullImmediateOverflow(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxQueueDepth = 3
	// One dispatcher worker parks inside RouteFunc; the drainer then fills
	// dispatchIn (cap workers*4) and finally the model queue — every stage
	// is channel-driven, so the chain saturates deterministically.
	cfg.DispatcherWorkers = 1
	hotCfg := &atomic.Value{}
	hotCfg.Store(&cfg)
	// The registry's unfinished limit is deliberately bound to
	// MaxQueueDepth (R1.8); this test isolates the Tier-1 queue-full path,
	// so re-open the registry admission window via its own knobs.
	blockRoute := make(chan struct{})
	p := NewPipeline(Deps{
		RouteFunc: func(context.Context, *QueuedRequest) ([]CredentialRef, error) {
			<-blockRoute
			return []CredentialRef{cred(1, ModeConcurrency, 1)}, nil
		},
		ModelResolveFunc: func(_ context.Context, requested string, _ []string) (string, []string, error) {
			return requested, nil, nil
		},
		ForwardFunc: func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome {
			return ForwardOutcome{Result: "ok"}
		},
		HotCfg: hotCfg,
	})
	p.registry.UpdateLimits(DefaultRegistryCapacity, DefaultCompletedWatermark, DefaultRegistryCapacity)
	p.Start()
	defer func() {
		close(blockRoute)
		p.Stop()
	}()

	// Submit fillers one at a time (bounded ctx). Requests the chain can
	// absorb park inside RouteFunc/dispatchIn until their ctx expires;
	// once the Tier-1 model queue itself is full the NEXT Submit is
	// rejected immediately — assert on that submission's own outcome and
	// latency (no cross-goroutine queue observation, fully deterministic).
	var (
		overflowErr error
		elapsed     time.Duration
	)
	deadline := time.Now().Add(5 * time.Second)
	for i := 0; i < 32 && time.Now().Before(deadline); i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
		qr := NewQueuedRequest(fmt.Sprintf("fill-%d", i), "t", "m", ctx, nil)
		start := time.Now()
		_, err := p.Submit(ctx, qr)
		elapsed = time.Since(start)
		cancel()
		var overflow *OverflowError
		if errors.As(err, &overflow) && overflow.Reason == "model_queue_full" {
			overflowErr = err
			break
		}
	}
	if overflowErr == nil {
		t.Fatal("model queue never saturated: no filler was overflow-rejected")
	}
	if !errors.Is(overflowErr, ErrOverflow) {
		t.Fatalf("overflow must wrap ErrOverflow, got %v", overflowErr)
	}
	var overflow *OverflowError
	if !errors.As(overflowErr, &overflow) {
		t.Fatalf("expected *OverflowError, got %T (%v)", overflowErr, overflowErr)
	}
	if overflow.Reason != "model_queue_full" || overflow.RetryAfter <= 0 {
		t.Fatalf("overflow details = %+v (G9 requires a Retry-After hint)", overflow)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("full queue must reject immediately (zero wait), took %v", elapsed)
	}
	if classifyError(overflowErr) != "overflow" {
		t.Fatalf("classifyError(overflow) = %q", classifyError(overflowErr))
	}
}

// TestSubmitRegistryUnfinishedFullImmediateOverflow (UT-DQ-06): pending+
// in-flight entries at the scheduling bound ⇒ the registry refuses admission
// and Submit returns the same immediate retryable overflow.
func TestSubmitRegistryUnfinishedFullImmediateOverflow(t *testing.T) {
	blockRoute := make(chan struct{})
	overflowSeen := make(chan QueueObservation, 4)
	p := NewPipeline(Deps{
		RouteFunc: func(context.Context, *QueuedRequest) ([]CredentialRef, error) {
			<-blockRoute
			return []CredentialRef{cred(1, ModeConcurrency, 1)}, nil
		},
		ModelResolveFunc: func(_ context.Context, requested string, _ []string) (string, []string, error) {
			return requested, nil, nil
		},
		ForwardFunc: func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome {
			return ForwardOutcome{Result: "ok"}
		},
	})
	p.SetQueueObservationSink(QueueObservationSinkFunc(func(observation QueueObservation) {
		if observation.Kind == QueueOverflow {
			overflowSeen <- observation
		}
	}))
	p.Start()
	defer func() {
		close(blockRoute)
		p.Stop()
	}()

	// Tighten the unfinished limit to 2 live (hot-reload path).
	cfg := DefaultConfig()
	cfg.MaxQueueDepth = 2
	p.Reload(cfg)

	for i := 0; i < 2; i++ {
		go func(i int) {
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			qr := NewQueuedRequest(fmt.Sprintf("hold-%d", i), "t", "m", ctx, nil)
			_, _ = p.Submit(ctx, qr)
		}(i)
	}
	deadline := time.Now().Add(time.Second)
	for p.registry.UnfinishedCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	qr := NewQueuedRequest("overflow-2", "t", "m", context.Background(), nil)
	_, err := p.Submit(context.Background(), qr)
	if err == nil || !errors.Is(err, ErrOverflow) {
		t.Fatalf("expected registry overflow, got %v", err)
	}
	var overflow *OverflowError
	if !errors.As(err, &overflow) || overflow.Reason != "registry_unfinished_full" || overflow.RetryAfter <= 0 {
		t.Fatalf("overflow details = %+v", overflow)
	}
	select {
	case observation := <-overflowSeen:
		if observation.OverflowReason != "registry_unfinished_full" {
			t.Fatalf("overflow observation reason = %q", observation.OverflowReason)
		}
	case <-time.After(time.Second):
		t.Fatal("overflow rejection must emit a queue observation (R1.8)")
	}
	// The refused request never entered the system: no registry entry may
	// exist for it (admission refusal ≠ lifecycle registration).
	if _, ok := p.registry.Get("overflow-2"); ok {
		t.Fatal("refused request must not be registered")
	}
}

// TestRegistryPressureNeverChangesOutcome (UT-DQ-07): with a tiny registry
// capacity, zero completed watermark, and a failing observation channel, a
// normal request still completes with the identical result — registry and
// projection bookkeeping are bypasses.
func TestRegistryPressureNeverChangesOutcome(t *testing.T) {
	f := &fakeDeps{
		refsByModel:  map[string][]CredentialRef{"m": {cred(1, ModeConcurrency, 4)}},
		forwardFn:    func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome { return ForwardOutcome{} },
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	// Squeeze the registry to capacity 1 with no completed retention.
	cfg := DefaultConfig()
	cfg.RegistryCapacity = 1
	cfg.CompletedWatermark = 0
	p.Reload(cfg)

	// Break the observation bypass: closed channel ⇒ emissions drop.
	p.StopObservationsForTest()

	for i := 0; i < 5; i++ {
		qr := NewQueuedRequest(fmt.Sprintf("bypass-%d", i), "t", "m", context.Background(), nil)
		res, err := p.Submit(context.Background(), qr)
		if err != nil || res == nil {
			t.Fatalf("request %d outcome changed by registry pressure: res=%v err=%v", i, res, err)
		}
	}
	snapshot := p.LifecycleSnapshot()
	if snapshot.Total > 1 {
		t.Fatalf("tiny registry should stay within capacity, got %#v", snapshot)
	}
}

// TestResultChSingleDeliveryAndTerminalCAS (UT-DQ-10 regression):
// complete() delivers exactly once even when called twice, and the journey
// terminal CAS lets only ONE terminal observation through.
func TestResultChSingleDeliveryAndTerminalCAS(t *testing.T) {
	p := NewPipeline(Deps{})
	p.Start()
	defer p.Stop()

	events := collectObservations(p)
	qr := NewQueuedRequest("once-1", "tenant", "m", context.Background(), nil)
	qr.GatewayInstanceID = "gw-1"
	// Submit installs the per-request observation emitter; complete() alone
	// does not, so wire the same emitter shape for this direct test.
	qr.setObservationEmitter(func(observation Observation) {
		p.observationChMu.RLock()
		if p.observationCh == nil || p.observationClosed {
			p.observationChMu.RUnlock()
			return
		}
		item := observationItem{ctx: context.Background(), observation: observation}
		select {
		case p.observationCh <- item:
		default:
		}
		p.observationChMu.RUnlock()
	})
	p.complete(qr, ForwardOutcome{Result: "first"})
	p.complete(qr, ForwardOutcome{Result: "second"})

	select {
	case out := <-qr.ResultCh:
		if out.Result != "first" {
			t.Fatalf("ResultCh must carry the first delivery, got %v", out.Result)
		}
	default:
		t.Fatal("complete must deliver to ResultCh")
	}
	select {
	case out := <-qr.ResultCh:
		t.Fatalf("ResultCh delivered twice: %v", out.Result)
	default:
	}

	// Observation delivery is asynchronous — poll for the single terminal.
	deadline := time.Now().Add(time.Second)
	for {
		terminal := 0
		for _, observation := range events() {
			if observation.Stage == StageTerminal {
				terminal++
			}
		}
		if terminal == 1 {
			break
		}
		if terminal > 1 {
			t.Fatalf("terminal observations = %d, want 1 (journey terminal CAS)", terminal)
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal observations = %d, want 1 (journey terminal CAS)", terminal)
		}
		time.Sleep(time.Millisecond)
	}
}

// StopObservationsForTest closes the observation channel so emissions drop
// (UT-DQ-07: a broken projection channel must not affect execution).
func (p *Pipeline) StopObservationsForTest() {
	p.observationChMu.Lock()
	if p.observationCh != nil && !p.observationClosed {
		p.observationClosed = true
		close(p.observationCh)
	}
	p.observationChMu.Unlock()
}

// ── Default config pinning ──────────────────────────────────────────────────

// TestDefaultConfigBackpressureV4 pins the R1.3 defaults: 300 scheduling
// depth, zero queue wait, registry 1000, watermark 50.
func TestDefaultConfigBackpressureV4(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MaxQueueDepth != 300 {
		t.Fatalf("MaxQueueDepth default = %d, want 300", cfg.MaxQueueDepth)
	}
	if cfg.MaxQueueWaitMS != 0 {
		t.Fatalf("MaxQueueWaitMS default = %d, want 0", cfg.MaxQueueWaitMS)
	}
	if DefaultRegistryCapacity != 1000 || cfg.RegistryCapacity != DefaultRegistryCapacity {
		t.Fatalf("RegistryCapacity default = %d, want 1000", cfg.RegistryCapacity)
	}
	if DefaultCompletedWatermark != 50 || cfg.CompletedWatermark != DefaultCompletedWatermark {
		t.Fatalf("CompletedWatermark default = %d, want 50", cfg.CompletedWatermark)
	}
}
