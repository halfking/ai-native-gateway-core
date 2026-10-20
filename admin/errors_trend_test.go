package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// 2026-09-05 审计闭环1：GET /api/errors/trend 回归。
// 主路径读 supplier_error_stats 预聚合；空窗口时回退 supplier_errors_unified
// 明细聚合（source=fallback），保证聚合器刚部署时趋势图仍有数据。

func statsTrendRows() *pgxmock.Rows {
	bucket := time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC)
	bySupplier := []byte(`{"zhipu":3,"openai":1}`)
	byType := []byte(`{"rate_limit":2,"timeout":2}`)
	return pgxmock.NewRows([]string{"stat_time", "error_count", "unique_requests", "affected_users", "by_supplier", "by_error_type"}).
		AddRow(bucket, 4, 3, 4, bySupplier, byType)
}

func trendBreakdownRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"kind", "key", "n"}).
		AddRow("type", "rate_limit", 2).
		AddRow("type", "timeout", 2).
		AddRow("supplier", "zhipu", 3).
		AddRow("supplier", "openai", 1).
		// R16 (2026-09-12): third UNION branch feeds summary.affected_credentials.
		AddRow("creds", "", 2)
}

func TestErrorsTrendFromStats(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery("FROM supplier_error_stats").
		WithArgs("hour", pgxmock.AnyArg(), pgxmock.AnyArg(), "", int64(0), "", "").
		WillReturnRows(statsTrendRows())
	mock.ExpectQuery("FROM supplier_errors_unified").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), "", int64(0), "", "").
		WillReturnRows(trendBreakdownRows())

	req := httptest.NewRequest(http.MethodGet, "/api/errors/trend?hours=24", nil)
	req = SetAuthContext(req, &AuthContext{Role: "super_admin"})
	rec := httptest.NewRecorder()
	(&errorsTrendHandlers{db: mock}).getErrorsTrend(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		Source      string `json:"source"`
		Granularity string `json:"granularity"`
		TimeSeries  []struct {
			Timestamp  time.Time      `json:"timestamp"`
			ErrorCount int            `json:"error_count"`
			BySupplier map[string]int `json:"by_supplier"`
		} `json:"time_series"`
		Summary struct {
			TotalErrors   int `json:"total_errors"`
			TopErrorTypes []struct {
				Key   string `json:"key"`
				Count int    `json:"count"`
			} `json:"top_error_types"`
			TopSuppliers []struct {
				Key   string `json:"key"`
				Count int    `json:"count"`
			} `json:"top_suppliers"`
			AffectedCreds int `json:"affected_credentials"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Source != "stats" {
		t.Fatalf("source=%s", body.Source)
	}
	if body.Granularity != "hour" || len(body.TimeSeries) != 1 {
		t.Fatalf("granularity=%s series=%d", body.Granularity, len(body.TimeSeries))
	}
	if body.Summary.TotalErrors != 4 {
		t.Fatalf("total=%d", body.Summary.TotalErrors)
	}
	if len(body.Summary.TopErrorTypes) != 2 || len(body.Summary.TopSuppliers) != 2 {
		t.Fatalf("breakdowns: %+v", body.Summary)
	}
	// R16: creds branch of loadBreakdowns must land in the summary KPI
	// (was declared-but-never-computed, i.e. always 0 on the panel).
	if body.Summary.AffectedCreds != 2 {
		t.Fatalf("affected_credentials=%d, want 2", body.Summary.AffectedCreds)
	}
	if body.TimeSeries[0].BySupplier["zhipu"] != 3 {
		t.Fatalf("by_supplier=%v", body.TimeSeries[0].BySupplier)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestErrorsTrendFallsBackToUnifiedDetail(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	// stats 空窗口 → 回退明细聚合。
	// 调用序列：stats 明细（空）→ stats breakdown（空）→ unified 明细 →
	// unified breakdown（loadFromStats 与 loadFromDetail 各跑一次 breakdown）。
	mock.ExpectQuery("FROM supplier_error_stats").
		WithArgs("minute", pgxmock.AnyArg(), pgxmock.AnyArg(), "", int64(0), "", "").
		WillReturnRows(pgxmock.NewRows([]string{"stat_time", "error_count", "unique_requests", "affected_users", "by_supplier", "by_error_type"}))
	mock.ExpectQuery("FROM supplier_errors_unified").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"kind", "key", "n"}))
	bucket := time.Date(2026, 9, 5, 8, 30, 0, 0, time.UTC)
	mock.ExpectQuery("FROM supplier_errors_unified").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"bucket", "error_count", "unique_requests", "affected_users", "by_supplier", "by_error_type"}).
			AddRow(bucket, 2, 2, 2, []byte(`{"zhipu":2}`), []byte(`{"timeout":2}`)))
	mock.ExpectQuery("FROM supplier_errors_unified").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(trendBreakdownRows())

	req := httptest.NewRequest(http.MethodGet, "/api/errors/trend?hours=1", nil)
	req = SetAuthContext(req, &AuthContext{Role: "super_admin"})
	rec := httptest.NewRecorder()
	(&errorsTrendHandlers{db: mock}).getErrorsTrend(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Source      string `json:"source"`
		Granularity string `json:"granularity"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Source != "fallback" {
		t.Fatalf("source=%s, want fallback", body.Source)
	}
	if body.Granularity != "minute" {
		t.Fatalf("granularity=%s, want minute for hours=1", body.Granularity)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestErrorsTrendValidationAndDBErrors(t *testing.T) {
	newMock := func(t *testing.T) pgxmock.PgxPoolIface {
		t.Helper()
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(mock.Close)
		return mock
	}
	// 2026-10-16 audit #154: pre-empt the role gate so these tests still
	// exercise their original intent (validation / nil-DB / SQL errors), not
	// the new role gate. Production always reaches the handler with an admin
	// role set by AdminMiddleware.
	adminReq := func(method, path string) *http.Request {
		req := httptest.NewRequest(method, path, nil)
		return SetAuthContext(req, &AuthContext{Role: "super_admin"})
	}
	t.Run("invalid hours", func(t *testing.T) {
		rec := httptest.NewRecorder()
		(&errorsTrendHandlers{db: newMock(t)}).getErrorsTrend(rec, adminReq(http.MethodGet, "/api/errors/trend?hours=48"))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d", rec.Code)
		}
	})
	t.Run("invalid granularity", func(t *testing.T) {
		rec := httptest.NewRecorder()
		(&errorsTrendHandlers{db: newMock(t)}).getErrorsTrend(rec, adminReq(http.MethodGet, "/api/errors/trend?granularity=week"))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d", rec.Code)
		}
	})
	t.Run("invalid credential_id", func(t *testing.T) {
		rec := httptest.NewRecorder()
		(&errorsTrendHandlers{db: newMock(t)}).getErrorsTrend(rec, adminReq(http.MethodGet, "/api/errors/trend?credential_id=abc"))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d", rec.Code)
		}
	})
	t.Run("stats db error surfaces 500", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectQuery("FROM supplier_error_stats").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(errors.New("db down"))
		rec := httptest.NewRecorder()
		(&errorsTrendHandlers{db: mock}).getErrorsTrend(rec, adminReq(http.MethodGet, "/api/errors/trend"))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("db not configured", func(t *testing.T) {
		rec := httptest.NewRecorder()
		(&errorsTrendHandlers{}).getErrorsTrend(rec, adminReq(http.MethodGet, "/api/errors/trend"))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d", rec.Code)
		}
	})
}

// R49-C1 回归门：tenant_admin 的租户 ID 必须真正落到 SQL 过滤参数里。
// 修复前 TenantID 解析后从未进 SQL（叠加读事务 RLS 旁路 ⇒ tenant_admin
// 可读全租户聚合）；本测试用「期望正则匹配 SQL 文本中的租户谓词」使
// 无谓词的旧实现直接失败（无匹配期望 ⇒ 查询报错 ⇒ 500）。
func TestErrorsTrendTenantScopedForTenantAdmin(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	// stats 腿：租户范围借 credential_id → credentials.tenant_id 子查询（$7）。
	mock.ExpectQuery(`tenant_id = \$7`).
		WithArgs("hour", pgxmock.AnyArg(), pgxmock.AnyArg(), "", int64(0), "", "acme").
		WillReturnRows(statsTrendRows())
	// breakdown 腿：unified 直带 tenant_id 谓词（$6）。
	mock.ExpectQuery(`tenant_id = \$6`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), "", int64(0), "", "acme").
		WillReturnRows(trendBreakdownRows())

	req := httptest.NewRequest(http.MethodGet, "/api/errors/trend?hours=24", nil)
	req = SetAuthContext(req, &AuthContext{Role: "tenant_admin", TenantID: "acme"})
	rec := httptest.NewRecorder()
	(&errorsTrendHandlers{db: mock}).getErrorsTrend(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Source  string `json:"source"`
		Summary struct {
			TotalErrors int `json:"total_errors"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Source != "stats" || body.Summary.TotalErrors != 4 {
		t.Fatalf("source=%s total=%d", body.Source, body.Summary.TotalErrors)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// 2026-10-16 audit #154: role gate on /api/errors/trend. Route is registered
// under AdminMiddleware (any authenticated user), but the SQL filter is
// `$N = ” OR tenant_id = $N` and EffectiveTenantIDAll returns "" for
// non-tenant_admin — a role=user caller would read cross-tenant aggregates.
// Block before any DB call.
func TestGetErrorsTrend_UserRoleForbidden(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	// mock.ExpectationsWereMet would fail if any DB query fires — proving the
	// 403 happens before any read. We assert the same here explicitly.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/errors/trend?hours=24", nil)
	req = SetAuthContext(req, &AuthContext{Role: "user", TenantID: "acme"})
	(&errorsTrendHandlers{db: mock}).getErrorsTrend(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403 body=%s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("role=user must not reach DB: %v", err)
	}
}

func TestGetErrorsTrend_TenantAdminAndSuperAdminAllowed(t *testing.T) {
	// Both tenant_admin and super_admin + admin_key must reach the handler —
	// not 403. We assert the gate is non-blocking by using a nil db: the
	// handler's next check is `if h == nil || h.db == nil → 503`, so any code
	// path past the gate surfaces as 503 (not 403). SQL semantics for these
	// roles are covered by TestErrorsTrendTenantScopedForTenantAdmin.
	cases := []struct {
		name   string
		role   string
		tenant string
	}{
		{"super_admin", "super_admin", ""},
		{"admin_key", "admin_key", "default"},
		{"tenant_admin", "tenant_admin", "acme"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/errors/trend?hours=24", nil)
			req = SetAuthContext(req, &AuthContext{Role: tc.role, TenantID: tc.tenant})
			(&errorsTrendHandlers{}).getErrorsTrend(rec, req)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("role=%s: status=%d want 503 (gate passed, db unconfigured) body=%s",
					tc.role, rec.Code, rec.Body.String())
			}
		})
	}
}
