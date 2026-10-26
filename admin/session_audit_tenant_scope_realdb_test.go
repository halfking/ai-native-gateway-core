package admin

// session_audit 跨租户可见性契约的判据（2026-10-16）。
//
// 缺陷背景：`withTenantTx` 对**空** tenantID 不是"不设 GUC"，而是让
// `setLocalTenantGUC` 返回 `empty tenant_id for SET LOCAL app.current_tenant`。
// session_audit.go 的三个端点都对 super_admin 显式留空 tenant，于是：
//   - list / export → 500；
//   - stats        → `_ =` 丢弃错误 ⇒ 响应是零值 `{"total":0}` + HTTP 200。
// 后者更危险：审计统计显示「零风险」而库里其实有待审批的拦截记录。
//
// 本判据直接打真库、用**跨两个租户**的数据，分别以 super_admin 与
// tenant_admin 身份验证「能不能看见别人的行」，并锁住两条安全边界：
//   - super_admin 不传 tenant ⇒ 必须跨租户（否则等于没修）；
//   - super_admin 传了 tenant ⇒ 必须只看那一个租户（否则 bypass 成了越权）；
//   - tenant_admin ⇒ 任何情况下都只看自己租户。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func sessionAuditDSN(t *testing.T) string {
	t.Helper()
	for _, k := range []string{"TEST_ADMIN_500_DSN", "LLM_GATEWAY_DATABASE_URL", "DATABASE_URL"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	t.Skip("set TEST_ADMIN_500_DSN (or LLM_GATEWAY_DATABASE_URL)")
	return ""
}

// seedSessionAuditRows 在两个**本判据专属**的租户下各插一行，返回 tenant 名。
// 用带随机后缀的租户名，避免与既有数据/其它并发判据串味（LESSONS 2026-10-15
// 「包测串味」条目）。
func seedSessionAuditRows(t *testing.T, pool *pgxpool.Pool) (tenantA, tenantB string) {
	t.Helper()
	ctx := context.Background()
	// 用测试名派生的 token 做后缀：并发的同名判据（同包多 -run 并行）之间
	// 不会互删对方的行，也不与既有数据串味。
	suffix := sanitizeSchemaToken(t.Name())
	tenantA = "sa-probe-a-" + suffix
	tenantB = "sa-probe-b-" + suffix

	// pgx 的扩展查询协议**不允许**一次 Exec 带多条语句（SQLSTATE 42601），
	// 所以必须显式开事务逐条发，不能用 BEGIN; …; COMMIT; 拼一条。
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // 已 Commit 时是预期错误
	if _, err := tx.Exec(ctx, "SET LOCAL app.bypass_rls = 'on'"); err != nil {
		t.Fatalf("set bypass_rls: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO session_audit_records
		  (session_id, tenant_id, request_id, detect_score, detect_decision, status, approval_status, security_score, content_title)
		VALUES
		  ($1, $2, 'req-probe-a', 88, 'block', 'need_approval', 'pending', 91, 'probe A'),
		  ($1, $3, 'req-probe-b', 12, 'pass',    'pass',        NULL,        5, 'probe B')
	`, "probe-sess-"+suffix, tenantA, tenantB); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	t.Cleanup(func() {
		delCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		tx, err := pool.Begin(delCtx)
		if err != nil {
			t.Errorf("cleanup begin: %v", err)
			return
		}
		defer tx.Rollback(delCtx) //nolint:errcheck
		if _, err := tx.Exec(delCtx, "SET LOCAL app.bypass_rls = 'on'"); err != nil {
			t.Errorf("cleanup bypass: %v", err)
			return
		}
		if _, err := tx.Exec(delCtx,
			"DELETE FROM session_audit_records WHERE tenant_id = ANY($1)",
			[]string{tenantA, tenantB}); err != nil {
			t.Errorf("cleanup delete: %v", err)
			return
		}
		if err := tx.Commit(delCtx); err != nil {
			t.Errorf("cleanup commit: %v", err)
		}
	})
	return tenantA, tenantB
}

func callSessionAuditList(t *testing.T, h *Handler, role, tenant, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := SetAuthContext(
		httptest.NewRequest(http.MethodGet, "/api/admin/session-audit"+query, nil),
		&AuthContext{Role: role, TenantID: tenant, Username: "probe"},
	)
	rec := httptest.NewRecorder()
	h.handleSessionAuditList(rec, req)
	return rec
}

// TestSessionAuditSuperAdminSeesAllTenants 是本缺陷的核心判据：super_admin
// 不传 tenant_id 时必须**跨租户**看见行。
//
// 变异要点（两个方向都会让它变红）：
//   - 改回 withTenantTx（空 tenant 报错）→ 500，红在 status；
//   - 继续静默吞掉错误               → 200 但 total=0，红在 total。
func TestSessionAuditSuperAdminSeesAllTenants(t *testing.T) {
	dsn := sessionAuditDSN(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	tenantA, tenantB := seedSessionAuditRows(t, pool)

	// super_admin 不传 tenant_id ⇒ 必须跨租户看见本判据的两行。
	rec := callSessionAuditList(t, &Handler{db: pool}, "super_admin", "", "?limit=50")
	if rec.Code != http.StatusOK {
		t.Fatalf("super_admin 无 tenant 应 200，实得 %d：%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Records []struct {
			TenantID string `json:"tenant_id"`
		} `json:"records"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	seen := map[string]bool{}
	for _, r := range body.Records {
		seen[r.TenantID] = true
	}
	if !seen[tenantA] || !seen[tenantB] {
		t.Fatalf("super_admin 应看见本判据的两个租户 %s / %s，实际看见 %v（total=%d）。"+
			"空 tenant 若退化成静默失败，这里会拿到 total=0 或只看见一个租户",
			tenantA, tenantB, seen, body.Total)
	}
}

// TestSessionAuditExplicitTenantIsNotABypass 锁住安全边界：
// super_admin 显式指定某个 tenant 时，bypass **不得**生效，否则就成了越权。
// 变异：把分派写成"只要 isSuper 就 bypass"→ 本判据变红。
func TestSessionAuditExplicitTenantIsNotABypass(t *testing.T) {
	dsn := sessionAuditDSN(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	tenantA, _ := seedSessionAuditRows(t, pool)

	h := &Handler{db: pool}
	rec := callSessionAuditList(t, h, "super_admin", "", "?limit=50&tenant_id="+tenantA)
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实得 %d：%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Records []struct {
			TenantID string `json:"tenant_id"`
		} `json:"records"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, r := range body.Records {
		if r.TenantID != tenantA {
			t.Fatalf("super_admin 指定 tenant_id=%s 时却看见了 %s —— bypass 越权了。", tenantA, r.TenantID)
		}
	}
	if len(body.Records) == 0 {
		t.Fatalf("指定 tenant 后应至少看见自己插的那 1 行，实得 0 行")
	}
}

// TestSessionAuditTenantAdminCannotSeeOtherTenant 锁住最关键的一条：
// tenant_admin 永远只看见自己租户。它保证本轮的分派没有把普通租户也一起 bypass。
func TestSessionAuditTenantAdminCannotSeeOtherTenant(t *testing.T) {
	dsn := sessionAuditDSN(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	tenantA, _ := seedSessionAuditRows(t, pool)

	h := &Handler{db: pool}
	rec := callSessionAuditList(t, h, "tenant_admin", tenantA, "?limit=50")
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实得 %d：%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Records []struct {
			TenantID string `json:"tenant_id"`
		} `json:"records"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, r := range body.Records {
		if r.TenantID != tenantA {
			t.Fatalf("tenant_admin(%s) 看见了 %s 的行 —— 越权。", tenantA, r.TenantID)
		}
	}

	// 显式要求别人的租户必须 403。
	rec2 := callSessionAuditList(t, h, "tenant_admin", tenantA, "?limit=50&tenant_id=somebody-else")
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("tenant_admin 跨租户请求应 403，实得 %d：%s", rec2.Code, rec2.Body.String())
	}
}

// TestSessionAuditStatsIsNotSilentlyZero 单独锁 stats 端点。
//
// 为什么必须独立成一条：list 与 stats 的缺陷形态**不同**——
//   - list/export 修复前是「报错」⇒ 500；
//   - stats         修复前是「静默」⇒ 200 + {"total":0}。
//
// 两者写在一个测试里时，list 先红会让断言短路，stats 的静默零值**永远看不到**，
// 于是判据只能证明「list 修好了」，不能证明「stats 不再静默」。
// 本轮实测确认过这个短路：组合变异后判据红在 list 的 500，stats 完全没被检验。
//
// 修复前真库有 2 行跨 2 租户而 stats 返回 total=0 + HTTP 200 ——
// 审计面板显示「零风险」，比 500 更危险。
func TestSessionAuditStatsIsNotSilentlyZero(t *testing.T) {
	dsn := sessionAuditDSN(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	seedSessionAuditRows(t, pool)

	h := &Handler{db: pool}
	sreq := SetAuthContext(
		httptest.NewRequest(http.MethodGet, "/api/admin/session-audit/stats", nil),
		&AuthContext{Role: "super_admin", TenantID: "", Username: "probe"},
	)
	srec := httptest.NewRecorder()
	h.handleSessionAuditStats(srec, sreq)
	if srec.Code != http.StatusOK {
		t.Fatalf("stats 应 200，实得 %d：%s", srec.Code, srec.Body.String())
	}
	var stats struct {
		Total    int            `json:"total"`
		ByStatus map[string]int `json:"by_status"`
		AvgScore float64        `json:"avg_score"`
	}
	if err := json.Unmarshal(srec.Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats.Total != 2 {
		t.Fatalf("stats.total=%d，判据刚插入 2 行。修复前这里是**静默**返回 0 且 HTTP 200"+
			"（`_ =` 丢掉了 empty tenant 错误）——审计面板会显示「零风险」。body=%s",
			stats.Total, srec.Body.String())
	}
	if stats.ByStatus["need_approval"] != 1 || stats.ByStatus["pass"] != 1 {
		t.Fatalf("by_status=%v，期望 need_approval=1 且 pass=1（证明分组聚合真跑到了）",
			stats.ByStatus)
	}
	if stats.AvgScore <= 0 {
		t.Fatalf("avg_score=%v，两行 detect_score 是 88 与 12，均值必须为正。"+
			"为 0 说明聚合子查询没跑到（单查询失败被静默吞掉）", stats.AvgScore)
	}
}
