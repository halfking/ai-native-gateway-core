package orchestrator

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/installer/internal/launcher/backend"
	"github.com/kaixuan/llm-gateway-go/installer/internal/launcher/store"
)

// fakeBackend records calls; configurable errors.
type fakeBackend struct {
	stageResult string
	stageErr    error
	healthErr   error
	drainErr    error
	removeErr   error
	calls       []string
}

func (f *fakeBackend) Name() string { return "fake" }
func (f *fakeBackend) Stage(ctx context.Context, r backend.Release) (string, error) {
	f.calls = append(f.calls, "stage:"+r.Version)
	return f.stageResult, f.stageErr
}
func (f *fakeBackend) Health(ctx context.Context, addr string) error {
	f.calls = append(f.calls, "health:"+addr)
	return f.healthErr
}
func (f *fakeBackend) Drain(ctx context.Context, addr string) error {
	f.calls = append(f.calls, "drain:"+addr)
	return f.drainErr
}
func (f *fakeBackend) Remove(ctx context.Context, addr string) error {
	f.calls = append(f.calls, "remove:"+addr)
	return f.removeErr
}

type fakeMigrator struct{ err error }

func (m *fakeMigrator) Migrate(ctx context.Context) error { return m.err }

type fakeSwitcher struct {
	current  string
	switched []string
}

func (s *fakeSwitcher) SwitchActive(addr string) {
	s.switched = append(s.switched, addr)
	s.current = addr
}
func (s *fakeSwitcher) ActiveAddr() string { return s.current }

// seedNotifiedPlan stores a NOTIFIED plan and returns its ID, ready for
// Prepare(planID). Tests use this instead of constructing inline since
// Prepare now operates on an existing plan in place (audit C4).
func seedNotifiedPlan(t *testing.T, st *store.Store, id, cur, target string) string {
	t.Helper()
	plan := &store.Plan{
		ID:        id,
		CreatedAt: time.Now().UTC(),
		State:     store.StateNotified,
		Current:   store.Release{Version: cur},
		Target:    store.Release{Version: target, DownloadURL: "http://x"},
		ActiveAddr: "127.0.0.1:8782",
		History:   []store.StateEvent{{State: store.StateNotified, At: time.Now().UTC()}},
	}
	if err := st.Save(plan); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPrepareHappyPath(t *testing.T) {
	st := store.New(t.TempDir())
	planID := seedNotifiedPlan(t, st, "p1", "v1.4.2", "v1.5.0")
	bk := &fakeBackend{stageResult: "127.0.0.1:8783"}

	o := New(Config{
		Store: st, Backend: bk, Migrator: &fakeMigrator{},
		ActiveSwitcher: &fakeSwitcher{current: "127.0.0.1:8782"},
		HealthRetries:  3, HealthInterval: 1 * time.Millisecond,
	})

	plan, err := o.Prepare(context.Background(), planID)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if plan.ID != planID {
		t.Fatalf("Prepare must return the SAME plan id (in-place transition), got %s want %s", plan.ID, planID)
	}
	if plan.State != store.StatePrepared {
		t.Fatalf("expected PREPARED, got %s", plan.State)
	}
	if plan.GreenAddr != "127.0.0.1:8783" {
		t.Fatalf("expected green :8783, got %s", plan.GreenAddr)
	}
	// BlueAddr should be read live from the switcher (audit I7).
	if plan.BlueAddr != "127.0.0.1:8782" {
		t.Fatalf("expected BlueAddr from live active, got %s", plan.BlueAddr)
	}
	// The original NOTIFIED plan must be the same file (not orphaned, audit C4).
	if len(o.cfg.ActiveSwitcher.(*fakeSwitcher).switched) != 0 {
		t.Fatal("Prepare should not switch active")
	}
}

func TestPrepareHealthFailCleansUp(t *testing.T) {
	st := store.New(t.TempDir())
	planID := seedNotifiedPlan(t, st, "p1", "v1.4.2", "v1.5.0")
	bk := &fakeBackend{stageResult: "127.0.0.1:8783", healthErr: errors.New("unhealthy")}
	sw := &fakeSwitcher{current: "127.0.0.1:8782"}

	o := New(Config{
		Store: st, Backend: bk, Migrator: &fakeMigrator{}, ActiveSwitcher: sw,
		HealthRetries: 2, HealthInterval: 1 * time.Millisecond,
	})

	plan, err := o.Prepare(context.Background(), planID)
	if err == nil {
		t.Fatal("expected error from Prepare (health failed)")
	}
	if plan.State != store.StateFailed {
		t.Fatalf("expected FAILED, got %s", plan.State)
	}
	foundRemove := false
	for _, c := range bk.calls {
		if c == "remove:127.0.0.1:8783" {
			foundRemove = true
		}
	}
	if !foundRemove {
		t.Fatalf("expected green Remove on health fail, calls: %v", bk.calls)
	}
	if len(sw.switched) != 0 {
		t.Fatalf("should not switch active on failure")
	}
}

func TestPrepareMigrateFailCleansUp(t *testing.T) {
	st := store.New(t.TempDir())
	planID := seedNotifiedPlan(t, st, "p1", "v1.4.2", "v1.5.0")
	bk := &fakeBackend{stageResult: "127.0.0.1:8783"}
	sw := &fakeSwitcher{current: "127.0.0.1:8782"}

	o := New(Config{
		Store: st, Backend: bk, Migrator: &fakeMigrator{err: errors.New("migration failed")},
		ActiveSwitcher: sw, HealthRetries: 3, HealthInterval: 1 * time.Millisecond,
	})

	plan, err := o.Prepare(context.Background(), planID)
	if err == nil {
		t.Fatal("expected error from Prepare (migrate failed)")
	}
	if plan.State != store.StateFailed {
		t.Fatalf("expected FAILED, got %s", plan.State)
	}
	foundRemove := false
	for _, c := range bk.calls {
		if c == "remove:127.0.0.1:8783" {
			foundRemove = true
		}
	}
	if !foundRemove {
		t.Fatalf("expected green Remove on migrate fail, calls: %v", bk.calls)
	}
}

// TestPrepareRejectsNonNotified (audit C4): Prepare must only accept NOTIFIED
// or FAILED plans, not arbitrary states. Prevents re-preparing an already
// PREPARED plan (double-Prepare would stage a second green).
func TestPrepareRejectsNonNotified(t *testing.T) {
	for _, state := range []string{store.StatePreparing, store.StatePrepared, store.StateDone} {
		t.Run(state, func(t *testing.T) {
			st := store.New(t.TempDir())
			_ = st.Save(&store.Plan{ID: "p1", State: state})
			o := New(Config{Store: st, Backend: &fakeBackend{}, Migrator: &fakeMigrator{},
				ActiveSwitcher: &fakeSwitcher{}})
			_, err := o.Prepare(context.Background(), "p1")
			if err == nil {
				t.Fatalf("expected Prepare of %s plan to fail", state)
			}
		})
	}
}

func TestApplySwitchesAndDrains(t *testing.T) {
	st := store.New(t.TempDir())
	_ = st.Save(&store.Plan{
		ID: "p1", State: store.StatePrepared,
		Current:    store.Release{Version: "v1.4.2"},
		Target:     store.Release{Version: "v1.5.0"},
		BlueAddr:   "127.0.0.1:8782",
		GreenAddr:  "127.0.0.1:8783",
		ActiveAddr: "127.0.0.1:8782",
	})

	bk := &fakeBackend{}
	sw := &fakeSwitcher{current: "127.0.0.1:8782"}
	o := New(Config{
		Store: st, Backend: bk, Migrator: &fakeMigrator{}, ActiveSwitcher: sw,
		RetainDuration: 1 * time.Hour,
	})

	if err := o.Apply(context.Background(), "p1"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, _ := st.Load("p1")
	if got.State != store.StateDone {
		t.Fatalf("expected DONE, got %s", got.State)
	}
	if len(sw.switched) != 1 || sw.switched[0] != "127.0.0.1:8783" {
		t.Fatalf("expected switch to green, got %v", sw.switched)
	}
	foundDrain := false
	for _, c := range bk.calls {
		if c == "drain:127.0.0.1:8782" {
			foundDrain = true
		}
	}
	if !foundDrain {
		t.Fatalf("expected blue drain, calls: %v", bk.calls)
	}
}

func TestApplyRejectsNonPrepared(t *testing.T) {
	st := store.New(t.TempDir())
	_ = st.Save(&store.Plan{ID: "p1", State: store.StateNotified, BlueAddr: "x", GreenAddr: "y"})
	o := New(Config{
		Store: st, Backend: &fakeBackend{}, Migrator: &fakeMigrator{},
		ActiveSwitcher: &fakeSwitcher{},
	})
	if err := o.Apply(context.Background(), "p1"); err == nil {
		t.Fatal("expected error applying non-PREPARED plan")
	}
}

// TestRollbackFailedCleansGreen (audit C10): Rollback of a FAILED plan
// (Prepare failed after staging green) removes green; active is still on
// blue so this is safe. NOTE: Rollback of DONE is now rejected (blue is
// drained/removed → would 502) — see TestRollbackRejectsDone.
func TestRollbackFailedCleansGreen(t *testing.T) {
	st := store.New(t.TempDir())
	_ = st.Save(&store.Plan{
		ID: "p1", State: store.StateFailed,
		BlueAddr:   "127.0.0.1:8782",
		GreenAddr:  "127.0.0.1:8783",
		ActiveAddr: "127.0.0.1:8782", // still on blue (Prepare failed)
	})
	bk := &fakeBackend{}
	sw := &fakeSwitcher{current: "127.0.0.1:8782"}
	o := New(Config{Store: st, Backend: bk, Migrator: &fakeMigrator{}, ActiveSwitcher: sw})

	if err := o.Rollback(context.Background(), "p1"); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	got, _ := st.Load("p1")
	if got.State != store.StateRolledBack {
		t.Fatalf("expected ROLLED_BACK, got %s", got.State)
	}
	foundRemove := false
	for _, c := range bk.calls {
		if c == "remove:127.0.0.1:8783" {
			foundRemove = true
		}
	}
	if !foundRemove {
		t.Fatalf("expected green Remove after rollback, calls: %v", bk.calls)
	}
}

// TestApplyPersistsActive (C1): Apply must write active.json so daemon
// restart restores the proxy target instead of reverting to drained blue.
func TestApplyPersistsActive(t *testing.T) {
	st := store.New(t.TempDir())
	_ = st.Save(&store.Plan{
		ID: "p1", State: store.StatePrepared,
		Current:    store.Release{Version: "v1.4.2"},
		Target:     store.Release{Version: "v1.5.0"},
		BlueAddr:   "127.0.0.1:8782",
		GreenAddr:  "127.0.0.1:8783",
		ActiveAddr: "127.0.0.1:8782",
	})
	o := New(Config{
		Store: st, Backend: &fakeBackend{}, Migrator: &fakeMigrator{},
		ActiveSwitcher: &fakeSwitcher{current: "127.0.0.1:8782"},
		RetainDuration: 1 * time.Hour,
	})
	if err := o.Apply(context.Background(), "p1"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	ap, err := st.LoadActive()
	if err != nil {
		t.Fatal(err)
	}
	if ap == nil || ap.Addr != "127.0.0.1:8783" || ap.Version != "v1.5.0" {
		t.Fatalf("active.json not persisted correctly: %+v", ap)
	}
}

// TestRollbackRejectsNonTerminal (I4): Rollback must reject plans that
// have nothing to cancel/revert (NOTIFIED, PREPARING). PREPARED is now
// accepted (audit I6: cancel a staged green without applying).
// DONE is now rejected too (audit C10: blue drained/removed → 502).
func TestRollbackRejectsNonTerminal(t *testing.T) {
	for _, state := range []string{store.StateNotified, store.StatePreparing, store.StateDone} {
		t.Run(state, func(t *testing.T) {
			st := store.New(t.TempDir())
			_ = st.Save(&store.Plan{
				ID: "p1", State: state,
				BlueAddr: "127.0.0.1:8782", GreenAddr: "127.0.0.1:8783",
			})
			o := New(Config{
				Store: st, Backend: &fakeBackend{}, Migrator: &fakeMigrator{},
				ActiveSwitcher: &fakeSwitcher{},
			})
			err := o.Rollback(context.Background(), "p1")
			if err == nil {
				t.Fatalf("expected rollback of %s plan to fail", state)
			}
		})
	}
}

// TestRollbackCancelsPrepared (audit I6): a PREPARED plan (green staged
// but not yet applied) can be rolled back to clean up green without ever
// switching traffic. Active should stay on blue.
func TestRollbackCancelsPrepared(t *testing.T) {
	st := store.New(t.TempDir())
	_ = st.Save(&store.Plan{
		ID: "p1", State: store.StatePrepared,
		BlueAddr:   "127.0.0.1:8782",
		GreenAddr:  "127.0.0.1:8783",
		ActiveAddr: "127.0.0.1:8782", // still on blue, never applied
	})
	bk := &fakeBackend{}
	sw := &fakeSwitcher{current: "127.0.0.1:8782"}
	o := New(Config{Store: st, Backend: bk, Migrator: &fakeMigrator{}, ActiveSwitcher: sw})

	if err := o.Rollback(context.Background(), "p1"); err != nil {
		t.Fatalf("Rollback of PREPARED: %v", err)
	}
	got, _ := st.Load("p1")
	if got.State != store.StateRolledBack {
		t.Fatalf("expected ROLLED_BACK, got %s", got.State)
	}
	// Active unchanged (still blue).
	if sw.current != "127.0.0.1:8782" {
		t.Fatalf("active changed to %s; PREPARED rollback shouldn't switch", sw.current)
	}
	foundRemove := false
	for _, c := range bk.calls {
		if c == "remove:127.0.0.1:8783" {
			foundRemove = true
		}
	}
	if !foundRemove {
		t.Fatalf("expected green Remove on PREPARED cancel, calls: %v", bk.calls)
	}
}

// TestPrepareSerializesConcurrent (I2): two concurrent Prepare calls must
// not both be inside Stage at the same time (would race on green port/
// container). opMu serializes them; the peak concurrent Stage count must
// be 1.
func TestPrepareSerializesConcurrent(t *testing.T) {
	st := store.New(t.TempDir())
	seedNotifiedPlan(t, st, "p1", "v1.4.2", "v1.5.0")
	seedNotifiedPlan(t, st, "p2", "v1.4.2", "v1.5.0")
	bk := &concurrencyBackend{stageResult: "127.0.0.1:8783"}
	o := New(Config{
		Store: st, Backend: bk, Migrator: &fakeMigrator{},
		ActiveSwitcher: &fakeSwitcher{current: "127.0.0.1:8782"},
		HealthRetries:  1, HealthInterval: 1 * time.Millisecond,
	})

	done := make(chan error, 2)
	for _, pid := range []string{"p1", "p2"} {
		go func(id string) {
			_, err := o.Prepare(context.Background(), id)
			done <- err
		}(pid)
	}
	<-done
	<-done

	if bk.peakInFlight() > 1 {
		t.Fatalf("concurrent Stage calls detected: peak=%d (opMu not serializing)", bk.peakInFlight())
	}
	if bk.stageCount() != 2 {
		t.Fatalf("expected 2 stage calls, got %d", bk.stageCount())
	}
}

// concurrencyBackend tracks peak concurrent Stage calls to detect races.
type concurrencyBackend struct {
	stageResult string
	inFlight    int32
	peak        int32
	count       int32
}

func (b *concurrencyBackend) Name() string { return "concurrency-test" }
func (b *concurrencyBackend) Stage(ctx context.Context, r backend.Release) (string, error) {
	cur := atomic.AddInt32(&b.inFlight, 1)
	for {
		p := atomic.LoadInt32(&b.peak)
		if cur <= p || atomic.CompareAndSwapInt32(&b.peak, p, cur) {
			break
		}
	}
	atomic.AddInt32(&b.count, 1)
	// Hold the slot briefly to maximize the chance of overlap if opMu
	// weren't there.
	time.Sleep(5 * time.Millisecond)
	atomic.AddInt32(&b.inFlight, -1)
	return b.stageResult, nil
}
func (b *concurrencyBackend) Health(ctx context.Context, addr string) error { return nil }
func (b *concurrencyBackend) Drain(ctx context.Context, addr string) error  { return nil }
func (b *concurrencyBackend) Remove(ctx context.Context, addr string) error { return nil }
func (b *concurrencyBackend) peakInFlight() int32 { return atomic.LoadInt32(&b.peak) }
func (b *concurrencyBackend) stageCount() int32   { return atomic.LoadInt32(&b.count) }
