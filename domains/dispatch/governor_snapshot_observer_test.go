package dispatch

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubProvider is a SnapshotProvider used by the observer tests. It
// returns the snapshots recorded in `snaps` and tracks call counts so
// the cadence test can assert tick frequency.
type stubProvider struct {
	mu    sync.Mutex
	snaps []GovernorSnapshot
	calls atomic.Int32
	rev   atomic.Uint64
}

func (s *stubProvider) ForEachCredSnapshot(fn func(snap GovernorSnapshot) error) error {
	s.calls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, snap := range s.snaps {
		if err := fn(snap); err != nil {
			return err
		}
	}
	return nil
}

func (s *stubProvider) ActiveRevision() uint64 {
	return s.rev.Load()
}

// TestObserverProducesSnapshotsAtConfiguredCadence verifies the tick
// goroutine fires approximately every tickInterval. We use a 20ms tick
// over a 200ms window; expect ~10 calls ±3 (allow for timer slack).
func TestObserverProducesSnapshotsAtConfiguredCadence(t *testing.T) {
	provider := &stubProvider{
		snaps: []GovernorSnapshot{
			{Backend: string(BackendLocal), Mode: ModeRPM, State: SnapshotStateReady, AgeMS: 0},
		},
	}
	o := NewGovernorSnapshotObserver(20 * time.Millisecond)
	o.SetProvider(provider, func() GovernorBackend { return nil }, provider.ActiveRevision)
	o.Start(context.Background())
	defer o.Stop()

	time.Sleep(200 * time.Millisecond)

	got := provider.calls.Load()
	if got < 6 || got > 14 {
		t.Fatalf("tick cadence off: got %d ticks in 200ms with 20ms tick; want 6..14", got)
	}
}

// TestObserverValidatesEverySnapshot verifies a snapshot that violates
// the bidirectional invariant (State != Unknown with non-nil BackendErr)
// is recovered by the observer — the tick goroutine keeps running.
func TestObserverValidatesEverySnapshot(t *testing.T) {
	provider := &stubProvider{
		snaps: []GovernorSnapshot{{
			Backend:    string(BackendLocal),
			Mode:       ModeRPM,
			State:      SnapshotStateReady, // consistent with no BackendErr
			BackendErr: errTestBackendFault,
		}},
	}
	o := NewGovernorSnapshotObserver(10 * time.Millisecond)
	o.SetProvider(provider, func() GovernorBackend { return nil }, provider.ActiveRevision)
	o.Start(context.Background())
	defer o.Stop()

	time.Sleep(50 * time.Millisecond)
	if provider.calls.Load() < 2 {
		t.Fatalf("observer should have ticked at least twice; got %d", provider.calls.Load())
	}
}

// TestObserverStopsCleanly verifies Stop is idempotent and does not
// deadlock when called before any tick or after several ticks.
func TestObserverStopsCleanly(t *testing.T) {
	o := NewGovernorSnapshotObserver(5 * time.Millisecond)
	o.SetProvider(&stubProvider{}, func() GovernorBackend { return nil }, func() uint64 { return 0 })
	o.Start(context.Background())
	time.Sleep(20 * time.Millisecond)
	o.Stop()
	// Second Stop must not panic / hang.
	done := make(chan struct{})
	go func() {
		o.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("second Stop deadlocked")
	}
}

// TestObserverDropsOffListLabels verifies the off-list rejection path:
// a snapshot whose backend/mode are not in the allowlist is dropped
// (no metric emit), the tick goroutine keeps running, and the
// dispatch_governor_unknown_label_drops counter increments.
func TestObserverDropsOffListLabels(t *testing.T) {
	provider := &stubProvider{
		snaps: []GovernorSnapshot{{
			Backend: "bogus",
			Mode:    ModeRPM,
			State:   SnapshotStateReady,
		}},
	}
	before := counterValueFromGatherer(t, "dispatch_governor_unknown_label_drops_total")

	o := NewGovernorSnapshotObserver(10 * time.Millisecond)
	o.SetProvider(provider, func() GovernorBackend { return nil }, provider.ActiveRevision)
	o.Start(context.Background())
	defer o.Stop()

	time.Sleep(50 * time.Millisecond)

	after := counterValueFromGatherer(t, "dispatch_governor_unknown_label_drops_total")
	if after-before < 1 {
		t.Fatalf("expected at least one unknown-label drop; got delta %v", after-before)
	}
}

// TestObserverNilProviderIsNoop verifies that an observer with no
// provider wired (e.g. composed but not yet attached to a Pipeline)
// does not panic and exits cleanly on Stop.
func TestObserverNilProviderIsNoop(t *testing.T) {
	o := NewGovernorSnapshotObserver(10 * time.Millisecond)
	o.Start(context.Background())
	time.Sleep(20 * time.Millisecond)
	o.Stop()
}

// TestPipelineForEachCredSnapshotReturnsZeroWhenEmpty pins the contract
// that a freshly-started Pipeline with no credForwarders returns nil
// from ForEachCredSnapshot (no panic, no walk).
func TestPipelineForEachCredSnapshotReturnsZeroWhenEmpty(t *testing.T) {
	p := newTestPipelineForObserver(t)
	called := false
	err := p.ForEachCredSnapshot(func(snap GovernorSnapshot) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("ForEachCredSnapshot: %v", err)
	}
	if called {
		t.Fatal("ForEachCredSnapshot invoked callback with empty forwarders map")
	}
	if got := p.ActiveRevision(); got != 0 {
		t.Fatalf("ActiveRevision: got %d want 0", got)
	}
}

// TestPipelineSetActivePolicyRevisionRoundTrips ensures the reader used
// by the observer sees the writer's value.
func TestPipelineSetActivePolicyRevisionRoundTrips(t *testing.T) {
	p := newTestPipelineForObserver(t)
	p.SetActivePolicyRevision(7)
	if got := p.ActiveRevision(); got != 7 {
		t.Fatalf("ActiveRevision: got %d want 7", got)
	}
	p.SetActivePolicyRevision(99)
	if got := p.ActiveRevision(); got != 99 {
		t.Fatalf("ActiveRevision after re-set: got %d want 99", got)
	}
}

// TestPipelineSetGovernorSnapshotObserverAfterStartPanics pins the
// composition-root ordering invariant.
func TestPipelineSetGovernorSnapshotObserverAfterStartPanics(t *testing.T) {
	p := newTestPipelineForObserver(t)
	p.Start()
	defer p.Stop()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic; got none")
		}
	}()
	p.SetGovernorSnapshotObserver(NewGovernorSnapshotObserver(10 * time.Millisecond))
}

// newTestPipelineForObserver builds a minimal Pipeline suitable for
// observer unit tests. The forwarders map starts empty so most tests
// can assert on the empty-path behavior; tests that need credForwarders
// build them explicitly.
func newTestPipelineForObserver(t *testing.T) *Pipeline {
	t.Helper()
	return NewPipeline(Deps{
		RouteFunc: func(ctx context.Context, qr *QueuedRequest) ([]CredentialRef, error) { return nil, nil },
		ModelResolveFunc: func(ctx context.Context, requested string, tried []string) (string, []string, error) {
			return requested, nil, nil
		},
		ForwardFunc: func(ctx context.Context, qr *QueuedRequest, cred CredentialRef) ForwardOutcome {
			return ForwardOutcome{}
		},
	})
}
