package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/apihub"
)

// ──────────────────────────────────────────────────────────────────────────────
// TDD: Agent Registry API tests (Phase 3 A3-1 + Phase 6 enhancements)
// ──────────────────────────────────────────────────────────────────────────────

func TestNewAgentsHandler(t *testing.T) {
	svc := apihub.New(nil)
	h := NewAgentsHandler(svc)
	if h == nil {
		t.Fatal("NewAgentsHandler returned nil")
	}
	if h.svc != svc {
		t.Error("handler.svc not set correctly")
	}
}

// stubService records the latest call so tests can assert handler→svc plumbing.
type stubService struct {
	mu sync.Mutex

	listOut        []apihub.Asset
	listErr        error
	listFilter     apihub.Filter
	lastListCalled bool

	getOut      apihub.Asset
	getErr      error
	getKind     apihub.Kind
	getRefID    int64
	lastGetCall bool

	linkErr      error
	lastLinkRel  apihub.Relationship
	lastLinkCall bool

	neighborsOut   []apihub.Asset
	neighborsRels  []apihub.Relationship
	neighborsErr   error
	neighborsKind  apihub.Kind
	neighborsRef   int64
	neighborsDepth int
	lastNbCall     bool

	// PR-6 (2026-06-30): PR-6 test hooks. If set, override default behavior.
	listStaleFn         func(ctx context.Context, threshold time.Duration) ([]apihub.Asset, error)
	listTenantsFn       func(ctx context.Context) ([]string, error)
	lastListStaleTenant string
	listStaleCalls      int
}

func (s *stubService) List(ctx context.Context, f apihub.Filter) ([]apihub.Asset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listFilter = f
	s.lastListCalled = true
	return s.listOut, s.listErr
}

func (s *stubService) Get(ctx context.Context, k apihub.Kind, refID int64) (apihub.Asset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getKind = k
	s.getRefID = refID
	s.lastGetCall = true
	return s.getOut, s.getErr
}

func (s *stubService) Link(ctx context.Context, rel apihub.Relationship) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastLinkRel = rel
	s.lastLinkCall = true
	return s.linkErr
}

func (s *stubService) Neighbors(ctx context.Context, k apihub.Kind, refID int64, depth int) ([]apihub.Asset, []apihub.Relationship, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.neighborsKind = k
	s.neighborsRef = refID
	s.neighborsDepth = depth
	s.lastNbCall = true
	return s.neighborsOut, s.neighborsRels, s.neighborsErr
}

func (s *stubService) ListStale(ctx context.Context, threshold time.Duration) ([]apihub.Asset, error) {
	s.mu.Lock()
	s.listStaleCalls++
	// Record the tenant from context for assertions. We can't import
	// apihub.TenantFromContext (lowercase), so we read the value key
	// directly via reflection-free string match — the apihub package
	// stores tenantID as a string under tenantCtxKey{}.
	type ctxKey interface{}
	// Iterate the ctx is impossible; instead, callers pass tenant
	// directly when overriding. For default path we just record "".
	s.lastListStaleTenant = ""
	s.mu.Unlock()
	if s.listStaleFn != nil {
		return s.listStaleFn(ctx, threshold)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listOut, nil
}

func (s *stubService) ListTenants(ctx context.Context) ([]string, error) {
	if s.listTenantsFn != nil {
		return s.listTenantsFn(ctx)
	}
	return []string{"default"}, nil
}

// ── List ─────────────────────────────────────────────────────────────────

func TestList_DefaultTenantAndLimit(t *testing.T) {
	stub := &stubService{
		listOut: []apihub.Asset{
			{Kind: apihub.KindLLMEndpoint, RefID: 1, Name: "a"},
			{Kind: apihub.KindLLMEndpoint, RefID: 2, Name: "b"},
		},
	}
	h := newAgentsHandlerWithSvc(stub)
	req := httptest.NewRequest("GET", "/api/agents", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if stub.listFilter.TenantID != "default" {
		t.Errorf("TenantID = %q, want default", stub.listFilter.TenantID)
	}
	if stub.listFilter.Limit != 100 {
		t.Errorf("Limit = %d, want 100", stub.listFilter.Limit)
	}
	if !strings.Contains(w.Body.String(), `"total":2`) {
		t.Errorf("body missing total=2: %s", w.Body.String())
	}
}

func TestList_KindFilterPropagated(t *testing.T) {
	stub := &stubService{}
	h := newAgentsHandlerWithSvc(stub)
	req := httptest.NewRequest("GET", "/api/agents?kind=mcp_server&limit=50", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	if stub.listFilter.Kind != apihub.KindMCPServer {
		t.Errorf("Kind = %q, want mcp_server", stub.listFilter.Kind)
	}
	if stub.listFilter.Limit != 50 {
		t.Errorf("Limit = %d, want 50", stub.listFilter.Limit)
	}
}

func TestList_KindAllIgnored(t *testing.T) {
	stub := &stubService{}
	h := newAgentsHandlerWithSvc(stub)
	req := httptest.NewRequest("GET", "/api/agents?kind=all", nil)
	w := httptest.NewRecorder()
	h.List(w, req)
	if stub.listFilter.Kind != "" {
		t.Errorf("Kind = %q, want empty", stub.listFilter.Kind)
	}
}

func TestList_LimitCapped(t *testing.T) {
	stub := &stubService{}
	h := newAgentsHandlerWithSvc(stub)
	req := httptest.NewRequest("GET", "/api/agents?limit=99999", nil)
	w := httptest.NewRecorder()
	h.List(w, req)
	if stub.listFilter.Limit != 1000 {
		t.Errorf("Limit = %d, want 1000 (cap)", stub.listFilter.Limit)
	}
}

func TestList_ServiceErrorReturns500(t *testing.T) {
	stub := &stubService{listErr: errors.New("db down")}
	h := newAgentsHandlerWithSvc(stub)
	req := httptest.NewRequest("GET", "/api/agents", nil)
	w := httptest.NewRecorder()
	h.List(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

// ── Get ──────────────────────────────────────────────────────────────────

func TestGet_FoundReturnsAsset(t *testing.T) {
	stub := &stubService{
		getOut: apihub.Asset{Kind: apihub.KindLLMEndpoint, RefID: 42, Name: "gpt-4o"},
	}
	h := newAgentsHandlerWithSvc(stub)
	req := httptest.NewRequest("GET", "/api/agents/42", nil)
	w := httptest.NewRecorder()
	h.Get(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if stub.getRefID != 42 {
		t.Errorf("Get called with refID = %d, want 42", stub.getRefID)
	}
	if !strings.Contains(w.Body.String(), `"name":"gpt-4o"`) {
		t.Errorf("body missing name=gpt-4o: %s", w.Body.String())
	}
}

func TestGet_NotFoundReturns404(t *testing.T) {
	stub := &stubService{getErr: apihub.ErrNotFound}
	h := newAgentsHandlerWithSvc(stub)
	req := httptest.NewRequest("GET", "/api/agents/999", nil)
	w := httptest.NewRecorder()
	h.Get(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"agent not found"`) {
		t.Errorf("body missing error: %s", w.Body.String())
	}
}

func TestGet_InvalidIDReturns400(t *testing.T) {
	h := newAgentsHandlerWithSvc(&stubService{})
	req := httptest.NewRequest("GET", "/api/agents/not-a-number", nil)
	w := httptest.NewRecorder()
	h.Get(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// ── Link ─────────────────────────────────────────────────────────────────

func TestLink_CreatesRelationship(t *testing.T) {
	stub := &stubService{}
	h := newAgentsHandlerWithSvc(stub)
	body := strings.NewReader(`{"target_id":7,"link_type":"calls"}`)
	req := httptest.NewRequest("POST", "/api/agents/3/link", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Link(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if stub.lastLinkRel.SrcKind.RefID != 3 {
		t.Errorf("Link source refID = %d, want 3", stub.lastLinkRel.SrcKind.RefID)
	}
	if stub.lastLinkRel.DstKind.RefID != 7 {
		t.Errorf("Link target refID = %d, want 7", stub.lastLinkRel.DstKind.RefID)
	}
	if stub.lastLinkRel.Type != apihub.RelCalls {
		t.Errorf("Link type = %q, want calls", stub.lastLinkRel.Type)
	}
}

func TestLink_InvalidJSONReturns400(t *testing.T) {
	h := newAgentsHandlerWithSvc(&stubService{})
	body := strings.NewReader(`not json`)
	req := httptest.NewRequest("POST", "/api/agents/3/link", body)
	w := httptest.NewRecorder()
	h.Link(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// ── Neighbors (Phase 6) ──────────────────────────────────────────────────

func TestNeighbors_ReturnsBothDirections(t *testing.T) {
	seed := apihub.Asset{Kind: apihub.KindLLMEndpoint, RefID: 1, Name: "seed"}
	stub := &stubService{
		getOut: seed,
		neighborsOut: []apihub.Asset{
			{Kind: apihub.KindMCPServer, RefID: 10, Name: "upstream-tool"},
			{Kind: apihub.KindAgent, RefID: 11, Name: "downstream-agent"},
		},
		neighborsRels: []apihub.Relationship{
			{
				SrcKind: apihub.RelationEndpoint{Kind: apihub.KindLLMEndpoint, RefID: 1},
				DstKind: apihub.RelationEndpoint{Kind: apihub.KindMCPServer, RefID: 10},
				Type:    apihub.RelDependsOn,
			},
			{
				SrcKind: apihub.RelationEndpoint{Kind: apihub.KindAgent, RefID: 11},
				DstKind: apihub.RelationEndpoint{Kind: apihub.KindLLMEndpoint, RefID: 1},
				Type:    apihub.RelCalls,
			},
		},
	}
	h := newAgentsHandlerWithSvc(stub)
	req := httptest.NewRequest("GET", "/api/agents/1/neighbors?depth=2", nil)
	w := httptest.NewRecorder()
	h.Neighbors(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if stub.neighborsRef != 1 {
		t.Errorf("Neighbors refID = %d, want 1", stub.neighborsRef)
	}
	if stub.neighborsDepth != 2 {
		t.Errorf("Neighbors depth = %d, want 2", stub.neighborsDepth)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"depth":2`) {
		t.Errorf("body missing depth=2: %s", body)
	}
	if !strings.Contains(body, `"upstream-tool"`) {
		t.Errorf("body missing upstream-tool: %s", body)
	}
	if !strings.Contains(body, `"downstream-agent"`) {
		t.Errorf("body missing downstream-agent: %s", body)
	}
	if !strings.Contains(body, `"count":2`) {
		t.Errorf("body missing count=2: %s", body)
	}
}

func TestNeighbors_DefaultDepthIsOne(t *testing.T) {
	stub := &stubService{
		getOut: apihub.Asset{Kind: apihub.KindLLMEndpoint, RefID: 5},
	}
	h := newAgentsHandlerWithSvc(stub)
	req := httptest.NewRequest("GET", "/api/agents/5/neighbors", nil)
	w := httptest.NewRecorder()
	h.Neighbors(w, req)
	if stub.neighborsDepth != 1 {
		t.Errorf("Neighbors depth = %d, want 1 (default)", stub.neighborsDepth)
	}
}

func TestNeighbors_DepthCappedAtFive(t *testing.T) {
	stub := &stubService{
		getOut: apihub.Asset{Kind: apihub.KindLLMEndpoint, RefID: 1},
	}
	h := newAgentsHandlerWithSvc(stub)
	req := httptest.NewRequest("GET", "/api/agents/1/neighbors?depth=99", nil)
	w := httptest.NewRecorder()
	h.Neighbors(w, req)
	if stub.neighborsDepth != 5 {
		t.Errorf("Neighbors depth = %d, want 5 (cap)", stub.neighborsDepth)
	}
}

func TestNeighbors_NotFoundReturns404(t *testing.T) {
	stub := &stubService{getErr: apihub.ErrNotFound}
	h := newAgentsHandlerWithSvc(stub)
	req := httptest.NewRequest("GET", "/api/agents/999/neighbors", nil)
	w := httptest.NewRecorder()
	h.Neighbors(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestNeighbors_ServiceErrorReturns500(t *testing.T) {
	stub := &stubService{
		getOut:       apihub.Asset{Kind: apihub.KindLLMEndpoint, RefID: 1},
		neighborsErr: errors.New("graph down"),
	}
	h := newAgentsHandlerWithSvc(stub)
	req := httptest.NewRequest("GET", "/api/agents/1/neighbors", nil)
	w := httptest.NewRecorder()
	h.Neighbors(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

// ── Stats (Phase 6) ─────────────────────────────────────────────────────

func TestStats_AggregatesByKindAndHealth(t *testing.T) {
	stub := &stubService{
		listOut: []apihub.Asset{
			{Kind: apihub.KindLLMEndpoint, RefID: 1, HealthState: apihub.HealthHealthy},
			{Kind: apihub.KindLLMEndpoint, RefID: 2, HealthState: apihub.HealthHealthy},
			{Kind: apihub.KindLLMEndpoint, RefID: 3, HealthState: apihub.HealthDown},
			{Kind: apihub.KindMCPServer, RefID: 4, HealthState: apihub.HealthUnknown},
			{Kind: apihub.KindLLMEndpoint, RefID: 5, Owner: "alice"},
		},
	}
	h := newAgentsHandlerWithSvc(stub)
	req := httptest.NewRequest("GET", "/api/agents/stats", nil)
	w := httptest.NewRecorder()
	h.Stats(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"total":5`) {
		t.Errorf("body missing total=5: %s", body)
	}
	if !strings.Contains(body, `"by_kind"`) {
		t.Errorf("body missing by_kind: %s", body)
	}
	if !strings.Contains(body, `"healthy":2`) {
		t.Errorf("body missing healthy=2: %s", body)
	}
	if !strings.Contains(body, `"down":1`) {
		t.Errorf("body missing down=1: %s", body)
	}
	if !strings.Contains(body, `"alice":1`) {
		t.Errorf("body missing alice owner count: %s", body)
	}
}

func TestStats_EmptyListReturnsZeroTotals(t *testing.T) {
	stub := &stubService{listOut: nil}
	h := newAgentsHandlerWithSvc(stub)
	req := httptest.NewRequest("GET", "/api/agents/stats", nil)
	w := httptest.NewRecorder()
	h.Stats(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"total":0`) {
		t.Errorf("body missing total=0: %s", w.Body.String())
	}
}

// ── Health (Phase 7) ─────────────────────────────────────────────────────

func TestHealth_ReturnsStaleAssets(t *testing.T) {
	stub := &stubService{
		listOut: []apihub.Asset{
			{Kind: apihub.KindLLMEndpoint, RefID: 1, TenantID: "t1", Name: "old", HealthState: apihub.HealthDegraded},
		},
	}
	h := newAgentsHandlerWithSvc(stub)
	req := httptest.NewRequest("GET", "/api/agents/health", nil)
	w := httptest.NewRecorder()
	h.Health(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"total":1`) {
		t.Errorf("body: %s", body)
	}
}

func TestHealth_CustomThresholdParsed(t *testing.T) {
	stub := &stubService{}
	h := newAgentsHandlerWithSvc(stub)
	req := httptest.NewRequest("GET", "/api/agents/health?threshold=30m", nil)
	w := httptest.NewRecorder()
	h.Health(w, req)

	if !strings.Contains(w.Body.String(), `"threshold":"30m0s"`) {
		t.Errorf("body: %s", w.Body.String())
	}
}

func TestHealth_BadThresholdFallsBack(t *testing.T) {
	stub := &stubService{}
	h := newAgentsHandlerWithSvc(stub)
	req := httptest.NewRequest("GET", "/api/agents/health?threshold=bogus", nil)
	w := httptest.NewRecorder()
	h.Health(w, req)

	if !strings.Contains(w.Body.String(), `"threshold":"6h0m0s"`) {
		t.Errorf("body: %s", w.Body.String())
	}
}

// PR-6 (2026-06-30): tenant_admin must see only their own tenant's
// stale assets (was seeing 'default' tenant's data due to RLS fallback
// in pg_store.ListTenants — audit P0-9). Super_admin retains the
// cross-tenant aggregation.
func TestHealth_TenantAdmin_SeesOnlyOwnTenant(t *testing.T) {
	stub := &stubService{
		listTenantsFn: func(ctx context.Context) ([]string, error) {
			t.Error("tenant_admin should NOT call ListTenants (RLS would restrict it to 'default')")
			return nil, nil
		},
		listStaleFn: func(ctx context.Context, threshold time.Duration) ([]apihub.Asset, error) {
			tenant := apihub.TenantFromContext(ctx)
			if tenant != "acme" {
				t.Errorf("ListStale called with tenant=%q, want acme", tenant)
			}
			return []apihub.Asset{
				{Kind: apihub.KindLLMEndpoint, RefID: 1, TenantID: "acme", Name: "stale-acme", HealthState: apihub.HealthDegraded},
			}, nil
		},
	}
	h := newAgentsHandlerWithSvc(stub)
	req := httptest.NewRequest("GET", "/api/agents/health", nil)
	req = SetAuthContext(req, &AuthContext{
		UserID:   42,
		TenantID: "acme",
		Username: "alice",
		Role:     "tenant_admin",
		IsJWT:    true,
	})
	w := httptest.NewRecorder()
	h.Health(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"total":1`) {
		t.Errorf("expected total=1 (own tenant only), got: %s", body)
	}
	if !strings.Contains(body, "stale-acme") {
		t.Errorf("expected own tenant's stale asset, got: %s", body)
	}
}

// PR-6: super_admin aggregates across all tenants via ListTenants loop.
func TestHealth_SuperAdmin_AggregatesAcrossTenants(t *testing.T) {
	tenants := []string{"default", "acme", "globex"}
	stub := &stubService{
		listTenantsFn: func(ctx context.Context) ([]string, error) {
			return tenants, nil
		},
		listStaleFn: func(ctx context.Context, threshold time.Duration) ([]apihub.Asset, error) {
			tenant := apihub.TenantFromContext(ctx)
			return []apihub.Asset{
				{Kind: apihub.KindLLMEndpoint, RefID: 1, TenantID: tenant, HealthState: apihub.HealthDegraded},
			}, nil
		},
	}
	h := newAgentsHandlerWithSvc(stub)
	req := httptest.NewRequest("GET", "/api/agents/health", nil)
	req = SetAuthContext(req, &AuthContext{
		UserID:   99,
		TenantID: "default",
		Username: "root",
		Role:     "super_admin",
		IsJWT:    true,
	})
	w := httptest.NewRecorder()
	h.Health(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"total":3`) {
		t.Errorf("expected total=3 (3 tenants × 1 stale), got: %s", body)
	}
}

// PR-6: legacy admin_key role also aggregates (treats as super).
func TestHealth_AdminKey_AggregatesAcrossTenants(t *testing.T) {
	tenants := []string{"default", "tenant-x"}
	stub := &stubService{
		listTenantsFn: func(ctx context.Context) ([]string, error) {
			return tenants, nil
		},
		listStaleFn: func(ctx context.Context, threshold time.Duration) ([]apihub.Asset, error) {
			return nil, nil
		},
	}
	h := newAgentsHandlerWithSvc(stub)
	req := httptest.NewRequest("GET", "/api/agents/health", nil)
	req = SetAuthContext(req, &AuthContext{
		Role: "admin_key",
	})
	w := httptest.NewRecorder()
	h.Health(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}
