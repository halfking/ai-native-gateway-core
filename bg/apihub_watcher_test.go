package bg

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/apihub"
)

// fakeSource is a controllable AssetSyncSource for testing. It records calls
// and returns whatever the test configures via the function fields.
type fakeSource struct {
	mu       sync.Mutex
	llmCalls int
	mcpCalls int
	llmFn    func(ctx context.Context) ([]apihub.Asset, error)
	mcpFn    func(ctx context.Context) ([]apihub.Asset, error)
}

func (f *fakeSource) LLMEndpoints(ctx context.Context) ([]apihub.Asset, error) {
	f.mu.Lock()
	f.llmCalls++
	f.mu.Unlock()
	if f.llmFn == nil {
		return nil, nil
	}
	return f.llmFn(ctx)
}

func (f *fakeSource) MCPServers(ctx context.Context) ([]apihub.Asset, error) {
	f.mu.Lock()
	f.mcpCalls++
	f.mu.Unlock()
	if f.mcpFn == nil {
		return nil, nil
	}
	return f.mcpFn(ctx)
}

// newWatcher constructs an AssetWatcher backed by a fresh in-memory apihub
// Service and a fake source. The watcher uses a short tick in tests.
func newWatcher(t *testing.T) (*AssetWatcher, *apihub.Service, *fakeSource) {
	t.Helper()
	// We need an apihub.Service with a Store. The memStore is unexported, but
	// apihub.New takes a Store interface. We build a tiny test store here.
	store := &testStore{}
	hub := apihub.New(store)
	src := &fakeSource{}
	w := NewAssetWatcher(hub, src).WithInterval(50 * time.Millisecond)
	return w, hub, src
}

// testStore is a minimal apihub.Store for bg tests. It reuses the same
// in-memory logic as apihub's internal memStore (duplicated here to avoid
// an import cycle on test helpers across packages).
type testStore struct {
	mu     sync.RWMutex
	assets map[testStoreKey]apihub.Asset
}

type testStoreKey struct {
	kind  apihub.Kind
	refID int64
}

func (s *testStore) Upsert(ctx context.Context, a apihub.Asset) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.assets == nil {
		s.assets = make(map[testStoreKey]apihub.Asset)
	}
	s.assets[testStoreKey{a.Kind, a.RefID}] = a
	return nil
}

func (s *testStore) Get(ctx context.Context, tenantID string, k apihub.Kind, refID int64) (apihub.Asset, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.assets[testStoreKey{k, refID}]
	if !ok || a.TenantID != tenantID {
		return apihub.Asset{}, apihub.ErrNotFound
	}
	return a, nil
}

func (s *testStore) List(ctx context.Context, f apihub.Filter) ([]apihub.Asset, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []apihub.Asset
	for _, a := range s.assets {
		if a.TenantID != f.TenantID {
			continue
		}
		if f.Kind != "" && a.Kind != f.Kind {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

func (s *testStore) Link(ctx context.Context, tenantID string, rel apihub.Relationship) error {
	return nil // not exercised by watcher
}

func (s *testStore) Neighbors(ctx context.Context, tenantID string, k apihub.Kind, refID int64, depth int) ([]apihub.Asset, []apihub.Relationship, error) {
	return nil, nil, nil
}

// --- tests ---

// TestSyncOnce_HappyPath verifies both source types are read and registered.
func TestSyncOnce_HappyPath(t *testing.T) {
	w, hub, src := newWatcher(t)
	src.llmFn = func(ctx context.Context) ([]apihub.Asset, error) {
		return []apihub.Asset{
			{RefID: 1, TenantID: "t1", Name: "gpt-4o"},
			{RefID: 2, TenantID: "t1", Name: "claude"},
		}, nil
	}
	src.mcpFn = func(ctx context.Context) ([]apihub.Asset, error) {
		return []apihub.Asset{{RefID: 10, TenantID: "t1", Name: "brandmind"}}, nil
	}

	ctx := apihub.WithTenant(context.Background(), "t1")
	llm, mcp, err := w.SyncOnce(ctx)
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if llm != 2 {
		t.Errorf("llm_added = %d, want 2", llm)
	}
	if mcp != 1 {
		t.Errorf("mcp_added = %d, want 1", mcp)
	}
	// Verify assets landed in the hub.
	got, _ := hub.List(ctx, apihub.Filter{TenantID: "t1"})
	if len(got) != 3 {
		t.Errorf("hub has %d assets, want 3", len(got))
	}
}

// TestSyncOnce_SourceErrorDoesNotBlockOther verifies that a failure reading
// one source type still lets the other sync (partial progress > none).
func TestSyncOnce_SourceErrorDoesNotBlockOther(t *testing.T) {
	w, hub, src := newWatcher(t)
	src.llmFn = func(ctx context.Context) ([]apihub.Asset, error) {
		return nil, errors.New("db down")
	}
	src.mcpFn = func(ctx context.Context) ([]apihub.Asset, error) {
		return []apihub.Asset{{RefID: 10, TenantID: "t1", Name: "brandmind"}}, nil
	}

	ctx := apihub.WithTenant(context.Background(), "t1")
	llm, mcp, err := w.SyncOnce(ctx)
	if err == nil {
		t.Fatal("expected error from LLMEndpoints, got nil")
	}
	if llm != 0 {
		t.Errorf("llm_added = %d, want 0 (source failed)", llm)
	}
	if mcp != 1 {
		t.Errorf("mcp_added = %d, want 1 (source ok)", mcp)
	}
	got, _ := hub.List(ctx, apihub.Filter{TenantID: "t1"})
	if len(got) != 1 {
		t.Errorf("hub should have the 1 MCP asset despite LLM failure, got %d", len(got))
	}
}

// TestSyncOnce_SetsKind verifies the watcher stamps the correct Kind on each
// asset (the source returns raw assets; the watcher owns the kind mapping).
func TestSyncOnce_SetsKind(t *testing.T) {
	w, hub, src := newWatcher(t)
	src.llmFn = func(ctx context.Context) ([]apihub.Asset, error) {
		return []apihub.Asset{{RefID: 1, TenantID: "t1", Name: "x"}}, nil
	}
	src.mcpFn = func(ctx context.Context) ([]apihub.Asset, error) {
		return []apihub.Asset{{RefID: 2, TenantID: "t1", Name: "y"}}, nil
	}

	ctx := apihub.WithTenant(context.Background(), "t1")
	_, _, _ = w.SyncOnce(ctx)
	got, _ := hub.List(ctx, apihub.Filter{TenantID: "t1"})
	kinds := map[apihub.Kind]bool{}
	for _, a := range got {
		kinds[a.Kind] = true
	}
	if !kinds[apihub.KindLLMEndpoint] {
		t.Error("expected an llm_endpoint asset")
	}
	if !kinds[apihub.KindMCPServer] {
		t.Error("expected an mcp_server asset")
	}
}

// TestWatcher_StartStop verifies the lifecycle: Start spawns a goroutine that
// ticks, Stop waits for it to exit (no goroutine leak).
func TestWatcher_StartStop(t *testing.T) {
	w, _, src := newWatcher(t)
	src.llmFn = func(ctx context.Context) ([]apihub.Asset, error) {
		return nil, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	// Let at least one tick fire (50ms interval).
	time.Sleep(120 * time.Millisecond)

	w.Stop()

	src.mu.Lock()
	defer src.mu.Unlock()
	if src.llmCalls < 1 {
		t.Errorf("expected ≥1 LLM sync call, got %d (initial + ticks)", src.llmCalls)
	}
}

// TestSyncOnce_NilHub is a nil-safety guard: a watcher constructed without a
// hub (e.g. feature disabled) must not panic.
func TestSyncOnce_NilHub(t *testing.T) {
	w := &AssetWatcher{stop: make(chan struct{}), done: make(chan struct{})}
	llm, mcp, err := w.SyncOnce(context.Background())
	if err != nil {
		t.Errorf("nil hub should not error, got %v", err)
	}
	if llm != 0 || mcp != 0 {
		t.Errorf("nil hub should register nothing, got llm=%d mcp=%d", llm, mcp)
	}
}
