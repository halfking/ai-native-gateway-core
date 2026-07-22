package e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/installer/internal/launcher/backend"
	"github.com/kaixuan/llm-gateway-go/installer/internal/launcher/orchestrator"
	"github.com/kaixuan/llm-gateway-go/installer/internal/launcher/proxy"
	"github.com/kaixuan/llm-gateway-go/installer/internal/launcher/store"
)

// restartBackend is a minimal backend for the restart-recovery test.
// Stage returns the green addr; Drain/Remove/Health are no-ops that succeed.
type restartBackend struct{ greenAddr string }

func (b *restartBackend) Name() string                                                        { return "restart-test" }
func (b *restartBackend) Stage(ctx context.Context, r backend.Release) (string, error)        { return b.greenAddr, nil }
func (b *restartBackend) Health(ctx context.Context, addr string) error                       { return nil }
func (b *restartBackend) Drain(ctx context.Context, addr string) error                        { return nil }
func (b *restartBackend) Remove(ctx context.Context, addr string) error                       { return nil }

type noopMigrator struct{}

func (noopMigrator) Migrate(ctx context.Context) error { return nil }

// TestApplySurvivesDaemonRestart (C1 regression): after a successful Apply
// that switches active to green, a simulated daemon restart (new
// orchestrator instance pointing at the same store) must restore the
// active address from active.json rather than reverting to the now-drained
// blue. Without the C1 fix (SaveActive in Apply), active.json is missing
// and the proxy would point at a stopped blue → 502s.
func TestApplySurvivesDaemonRestart(t *testing.T) {
	dataDir := t.TempDir()
	st := store.New(dataDir)

	// Seed a PREPARED plan (simulating Prepare already ran).
	plan := &store.Plan{
		ID:         "p1",
		CreatedAt:  time.Now().UTC(),
		State:      store.StatePrepared,
		Current:    store.Release{Version: "v1.4.2"},
		Target:     store.Release{Version: "v1.5.0"},
		BlueAddr:   "127.0.0.1:8782",
		GreenAddr:  "127.0.0.1:8783",
		ActiveAddr: "127.0.0.1:8782",
	}
	if err := st.Save(plan); err != nil {
		t.Fatal(err)
	}

	// First "daemon" instance: proxy pointing at blue, apply.
	pr1 := proxy.New("127.0.0.1:8782")
	o1 := orchestrator.New(orchestrator.Config{
		Store:          st,
		Backend:        &restartBackend{greenAddr: "127.0.0.1:8783"},
		Migrator:       noopMigrator{},
		ActiveSwitcher: &proxySwitcher{p: pr1},
		RetainDuration: 1 * time.Hour, // don't fire retained-remove during test
	})
	if err := o1.Apply(context.Background(), "p1"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if pr1.ActiveAddr() != "127.0.0.1:8783" {
		t.Fatalf("after Apply, proxy should point at green, got %s", pr1.ActiveAddr())
	}
	o1.Stop()

	// Simulate daemon restart: brand-new proxy + orchestrator reading the
	// same store. The daemon's main() calls store.LoadActive() to restore.
	restored, err := st.LoadActive()
	if err != nil {
		t.Fatalf("LoadActive: %v (C1 regression — active.json not persisted)", err)
	}
	if restored == nil {
		t.Fatal("LoadActive returned nil (C1 regression — Apply did not persist active.json)")
	}
	if restored.Addr != "127.0.0.1:8783" {
		t.Fatalf("restored active = %s, want 127.0.0.1:8783 (C1 regression)", restored.Addr)
	}

	// New proxy instance restored from store — this is what daemon main() does.
	pr2 := proxy.New(restored.Addr)
	if pr2.ActiveAddr() != "127.0.0.1:8783" {
		t.Fatalf("restored proxy points at %s, not green — traffic would 502", pr2.ActiveAddr())
	}

	// Active.json must be on disk (not just in-memory).
	if _, err := os.ReadFile(filepath.Join(dataDir, "active.json")); err != nil {
		t.Fatalf("active.json missing on disk: %v", err)
	}
}

// proxySwitcher adapts *proxy.Proxy to orchestrator.ActiveSwitcher.
// (Defined here too so this test file is self-contained.)
type proxySwitcher struct{ p *proxy.Proxy }

func (s *proxySwitcher) SwitchActive(addr string) { s.p.SwitchActive(addr) }
func (s *proxySwitcher) ActiveAddr() string       { return s.p.ActiveAddr() }
