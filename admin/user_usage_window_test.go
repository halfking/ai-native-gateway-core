// user_usage_window_test.go — 员工窗口用量聚合 + 员工×模型矩阵钉测（2026-11-20）。
//
// 覆盖：
//   - usageWindowBounds：缺省近 30 天 / YYYY-MM-DD（end=当日结束）/
//     非法格式 400 / 跨度 >90 天 400（与 handleUserModelUsage 同闸）。
//   - usageAggregatesByOwner / queryUserUsageMatrix：pgxmock 全查询路径
//     （usageQuerier 缝隙注入，含 NULL 成本列）。
//   - resolveUsageMatrixParams：普通用户锁定 owner=自己 + 越权 403。
//   - 源码结构钉测：handleUserModelUsage 的 SQL 必须**不含** rl.created_at
//     （canonical 视图列面是 ts —— 252 生产 information_schema 实测），
//     防 42703 活体 500 回归。
package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pashagolub/pgxmock/v4"
)

// ─────────────────────────────────────────────────────────────────────────
// usageWindowBounds
// ─────────────────────────────────────────────────────────────────────────

func TestUsageWindowBoundsDefault30d(t *testing.T) {
	rr := httptest.NewRecorder()
	start, end, ok := usageWindowBounds(rr, httptest.NewRequest(http.MethodGet, "/x", nil), "", "")
	if !ok {
		t.Fatalf("default window should be ok")
	}
	if d := end.Sub(start); d < 29*24*time.Hour || d > 31*24*time.Hour {
		t.Fatalf("default window ≈ 30d, got %v", d)
	}
}

func TestUsageWindowBoundsExplicitFormats(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	start, end, ok := usageWindowBounds(rr, req, "2026-11-01", "2026-11-03")
	if !ok {
		t.Fatalf("YYYY-MM-DD should parse")
	}
	d1, _ := time.Parse("2006-01-02", "2026-11-01")
	if !start.Equal(d1) {
		t.Fatalf("start = %v, want %v", start, d1)
	}
	// end = date_to + 24h（当日结束，与 handleUserModelUsage 同语义）。
	if !end.Equal(d1.Add(72 * time.Hour)) {
		t.Fatalf("end = %v, want date_to+24h=%v", end, d1.Add(72*time.Hour))
	}
}

func TestUsageWindowBoundsInvalidFormat400(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x?date_from=not-a-date", nil)
	_, _, ok := usageWindowBounds(rr, req, "not-a-date", "")
	if ok {
		t.Fatalf("invalid date_from must fail")
	}
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid format should write 400, got %d", rr.Code)
	}
}

func TestUsageWindowBoundsOver90d400(t *testing.T) {
	rr := httptest.NewRecorder()
	_, _, ok := usageWindowBounds(rr, httptest.NewRequest(http.MethodGet, "/x", nil), "2026-01-01", "2026-11-01")
	if ok {
		t.Fatalf(">90d window must fail")
	}
	if rr.Code != http.StatusBadRequest {
		t.Fatalf(">90d should write 400, got %d", rr.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// usageAggregatesByOwner（pgxmock 全查询路径）
// ─────────────────────────────────────────────────────────────────────────

func TestUsageAggregatesByOwnerScansAndMerges(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 7)

	// 出处钉测：查询必须打在 request_logs_with_current_month（canonical 视图）。
	mock.ExpectQuery(regexp.QuoteMeta("FROM request_logs_with_current_month rl")).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(mock.NewRows([]string{
			"owner_user", "request_count", "input_tokens", "output_tokens",
			"total_tokens", "internal_cost_usd", "external_cost_usd",
			"session_count", "model_count",
		}).AddRow("alice", 12, 1000, 2000, 3000, 0.5, 0.8, 3, 2).
			AddRow("bob", 4, 100, 50, 150, nil, nil, 1, 1))

	got, err := usageAggregatesByOwner(context.Background(), mock, start, end, "", "")
	if err != nil {
		t.Fatalf("usageAggregatesByOwner: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 owners, got %d", len(got))
	}
	a := got["alice"]
	if a == nil || a.RequestCount != 12 || a.TotalTokens != 3000 || a.SessionCount != 3 || a.ModelCount != 2 {
		t.Fatalf("alice aggregate wrong: %+v", a)
	}
	if a.InternalCostUSD != 0.5 || a.ExternalCostUSD != 0.8 {
		t.Fatalf("alice costs wrong: %+v", a)
	}
	// NULL 成本列 → 0（不是 scan 报错）。
	b := got["bob"]
	if b == nil || b.InternalCostUSD != 0 || b.ExternalCostUSD != 0 {
		t.Fatalf("bob NULL costs should coalesce to 0: %+v", b)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// resolveUsageMatrixParams（隔离与校验，不触 DB）
// ─────────────────────────────────────────────────────────────────────────

func TestResolveUsageMatrixParamsRegularUserLockedToSelf(t *testing.T) {
	// owner=bob 越权 → 403 + ok=false。
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/admin/session-analytics/users/usage-matrix?owner=bob", nil)
	req = SetAuthContext(req, &AuthContext{
		UserID: 7, TenantID: "default", Username: "alice", Role: "user", IsJWT: true,
	})
	_, ok := resolveUsageMatrixParams(rr, req)
	if ok || rr.Code != http.StatusForbidden {
		t.Fatalf("owner=bob for regular user should 403+false, got ok=%v code=%d", ok, rr.Code)
	}

	// owner 缺省 → 锁定自己。
	rr2 := httptest.NewRecorder()
	req2 := SetAuthContext(httptest.NewRequest(http.MethodGet,
		"/api/admin/session-analytics/users/usage-matrix", nil),
		&AuthContext{UserID: 7, TenantID: "default", Username: "alice", Role: "user", IsJWT: true})
	p, ok2 := resolveUsageMatrixParams(rr2, req2)
	if !ok2 {
		t.Fatalf("self matrix params should resolve")
	}
	if p.Owner != "alice" {
		t.Fatalf("regular user owner must lock to self, got %q", p.Owner)
	}
	if p.TenantID != "default" {
		t.Fatalf("regular user tenant must lock to own tenant, got %q", p.TenantID)
	}
}

func TestResolveUsageMatrixParamsSuperAdminOptionalTenant(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/admin/session-analytics/users/usage-matrix?tenant_id=acme&owner=bob&limit=99", nil)
	req = SetAuthContext(req, &AuthContext{
		UserID: 1, TenantID: "default", Username: "admin", Role: "admin_key",
	})
	p, ok := resolveUsageMatrixParams(rr, req)
	if !ok {
		t.Fatalf("super admin params should resolve")
	}
	if p.TenantID != "acme" || p.Owner != "bob" || p.Limit != 99 {
		t.Fatalf("params wrong: %+v", p)
	}
	// limit 越界回落默认 500。
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet,
		"/api/admin/session-analytics/users/usage-matrix?limit=99999", nil)
	p2, ok2 := resolveUsageMatrixParams(rr2, req2)
	if !ok2 || p2.Limit != 500 {
		t.Fatalf("limit clamp wrong: ok=%v limit=%d", ok2, p2.Limit)
	}
}

func TestResolveUsageMatrixParamsInvalidDate400(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/admin/session-analytics/users/usage-matrix?start_date=garbage", nil)
	_, ok := resolveUsageMatrixParams(rr, req)
	if ok || rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid start_date should 400+false, got ok=%v code=%d", ok, rr.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// queryUserUsageMatrix（pgxmock 全查询路径）
// ─────────────────────────────────────────────────────────────────────────

func TestQueryUserUsageMatrixScansRows(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	// 出处 + 隔离钉测：查询打 canonical 视图；owner 过滤在 WHERE 里。
	mock.ExpectQuery(regexp.QuoteMeta("FROM request_logs_with_current_month rl")).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(mock.NewRows([]string{
			"owner_user", "model_name", "provider", "request_count",
			"input_tokens", "output_tokens", "total_tokens",
			"internal_cost_usd", "external_cost_usd", "first_used_at", "last_used_at",
		}).AddRow("alice", "glm-5.3", "Zhipu", 10, 500, 1500, 2000, 0.4, 0.9,
			time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC),
			time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)))

	p := usageMatrixParams{
		Start: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC),
		Owner: "alice",
		Limit: 500,
	}
	rows, err := queryUserUsageMatrix(context.Background(), mock, p)
	if err != nil {
		t.Fatalf("queryUserUsageMatrix: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	row := rows[0]
	if row.OwnerUser != "alice" || row.ModelName != "glm-5.3" || row.TotalTokens != 2000 {
		t.Fatalf("row wrong: %+v", row)
	}
	if row.InternalCostUSD != 0.4 || row.ExternalCostUSD != 0.9 {
		t.Fatalf("costs wrong: %+v", row)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// handleUserUsageMatrix：薄层守卫
// ─────────────────────────────────────────────────────────────────────────

func TestUserUsageMatrixMethodGuardAndNilDB(t *testing.T) {
	h := &Handler{db: nil}

	rec := httptest.NewRecorder()
	h.handleUserUsageMatrix(rec, httptest.NewRequest(http.MethodPost, "/api/admin/session-analytics/users/usage-matrix", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST should 405, got %d", rec.Code)
	}

	rec2 := httptest.NewRecorder()
	h.handleUserUsageMatrix(rec2, httptest.NewRequest(http.MethodGet, "/api/admin/session-analytics/users/usage-matrix", nil))
	if rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil db should 503, got %d", rec2.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// 源码结构钉测：canonical 视图时间列是 ts，不是 created_at
// ─────────────────────────────────────────────────────────────────────────

// TestUserProfileModelsSQLUsesTsNotCreatedAt 钉住 handleUserModelUsage 的
// 2026-11-20 修活：request_logs_with_current_month 的列面（890）只有 ts，
// 没有 created_at —— 原 SQL 在真库每次执行都 42703（且 IsSchemaBehindError
// 不认 ⇒ 无降级，详情页模型卡恒 500）。源码级断言防回归。
func TestUserProfileModelsSQLUsesTsNotCreatedAt(t *testing.T) {
	b, err := os.ReadFile("user_profile.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	src := string(b)
	// 只看活体 SQL，不看注释（注释里提「created_at 旧写法」是修活记录，不是引用）。
	var code strings.Builder
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		code.WriteString(line)
		code.WriteString("\n")
	}
	if strings.Contains(code.String(), "rl.created_at") {
		t.Fatalf("user_profile.go 仍引用 rl.created_at —— canonical 视图没有该列，" +
			"真库执行必 42703（详情页模型卡恒 500）。时间列必须用 rl.ts。")
	}
	if !strings.Contains(src, "rl.ts >= $2 AND rl.ts < $3") {
		t.Fatalf("models 端点窗口过滤形状变了 —— 请同步更新钉测与出处说明")
	}
}

// ─────────────────────────────────────────────────────────────────────────
// queryUserProjects（员工→项目，L1 收口）
// ─────────────────────────────────────────────────────────────────────────

func TestQueryUserProjectsScansRows(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	// 出处钉测：查询打在 session_summaries × session_dim（会话级口径；
	// 不触 canonical 请求视图 ⇒ 无登记簿影响；不经 session_owners——其行集
	// 在生产是陈旧子集，叠加时间窗恒空，2026-11-20 活体打回）。
	mock.ExpectQuery(regexp.QuoteMeta("FROM session_summaries ss")).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(mock.NewRows([]string{
			"project_id", "session_count", "task_count", "request_count",
			"total_tokens", "total_cost_usd", "total_credits_charged", "last_activity_at",
		}).AddRow("prj-alpha", 12, 3, 45, 9000, 12.5, 300,
			time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)).
			AddRow("prj-beta", 2, 0, 5, 100, nil, nil, nil))

	rows, err := queryUserProjects(context.Background(), mock, "alice", "", 30, 100)
	if err != nil {
		t.Fatalf("queryUserProjects: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	if rows[0].ProjectID != "prj-alpha" || rows[0].SessionCount != 12 || rows[0].TaskCount != 3 || rows[0].TotalTokens != 9000 {
		t.Fatalf("row0 wrong: %+v", rows[0])
	}
	if rows[0].TotalCostUSD != 12.5 || rows[0].TotalCredits != 300 {
		t.Fatalf("row0 costs wrong: %+v", rows[0])
	}
	// NULL 成本/时间 → 零值（该行无成本是正常状态，不是 scan 报错）
	if rows[1].TotalCostUSD != 0 || rows[1].TotalCredits != 0 || !rows[1].LastActivityAt.IsZero() {
		t.Fatalf("row1 NULL coalesce wrong: %+v", rows[1])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestUserProjectsHandlerGuards(t *testing.T) {
	h := &Handler{db: nil}

	rec := httptest.NewRecorder()
	h.handleUserOwnerProjects(rec, httptest.NewRequest(http.MethodPost, "/api/admin/session-analytics/users/alice/projects", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST should 405, got %d", rec.Code)
	}

	rec2 := httptest.NewRecorder()
	h.handleUserOwnerProjects(rec2, httptest.NewRequest(http.MethodGet, "/api/admin/session-analytics/users/alice/projects", nil))
	if rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil db should 503, got %d", rec2.Code)
	}
}

func TestUserProjectsHandlerRegularUserLockedToSelf(t *testing.T) {
	// 空 owner 路径（/users//projects）→ 400：钉「owner 从 /users/{owner}/projects
	// 段提取」的路径解析；需要非 nil db（nil-db 503 先于参数检查）⇒ 用惰性连接池
	//（pgxpool.New 不立即建连，400 发生在任何查询之前 ⇒ 不会真连）。
	pool, err := pgxpool.New(context.Background(), "postgres://127.0.0.1:1/none?connect_timeout=1")
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()
	h := &Handler{db: pool}

	rec := httptest.NewRecorder()
	h.handleUserOwnerProjects(rec, httptest.NewRequest(http.MethodGet, "/api/admin/session-analytics/users//projects", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty owner should 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}
