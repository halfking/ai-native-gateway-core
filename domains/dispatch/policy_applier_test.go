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
	if got := a.ActiveRevision(context.Background()); got != 0 {
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
	if got := a.ActiveRevision(context.Background()); got != 5 {
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
	if got := a.ActiveRevision(context.Background()); got != 0 {
		t.Fatalf("active revision after failure: got %d want 0", got)
	}
}

// Concurrent ApplyPolicy must serialize (single-writer contract) and the
// final ActiveRevision must reflect the last successfully applied call.
// Stage E will use the same hook to assert no torn reads under
// shadow-load tests.
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
	if rec.calls != N {
		t.Fatalf("calls: got %d want %d", rec.calls, N)
	}
	got := a.ActiveRevision(context.Background())
	if got == 0 || got > N {
		t.Fatalf("active revision out of range: got %d", got)
	}
}

// Stage E "no-op publication" contract: a GovernorPolicy whose Revision
// is not strictly greater than ActiveRevision() is a no-op (record the
// call but do not change state). Stage A only verifies the type is
// capable of carrying that semantics — the strict-monotonicity rule is
// enforced by the production impl that lands in Stage E.
func TestRecordingApplierAcceptsAnyRevisionForNow(t *testing.T) {
	a := NewRecordingApplier(nil)
	_ = a.ApplyPolicy(context.Background(), GovernorPolicy{Revision: 3})
	_ = a.ApplyPolicy(context.Background(), GovernorPolicy{Revision: 3}) // same rev
	rec, _ := AsRecordingApplier(a)
	if rec.calls != 2 {
		t.Fatalf("calls: got %d want 2 (Stage A allows same-rev replay)", rec.calls)
	}
}
