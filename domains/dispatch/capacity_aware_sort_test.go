package dispatch

import (
	"fmt"
	"reflect"
	"testing"
)

// snapMap is a test helper: builds a snapFn from a credID → SnapshotState
// map. Missing entries return (_, false) → fail-open as Ready.
func snapMap(states map[int]SnapshotState) func(int) (SnapshotState, bool) {
	return func(credID int) (SnapshotState, bool) {
		s, ok := states[credID]
		return s, ok
	}
}

// credSlice is a builder for test inputs. IDs start at 100 to make
// test failures easy to read.
func credSlice(ids ...int) []CredentialRef {
	out := make([]CredentialRef, len(ids))
	for i, id := range ids {
		out[i] = CredentialRef{CredentialID: id, ProviderID: 1}
	}
	return out
}

// ids extracts the CredentialID sequence from a sorted list for
// order-comparison assertions.
func ids(refs []CredentialRef) []int {
	out := make([]int, len(refs))
	for i, r := range refs {
		out[i] = r.CredentialID
	}
	return out
}

func TestApplySoftPenaltyKeepsReadyOrder(t *testing.T) {
	in := credSlice(100, 101, 102, 103)
	got := ApplySoftPenalty(in, snapMap(nil))
	want := []int{100, 101, 102, 103}
	if !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("all-Ready order: got %v want %v", ids(got), want)
	}
}

func TestApplySoftPenaltyDemotesQueueFull(t *testing.T) {
	// creds: R, Q, R  → expect R, R, Q
	in := credSlice(100, 101, 102)
	got := ApplySoftPenalty(in, snapMap(map[int]SnapshotState{
		100: SnapshotStateReady,
		101: SnapshotStateQueueFull,
		102: SnapshotStateReady,
	}))
	want := []int{100, 102, 101}
	if !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("QueueFull demotion: got %v want %v", ids(got), want)
	}
}

func TestApplySoftPenaltyDemotesGovernorSaturated(t *testing.T) {
	in := credSlice(200, 201, 202, 203)
	got := ApplySoftPenalty(in, snapMap(map[int]SnapshotState{
		200: SnapshotStateReady,
		201: SnapshotStateGovernorSaturated,
		202: SnapshotStateReady,
		203: SnapshotStateGovernorSaturated,
	}))
	want := []int{200, 202, 201, 203}
	if !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("Saturated demotion: got %v want %v", ids(got), want)
	}
}

func TestApplySoftPenaltyDemotesMixedSaturated(t *testing.T) {
	// S1, R1, S2, R2 → expect R1, R2, S1, S2 (relative order
	// preserved inside each bucket).
	in := credSlice(300, 301, 302, 303)
	got := ApplySoftPenalty(in, snapMap(map[int]SnapshotState{
		300: SnapshotStateQueueFull,
		301: SnapshotStateReady,
		302: SnapshotStateGovernorSaturated,
		303: SnapshotStateReady,
	}))
	want := []int{301, 303, 300, 302}
	if !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("mixed demotion: got %v want %v", ids(got), want)
	}
}

func TestApplySoftPenaltyStableAmongDemoted(t *testing.T) {
	// Q1, R, Q2 → expect R, Q1, Q2 (Q1 before Q2 preserved).
	in := credSlice(400, 401, 402)
	got := ApplySoftPenalty(in, snapMap(map[int]SnapshotState{
		400: SnapshotStateQueueFull,
		401: SnapshotStateReady,
		402: SnapshotStateQueueFull,
	}))
	want := []int{401, 400, 402}
	if !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("stable among demoted: got %v want %v", ids(got), want)
	}
}

func TestApplySoftPenaltyHandlesUnknownAsReady(t *testing.T) {
	// BackendErr path: snapFn returns ok=false → must NOT be demoted.
	in := credSlice(500, 501, 503)
	got := ApplySoftPenalty(in, snapMap(nil))
	want := []int{500, 501, 503}
	if !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("unknown treated as Ready: got %v want %v", ids(got), want)
	}
}

func TestApplySoftPenaltyDefensiveCopy(t *testing.T) {
	in := credSlice(600, 601, 602)
	got := ApplySoftPenalty(in, snapMap(map[int]SnapshotState{
		600: SnapshotStateReady,
		601: SnapshotStateQueueFull,
		602: SnapshotStateReady,
	}))
	// Mutate the input after the call — the output must not change.
	in[0].CredentialID = 9999
	if ids(got)[0] != 600 {
		t.Fatalf("output reflects input mutation; got[0]=%d want 600", ids(got)[0])
	}
	// Also: the result is a distinct slice header.
	if &in[0] == &got[0] {
		t.Fatal("returned slice aliases input")
	}
}

func TestApplySoftPenaltyNilSnapFnIsNoOp(t *testing.T) {
	in := credSlice(700, 701)
	got := ApplySoftPenalty(in, nil)
	if !reflect.DeepEqual(ids(got), []int{700, 701}) {
		t.Fatalf("nil snapFn: got %v want %v", ids(got), []int{700, 701})
	}
}

func TestApplySoftPenaltyEmptyInput(t *testing.T) {
	got := ApplySoftPenalty(nil, snapMap(nil))
	if got != nil {
		t.Fatalf("nil input → nil output; got %v", got)
	}
	got = ApplySoftPenalty([]CredentialRef{}, snapMap(nil))
	if got == nil {
		t.Fatal("empty (non-nil) input → empty (non-nil) output; got nil")
	}
	if len(got) != 0 {
		t.Fatalf("empty output length: got %d want 0", len(got))
	}
}

func TestSnapshotFnFromProviderNil(t *testing.T) {
	if fn := SnapshotFnFromProvider(nil); fn != nil {
		t.Fatal("SnapshotFnFromProvider(nil) must return nil")
	}
}

func TestSnapshotFnFromProviderAdapts(t *testing.T) {
	p := &stubProvider{}
	fn := SnapshotFnFromProvider(p)
	if fn == nil {
		t.Fatal("non-nil provider → non-nil snapFn")
	}
	state, ok := fn(42)
	if !ok || state != SnapshotStateReady {
		t.Fatalf("snapFn call: got state=%q ok=%v want (Ready, true)", state, ok)
	}
}

// TestPipelineSnapshotForCredMissFailsOpen verifies the production
// implementation: a freshly-started Pipeline (no observer ticks yet)
// must report (Unknown, false) for any credID so Stage D's sort
// degenerates to no-op rather than penalising healthy candidates.
func TestPipelineSnapshotForCredMissFailsOpen(t *testing.T) {
	p := newTestPipelineForObserver(t)
	defer p.Stop()

	state, ok := p.SnapshotForCred(999)
	if ok {
		t.Fatalf("empty cache must miss; got ok=true state=%q", state)
	}
	if state != SnapshotStateUnknown {
		t.Fatalf("empty cache miss must return SnapshotStateUnknown; got %q", state)
	}
}

// TestPipelineSnapshotForCredNilReceiver is a defensive check.
func TestPipelineSnapshotForCredNilReceiver(t *testing.T) {
	var p *Pipeline
	state, ok := p.SnapshotForCred(1)
	if ok || state != SnapshotStateUnknown {
		t.Fatalf("nil receiver must return (Unknown, false); got (%q, %v)", state, ok)
	}
}

// TestPipelineForEachCredSnapshotPopulatesCache verifies the
// credStateCache is written by ForEachCredSnapshot under credMu and
// read on the per-request path via credStateCacheMu — so a subsequent
// SnapshotForCred returns the same state the observer just emitted
// (modulo the State != Unknown filter).
//
// The test seeds credForwarder entries directly (skipping the
// goroutine-spawning newCredForwarder path) so we can inject a
// known-state gov and observe the cache write without involving
// the dispatch loop / queue-mirroring / observer goroutine.
func TestPipelineForEachCredSnapshotPopulatesCache(t *testing.T) {
	p := newTestPipelineForObserver(t)
	// No defer p.Stop() — we never called Start(), and Stop() with no
	// forwarders of its own is a no-op; teardown below manually clears
	// the seeded credForwarder to keep the field hygiene predictable.

	// Pre-condition: empty forwarders → empty cache (fail-open miss).
	if _, ok := p.SnapshotForCred(1); ok {
		t.Fatal("cache must be empty when forwarders map is empty")
	}

	// Seed a credForwarder with a saturated governor: cap=2, used=2 →
	// snapshotForCredForwarderLocked sees snap.Used (2) >= snap.Limit
	// (cf.limit=2) → State = GovernorSaturated.
	cf := &credForwarder{
		cred:  CredentialRef{CredentialID: 7, ProviderID: 1, ConcurrencyMode: ModeConcurrency, ConcurrencyLimit: 2},
		limit: 2,
		gov:   newConcurrencyGovernor(2),
		pipe:  p,
	}
	cf.gov.(*concurrencyGovernor).used.Store(2) // saturate: Used=Limit

	p.credMu.Lock()
	p.forwarders[cf.cred.CredentialID] = cf
	p.credMu.Unlock()

	// Drive ForEachCredSnapshot — no observer goroutine, no queue
	// mirror, just the read-side walk + cache write.
	var walkErr error
	if err := p.ForEachCredSnapshot(func(snap GovernorSnapshot) error {
		if snap.State != SnapshotStateGovernorSaturated {
			walkErr = fmt.Errorf("seeded governor must produce GovernorSaturated; got %q", snap.State)
		}
		return walkErr
	}); walkErr != nil {
		t.Fatal(walkErr)
	} else if err != nil {
		t.Fatalf("ForEachCredSnapshot: %v", err)
	}

	// After the walk, SnapshotForCred must return the cached state.
	state, ok := p.SnapshotForCred(7)
	if !ok {
		t.Fatal("SnapshotForCred(7) must hit after ForEachCredSnapshot populates cache")
	}
	if state != SnapshotStateGovernorSaturated {
		t.Fatalf("cached state: got %q want %q", state, SnapshotStateGovernorSaturated)
	}

	// Clear the seeded forwarder to avoid leaking state to other tests.
	p.credMu.Lock()
	delete(p.forwarders, 7)
	p.credMu.Unlock()
	p.credStateCacheMu.Lock()
	delete(p.credStateCache, 7)
	p.credStateCacheMu.Unlock()
}

// TestPipelineForEachCredSnapshotAppliesCacheOutsideCredMu verifies
// the post-fix invariant: the credStateCacheMu.Lock acquisition for
// the batch write happens AFTER credMu.Unlock. The audit found a
// regression where applyStateWrites ran inside the credMu section,
// defeating the design intent of decoupling the two mutexes.
//
// We assert this structurally by injecting a credForwarder whose walk
// fn checks the cache during the walk — the cache must NOT yet be
// populated (the write happens after credMu.Unlock, by design).
func TestPipelineForEachCredSnapshotAppliesCacheOutsideCredMu(t *testing.T) {
	p := newTestPipelineForObserver(t)

	cf := &credForwarder{
		cred:  CredentialRef{CredentialID: 11, ProviderID: 1, ConcurrencyMode: ModeConcurrency, ConcurrencyLimit: 2},
		limit: 2,
		gov:   newConcurrencyGovernor(2),
		pipe:  p,
	}
	cf.gov.(*concurrencyGovernor).used.Store(2) // saturate
	p.credMu.Lock()
	p.forwarders[cf.cred.CredentialID] = cf
	p.credMu.Unlock()

	// During the walk, the cache write has not happened yet (we're
	// still inside the credMu section). A peek from the walk fn MUST
	// see a miss — if it sees a hit, applyStateWrites ran too early.
	walkSawHit := false
	if err := p.ForEachCredSnapshot(func(snap GovernorSnapshot) error {
		_, ok := p.SnapshotForCred(11)
		walkSawHit = ok
		return nil
	}); err != nil {
		t.Fatalf("ForEachCredSnapshot: %v", err)
	}
	if walkSawHit {
		t.Fatal("applyStateWrites must run AFTER credMu.Unlock(); walk saw a cache hit, implying the write happened before the walk returned")
	}

	// After the walk returns, the cache MUST be populated.
	state, ok := p.SnapshotForCred(11)
	if !ok || state != SnapshotStateGovernorSaturated {
		t.Fatalf("post-walk cache: got (%q, %v) want (%q, true)", state, ok, SnapshotStateGovernorSaturated)
	}

	// Cleanup seeded state.
	p.credMu.Lock()
	delete(p.forwarders, 11)
	p.credMu.Unlock()
	p.credStateCacheMu.Lock()
	delete(p.credStateCache, 11)
	p.credStateCacheMu.Unlock()
}
