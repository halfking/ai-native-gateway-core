package orchestrator

import (
	"context"
	"errors"
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

func TestPrepareHappyPath(t *testing.T) {
	st := store.New(t.TempDir())
	bk := &fakeBackend{stageResult: "127.0.0.1:8783"}
	mig := &fakeMigrator{}
	sw := &fakeSwitcher{current: "127.0.0.1:8782"}

	o := New(Config{
		Store:          st,
		Backend:        bk,
		Migrator:       mig,
		ActiveSwitcher: sw,
		CurrentVersion: "v1.4.2",
		CurrentAddr:    "127.0.0.1:8782",
		HealthRetries:  3,
		HealthInterval: 1 * time.Millisecond,
	})

	plan, err := o.Prepare(context.Background(),
		store.Release{Version: "v1.4.2"},
		store.Release{Version: "v1.5.0", DownloadURL: "http://x"},
	)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if plan.State != store.StatePrepared {
		t.Fatalf("expected PREPARED, got %s", plan.State)
	}
	if plan.GreenAddr != "127.0.0.1:8783" {
		t.Fatalf("expected green :8783, got %s", plan.GreenAddr)
	}
	if len(sw.switched) != 0 {
		t.Fatalf("Prepare should not switch active, got %v", sw.switched)
	}
}

func TestPrepareHealthFailCleansUp(t *testing.T) {
	st := store.New(t.TempDir())
	bk := &fakeBackend{stageResult: "127.0.0.1:8783", healthErr: errors.New("unhealthy")}
	mig := &fakeMigrator{}
	sw := &fakeSwitcher{current: "127.0.0.1:8782"}

	o := New(Config{
		Store: st, Backend: bk, Migrator: mig, ActiveSwitcher: sw,
		CurrentVersion: "v1.4.2", CurrentAddr: "127.0.0.1:8782",
		HealthRetries: 2, HealthInterval: 1 * time.Millisecond,
	})

	plan, err := o.Prepare(context.Background(),
		store.Release{Version: "v1.4.2"}, store.Release{Version: "v1.5.0"},
	)
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
	bk := &fakeBackend{stageResult: "127.0.0.1:8783"}
	mig := &fakeMigrator{err: errors.New("migration failed")}
	sw := &fakeSwitcher{current: "127.0.0.1:8782"}

	o := New(Config{
		Store: st, Backend: bk, Migrator: mig, ActiveSwitcher: sw,
		CurrentVersion: "v1.4.2", CurrentAddr: "127.0.0.1:8782",
		HealthRetries: 3, HealthInterval: 1 * time.Millisecond,
	})

	plan, err := o.Prepare(context.Background(),
		store.Release{Version: "v1.4.2"}, store.Release{Version: "v1.5.0"},
	)
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
	mig := &fakeMigrator{}
	sw := &fakeSwitcher{current: "127.0.0.1:8782"}
	o := New(Config{
		Store: st, Backend: bk, Migrator: mig, ActiveSwitcher: sw,
		CurrentAddr: "127.0.0.1:8782", RetainDuration: 1 * time.Hour,
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

func TestRollbackSwitchesBack(t *testing.T) {
	st := store.New(t.TempDir())
	_ = st.Save(&store.Plan{
		ID: "p1", State: store.StateDone,
		BlueAddr:   "127.0.0.1:8782",
		GreenAddr:  "127.0.0.1:8783",
		ActiveAddr: "127.0.0.1:8783",
	})
	bk := &fakeBackend{}
	sw := &fakeSwitcher{current: "127.0.0.1:8783"}
	o := New(Config{Store: st, Backend: bk, Migrator: &fakeMigrator{}, ActiveSwitcher: sw})

	if err := o.Rollback(context.Background(), "p1"); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	got, _ := st.Load("p1")
	if got.State != store.StateRolledBack {
		t.Fatalf("expected ROLLED_BACK, got %s", got.State)
	}
	if sw.current != "127.0.0.1:8782" {
		t.Fatalf("expected switch back to blue, got %s", sw.current)
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