package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/requestdetail"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

type bodyFetcherFunc func(context.Context, string) (any, any, error)

func (f bodyFetcherFunc) fetchRequestBodies(ctx context.Context, requestID string) (any, any, error) {
	return f(ctx, requestID)
}

func TestHandleUnifiedRequestDetailFromMemory(t *testing.T) {
	h := &Handler{}
	store, err := requestdetail.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	status := "in_progress"
	meta := requestdetail.Meta{RequestID: "req-unified-01", TenantID: "default", Status: &status}
	bodies := requestdetail.Bodies{RequestBody: json.RawMessage(`{"messages":[]}`)}
	if err := store.Put(meta, &bodies); err != nil {
		t.Fatal(err)
	}
	h.SetRequestDetailStore(store)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/request-detail/req-unified-01", nil)
	rr := httptest.NewRecorder()
	h.handleUnifiedRequestDetail(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got requestdetail.Detail
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Source != requestdetail.SourceFile && got.Source != requestdetail.SourceMemory {
		t.Fatalf("unexpected source %s", got.Source)
	}
	if got.Persistence != requestdetail.PersistenceInFlight {
		t.Fatalf("unexpected persistence %s", got.Persistence)
	}
}

func TestPGBodyReaderResolvesClientRequestIDToCanonicalID(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	// 2026-08-27: loadRequestLogMeta resolves in cascade — request_id on hot
	// table first, then client_request_id on hot, then the current-month
	// view. A client-provided id misses the first lookup (empty rows →
	// ErrNoRows) and resolves on the second.
	metaCols := []string{
		"request_id", "tenant_id", "gw_session_id", "gw_task_id", "client_model", "request_status", "success", "latency_ms",
	}
	mock.ExpectQuery(`(?s)SELECT request_id, COALESCE\(tenant_id, ''\).*FROM request_logs_hot\s+WHERE request_id = \$1\s+LIMIT 1`).
		WithArgs("client-req-1").
		WillReturnRows(pgxmock.NewRows(metaCols))
	mock.ExpectQuery(`(?s)SELECT request_id, COALESCE\(tenant_id, ''\).*FROM request_logs_hot\s+WHERE client_request_id = \$1\s+ORDER BY ts DESC\s+LIMIT 1`).
		WithArgs("client-req-1").
		WillReturnRows(pgxmock.NewRows(metaCols).
			AddRow("gateway-req-1", "tenant-a", nil, nil, "claude-test", "success", true, 12))
	mock.ExpectQuery(`SELECT outbound_body::text\s+FROM request_logs_bodies_hot`).
		WithArgs("gateway-req-1").
		WillReturnRows(pgxmock.NewRows([]string{"outbound_body"}).AddRow(`{"messages":[]}`))

	reader := &pgBodyReader{db: mock, fetch: bodyFetcherFunc(func(context.Context, string) (any, any, error) {
		return `{"messages":[]}`, `{"content":"ok"}`, nil
	})}
	bodies, meta, err := reader.ReadRequestLogsBodies(context.Background(), "client-req-1", false)
	require.NoError(t, err)
	require.Equal(t, "gateway-req-1", meta.RequestID)
	require.Equal(t, "tenant-a", meta.TenantID)
	require.JSONEq(t, `{"messages":[]}`, string(bodies.RequestBody))
	require.JSONEq(t, `{"content":"ok"}`, string(bodies.ResponseBody))
	require.JSONEq(t, `{"messages":[]}`, string(bodies.OutboundBody))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestHandleUnifiedRequestDetailNotConfigured(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/request-detail/req-x", nil)
	rr := httptest.NewRecorder()
	h.handleUnifiedRequestDetail(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 got %d", rr.Code)
	}
}

// 2026-08-26 (P1-29 fix): before this commit, the handler only applied
// the tenant check to PersistencePersisted (DB-backed) details — an
// in-flight file / memory entry belonging to another tenant could be
// fetched by any tenant_admin who knew the request_id. These tests
// pin the new behaviour: ALL detail sources are gated, the response
// is 404 (not 403) so we don't reveal existence across tenants.

func TestHandleUnifiedRequestDetail_TenantIsolation_FromFile(t *testing.T) {
	h := &Handler{}
	store, err := requestdetail.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// tenant-a writes an in-flight file-backed detail.
	meta := requestdetail.Meta{RequestID: "req-cross-tenant", TenantID: "tenant-a"}
	bodies := requestdetail.Bodies{RequestBody: json.RawMessage(`{"x":1}`)}
	if err := store.Put(meta, &bodies); err != nil {
		t.Fatal(err)
	}
	h.SetRequestDetailStore(store)

	// tenant-b admin tries to read it.
	req := httptest.NewRequest(http.MethodGet, "/api/admin/request-detail/req-cross-tenant", nil)
	req = SetAuthContext(req, &AuthContext{
		UserID: 7, TenantID: "tenant-b", Username: "b-admin",
		Role: "tenant_admin", IsJWT: true,
	})
	rr := httptest.NewRecorder()
	h.handleUnifiedRequestDetail(rr, req)
	// Must look identical to "not found" — do NOT leak that the id
	// exists for tenant-a.
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant in-flight read must be 404, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleUnifiedRequestDetail_TenantIsolation_FromMemory(t *testing.T) {
	h := &Handler{}
	store, err := requestdetail.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Memory-only meta (no bodies on disk) — tenant-a.
	if err := store.PutMeta(requestdetail.Meta{RequestID: "req-mem-only", TenantID: "tenant-a"}); err != nil {
		t.Fatal(err)
	}
	h.SetRequestDetailStore(store)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/request-detail/req-mem-only", nil)
	req = SetAuthContext(req, &AuthContext{
		UserID: 7, TenantID: "tenant-b", Username: "b-admin",
		Role: "tenant_admin", IsJWT: true,
	})
	rr := httptest.NewRecorder()
	h.handleUnifiedRequestDetail(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant in-memory read must be 404, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleUnifiedRequestDetail_TenantIsolation_EmptyTenantID(t *testing.T) {
	// Legacy in-flight meta recorded before the P1-29 fix may carry
	// an empty TenantID. tenant_admins must NOT see those — fail
	// closed (treat as "unknown origin").
	h := &Handler{}
	store, err := requestdetail.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutMeta(requestdetail.Meta{RequestID: "req-orphan", TenantID: ""}); err != nil {
		t.Fatal(err)
	}
	h.SetRequestDetailStore(store)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/request-detail/req-orphan", nil)
	req = SetAuthContext(req, &AuthContext{
		UserID: 7, TenantID: "tenant-b", Username: "b-admin",
		Role: "tenant_admin", IsJWT: true,
	})
	rr := httptest.NewRecorder()
	h.handleUnifiedRequestDetail(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("orphan (no tenant) detail must be 404 for tenant_admin, got %d body=%s",
			rr.Code, rr.Body.String())
	}
}

func TestHandleUnifiedRequestDetail_TenantIsolation_SuperAdminBypass(t *testing.T) {
	// super_admin must still see cross-tenant entries — they need
	// platform-wide visibility for ops.
	h := &Handler{}
	store, err := requestdetail.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	meta := requestdetail.Meta{RequestID: "req-cross-tenant-sa", TenantID: "tenant-a"}
	if err := store.Put(meta, &requestdetail.Bodies{RequestBody: json.RawMessage(`{"x":1}`)}); err != nil {
		t.Fatal(err)
	}
	h.SetRequestDetailStore(store)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/request-detail/req-cross-tenant-sa", nil)
	req = SetAuthContext(req, &AuthContext{
		UserID: 1, TenantID: "default", Username: "ops",
		Role: "super_admin", IsJWT: true,
	})
	rr := httptest.NewRecorder()
	h.handleUnifiedRequestDetail(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("super_admin cross-tenant read must succeed, got %d body=%s",
			rr.Code, rr.Body.String())
	}
}

func TestHandleUnifiedRequestDetail_TenantIsolation_SameTenant(t *testing.T) {
	// Sanity: same-tenant admin can still read in-flight details.
	h := &Handler{}
	store, err := requestdetail.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	meta := requestdetail.Meta{RequestID: "req-same-tenant", TenantID: "tenant-a"}
	if err := store.Put(meta, &requestdetail.Bodies{RequestBody: json.RawMessage(`{"x":1}`)}); err != nil {
		t.Fatal(err)
	}
	h.SetRequestDetailStore(store)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/request-detail/req-same-tenant", nil)
	req = SetAuthContext(req, &AuthContext{
		UserID: 7, TenantID: "tenant-a", Username: "a-admin",
		Role: "tenant_admin", IsJWT: true,
	})
	rr := httptest.NewRecorder()
	h.handleUnifiedRequestDetail(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("same-tenant admin read must succeed, got %d body=%s",
			rr.Code, rr.Body.String())
	}
}
