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
	allowChange bool
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
		allowChange: true,
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("r1", "t", "a", context.Background(), nil)
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
		allowChange: false,
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("r1", "t", "a", context.Background(), nil)
	_, err := p.Submit(context.Background(), qr)
	if !errors.Is(err, ErrNoRoute) {
		t.Fatalf("expected ErrNoRoute, got: %v", err)
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
