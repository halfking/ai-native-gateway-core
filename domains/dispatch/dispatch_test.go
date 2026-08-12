package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeDeps builds a Pipeline with controllable callbacks for testing.
type fakeDeps struct {
	mu sync.Mutex
	// refsByModel returns a fixed candidate list per model.
	refsByModel map[string][]CredentialRef
	// alternatives per requested model (for model-change).
	altsByModel map[string][]string
	// forwardFn is invoked on each forward attempt.
	forwardFn func(ctx context.Context, qr *QueuedRequest, cred CredentialRef) ForwardOutcome
	// forwardCalls counts forward attempts per credential.
	forwardCalls map[int]int
	allowChange  bool
}

func (f *fakeDeps) routeFunc(ctx context.Context, qr *QueuedRequest) ([]CredentialRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	refs := f.refsByModel[qr.ResolvedModel]
	out := make([]CredentialRef, 0, len(refs))
	for _, r := range refs {
		if qr.hasTriedCredential(r.CredentialID) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeDeps) modelResolveFunc(ctx context.Context, requested string, tried []string) (string, []string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !isAutoModel(requested) && requested != "" {
		// concrete model: resolved == requested; alternatives from map.
		alts := filterAlts(f.altsByModel[requested], tried)
		return requested, alts, nil
	}
	// auto: pick first concrete model not tried.
	for m := range f.refsByModel {
		if !contains(tried, m) {
			return m, filterAlts(keysOf(f.refsByModel), tried), nil
		}
	}
	return "", nil, errors.New("no model")
}

func (f *fakeDeps) forwardFunc(ctx context.Context, qr *QueuedRequest, cred CredentialRef) ForwardOutcome {
	f.mu.Lock()
	f.forwardCalls[cred.CredentialID]++
	calls := f.forwardCalls[cred.CredentialID]
	fn := f.forwardFn
	f.mu.Unlock()
	var out ForwardOutcome
	if fn != nil {
		out = fn(ctx, qr, cred)
	}
	if out.Err == nil {
		out.Result = fmt.Sprintf("ok:cred%d:call%d", cred.CredentialID, calls)
	} else {
		out.Result = nil
	}
	return out
}

func (f *fakeDeps) pipeline() *Pipeline {
	return NewPipeline(Deps{
		RouteFunc:        f.routeFunc,
		ModelResolveFunc: f.modelResolveFunc,
		ForwardFunc:      f.forwardFunc,
		AllowModelChange: f.allowChange,
	})
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func filterAlts(all []string, tried []string) []string {
	out := make([]string, 0, len(all))
	for _, a := range all {
		if !contains(tried, a) {
			out = append(out, a)
		}
	}
	return out
}

func keysOf(m map[string][]CredentialRef) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func cred(id int, mode string, limit int) CredentialRef {
	switch mode {
	case ModeRPM:
		return CredentialRef{CredentialID: id, ConcurrencyMode: ModeRPM, RPMLimit: limit}
	case ModeTPM:
		return CredentialRef{CredentialID: id, ConcurrencyMode: ModeTPM, TPMLimit: limit}
	case ModeDisabled:
		return CredentialRef{CredentialID: id, ConcurrencyMode: ModeDisabled}
	default:
		return CredentialRef{CredentialID: id, ConcurrencyMode: ModeConcurrency, ConcurrencyLimit: limit}
	}
}

// credWithProvider builds a CredentialRef pinned to a specific provider so
// request-level provider-switch policy can be exercised.
func credWithProvider(id, provider int, limit int) CredentialRef {
	return CredentialRef{
		CredentialID:     id,
		ProviderID:       provider,
		ConcurrencyMode:  ModeConcurrency,
		ConcurrencyLimit: limit,
	}
}

// TestSubmitSuccess: a single healthy credential forwards and returns the result.
func TestSubmitSuccess(t *testing.T) {
	f := &fakeDeps{
		refsByModel:  map[string][]CredentialRef{"gpt4": {cred(1, ModeConcurrency, 5)}},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("r1", "t", "gpt4", context.Background(), "payload")
	res, err := p.Submit(context.Background(), qr)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res != "ok:cred1:call1" {
		t.Fatalf("unexpected result: %v", res)
	}
}

// TestFailoverSwitch: first credential fails pre-firstbyte; second succeeds.
func TestFailoverSwitch(t *testing.T) {
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{"gpt4": {
			cred(1, ModeConcurrency, 5),
			cred(2, ModeConcurrency, 5),
		}},
		forwardFn: func(ctx context.Context, qr *QueuedRequest, c CredentialRef) ForwardOutcome {
			if c.CredentialID == 1 {
				return ForwardOutcome{Err: errors.New("upstream 500")} // pre-firstbyte
			}
			return ForwardOutcome{}
		},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("r1", "t", "gpt4", context.Background(), nil)
	res, err := p.Submit(context.Background(), qr)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res != "ok:cred2:call1" {
		t.Fatalf("expected failover to cred2, got: %v", res)
	}
	if !qr.hasTriedCredential(1) {
		t.Fatalf("cred1 should be marked tried")
	}
}

// TestPostFirstByteNoSwitch: a failure after bytes were sent must NOT switch.
func TestPostFirstByteNoSwitch(t *testing.T) {
	var switched atomic.Int32
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{"gpt4": {
			cred(1, ModeConcurrency, 5), cred(2, ModeConcurrency, 5),
		}},
		forwardFn: func(ctx context.Context, qr *QueuedRequest, c CredentialRef) ForwardOutcome {
			if c.CredentialID == 2 {
				switched.Add(1)
			}
			return ForwardOutcome{BytesSent: true, Err: errors.New("mid-stream break")}
		},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("r1", "t", "gpt4", context.Background(), nil)
	_, err := p.Submit(context.Background(), qr)
	if err == nil {
		t.Fatalf("expected terminal error after post-firstbyte failure")
	}
	if switched.Load() != 0 {
		t.Fatalf("must not switch credential after bytes sent; switched=%d", switched.Load())
	}
}

// TestModelChange: all creds under model A fail → switch to model B.
func TestModelChange(t *testing.T) {
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{
			"a": {cred(1, ModeConcurrency, 5)},
			"b": {cred(2, ModeConcurrency, 5)},
		},
		altsByModel: map[string][]string{"a": {"b"}, "b": {"a"}},
		forwardFn: func(ctx context.Context, qr *QueuedRequest, c CredentialRef) ForwardOutcome {
			if c.CredentialID == 1 {
				return ForwardOutcome{Err: errors.New("fail")}
			}
			return ForwardOutcome{}
		},
		forwardCalls: map[int]int{},
		allowChange:  true,
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("r1", "t", "a", context.Background(), nil)
	qr.AllowModelChange = true
	res, err := p.Submit(context.Background(), qr)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res != "ok:cred2:call1" {
		t.Fatalf("expected model-change to cred2, got: %v", res)
	}
}

// TestNoRoute: everything fails, model-change disabled → ErrNoRoute.
func TestNoRoute(t *testing.T) {
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{"a": {cred(1, ModeConcurrency, 5)}},
		altsByModel: map[string][]string{"a": {"b"}}, // b has no creds anyway
		forwardFn: func(ctx context.Context, qr *QueuedRequest, c CredentialRef) ForwardOutcome {
			return ForwardOutcome{Err: errors.New("fail")}
		},
		forwardCalls: map[int]int{},
		allowChange:  false,
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("r1", "t", "a", context.Background(), nil)
	_, err := p.Submit(context.Background(), qr)
	if err == nil || err.Error() != "fail" {
		t.Fatalf("expected preserved upstream error, got: %v", err)
	}
}

// TestConcurrencyCapNeverExceeded: with ConcurrencyLimit=2, never more than 2
// concurrent forwards despite many queued requests.
func TestConcurrencyCapNeverExceeded(t *testing.T) {
	var inFlight, maxInFlight atomic.Int32
	const N = 20
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{"m": {cred(1, ModeConcurrency, 2)}},
		forwardFn: func(ctx context.Context, qr *QueuedRequest, c CredentialRef) ForwardOutcome {
			cur := inFlight.Add(1)
			for {
				old := maxInFlight.Load()
				if cur <= old || maxInFlight.CompareAndSwap(old, cur) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			inFlight.Add(-1)
			return ForwardOutcome{}
		},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			qr := NewQueuedRequest(fmt.Sprintf("r%d", i), "t", "m", context.Background(), nil)
			_, _ = p.Submit(context.Background(), qr)
		}(i)
	}
	wg.Wait()
	if maxInFlight.Load() > 2 {
		t.Fatalf("concurrency cap exceeded: max=%d (limit=2)", maxInFlight.Load())
	}
}

// TestRPMGovernorPaces: the token bucket starts full (burst=rpm), so we drain
// it first, then verify the next acquire waits ~one interval.
func TestRPMGovernorPaces(t *testing.T) {
	const rpm = 200 // interval = 60s/200 = 300ms, burst = 200
	g := newRPMGovernor(rpm)
	ctx := context.Background()
	giveUp := time.Now().Add(5 * time.Second)
	qr := &QueuedRequest{Ctx: ctx}
	// Drain the burst bucket.
	for i := 0; i < rpm; i++ {
		if err := g.Acquire(ctx, qr, giveUp); err != nil {
			t.Fatalf("drain acquire %d: %v", i, err)
		}
	}
	// Next acquire must wait ~300ms for one refill.
	start := time.Now()
	if err := g.Acquire(ctx, qr, giveUp); err != nil {
		t.Fatalf("paced acquire: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 150*time.Millisecond {
		t.Fatalf("rpm governor did not pace: elapsed=%v (expected >=~300ms)", elapsed)
	}
}

// TestRPMGovernorRespectsQueueBudget ensures the governor never sleeps past
// the queue-wait deadline when pacing would otherwise take longer.
func TestRPMGovernorRespectsQueueBudget(t *testing.T) {
	const rpm = 60 // one token per second
	g := newRPMGovernor(rpm)
	ctx := context.Background()
	qr := &QueuedRequest{Ctx: ctx}
	// Drain the initial burst so the next acquire would naturally wait ~1s.
	for i := 0; i < rpm; i++ {
		if err := g.Acquire(ctx, qr, time.Now().Add(5*time.Second)); err != nil {
			t.Fatalf("drain acquire %d: %v", i, err)
		}
	}
	giveUp := time.Now().Add(40 * time.Millisecond)
	start := time.Now()
	if err := g.Acquire(ctx, qr, giveUp); !errors.Is(err, errPaceTimeout) {
		t.Fatalf("expected errPaceTimeout, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("rpm governor exceeded budget: elapsed=%v", elapsed)
	}
}

// TestTPMGovernorRespectsQueueBudget ensures the governor never sleeps past
// the queue-wait deadline when token replenishment would otherwise take longer.
func TestTPMGovernorRespectsQueueBudget(t *testing.T) {
	g := newTPMGovernor(60) // one token per second
	ctx := context.Background()
	// Drain the initial burst so the next acquire would naturally wait ~1s.
	if err := g.Acquire(ctx, &QueuedRequest{Ctx: ctx, EstimatedTokens: 60}, time.Now().Add(5*time.Second)); err != nil {
		t.Fatalf("drain acquire: %v", err)
	}
	giveUp := time.Now().Add(40 * time.Millisecond)
	start := time.Now()
	if err := g.Acquire(ctx, &QueuedRequest{Ctx: ctx, EstimatedTokens: 1}, giveUp); !errors.Is(err, errPaceTimeout) {
		t.Fatalf("expected errPaceTimeout, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("tpm governor exceeded budget: elapsed=%v", elapsed)
	}
}

// TestCtxCancelAbandoned: Submit returns ctx.Err() on cancel and the qr is
// marked abandoned (complete becomes a no-op).
func TestCtxCancelAbandoned(t *testing.T) {
	block := make(chan struct{})
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{"m": {cred(1, ModeConcurrency, 1)}},
		forwardFn: func(ctx context.Context, qr *QueuedRequest, c CredentialRef) ForwardOutcome {
			<-block // hang until test releases
			return ForwardOutcome{}
		},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	qr := NewQueuedRequest("r1", "t", "m", ctx, nil)
	_, err := p.Submit(ctx, qr)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got: %v", err)
	}
	if !qr.abandoned.Load() {
		t.Fatalf("qr should be marked abandoned")
	}
	close(block) // release the hung forward; complete must no-op (no send panic)
	time.Sleep(30 * time.Millisecond)
}

// TestStopNoSendOnClosedRace: concurrent Submit (enqueueModel) while Stop
// runs must not panic on send-to-closed channel. Regression for the
// drainer/Stop design (Stop must NOT close mq.ch).
func TestStopNoSendOnClosedRace(t *testing.T) {
	f := &fakeDeps{
		refsByModel:  map[string][]CredentialRef{"m": {cred(1, ModeConcurrency, 2)}},
		forwardFn:    func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome { return ForwardOutcome{} },
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.Start()

	var wg sync.WaitGroup
	// Hammer Submit/abandon concurrently; Stop in the middle.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
			defer cancel()
			qr := NewQueuedRequest(fmt.Sprintf("r%d", i), "t", "m", ctx, nil)
			_, _ = p.Submit(ctx, qr) // outcome irrelevant; must not panic
		}(i)
	}
	time.Sleep(2 * time.Millisecond)
	p.Stop() // must not race with in-flight sends
	wg.Wait()
}

// TestPacingTimeoutSkipsSameCredRetry: a pacing-timeout (concurrency
// saturated) must NOT burn the same-credential retry budget — it should
// switch to the next credential immediately.
func TestPacingTimeoutSkipsSameCredRetry(t *testing.T) {
	var attempts atomic.Int64
	// cred 1 caps at 1 in-flight; forward hangs so the slot stays taken →
	// second arrival at cred 1 paces out. cred 2 succeeds.
	block := make(chan struct{})
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{"m": {
			cred(1, ModeConcurrency, 1),
			cred(2, ModeConcurrency, 1),
		}},
		forwardFn: func(ctx context.Context, qr *QueuedRequest, c CredentialRef) ForwardOutcome {
			n := attempts.Add(1)
			if c.CredentialID == 1 {
				<-block // hold the single slot
				_ = n
				return ForwardOutcome{}
			}
			return ForwardOutcome{}
		},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.Start()
	defer func() {
		close(block)
		p.Stop()
	}()

	// First request occupies cred 1's single slot (hangs).
	go func() {
		qr1 := NewQueuedRequest("r1", "t", "m", context.Background(), nil)
		_, _ = p.Submit(context.Background(), qr1)
	}()
	time.Sleep(20 * time.Millisecond) // let qr1 acquire cred1's slot

	// Second request: dispatcher also picks cred1 (only non-tried), but its
	// governor is saturated → pacing timeout (short budget) → must skip retry
	// and switch to cred2. Give a tiny queue-wait budget via config.
	cfg := DefaultConfig()
	cfg.MaxQueueWaitMS = 50
	p.Reload(cfg)
	qr2 := NewQueuedRequest("r2", "t", "m", context.Background(), nil)
	res, err := p.Submit(context.Background(), qr2)
	if err != nil {
		t.Fatalf("expected success via cred2, got err: %v", err)
	}
	if res == nil {
		t.Fatalf("expected non-nil result via cred2")
	}
}

// TestAttemptCap: a request that fails on every credential must terminate at
// the maxAttempts ceiling, not loop forever.
func TestAttemptCap(t *testing.T) {
	// Cycle of 3 credentials that all fail pre-firstbyte; without the cap the
	// mover would keep switching forever (Tried set grows but routeFunc keeps
	// returning fresh IDs if we supply many). Use many failing credentials.
	creds := make([]CredentialRef, 0, 60)
	for i := 1; i <= 60; i++ {
		creds = append(creds, cred(i, ModeConcurrency, 1))
	}
	var forwardCalls atomic.Int64
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{"m": creds},
		forwardFn: func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome {
			forwardCalls.Add(1)
			return ForwardOutcome{Err: errors.New("fail")}
		},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("r1", "t", "m", context.Background(), nil)
	_, err := p.Submit(context.Background(), qr)
	if err == nil {
		t.Fatalf("expected terminal error")
	}
	if got := forwardCalls.Load(); got > maxAttempts {
		t.Fatalf("attempt cap violated: %d forwards (cap=%d)", got, maxAttempts)
	}
}

// TestStopNoDrainLoss is a regression for a bug introduced by dispatch audit
// round 2 (commit f97896db): runModelDrainer reads qr from mq.ch, then on
// Stop fires the inner select on (dispatchIn <- qr / stopCh). If dispatchIn
// is full and stopCh wins, the drainer returns without forwarding qr OR
// calling complete(qr) — Submit's caller (waiting on qr.ResultCh) blocks
// forever, and the request is silently dropped.
//
// Reproduce: dispatchIn must be full AND have no reader. We achieve this by
// setting DispatcherWorkers=0 (no dispatcher goroutine to drain dispatchIn)
// — dispatchIn becomes an unbuffered (cap 0) channel where every drainer
// send blocks. The drainer pops qr from mq.ch and then parks forever on
// `p.dispatchIn <- qr`. When Stop fires, the inner select picks stopCh and
// (under the bug) returns without completing qr.
func TestStopNoDrainLoss(t *testing.T) {
	// cfg.DispatcherWorkers=0 → dispatchIn has cap 0, no worker drains it.
	cfg := DefaultConfig()
	cfg.DispatcherWorkers = 0
	cfg.MaxQueueDepth = 8 // mq.ch buffer; keep small so the test stays deterministic
	hotCfg := &atomic.Value{}
	hotCfg.Store(&cfg)

	p := NewPipeline(Deps{
		RouteFunc: func(ctx context.Context, qr *QueuedRequest) ([]CredentialRef, error) {
			return []CredentialRef{cred(1, ModeConcurrency, 1)}, nil
		},
		ModelResolveFunc: func(ctx context.Context, requested string, tried []string) (string, []string, error) {
			return "m", nil, nil
		},
		ForwardFunc: func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome {
			// Unreachable in this test (dispatchIn blocks the drainer), but
			// kept for completeness.
			return ForwardOutcome{}
		},
		AllowModelChange: false,
		HotCfg:           hotCfg,
	})
	p.Start()

	// Submit a qr that lands in mq.ch. The drainer pops it and blocks on
	// dispatchIn (cap 0, no reader). Now Stop will fire stopCh and the
	// drainer must complete the qr with ErrShutdown instead of dropping it.
	victimDone := make(chan struct{})
	qrVictim := NewQueuedRequest("victim", "t", "m", context.Background(), nil)
	go func() {
		defer close(victimDone)
		_, _ = p.Submit(context.Background(), qrVictim)
	}()
	// Give the drainer a moment to pop qrVictim and park on dispatchIn.
	time.Sleep(50 * time.Millisecond)

	p.Stop()

	select {
	case <-victimDone:
		// Submit returned. Drainer called complete(qr, ErrShutdown). Under
		// the bug this case would have timed out instead. We don't need to
		// re-read qrVictim.ResultCh: it's buffered cap 1 and Submit already
		// consumed the value when it returned.
	case <-time.After(2 * time.Second):
		t.Fatal("Submit never returned: drainer dropped qrVictim instead of completing it on Stop")
	}
}

func TestTier2QueueBoundIncludesGovernorWait(t *testing.T) {
	forwardStarted := make(chan struct{})
	releaseForward := make(chan struct{})
	ref := cred(1, ModeConcurrency, 1)
	ref.MaxQueueDepth = 2

	cfg := DefaultConfig()
	cfg.RetryPerCredential = 0
	hotCfg := &atomic.Value{}
	hotCfg.Store(&cfg)
	p := NewPipeline(Deps{
		RouteFunc: func(context.Context, *QueuedRequest) ([]CredentialRef, error) {
			return []CredentialRef{ref}, nil
		},
		ModelResolveFunc: func(context.Context, string, []string) (string, []string, error) {
			return "m", nil, nil
		},
		ForwardFunc: func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome {
			select {
			case <-forwardStarted:
			default:
				close(forwardStarted)
			}
			<-releaseForward
			return ForwardOutcome{}
		},
		HotCfg: hotCfg,
	})
	p.Start()
	defer func() {
		close(releaseForward)
		p.Stop()
	}()

	go func() {
		_, _ = p.Submit(context.Background(), NewQueuedRequest("in-flight", "t", "m", context.Background(), nil))
	}()
	select {
	case <-forwardStarted:
	case <-time.After(time.Second):
		t.Fatal("first request did not enter forward")
	}

	for i := 0; i < ref.MaxQueueDepth; i++ {
		go func(i int) {
			_, _ = p.Submit(context.Background(), NewQueuedRequest(fmt.Sprintf("queued-%d", i), "t", "m", context.Background(), nil))
		}(i)
	}

	deadline := time.Now().Add(time.Second)
	for {
		_, snapshots := p.Snapshot()
		if len(snapshots) == 1 && snapshots[0].Depth == int64(ref.MaxQueueDepth) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("credential queue never reached configured bound: snapshots=%v", snapshots)
		}
		time.Sleep(time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := p.Submit(ctx, NewQueuedRequest("overflow", "t", "m", ctx, nil))
	if !errors.Is(err, ErrNoRoute) {
		t.Fatalf("expected queue overflow to reject through no-route, got %v", err)
	}
}

// TestRequestLevelModelChangeDisabled: even with global AllowModelChange=true
// and alternatives available, a request with AllowModelChange=false must NOT
// switch models — it terminates with the real upstream error.
func TestRequestLevelModelChangeDisabled(t *testing.T) {
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{
			"a": {cred(1, ModeConcurrency, 5)},
			"b": {cred(2, ModeConcurrency, 5)},
		},
		altsByModel: map[string][]string{"a": {"b"}, "b": {"a"}},
		forwardFn: func(ctx context.Context, qr *QueuedRequest, c CredentialRef) ForwardOutcome {
			return ForwardOutcome{Err: errors.New("upstream-down")}
		},
		forwardCalls: map[int]int{},
		allowChange:  true, // global switch ON
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("r1", "t", "a", context.Background(), nil)
	qr.AllowModelChange = false // request denies model switch
	qr.ModelAlternatives = []string{"b"}
	_, err := p.Submit(context.Background(), qr)
	if err == nil || err.Error() != "upstream-down" {
		t.Fatalf("expected preserved upstream error without model switch, got: %v", err)
	}
	if forwardCalls := f.forwardCalls[2]; forwardCalls != 0 {
		t.Fatalf("model b credential must not be tried when request denies model change; calls=%d", forwardCalls)
	}
}

// TestRequestLevelProviderSwitchScoped: when AllowProviderChange=false the
// mover may only switch credentials within the initial provider. Credentials
// from a second provider are skipped even if available.
func TestRequestLevelProviderSwitchScoped(t *testing.T) {
	// Provider 10 has two failing credentials; provider 20 has a healthy one.
	refs := []CredentialRef{
		credWithProvider(1, 10, 5),
		credWithProvider(2, 10, 5),
		credWithProvider(3, 20, 5),
	}
	var provider20Tried atomic.Int32
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{"m": refs},
		forwardFn: func(ctx context.Context, qr *QueuedRequest, c CredentialRef) ForwardOutcome {
			if c.ProviderID == 20 {
				provider20Tried.Add(1)
			}
			return ForwardOutcome{Err: errors.New("fail")}
		},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("r1", "t", "m", context.Background(), nil)
	qr.AllowProviderChange = false
	_, _ = p.Submit(context.Background(), qr)

	if got := provider20Tried.Load(); got != 0 {
		t.Fatalf("provider 20 credential must not be tried when request denies provider switch; tried=%d", got)
	}
	// Both provider-10 credentials should have been attempted.
	if f.forwardCalls[1] == 0 || f.forwardCalls[2] == 0 {
		t.Fatalf("same-provider credentials should be tried: calls=%v", f.forwardCalls)
	}
}

// TestRequestLevelProviderSwitchAllowed: with AllowProviderChange=true the
// mover crosses providers and succeeds on the second provider's credential.
func TestRequestLevelProviderSwitchAllowed(t *testing.T) {
	refs := []CredentialRef{
		credWithProvider(1, 10, 5),
		credWithProvider(2, 20, 5),
	}
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{"m": refs},
		forwardFn: func(ctx context.Context, qr *QueuedRequest, c CredentialRef) ForwardOutcome {
			if c.ProviderID == 10 {
				return ForwardOutcome{Err: errors.New("provider-10-down")}
			}
			return ForwardOutcome{}
		},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("r1", "t", "m", context.Background(), nil)
	qr.AllowProviderChange = true
	res, err := p.Submit(context.Background(), qr)
	if err != nil {
		t.Fatalf("expected cross-provider success, got: %v", err)
	}
	if res != "ok:cred2:call1" {
		t.Fatalf("expected provider-20 credential to serve, got: %v", res)
	}
}

// TestLastUpstreamErrorPreserved: when every credential fails with a concrete
// upstream error, the Submit caller receives THAT error, not a synthetic
// ErrNoRoute. ErrNoRoute is reserved for the never-routed case.
func TestLastUpstreamErrorPreserved(t *testing.T) {
	want := errors.New("persistent-502")
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{"m": {
			cred(1, ModeConcurrency, 5),
			cred(2, ModeConcurrency, 5),
		}},
		forwardFn: func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome {
			return ForwardOutcome{Err: want}
		},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("r1", "t", "m", context.Background(), nil)
	_, err := p.Submit(context.Background(), qr)
	if !errors.Is(err, want) {
		t.Fatalf("expected preserved upstream error %v, got: %v", want, err)
	}
	if errors.Is(err, ErrNoRoute) {
		t.Fatalf("must not collapse concrete upstream error into ErrNoRoute")
	}
}

// TestConcurrentSubmitStopRace: hammer Submit while Stop runs to exercise the
// shutdown/admission race under the race detector. Must not panic or leak.
func TestConcurrentSubmitStopRace(t *testing.T) {
	f := &fakeDeps{
		refsByModel:  map[string][]CredentialRef{"m": {cred(1, ModeConcurrency, 4)}},
		forwardFn:    func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome { return ForwardOutcome{} },
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.Start()

	const N = 200
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
			defer cancel()
			qr := NewQueuedRequest(fmt.Sprintf("r%d", i), "t", "m", ctx, nil)
			_, _ = p.Submit(ctx, qr)
		}(i)
	}
	p.Stop() // race with in-flight submits
	wg.Wait()
}

// TestRetryBudgetRequestOverride: RetryPerCredential on the request overrides
// the global config. A request with budget 0 switches immediately on the first
// pre-firstbyte failure, while the global default is 1.
func TestRetryBudgetRequestOverride(t *testing.T) {
	var calls atomic.Int64
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{"m": {
			cred(1, ModeConcurrency, 5),
			cred(2, ModeConcurrency, 5),
		}},
		forwardFn: func(ctx context.Context, qr *QueuedRequest, c CredentialRef) ForwardOutcome {
			calls.Add(1)
			if c.CredentialID == 1 {
				return ForwardOutcome{Err: errors.New("fail")}
			}
			return ForwardOutcome{}
		},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("r1", "t", "m", context.Background(), nil)
	qr.RetryPerCredential = 0 // switch immediately, do not retry cred 1
	res, err := p.Submit(context.Background(), qr)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res != "ok:cred2:call1" {
		t.Fatalf("expected immediate switch to cred2, got: %v", res)
	}
	// cred 1 should be called exactly once (no same-credential retry).
	if got := f.forwardCalls[1]; got != 1 {
		t.Fatalf("cred1 should be tried once with request budget 0; calls=%d", got)
	}
}

// TestTier2StopDrainsQueuedRequests is the Tier-2 analogue of
// TestStopNoDrainLoss. A request sits in the credential forwarder's Tier-2
// queue (governor pacing wait) when Stop() fires. Under the bug the
// forwarder loop returned on <-cf.ctx.Done() without draining cf.queue, so
// the qr was never completed and its Submit caller blocked forever. The fix
// (drainAndComplete) completes every buffered qr with ErrShutdown.
//
// Reproduce: a concurrency-1 credential whose single in-flight slot is held
// by a blocking forward. A second request is admitted into the Tier-2 queue
// (governor waiting for the slot). Stop() then cancels the forwarder; the
// queued request must complete (Submit returns ErrShutdown), not hang.
func TestTier2StopDrainsQueuedRequests(t *testing.T) {
	releaseForward := make(chan struct{})
	ref := cred(1, ModeConcurrency, 1)
	ref.MaxQueueDepth = 4

	cfg := DefaultConfig()
	cfg.RetryPerCredential = 0
	hotCfg := &atomic.Value{}
	hotCfg.Store(&cfg)
	forwardStarted := make(chan struct{})
	p := NewPipeline(Deps{
		RouteFunc: func(context.Context, *QueuedRequest) ([]CredentialRef, error) {
			return []CredentialRef{ref}, nil
		},
		ModelResolveFunc: func(context.Context, string, []string) (string, []string, error) {
			return "m", nil, nil
		},
		ForwardFunc: func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome {
			select {
			case <-forwardStarted:
			default:
				close(forwardStarted)
			}
			<-releaseForward
			return ForwardOutcome{}
		},
		HotCfg: hotCfg,
	})
	p.Start()

	// First request: takes the single concurrency slot and blocks.
	go func() {
		_, _ = p.Submit(context.Background(), NewQueuedRequest("in-flight", "t", "m", context.Background(), nil))
	}()
	select {
	case <-forwardStarted:
	case <-time.After(time.Second):
		t.Fatal("first request never entered forward")
	}

	// Second request: admitted to the Tier-2 queue, parked in governor wait.
	queuedDone := make(chan error, 1)
	go func() {
		_, err := p.Submit(context.Background(), NewQueuedRequest("queued", "t", "m", context.Background(), nil))
		queuedDone <- err
	}()
	// Give it a moment to land in cf.queue (governor pacing).
	time.Sleep(80 * time.Millisecond)

	// Stop races with the queued request. The forwarder loop must drain its
	// queue and complete the queued qr instead of dropping it.
	close(releaseForward)
	p.Stop()

	select {
	case err := <-queuedDone:
		// Submit returned — the queued request was completed. Under the bug
		// this select would time out and the goroutine would leak.
		_ = err
	case <-time.After(2 * time.Second):
		t.Fatal("queued Submit never returned: Tier-2 forwarder dropped qr on Stop instead of draining")
	}
}

// TestCredEnqueuedAtSetBeforeSend pins the data-race fix in tryEnqueueCred.
// qr.CredEnqueuedAt must be written BEFORE the channel send so the
// forwarder loop's read (acquire → metricCredQueueWait) is ordered by the
// channel hand-off. Run with -race to catch the regression.
func TestCredEnqueuedAtSetBeforeSend(t *testing.T) {
	t.Parallel()
	ref := cred(1, ModeConcurrency, 8)
	cfg := DefaultConfig()
	cfg.RetryPerCredential = 0
	hotCfg := &atomic.Value{}
	hotCfg.Store(&cfg)
	p := NewPipeline(Deps{
		RouteFunc: func(context.Context, *QueuedRequest) ([]CredentialRef, error) {
			return []CredentialRef{ref}, nil
		},
		ModelResolveFunc: func(context.Context, string, []string) (string, []string, error) {
			return "m", nil, nil
		},
		ForwardFunc: func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome {
			return ForwardOutcome{}
		},
		HotCfg: hotCfg,
	})
	p.Start()
	defer p.Stop()

	const N = 200
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, _ = p.Submit(context.Background(), NewQueuedRequest("r", "t", "m", context.Background(), nil))
		}()
	}
	wg.Wait()
}
