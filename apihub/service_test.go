package apihub

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// memStore is an in-memory implementation of Store for testing. It mimics the
// RLS behavior by keying assets on tenant_id and refusing cross-tenant reads.
type memStore struct {
	mu     sync.RWMutex
	assets map[assetKey]Asset
	edges  []edge
}

type assetKey struct {
	kind  Kind
	refID int64
}

type edge struct {
	rel    Relationship
	tenant string
}

func newMemStore() *memStore {
	return &memStore{assets: make(map[assetKey]Asset)}
}

func (m *memStore) Upsert(ctx context.Context, a Asset) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a.RegisteredAt = time.Now().UTC()
	m.assets[assetKey{a.Kind, a.RefID}] = a
	return nil
}

func (m *memStore) Get(ctx context.Context, tenantID string, k Kind, refID int64) (Asset, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.assets[assetKey{k, refID}]
	if !ok {
		return Asset{}, ErrNotFound
	}
	if a.TenantID != tenantID {
		return Asset{}, ErrNotFound // RLS: hide existence across tenants
	}
	return a, nil
}

func (m *memStore) List(ctx context.Context, f Filter) ([]Asset, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []Asset{}
	for _, a := range m.assets {
		if a.TenantID != f.TenantID {
			continue // RLS
		}
		if f.Kind != "" && a.Kind != f.Kind {
			continue
		}
		if f.Health != "" && a.HealthState != f.Health {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

func (m *memStore) Link(ctx context.Context, tenantID string, rel Relationship) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.assets[assetKey{rel.SrcKind.Kind, rel.SrcKind.RefID}]
	if !ok || s.TenantID != tenantID {
		return ErrNotFound
	}
	d, ok := m.assets[assetKey{rel.DstKind.Kind, rel.DstKind.RefID}]
	if !ok || d.TenantID != tenantID {
		return ErrNotFound
	}
	m.edges = append(m.edges, edge{rel, tenantID})
	return nil
}

func (m *memStore) Neighbors(ctx context.Context, tenantID string, k Kind, refID int64, depth int) ([]Asset, []Relationship, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if depth < 1 {
		depth = 1
	}
	seen := map[assetKey]bool{{k, refID}: true}
	queue := []assetKey{{k, refID}}
	var assets []Asset
	var rels []Relationship
	for d := 0; d < depth && len(queue) > 0; d++ {
		var next []assetKey
		for _, cur := range queue {
			for _, e := range m.edges {
				if e.tenant != tenantID {
					continue
				}
				var nb assetKey
				if e.rel.SrcKind.Kind == cur.kind && e.rel.SrcKind.RefID == cur.refID {
					nb = assetKey{e.rel.DstKind.Kind, e.rel.DstKind.RefID}
				} else if e.rel.DstKind.Kind == cur.kind && e.rel.DstKind.RefID == cur.refID {
					nb = assetKey{e.rel.SrcKind.Kind, e.rel.SrcKind.RefID}
				} else {
					continue
				}
				rels = append(rels, e.rel)
				if !seen[nb] {
					seen[nb] = true
					next = append(next, nb)
					if a, ok := m.assets[nb]; ok {
						assets = append(assets, a)
					}
				}
			}
		}
		queue = next
	}
	return assets, rels, nil
}

// testCtx returns a context carrying the given tenant id.
func testCtx(t *testing.T, tenant string) context.Context {
	t.Helper()
	return WithTenant(context.Background(), tenant)
}

// --- Test scenarios ---

// TestRegister_ThenGet verifies the core upsert + read path.
func TestRegister_ThenGet(t *testing.T) {
	svc := New(newMemStore())
	a := Asset{Kind: KindLLMEndpoint, RefID: 1, TenantID: "t1", Name: "gpt-4o", HealthState: HealthHealthy}
	if err := svc.Register(testCtx(t, "t1"), a); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := svc.Get(testCtx(t, "t1"), KindLLMEndpoint, 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "gpt-4o" {
		t.Errorf("got name %q, want gpt-4o", got.Name)
	}
}

// TestGet_NotFound verifies ErrNotFound for a missing asset.
func TestGet_NotFound(t *testing.T) {
	svc := New(newMemStore())
	_, err := svc.Get(testCtx(t, "t1"), KindLLMEndpoint, 999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestList_FilterByKind verifies kind-based filtering.
func TestList_FilterByKind(t *testing.T) {
	svc := New(newMemStore())
	_ = svc.Register(testCtx(t, "t1"), Asset{Kind: KindLLMEndpoint, RefID: 1, TenantID: "t1", Name: "a"})
	_ = svc.Register(testCtx(t, "t1"), Asset{Kind: KindMCPServer, RefID: 2, TenantID: "t1", Name: "b"})

	got, err := svc.List(testCtx(t, "t1"), Filter{TenantID: "t1", Kind: KindLLMEndpoint})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Kind != KindLLMEndpoint {
		t.Errorf("expected 1 LLM endpoint, got %v", got)
	}
}

// TestLink_ThenNeighbors verifies topology traversal.
func TestLink_ThenNeighbors(t *testing.T) {
	svc := New(newMemStore())
	ctx := testCtx(t, "t1")
	_ = svc.Register(ctx, Asset{Kind: KindLLMEndpoint, RefID: 1, TenantID: "t1", Name: "llm"})
	_ = svc.Register(ctx, Asset{Kind: KindMCPServer, RefID: 2, TenantID: "t1", Name: "mcp"})
	_ = svc.Register(ctx, Asset{Kind: KindAgent, RefID: 3, TenantID: "t1", Name: "agent"})

	_ = svc.Link(ctx, Relationship{
		SrcKind: RelationEndpoint{KindAgent, 3},
		DstKind: RelationEndpoint{KindLLMEndpoint, 1},
		Type:    RelCalls,
	})
	_ = svc.Link(ctx, Relationship{
		SrcKind: RelationEndpoint{KindAgent, 3},
		DstKind: RelationEndpoint{KindMCPServer, 2},
		Type:    RelDependsOn,
	})

	assets, rels, err := svc.Neighbors(ctx, KindAgent, 3, 1)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	if len(assets) != 2 {
		t.Errorf("expected 2 neighbors, got %d", len(assets))
	}
	if len(rels) != 2 {
		t.Errorf("expected 2 relationships, got %d", len(rels))
	}
}

// TestMultiTenant_Isolation is the CRITICAL test: tenant A must never see
// tenant B's assets. This mirrors the DB RLS policy.
func TestMultiTenant_Isolation(t *testing.T) {
	svc := New(newMemStore())
	_ = svc.Register(testCtx(t, "tA"), Asset{Kind: KindLLMEndpoint, RefID: 1, TenantID: "tA", Name: "secret-A"})

	_, err := svc.Get(testCtx(t, "tB"), KindLLMEndpoint, 1)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant Get must return ErrNotFound (RLS), got %v", err)
	}

	got, err := svc.List(testCtx(t, "tB"), Filter{TenantID: "tB"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, a := range got {
		if a.TenantID == "tA" {
			t.Fatal("RLS breach: tB saw tA's asset")
		}
	}
}

// TestLink_CrossTenant_Rejected verifies that a relationship cannot span two
// tenants even if an attacker forges the request.
func TestLink_CrossTenant_Rejected(t *testing.T) {
	svc := New(newMemStore())
	_ = svc.Register(testCtx(t, "tA"), Asset{Kind: KindLLMEndpoint, RefID: 1, TenantID: "tA", Name: "a"})
	_ = svc.Register(testCtx(t, "tB"), Asset{Kind: KindAgent, RefID: 2, TenantID: "tB", Name: "b"})

	err := svc.Link(testCtx(t, "tA"), Relationship{
		SrcKind: RelationEndpoint{KindAgent, 2},
		DstKind: RelationEndpoint{KindLLMEndpoint, 1},
		Type:    RelCalls,
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant Link must be rejected, got %v", err)
	}
}

// TestRegister_InvalidKind rejects an unknown Kind.
func TestRegister_InvalidKind(t *testing.T) {
	svc := New(newMemStore())
	err := svc.Register(testCtx(t, "t1"), Asset{Kind: Kind("bogus"), RefID: 1, TenantID: "t1", Name: "x"})
	if !errors.Is(err, ErrInvalidKind) {
		t.Fatalf("expected ErrInvalidKind, got %v", err)
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error should name the bad kind: %v", err)
	}
}

// TestRegister_MissingTenant rejects an asset without tenant_id.
func TestRegister_MissingTenant(t *testing.T) {
	svc := New(newMemStore())
	err := svc.Register(context.Background(), Asset{Kind: KindLLMEndpoint, RefID: 1, Name: "x"})
	if err == nil {
		t.Fatal("expected error for missing tenant_id, got nil")
	}
}

// TestGet_CacheHit verifies the cache short-circuits Store calls after a Get.
func TestGet_CacheHit(t *testing.T) {
	store := newMemStore()
	svc := New(store)
	ctx := testCtx(t, "t1")
	_ = svc.Register(ctx, Asset{Kind: KindLLMEndpoint, RefID: 1, TenantID: "t1", Name: "cached"})

	// First Get populates cache.
	a, err := svc.Get(ctx, KindLLMEndpoint, 1)
	if err != nil {
		t.Fatal(err)
	}
	// Mutate the store directly to simulate a stale backend; cache should win.
	store.mu.Lock()
	store.assets[assetKey{KindLLMEndpoint, 1}] = Asset{Kind: KindLLMEndpoint, RefID: 1, TenantID: "t1", Name: "CHANGED"}
	store.mu.Unlock()

	a2, err := svc.Get(ctx, KindLLMEndpoint, 1)
	if err != nil {
		t.Fatal(err)
	}
	if a2.Name != "cached" {
		t.Errorf("cache miss: expected cached name 'cached', got %q (store was mutated)", a2.Name)
	}
	_ = a
}
