package dispatch

// Stage F — Pipeline.ApplyPolicy live-path tests.
//
// These tests pin the contract declared in policy_applier.go:17:
//   - Strict-monotonic revision (no-op replay is allowed and returns nil)
//   - Atomic per-cred Governor swap on a live credForwarder
//   - Spec-staged Governor consumed by the FIRST getOrCreateForwarder
//     after a publish-before-traffic (cold start order)
//   - Backend NotifyRevisions runs BEFORE any local swap; a backend
//     error leaves the active revision pinned
//   - ApplyPolicy on an empty Pipeline is a safe no-op apart from
//     stamping the revision

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestApplyPolicyRejectsZeroRevision(t *testing.T) {
	p := NewPipeline(Deps{})
	defer p.Stop()

	if err := p.ApplyPolicy(context.Background(), GovernorPolicy{Revision: 0}); err == nil {
		t.Fatal("ApplyPolicy with revision=0 must return error")
	}
	if got := p.ActiveRevision(); got != 0 {
		t.Fatalf("active revision after rejected zero-rev: got %d want 0", got)
	}
}

func TestApplyPolicyOnEmptyPipelineOnlyStampsRevision(t *testing.T) {
	p := NewPipeline(Deps{})
	defer p.Stop()

	if err := p.ApplyPolicy(context.Background(), GovernorPolicy{
		Revision:    3,
		GeneratedAt: time.Now(),
	}); err != nil {
		t.Fatalf("ApplyPolicy: %v", err)
	}
	if got := p.ActiveRevision(); got != 3 {
		t.Fatalf("active revision: got %d want 3", got)
	}
}

func TestApplyPolicyRejectsNonMonotonicRevision(t *testing.T) {
	p := NewPipeline(Deps{})
	defer p.Stop()

	if err := p.ApplyPolicy(context.Background(), GovernorPolicy{Revision: 5}); err != nil {
		t.Fatalf("first ApplyPolicy: %v", err)
	}
	// Same revision is a no-op (publisher replay).
	if err := p.ApplyPolicy(context.Background(), GovernorPolicy{Revision: 5}); err != nil {
		t.Fatalf("replay ApplyPolicy: %v", err)
	}
	// Older revision is also a no-op (out-of-order NOTIFY).
	if err := p.ApplyPolicy(context.Background(), GovernorPolicy{Revision: 3}); err != nil {
		t.Fatalf("older ApplyPolicy: %v", err)
	}
	if got := p.ActiveRevision(); got != 5 {
		t.Fatalf("active revision: got %d want 5 (must not regress)", got)
	}
}

func TestApplyPolicySwapsGovernorOnExistingForwarder(t *testing.T) {
	p := NewPipeline(Deps{})
	defer p.Stop()

	ref := cred(7, ModeConcurrency, 2)
	cf := p.getOrCreateForwarder(ref)
	if cf == nil {
		t.Fatal("getOrCreateForwarder returned nil")
	}
	original, ok := cf.govLocked().(*concurrencyGovernor)
	if !ok {
		t.Fatalf("initial governor type = %T, want *concurrencyGovernor", cf.govLocked())
	}
	if original.cap != 2 {
		t.Fatalf("initial governor cap = %d, want 2", original.cap)
	}

	// Stage F: publish a new spec that doubles the cap on the same
	// credential. The live forwarder's governor must be swapped.
	newPolicy := GovernorPolicy{
		Revision:    1,
		GeneratedAt: time.Now(),
		Specs: []GovernorSpec{
			{CredentialID: 7, Mode: ModeConcurrency, Limit: 8},
		},
	}
	if err := p.ApplyPolicy(context.Background(), newPolicy); err != nil {
		t.Fatalf("ApplyPolicy: %v", err)
	}
	if got := p.ActiveRevision(); got != 1 {
		t.Fatalf("active revision: got %d want 1", got)
	}
	swapped, ok := cf.govLocked().(*concurrencyGovernor)
	if !ok {
		t.Fatalf("swapped governor type = %T, want *concurrencyGovernor", cf.govLocked())
	}
	if swapped == original {
		t.Fatal("ApplyPolicy did not replace the Governor pointer")
	}
	if swapped.cap != 8 {
		t.Fatalf("swapped governor cap = %d, want 8", swapped.cap)
	}
}

func TestApplyPolicyStagePreStartGovernor(t *testing.T) {
	// Stage F: a policy published BEFORE the first request creates a
	// forwarder. The spec-derived Governor must be the one the freshly
	// constructed forwarder ends up with, not the CredentialRef default.
	p := NewPipeline(Deps{})
	defer p.Stop()

	if err := p.ApplyPolicy(context.Background(), GovernorPolicy{
		Revision:    1,
		GeneratedAt: time.Now(),
		Specs: []GovernorSpec{
			{CredentialID: 11, Mode: ModeConcurrency, Limit: 12},
		},
	}); err != nil {
		t.Fatalf("ApplyPolicy (pre-traffic): %v", err)
	}

	cf := p.getOrCreateForwarder(cred(11, ModeConcurrency, 99))
	if cf == nil {
		t.Fatal("getOrCreateForwarder returned nil")
	}
	got, ok := cf.govLocked().(*concurrencyGovernor)
	if !ok {
		t.Fatalf("staged governor type = %T, want *concurrencyGovernor", cf.govLocked())
	}
	if got.cap != 12 {
		t.Fatalf("staged governor cap = %d, want 12 (spec-driven, not 99 from CredentialRef)", got.cap)
	}
	if pending := len(p.pendingGov); pending != 0 {
		t.Fatalf("pendingGov not drained after claim: %d entries remain", pending)
	}
}

func TestApplyPolicyCallsBackendNotifyRevisions(t *testing.T) {
	p := NewPipeline(Deps{})
	defer p.Stop()

	backend := &fakeBackend{kind: BackendRedisEnforce, name: "redis"}
	p.SetGovernorBackend(backend)

	if err := p.ApplyPolicy(context.Background(), GovernorPolicy{
		Revision:    7,
		GeneratedAt: time.Now(),
		Specs: []GovernorSpec{
			{CredentialID: 13, Mode: ModeConcurrency, Limit: 3},
		},
	}); err != nil {
		t.Fatalf("ApplyPolicy: %v", err)
	}
	if got, want := len(backend.notifs), 1; got != want {
		t.Fatalf("NotifyRevisions calls: got %d want %d", got, want)
	}
	if backend.notifs[0] != 7 {
		t.Fatalf("NotifyRevisions rev: got %d want 7", backend.notifs[0])
	}
}

func TestApplyPolicyBackendNotifyFailureLeavesRevisionPinned(t *testing.T) {
	p := NewPipeline(Deps{})
	defer p.Stop()

	ref := cred(15, ModeConcurrency, 1)
	cf := p.getOrCreateForwarder(ref)
	if cf == nil {
		t.Fatal("getOrCreateForwarder returned nil")
	}
	original, _ := cf.govLocked().(*concurrencyGovernor)

	want := errors.New("redis: cluster down")
	backend := &fakeBackend{kind: BackendRedisEnforce, name: "redis", notifs: nil}
	backend.notifyErr = want
	p.SetGovernorBackend(backend)

	err := p.ApplyPolicy(context.Background(), GovernorPolicy{
		Revision:    1,
		GeneratedAt: time.Now(),
		Specs: []GovernorSpec{
			{CredentialID: 15, Mode: ModeConcurrency, Limit: 99},
		},
	})
	if !errors.Is(err, want) {
		t.Fatalf("ApplyPolicy error: got %v want %v", err, want)
	}
	if got := p.ActiveRevision(); got != 0 {
		t.Fatalf("active revision after backend failure: got %d want 0", got)
	}
	current, _ := cf.govLocked().(*concurrencyGovernor)
	if current != original {
		t.Fatalf("Governor pointer swapped despite backend failure: was %p now %p", original, current)
	}
}

func TestApplyPolicyLocalBackendSkipsNotifyRevisions(t *testing.T) {
	p := NewPipeline(Deps{})
	defer p.Stop()

	backend := &fakeBackend{kind: BackendLocal, name: "local"}
	p.SetGovernorBackend(backend)

	ref := cred(20, ModeConcurrency, 2)
	cf := p.getOrCreateForwarder(ref)
	if cf == nil {
		t.Fatal("getOrCreateForwarder returned nil")
	}

	if err := p.ApplyPolicy(context.Background(), GovernorPolicy{
		Revision:    2,
		GeneratedAt: time.Now(),
		Specs: []GovernorSpec{
			{CredentialID: 20, Mode: ModeConcurrency, Limit: 16},
		},
	}); err != nil {
		t.Fatalf("ApplyPolicy: %v", err)
	}
	if got, want := len(backend.notifs), 0; got != want {
		t.Fatalf("NotifyRevisions calls on local backend: got %d want %d", got, want)
	}
	got, _ := cf.govLocked().(*concurrencyGovernor)
	if got.cap != 16 {
		t.Fatalf("local swap cap = %d, want 16", got.cap)
	}
}

type countingGovernor struct {
	mode         string
	acquireCalls int
	releaseCalls int
}

func (g *countingGovernor) Mode() string { return g.mode }
func (g *countingGovernor) Acquire(context.Context, *QueuedRequest, time.Time) error {
	g.acquireCalls++
	return nil
}
func (g *countingGovernor) Release(*QueuedRequest) { g.releaseCalls++ }

type gatedNotifyBackend struct {
	fakeBackend
	blockRevision uint64
	entered       chan struct{}
	release       chan struct{}
}

func (b *gatedNotifyBackend) NotifyRevisions(ctx context.Context, rev uint64) error {
	b.notifs = append(b.notifs, rev)
	if rev == b.blockRevision {
		select {
		case b.entered <- struct{}{}:
		default:
		}
		select {
		case <-b.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return b.notifyErr
}

func TestApplyPolicySerializesConcurrentRevisions(t *testing.T) {
	backend := &gatedNotifyBackend{
		fakeBackend:   fakeBackend{kind: BackendRedisEnforce, name: "redis"},
		blockRevision: 10,
		entered:       make(chan struct{}, 1),
		release:       make(chan struct{}),
	}
	p := NewPipeline(Deps{})
	defer p.Stop()
	p.SetGovernorBackend(backend)

	firstDone := make(chan error, 1)
	go func() { firstDone <- p.ApplyPolicy(context.Background(), GovernorPolicy{Revision: 10}) }()
	select {
	case <-backend.entered:
	case <-time.After(time.Second):
		t.Fatal("revision 10 did not enter backend notification")
	}

	secondDone := make(chan error, 1)
	go func() { secondDone <- p.ApplyPolicy(context.Background(), GovernorPolicy{Revision: 11}) }()
	select {
	case err := <-secondDone:
		t.Fatalf("revision 11 completed before revision 10: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(backend.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("revision 10: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("revision 11: %v", err)
	}
	if got := p.ActiveRevision(); got != 11 {
		t.Fatalf("active revision = %d, want 11", got)
	}
	if len(backend.notifs) != 2 || backend.notifs[0] != 10 || backend.notifs[1] != 11 {
		t.Fatalf("backend revisions = %v, want [10 11]", backend.notifs)
	}
}

func TestForwarderReleasesGovernorCapturedBeforeHotSwap(t *testing.T) {
	forwardStarted := make(chan struct{})
	forwardRelease := make(chan struct{})
	p := NewPipeline(Deps{
		ForwardFunc: func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome {
			close(forwardStarted)
			<-forwardRelease
			return ForwardOutcome{Result: "ok"}
		},
	})
	defer p.Stop()

	oldGov := &countingGovernor{mode: ModeConcurrency}
	newGov := &countingGovernor{mode: ModeConcurrency}
	ref := cred(31, ModeConcurrency, 1)
	cf := &credForwarder{
		cred:  ref,
		queue: make(chan *QueuedRequest, 1),
		limit: 1,
		gov:   oldGov,
		pipe:  p,
		ctx:   context.Background(),
	}
	cf.depth.Store(1)
	qr := NewQueuedRequest("hot-swap-release", "tenant", "model", context.Background(), nil)
	gov, acquired := cf.acquire(qr)
	if !acquired {
		t.Fatal("governor admission failed")
	}
	cf.replaceGov(newGov)
	cf.wg.Add(1)
	go cf.attempt(qr, gov)
	select {
	case <-forwardStarted:
	case <-time.After(time.Second):
		t.Fatal("forward attempt did not start")
	}
	close(forwardRelease)
	select {
	case <-qr.ResultCh:
	case <-time.After(time.Second):
		t.Fatal("forward attempt did not complete")
	}
	cf.wg.Wait()
	if oldGov.acquireCalls != 1 || oldGov.releaseCalls != 1 {
		t.Fatalf("old governor calls = acquire %d release %d, want 1/1", oldGov.acquireCalls, oldGov.releaseCalls)
	}
	if newGov.releaseCalls != 0 {
		t.Fatalf("new governor release calls = %d, want 0", newGov.releaseCalls)
	}
}

// TestApplyPolicyRPMSpecRoutesCanonicalLimit pins the mode-correct limit
// routing: an RPM policy that sets only the canonical Limit field (no
// mirror in RPMLimit) must populate the CredentialRef's RPMLimit so the
// governor factory builds an RPM token bucket, not a no-op.
func TestApplyPolicyRPMSpecRoutesCanonicalLimit(t *testing.T) {
	p := NewPipeline(Deps{})
	defer p.Stop()

	backend := &fakeBackend{kind: BackendRedisEnforce, name: "redis"}
	p.SetGovernorBackend(backend)

	if err := p.ApplyPolicy(context.Background(), GovernorPolicy{
		Revision:    1,
		GeneratedAt: time.Now(),
		Specs: []GovernorSpec{
			// No RPMLimit mirror: only the canonical Limit field.
			{CredentialID: 41, Mode: ModeRPM, Limit: 100},
		},
	}); err != nil {
		t.Fatalf("ApplyPolicy: %v", err)
	}
	if got := p.ActiveRevision(); got != 1 {
		t.Fatalf("active revision: got %d want 1", got)
	}
	if got := backend.newSpecs[0].Mode; got != ModeRPM {
		t.Fatalf("backend spec mode = %q, want %q", got, ModeRPM)
	}
	if got := backend.newSpecs[0].Limit; got != 100 {
		t.Fatalf("backend spec limit = %d, want 100 (canonical RPM cap)", got)
	}
	if got := backend.newSpecs[0].RPMLimit; got != 100 {
		t.Fatalf("backend spec RPMLimit = %d, want 100 (derived from canonical Limit)", got)
	}
}

// TestApplyPolicyTPMSpecRoutesCanonicalLimit mirrors the RPM test for TPM.
func TestApplyPolicyTPMSpecRoutesCanonicalLimit(t *testing.T) {
	p := NewPipeline(Deps{})
	defer p.Stop()

	backend := &fakeBackend{kind: BackendRedisEnforce, name: "redis"}
	p.SetGovernorBackend(backend)

	if err := p.ApplyPolicy(context.Background(), GovernorPolicy{
		Revision:    1,
		GeneratedAt: time.Now(),
		Specs: []GovernorSpec{
			// No TPMLimit mirror: only the canonical Limit field.
			{CredentialID: 42, Mode: ModeTPM, Limit: 8000},
		},
	}); err != nil {
		t.Fatalf("ApplyPolicy: %v", err)
	}
	if got := backend.newSpecs[0].Mode; got != ModeTPM {
		t.Fatalf("backend spec mode = %q, want %q", got, ModeTPM)
	}
	if got := backend.newSpecs[0].Limit; got != 8000 {
		t.Fatalf("backend spec limit = %d, want 8000 (canonical TPM cap)", got)
	}
	if got := backend.newSpecs[0].TPMLimit; got != 8000 {
		t.Fatalf("backend spec TPMLimit = %d, want 8000 (derived from canonical Limit)", got)
	}
}

// TestApplyPolicyPassesNewRevisionToBackendNew pins the contract that
// governor construction during a live policy apply carries the new
// policy's revision, not the previous one. The Redis backend uses
// spec.Revision as the cache-invalidation identity (see
// redis_backend.go:634 nextToken and the per-cred revision sequence),
// so a stale revision would silently reuse stale lease state.
func TestApplyPolicyPassesNewRevisionToBackendNew(t *testing.T) {
	p := NewPipeline(Deps{})
	defer p.Stop()

	backend := &fakeBackend{kind: BackendRedisEnforce, name: "redis"}
	p.SetGovernorBackend(backend)

	if err := p.ApplyPolicy(context.Background(), GovernorPolicy{
		Revision:    5,
		GeneratedAt: time.Now(),
		Specs: []GovernorSpec{
			{CredentialID: 43, Mode: ModeConcurrency, Limit: 4},
		},
	}); err != nil {
		t.Fatalf("first ApplyPolicy: %v", err)
	}
	if got := backend.newSpecs[0].Revision; got != 5 {
		t.Fatalf("first apply: backend spec revision = %d, want 5 (new policy revision)", got)
	}

	if err := p.ApplyPolicy(context.Background(), GovernorPolicy{
		Revision:    6,
		GeneratedAt: time.Now(),
		Specs: []GovernorSpec{
			{CredentialID: 43, Mode: ModeConcurrency, Limit: 8},
		},
	}); err != nil {
		t.Fatalf("second ApplyPolicy: %v", err)
	}
	if got := backend.newSpecs[1].Revision; got != 6 {
		t.Fatalf("second apply: backend spec revision = %d, want 6 (new policy revision, not 5)", got)
	}
}

// TestApplyPolicyFailClosedOnBackendNewError pins that a Redis backend
// failure during ApplyPolicy aborts the swap, leaves activePolicyRevision
// pinned at its previous value, and surfaces ErrGovernorUnavailable so the
// publisher's retry loop can replay the same delta. Pre-fix, the
// unavailableGovernor was swapped in and the revision was advanced,
// silently denying subsequent admissions.
func TestApplyPolicyFailClosedOnBackendNewError(t *testing.T) {
	p := NewPipeline(Deps{})
	defer p.Stop()

	want := errors.New("redis: cluster rebalancing")
	backend := &fakeBackend{kind: BackendRedisEnforce, name: "redis", newErr: want}
	p.SetGovernorBackend(backend)

	// Stage an in-process forwarder so we can verify the live governor is
	// preserved across the failed apply.
	ref := cred(44, ModeConcurrency, 1)
	cf := p.getOrCreateForwarder(ref)
	original := cf.gov

	err := p.ApplyPolicy(context.Background(), GovernorPolicy{
		Revision:    1,
		GeneratedAt: time.Now(),
		Specs: []GovernorSpec{
			{CredentialID: 44, Mode: ModeConcurrency, Limit: 99},
		},
	})
	if !errors.Is(err, want) {
		t.Fatalf("ApplyPolicy error = %v, want %v", err, want)
	}
	if !IsGovernorUnavailable(err) {
		t.Fatalf("ApplyPolicy error is not ErrGovernorUnavailable: %v", err)
	}
	if got := p.ActiveRevision(); got != 0 {
		t.Fatalf("active revision after backend failure: got %d want 0 (must stay pinned)", got)
	}
	if current := cf.gov; current != original {
		t.Fatalf("live forwarder governor swapped despite backend failure: was %T now %T", original, current)
	}

	// Recovery: when the backend is reachable again, the next ApplyPolicy
	// must succeed. This proves the failure path did not wedge the
	// pipeline into a stuck state.
	backend.newErr = nil
	backend.newGov = newConcurrencyGovernor(2)
	if err := p.ApplyPolicy(context.Background(), GovernorPolicy{
		Revision:    2,
		GeneratedAt: time.Now(),
		Specs: []GovernorSpec{
			{CredentialID: 44, Mode: ModeConcurrency, Limit: 4},
		},
	}); err != nil {
		t.Fatalf("follow-up ApplyPolicy: %v (pipeline should not be wedged)", err)
	}
	if got := p.ActiveRevision(); got != 2 {
		t.Fatalf("active revision after recovery: got %d want 2", got)
	}
}
