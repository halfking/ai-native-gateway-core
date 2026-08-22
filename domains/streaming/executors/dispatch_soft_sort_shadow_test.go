package executors

import (
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
)

// stubProviderSoftSort is a SnapshotProvider stub that returns a
// fixed map for the soft-sort shadow tests. Mirrors the
// dispatch.stubProvider API surface (ForEachCredSnapshot + ActiveRevision
// + SnapshotForCred) so the same composition wiring works.
type stubProviderSoftSort struct {
	states map[int]dispatch.SnapshotState
}

func (s *stubProviderSoftSort) ForEachCredSnapshot(fn func(snap dispatch.GovernorSnapshot) error) error {
	for id, st := range s.states {
		if err := fn(dispatch.GovernorSnapshot{
			Backend:    string(dispatch.BackendLocal),
			Mode:       dispatch.ModeConcurrency,
			State:      st,
			BackendErr: nil,
		}); err != nil {
			_ = id
			return err
		}
	}
	return nil
}
func (s *stubProviderSoftSort) ActiveRevision() uint64 { return 0 }
func (s *stubProviderSoftSort) SnapshotForCred(credID int) (dispatch.SnapshotState, bool) {
	st, ok := s.states[credID]
	return st, ok
}

func makeRefs(ids ...int) []dispatch.CredentialRef {
	out := make([]dispatch.CredentialRef, len(ids))
	for i, id := range ids {
		out[i] = dispatch.CredentialRef{CredentialID: id, ProviderID: 1}
	}
	return out
}

func extractIDs(refs []dispatch.CredentialRef) []int {
	out := make([]int, len(refs))
	for i, r := range refs {
		out[i] = r.CredentialID
	}
	return out
}

// TestDispatchRouteSoftRankFlagOffIsNoOp pins the strict no-op
// contract: when capacityAwareSortOn is false (the default), the
// candidate slice passes through untouched regardless of the snapFn.
func TestDispatchRouteSoftRankFlagOffIsNoOp(t *testing.T) {
	e := &Executor{}
	snapFn := func(int) (dispatch.SnapshotState, bool) {
		return dispatch.SnapshotStateQueueFull, true
	}
	e.SetCapacityAwareSort(false, snapFn) // on=false → no-op

	in := makeRefs(100, 101, 102)
	got := e.dispatchRouteSoftRank(in)
	if !reflect.DeepEqual(extractIDs(got), []int{100, 101, 102}) {
		t.Fatalf("flag off must not re-rank; got %v", extractIDs(got))
	}
}

// TestDispatchRouteSoftRankFlagOnDemotes verifies the happy path: a
// QueueFull candidate moves to the tail while a Ready candidate stays
// at the head.
func TestDispatchRouteSoftRankFlagOnDemotes(t *testing.T) {
	e := &Executor{}
	snapFn := func(credID int) (dispatch.SnapshotState, bool) {
		if credID == 101 {
			return dispatch.SnapshotStateQueueFull, true
		}
		return dispatch.SnapshotStateReady, true
	}
	e.SetCapacityAwareSort(true, snapFn)

	in := makeRefs(100, 101, 102)
	got := e.dispatchRouteSoftRank(in)
	want := []int{100, 102, 101}
	if !reflect.DeepEqual(extractIDs(got), want) {
		t.Fatalf("QueueFull demotion: got %v want %v", extractIDs(got), want)
	}
}

// TestDispatchRouteSoftRankNilSnapFnIsNoOp verifies the fail-open
// behavior: nil snapFn must NOT panic and must leave the input
// untouched even when the on flag is true.
func TestDispatchRouteSoftRankNilSnapFnIsNoOp(t *testing.T) {
	e := &Executor{}
	e.SetCapacityAwareSort(true, nil)

	in := makeRefs(200, 201, 202)
	got := e.dispatchRouteSoftRank(in)
	if !reflect.DeepEqual(extractIDs(got), []int{200, 201, 202}) {
		t.Fatalf("nil snapFn must not re-rank; got %v", extractIDs(got))
	}
}

// TestDispatchRouteSoftRankUnknownCredNotDemoted: snapFn returning
// (Unknown, false) must be treated as Ready (fail-open). This is the
// "observer is off, cache is empty" production scenario.
func TestDispatchRouteSoftRankUnknownCredNotDemoted(t *testing.T) {
	e := &Executor{}
	snapFn := func(int) (dispatch.SnapshotState, bool) {
		return dispatch.SnapshotStateUnknown, false
	}
	e.SetCapacityAwareSort(true, snapFn)

	in := makeRefs(300, 301, 302)
	got := e.dispatchRouteSoftRank(in)
	if !reflect.DeepEqual(extractIDs(got), []int{300, 301, 302}) {
		t.Fatalf("ok=false must not demote; got %v", extractIDs(got))
	}
}

// TestDispatchRouteSoftRankAllReadyNoRegression is the shadow no-op
// baseline: when every cred reports Ready, the sorted output is
// identical to the input. This is what guarantees "off" and "on" are
// indistinguishable under non-saturation conditions.
func TestDispatchRouteSoftRankAllReadyNoRegression(t *testing.T) {
	e := &Executor{}
	snapFn := func(int) (dispatch.SnapshotState, bool) {
		return dispatch.SnapshotStateReady, true
	}
	e.SetCapacityAwareSort(true, snapFn)

	in := makeRefs(400, 401, 402, 403)
	got := e.dispatchRouteSoftRank(in)
	if !reflect.DeepEqual(extractIDs(got), []int{400, 401, 402, 403}) {
		t.Fatalf("all-Ready regression: got %v want %v", extractIDs(got), []int{400, 401, 402, 403})
	}
}

// TestSnapshotFnFromProviderAdapterBehavior verifies that the
// dispatch.SnapshotFnFromProvider closure correctly delegates to the
// stub provider's SnapshotForCred.
func TestSnapshotFnFromProviderAdapterBehavior(t *testing.T) {
	p := &stubProviderSoftSort{states: map[int]dispatch.SnapshotState{
		500: dispatch.SnapshotStateGovernorSaturated,
		501: dispatch.SnapshotStateReady,
	}}
	fn := dispatch.SnapshotFnFromProvider(p)
	state, ok := fn(500)
	if !ok || state != dispatch.SnapshotStateGovernorSaturated {
		t.Fatalf("snapFn(500): got (%q, %v) want (governor_saturated, true)", state, ok)
	}
	state, ok = fn(501)
	if !ok || state != dispatch.SnapshotStateReady {
		t.Fatalf("snapFn(501): got (%q, %v) want (ready, true)", state, ok)
	}
	// Miss path
	state, ok = fn(999)
	if ok {
		t.Fatalf("snapFn(999): got ok=true state=%q want (_, false)", state)
	}
}

// TestDispatchRouteSoftRankRaceUnderConcurrentDispatch verifies that
// concurrent dispatchRouteSoftRank invocations (mimicking per-request
// hot path) don't race or leak goroutines under -race. We don't
// dispatch via the full Pipeline here — that path is exercised by the
// existing journey_test.go tests; this one is the targeted concurrency
// pin for the soft-rank closure itself.
func TestDispatchRouteSoftRankRaceUnderConcurrentDispatch(t *testing.T) {
	e := &Executor{}
	snapFn := func(credID int) (dispatch.SnapshotState, bool) {
		if credID%2 == 0 {
			return dispatch.SnapshotStateReady, true
		}
		return dispatch.SnapshotStateQueueFull, true
	}
	e.SetCapacityAwareSort(true, snapFn)

	before := runtime.NumGoroutine()

	done := make(chan struct{})
	for i := 0; i < 50; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 20; j++ {
				_ = e.dispatchRouteSoftRank(makeRefs(1, 2, 3, 4, 5, 6, 7, 8))
			}
		}()
	}
	for i := 0; i < 50; i++ {
		<-done
	}

	// Allow the runtime to settle briefly. Goroutine count may not
	// drop instantly, but it should not grow unbounded (allow a small
	// slack for OS-level threads).
	time.Sleep(50 * time.Millisecond)
	after := runtime.NumGoroutine()
	if delta := after - before; delta > 5 {
		t.Fatalf("goroutine leak: before=%d after=%d delta=%d", before, after, delta)
	}
}
