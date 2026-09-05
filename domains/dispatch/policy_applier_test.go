package dispatch

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRecordingApplierInitialActiveRevisionIsZero(t *testing.T) {
	a := NewRecordingApplier(nil)
	if got := a.ActiveRevision(); got != 0 {
		t.Fatalf("initial active revision: got %d want 0", got)
	}
}

func TestRecordingApplierRecordsCalls(t *testing.T) {
	a := NewRecordingApplier(nil)
	pol := GovernorPolicy{Revision: 5, GeneratedAt: time.Now()}
	if err := a.ApplyPolicy(context.Background(), pol); err != nil {
		t.Fatalf("ApplyPolicy: %v", err)
	}
	rec, ok := AsRecordingApplier(a)
	if !ok {
		t.Fatalf("expected recordingApplier")
	}
	if rec.calls != 1 {
		t.Fatalf("calls: got %d want 1", rec.calls)
	}
	if rec.last.Revision != 5 {
		t.Fatalf("last revision: got %d want 5", rec.last.Revision)
	}
	if got := a.ActiveRevision(); got != 5 {
		t.Fatalf("active revision: got %d want 5", got)
	}
}

func TestRecordingApplierPropagatesError(t *testing.T) {
	want := errors.New("redis: no route")
	a := NewRecordingApplier(want)
	if err := a.ApplyPolicy(context.Background(), GovernorPolicy{Revision: 1}); !errors.Is(err, want) {
		t.Fatalf("ApplyPolicy error propagation: got %v want %v", err, want)
	}
	rec, _ := AsRecordingApplier(a)
	if rec.calls != 0 {
		t.Fatalf("failed apply must not bump calls: got %d", rec.calls)
	}
	if got := a.ActiveRevision(); got != 0 {
		t.Fatalf("active revision after failure: got %d want 0", got)
	}
}

// Concurrent ApplyPolicy must serialize (single-writer contract) and the
// final ActiveRevision must reflect the highest revision that landed. With
// the strict-monotonic contract, calls with revision <= active are no-ops,
// so the number of recorded calls is at least 1 and at most N — exactly 1
// when the goroutines happen to be served in decreasing order, exactly N
// when served in increasing order. The invariant under test is that
// ActiveRevision equals the max revision supplied and never regresses.
func TestRecordingApplierConcurrentApplySerializes(t *testing.T) {
	a := NewRecordingApplier(nil)
	const N = 64
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(rev uint64) {
			defer wg.Done()
			err := a.ApplyPolicy(context.Background(), GovernorPolicy{
				Revision:    rev,
				GeneratedAt: time.Now(),
			})
			if err != nil {
				t.Errorf("ApplyPolicy(%d): %v", rev, err)
			}
		}(uint64(i + 1))
	}
	wg.Wait()
	rec, _ := AsRecordingApplier(a)
	if rec.calls < 1 || rec.calls > N {
		t.Fatalf("calls: got %d want 1..%d (monotonic guard rejects older revs)", rec.calls, N)
	}
	got := a.ActiveRevision()
	if got != N {
		t.Fatalf("active revision: got %d want %d (highest revision must win)", got, N)
	}
}

// Stage E/F monotonic contract: a GovernorPolicy whose Revision is not
// strictly greater than the current ActiveRevision is a no-op — calls is
// NOT bumped and active does NOT regress. Stage A documented the opposite
// behavior ("accepts any revision") only because the production impl had
// not yet been wired; that test double is now expected to honor the same
// contract as the Pipeline, otherwise the two impls drift apart in tests.
func TestRecordingApplierRejectsOlderRevision(t *testing.T) {
	a := NewRecordingApplier(nil)
	if err := a.ApplyPolicy(context.Background(), GovernorPolicy{Revision: 3}); err != nil {
		t.Fatalf("first ApplyPolicy: %v", err)
	}
	// Same revision is a no-op (publisher replay).
	if err := a.ApplyPolicy(context.Background(), GovernorPolicy{Revision: 3}); err != nil {
		t.Fatalf("replay ApplyPolicy: %v", err)
	}
	// Older revision MUST be a no-op (active must not regress).
	if err := a.ApplyPolicy(context.Background(), GovernorPolicy{Revision: 1}); err != nil {
		t.Fatalf("older ApplyPolicy: %v", err)
	}
	rec, _ := AsRecordingApplier(a)
	if rec.calls != 1 {
		t.Fatalf("calls: got %d want 1 (replay + older must not bump counter)", rec.calls)
	}
	if got := a.ActiveRevision(); got != 3 {
		t.Fatalf("active revision after older apply: got %d want 3", got)
	}
}
