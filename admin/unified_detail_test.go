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

	// ★ 2026-10-20（迁移 890，v1 族退役）：元数据解析的两条腿都读**会话族**
	// 原生源（db.SessionFamilyTurnsSourceSQL：hot ∪ 母表 + 734 details 层）。
	//
	// 原来的形态是四条腿：`_request_logs_hot`（request_id）→
	// `_request_logs_hot`（client_request_id）→ canonical 视图（request_id）
	// → canonical 视图（client_request_id）。v1 族被 DROP 之后那四条腿里的
	// 两条（v1 热表）根本不存在，另两条（canonical 视图）在 890 之后**就是**
	// 会话族两臂体 ⇒ 走视图等于把同一份会话族数据查两遍。
	//
	// ⇒ 现在是两条腿，都在会话族原生源上。判据用**结构**（两腿 + 关系名），
	// 不用列投影：投影会随 118 列契约变，关系名不会。
	metaCols := []string{
		"request_id", "tenant_id", "gw_session_id", "gw_task_id", "client_model", "request_status", "success", "latency_ms",
	}
	mock.ExpectQuery(sessionFamilyTurnsLookupRE(false)).
		WithArgs("client-req-1").
		WillReturnRows(pgxmock.NewRows(metaCols))
	mock.ExpectQuery(sessionFamilyTurnsByClientRequestIDRE(false)).
		WithArgs("client-req-1").
		WillReturnRows(pgxmock.NewRows(metaCols).
			AddRow("gateway-req-1", "tenant-a", nil, nil, "claude-test", "success", true, 12))
	mock.ExpectQuery(sessionFamilyBodiesLookupRE()).
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

func TestPGBodyReaderFillsPartialBodiesFromSessionTurns(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	// ★ 2026-10-20（890）：元数据腿与正文腿都从 v1 切到会话族；第三条腿
	// （session_turns 补空）不变。
	metaCols := []string{
		"request_id", "tenant_id", "gw_session_id", "gw_task_id", "client_model", "request_status", "success", "latency_ms",
	}
	mock.ExpectQuery(sessionFamilyTurnsLookupRE(false)).
		WithArgs("req-partial").
		WillReturnRows(pgxmock.NewRows(metaCols).
			AddRow("req-partial", "default", "session-1", nil, "model", "success", true, 10))
	mock.ExpectQuery(sessionFamilyBodiesLookupRE()).
		WithArgs("req-partial").
		WillReturnRows(pgxmock.NewRows([]string{"outbound_body"}).AddRow(`{"messages":["from-logs"]}`))
	mock.ExpectQuery(`(?s)SELECT t.session_id, t.turn_no,.*FROM public.session_turns_with_current_month t`).
		WithArgs("req-partial").
		WillReturnRows(pgxmock.NewRows([]string{
			"session_id", "turn_no", "tenant_id", "request_delta", "response_delta", "outbound_body", "model", "latency_ms",
		}).AddRow("session-1", 1, "default", []byte(`{"messages":["from-session"]}`), []byte(`{"choices":[{"message":{"content":"reply"}}]}`), []byte(`{"messages":["from-session"]}`), "model", 10))

	reader := &pgBodyReader{db: mock, fetch: bodyFetcherFunc(func(context.Context, string) (any, any, error) {
		return `{"messages":["from-logs"]}`, nil, nil
	})}
	bodies, _, err := reader.ReadRequestLogsBodies(context.Background(), "req-partial", false)
	require.NoError(t, err)
	require.JSONEq(t, `{"messages":["from-logs"]}`, string(bodies.RequestBody))
	require.JSONEq(t, `{"messages":["from-logs"]}`, string(bodies.OutboundBody))
	require.JSONEq(t, `{"choices":[{"message":{"content":"reply"}}]}`, string(bodies.ResponseBody))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPGBodyReaderOmitBodySkipsBodyQueries(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	// ★ 2026-10-20（890）：元数据腿从 v1 热表切到会话族原生源。
	metaCols := []string{
		"request_id", "tenant_id", "gw_session_id", "gw_task_id", "client_model", "request_status", "success", "latency_ms",
	}
	mock.ExpectQuery(sessionFamilyTurnsLookupRE(false)).
		WithArgs("req-meta-only").
		WillReturnRows(pgxmock.NewRows(metaCols).
			AddRow("req-meta-only", "default", "session-1", nil, "model", "success", true, 10))

	called := false
	reader := &pgBodyReader{db: mock, fetch: bodyFetcherFunc(func(context.Context, string) (any, any, error) {
		called = true
		return nil, nil, nil
	})}
	bodies, meta, err := reader.ReadRequestLogsBodies(context.Background(), "req-meta-only", true)
	require.NoError(t, err)
	require.Empty(t, bodies)
	require.Equal(t, "req-meta-only", meta.RequestID)
	require.False(t, called, "omit_body must not invoke body fetch")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReadSessionTurnsBodiesOmitBodyReturnsMetadata(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery(`(?s)SELECT t.session_id, t.turn_no, t.tenant_id,\s+NULL::text, NULL::text, NULL::text,\s+t.model, t.latency_ms\s+FROM public.session_turns_with_current_month t\s+WHERE t.request_id = \$1`).
		WithArgs("req-session-only").
		WillReturnRows(pgxmock.NewRows([]string{
			"session_id", "turn_no", "tenant_id", "request_delta", "response_delta", "outbound_body", "model", "latency_ms",
		}).AddRow("session-1", 3, "tenant-a", nil, nil, nil, "model-a", 15))

	reader := &pgBodyReader{db: mock}
	bodies, meta, err := reader.ReadSessionTurnsBodies(context.Background(), "req-session-only", true)
	require.NoError(t, err)
	require.Empty(t, bodies)
	require.Equal(t, "req-session-only", meta.RequestID)
	require.Equal(t, "tenant-a", meta.TenantID)
	require.Equal(t, "session-1", *meta.GwSessionID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestHandleUnifiedRequestDetailStoreMissWithoutDBReturnsNotFound(t *testing.T) {
	h := &Handler{}
	store, err := requestdetail.NewStore("")
	require.NoError(t, err)
	h.SetRequestDetailStore(store)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/request-detail/req-store-miss", nil)
	rr := httptest.NewRecorder()
	h.handleUnifiedRequestDetail(rr, req)
	require.Equal(t, http.StatusNotFound, rr.Code)
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

func TestHandleUnifiedRequestDetail_UnknownRoleIsTenantScoped(t *testing.T) {
	h := &Handler{}
	store, err := requestdetail.NewStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, store.PutMeta(requestdetail.Meta{RequestID: "req-unknown-role", TenantID: "tenant-a"}))
	h.SetRequestDetailStore(store)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/request-detail/req-unknown-role", nil)
	req = SetAuthContext(req, &AuthContext{UserID: 9, TenantID: "tenant-b", Role: "viewer", IsJWT: true})
	rr := httptest.NewRecorder()
	h.handleUnifiedRequestDetail(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("non-super-admin cross-tenant read must be 404, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleUnifiedRequestDetail_InvalidRequestID(t *testing.T) {
	h := &Handler{}
	store, err := requestdetail.NewStore(t.TempDir())
	require.NoError(t, err)
	h.SetRequestDetailStore(store)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/request-detail/short", nil)
	rr := httptest.NewRecorder()
	h.handleUnifiedRequestDetail(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid request ID must be 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestHandleUnifiedRequestDetail_OversizedBodyReturns413 (2026-08-29 audit
// follow-up): a Locator.Get that returns ErrBodyTooLarge must be mapped
// to HTTP 413, not 500. The previous code's only explicit error mapping
// was for ErrNotFound; ErrBodyTooLarge fell through to the generic 500
// path. This test pins the new mapping.
//
// We inject a stub locator via the private field (same package) so we
// don't need to mock the entire pgBodyReader chain.
func TestHandleUnifiedRequestDetail_OversizedBodyReturns413(t *testing.T) {
	h := &Handler{}
	store, err := requestdetail.NewStore(t.TempDir())
	require.NoError(t, err)
	h.SetRequestDetailStore(store)

	// Replace the locator with one whose Get returns ErrBodyTooLarge.
	h.requestDetailLocator = &requestdetail.Locator{
		Store:  store,
		Bodies: oversizedBodyReader{},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/request-detail/req-oversize-test-01", nil)
	rr := httptest.NewRecorder()
	h.handleUnifiedRequestDetail(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body must be 413, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// oversizedBodyReader is a stub that always returns ErrBodyTooLarge so
// the handler's error mapping path is exercised end-to-end without
// requiring a real oversized body file or DB row.
type oversizedBodyReader struct{}

func (oversizedBodyReader) ReadRequestLogsBodies(_ context.Context, _ string, _ bool) (requestdetail.Bodies, requestdetail.Meta, error) {
	return requestdetail.Bodies{}, requestdetail.Meta{}, requestdetail.ErrBodyTooLarge
}
func (oversizedBodyReader) ReadSessionTurnsBodies(_ context.Context, _ string, _ bool) (requestdetail.Bodies, requestdetail.Meta, error) {
	return requestdetail.Bodies{}, requestdetail.Meta{}, requestdetail.ErrBodyTooLarge
}

func TestAnyToRawQuotesPlainText(t *testing.T) {
	raw := anyToRaw("upstream returned plain text")
	if !json.Valid(raw) {
		t.Fatalf("plain text must be encoded as valid JSON, got %q", raw)
	}
	var got string
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "upstream returned plain text", got)
}

// 2026-08-28 (audit follow-up, P1-29 follow-up): the previous implementation
// accepted tenant_admin lookups but only gated them at the HTTP handler. A
// SQL-level collision was still possible: when a client-supplied
// client_request_id collides across tenants, the SQL query without a
// tenant_id predicate could resolve to the wrong tenant's row. These tests
// pin the new behaviour: with a tenant-scoped LookupScope, the SQL filter
// (and a defense-in-depth post-scan check) MUST keep the response 404.
//
// The mock expectation captures BOTH the SQL pattern (with `AND tenant_id =
// $2`) and the WithArgs (with the tenant_id arg). If a regression regresses
// the SQL to the unrestricted form, the mock will fail.

func TestPGBodyReader_ClientRequestIDCollision_RespectsTenantScope(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	// ★ 2026-10-20（890）：元数据腿从「v1 热表 + canonical 视图」四条腿
	// 收敛为「会话族原生源」两条腿。租户谓词（AND tenant_id = $2）仍在，
	// 这道门守的正是它。
	metaCols := []string{
		"request_id", "tenant_id", "gw_session_id", "gw_task_id", "client_model", "request_status", "success", "latency_ms",
	}
	// request_id lookup must include tenant_id predicate.
	mock.ExpectQuery(sessionFamilyTurnsLookupRE(true)).
		WithArgs("client-req-collision", "tenant-a").
		WillReturnRows(pgxmock.NewRows(metaCols))
	// client_request_id fallback must also include tenant_id predicate.
	mock.ExpectQuery(sessionFamilyTurnsByClientRequestIDRE(true)).
		WithArgs("client-req-collision", "tenant-a").
		WillReturnRows(pgxmock.NewRows(metaCols))

	reader := &pgBodyReader{db: mock, fetch: bodyFetcherFunc(func(context.Context, string) (any, any, error) {
		return nil, nil, nil
	})}
	ctx := requestdetail.WithLookupScope(context.Background(), requestdetail.LookupScope{TenantID: "tenant-a"})
	_, _, err = reader.ReadRequestLogsBodies(ctx, "client-req-collision", false)
	require.ErrorIs(t, err, requestdetail.ErrNotFound,
		"tenant-scoped query must not surface cross-tenant rows; got %v", err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPGBodyReader_ClientRequestIDCollision_AllowsOwnTenant(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	metaCols := []string{
		"request_id", "tenant_id", "gw_session_id", "gw_task_id", "client_model", "request_status", "success", "latency_ms",
	}
	// ★ 2026-10-20（890）：元数据腿读会话族原生源；正文腿读会话族正文源。
	// Body tables are keyed only by the canonical request ID; tenant scope was
	// enforced while resolving that ID from the session-family turns source.
	mock.ExpectQuery(sessionFamilyTurnsLookupRE(true)).
		WithArgs("client-req-own", "tenant-a").
		WillReturnRows(pgxmock.NewRows(metaCols).
			AddRow("gateway-req-own", "tenant-a", nil, nil, "m", "success", true, 10))
	mock.ExpectQuery(sessionFamilyBodiesLookupRE()).
		WithArgs("gateway-req-own").
		WillReturnRows(pgxmock.NewRows([]string{"outbound_body"}).AddRow(`{"o":1}`))

	reader := &pgBodyReader{db: mock, fetch: bodyFetcherFunc(func(context.Context, string) (any, any, error) {
		return `{"req":1}`, `{"resp":1}`, nil
	})}
	ctx := requestdetail.WithLookupScope(context.Background(), requestdetail.LookupScope{TenantID: "tenant-a"})
	bodies, meta, err := reader.ReadRequestLogsBodies(ctx, "client-req-own", false)
	require.NoError(t, err)
	require.Equal(t, "gateway-req-own", meta.RequestID)
	require.Equal(t, "tenant-a", meta.TenantID)
	require.JSONEq(t, `{"req":1}`, string(bodies.RequestBody))
	require.JSONEq(t, `{"resp":1}`, string(bodies.ResponseBody))
	require.JSONEq(t, `{"o":1}`, string(bodies.OutboundBody))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPGBodyReader_DefenseInDepth_TenantMismatch(t *testing.T) {
	// Even if RLS/bypass GUCs widen the SQL view and the row comes back with a
	// tenant_id different from the caller's scope, the post-scan check must
	// still treat it as ErrNotFound.
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	metaCols := []string{
		"request_id", "tenant_id", "gw_session_id", "gw_task_id", "client_model", "request_status", "success", "latency_ms",
	}
	// ★ 2026-10-20（890）：元数据腿读会话族原生源。
	mock.ExpectQuery(sessionFamilyTurnsLookupRE(true)).
		WithArgs("client-req-defense", "tenant-a").
		WillReturnRows(pgxmock.NewRows(metaCols).
			AddRow("gateway-req-defense", "tenant-b", nil, nil, "m", "success", true, 10))

	reader := &pgBodyReader{db: mock, fetch: bodyFetcherFunc(func(context.Context, string) (any, any, error) {
		return nil, nil, nil
	})}
	ctx := requestdetail.WithLookupScope(context.Background(), requestdetail.LookupScope{TenantID: "tenant-a"})
	_, _, err = reader.ReadRequestLogsBodies(ctx, "client-req-defense", false)
	require.ErrorIs(t, err, requestdetail.ErrNotFound,
		"defense-in-depth tenant mismatch must be ErrNotFound, got %v", err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPGBodyReader_SuperAdminSeesCrossTenant(t *testing.T) {
	// Sanity: unrestricted (super_admin) callers still see cross-tenant rows.
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	metaCols := []string{
		"request_id", "tenant_id", "gw_session_id", "gw_task_id", "client_model", "request_status", "success", "latency_ms",
	}
	// No `AND tenant_id` predicate when scope is unrestricted.
	mock.ExpectQuery(sessionFamilyTurnsLookupRE(false)).
		WithArgs("client-req-sa").
		WillReturnRows(pgxmock.NewRows(metaCols).
			AddRow("gateway-req-sa", "tenant-a", nil, nil, "m", "success", true, 10))
	mock.ExpectQuery(sessionFamilyBodiesLookupRE()).
		WithArgs("gateway-req-sa").
		WillReturnRows(pgxmock.NewRows([]string{"outbound_body"}).AddRow(`{}`))

	reader := &pgBodyReader{db: mock, fetch: bodyFetcherFunc(func(context.Context, string) (any, any, error) {
		return `{}`, `{}`, nil
	})}
	// Default LookupScope is unrestricted (no WithLookupScope called).
	bodies, meta, err := reader.ReadRequestLogsBodies(context.Background(), "client-req-sa", false)
	require.NoError(t, err)
	require.Equal(t, "gateway-req-sa", meta.RequestID)
	require.Equal(t, "tenant-a", meta.TenantID)
	require.JSONEq(t, `{}`, string(bodies.RequestBody))
	require.JSONEq(t, `{}`, string(bodies.ResponseBody))
	require.JSONEq(t, `{}`, string(bodies.OutboundBody))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReadSessionTurnsBodies_TenantScopeFiltersRows(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery(`(?s)SELECT t.session_id, t.turn_no,.*FROM public.session_turns_with_current_month t`).
		WithArgs("req-turns-x", "tenant-a").
		WillReturnRows(pgxmock.NewRows([]string{
			"session_id", "turn_no", "tenant_id", "request_delta", "response_delta", "outbound_body", "model", "latency_ms",
		}))

	reader := &pgBodyReader{db: mock, fetch: bodyFetcherFunc(func(context.Context, string) (any, any, error) {
		return nil, nil, nil
	})}
	ctx := requestdetail.WithLookupScope(context.Background(), requestdetail.LookupScope{TenantID: "tenant-a"})
	_, _, err = reader.ReadSessionTurnsBodies(ctx, "req-turns-x", false)
	require.ErrorIs(t, err, requestdetail.ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}
