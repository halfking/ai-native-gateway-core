package dispatch

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestConcurrencyGovernorZeroGiveUpWaitsForRelease: giveUp.IsZero() must park
// until Release (or ctx cancel), not return pace_timeout immediately.
func TestConcurrencyGovernorZeroGiveUpWaitsForRelease(t *testing.T) {
	g := newConcurrencyGovernor(1)
	ctx := context.Background()
	if err := g.Acquire(ctx, &QueuedRequest{}, time.Time{}); err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- g.Acquire(ctx, &QueuedRequest{}, time.Time{}) // zero giveUp
	}()

	select {
	case err := <-done:
		t.Fatalf("second acquire returned before release: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	g.Release(&QueuedRequest{})
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second acquire after release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second acquire did not complete after release")
	}
}

// TestConcurrencySnapshotUsesCapNotQueueDepth: queue depth 300 must not mask
// a saturated concurrency governor (cap=2, used=2 → GovernorSaturated).
func TestConcurrencySnapshotUsesCapNotQueueDepth(t *testing.T) {
	p := NewPipeline(Deps{})
	cf := &credForwarder{
		cred:  CredentialRef{CredentialID: 99, ProviderID: 1, ConcurrencyMode: ModeConcurrency, ConcurrencyLimit: 2},
		limit: 300,
		gov:   newConcurrencyGovernor(2),
		pipe:  p,
	}
	cf.gov.(*concurrencyGovernor).used.Store(2)

	p.credMu.Lock()
	p.forwarders[cf.cred.CredentialID] = cf
	p.credMu.Unlock()
	defer func() {
		p.credMu.Lock()
		delete(p.forwarders, cf.cred.CredentialID)
		p.credMu.Unlock()
	}()

	var saw GovernorSnapshot
	var found bool
	if err := p.ForEachCredSnapshot(func(snap GovernorSnapshot) error {
		if snap.Mode == ModeConcurrency && snap.Used == 2 {
			saw = snap
			found = true
		}
		return nil
	}); err != nil {
		t.Fatalf("ForEachCredSnapshot: %v", err)
	}
	if !found {
		t.Fatal("expected concurrency snapshot for seeded forwarder")
	}
	if saw.Limit != 2 {
		t.Fatalf("snap.Limit = %d, want concurrency cap 2 (not queue depth 300)", saw.Limit)
	}
	if saw.State != SnapshotStateGovernorSaturated {
		t.Fatalf("snap.State = %q, want %q", saw.State, SnapshotStateGovernorSaturated)
	}
}

func TestAcquireGiveUpZeroBudgetConcurrency(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxQueueWaitMS = 0
	hot := &atomic.Value{}
	hot.Store(&cfg)
	p := NewPipeline(Deps{HotCfg: hot})
	ref := cred(1, ModeConcurrency, 1)
	cf := &credForwarder{cred: ref, gov: newGovernor(ref), pipe: p}
	qr := NewQueuedRequest("g", "t", "m", context.Background(), nil)
	qr.SelectedCred = ref
	got := cf.acquireGiveUp(qr)
	if !got.IsZero() {
		t.Fatalf("concurrency zero-budget giveUp = %v, want zero time.Time", got)
	}
}

func TestAcquireGiveUpZeroBudgetRPM(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxQueueWaitMS = 0
	hot := &atomic.Value{}
	hot.Store(&cfg)
	p := NewPipeline(Deps{HotCfg: hot})
	ref := cred(1, ModeRPM, 60)
	cf := &credForwarder{cred: ref, gov: newGovernor(ref), pipe: p}
	qr := NewQueuedRequest("g", "t", "m", context.Background(), nil)
	qr.SelectedCred = ref
	before := time.Now()
	got := cf.acquireGiveUp(qr)
	if got.IsZero() || got.Before(before.Add(-time.Millisecond)) {
		t.Fatalf("rpm zero-budget giveUp = %v, want ~now", got)
	}
}

func TestWaitWithinBudgetZeroGiveUpAllowsSleep(t *testing.T) {
	wait, ok := waitWithinBudget(time.Now(), 50*time.Millisecond, time.Time{})
	if !ok || wait != 50*time.Millisecond {
		t.Fatalf("zero giveUp: wait=%v ok=%v, want 50ms true", wait, ok)
	}
}
