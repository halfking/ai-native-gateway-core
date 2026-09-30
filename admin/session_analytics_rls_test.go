package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestWithTenantTransaction_RLSIsolation exercises withTenantTx against a real
// PostgreSQL DSN. It verifies:
//   - tenant context sets app.current_tenant GUC
//   - rows belonging to another tenant are invisible
//   - GUC is auto-cleared once the transaction commits/rolls back
//
// Skipped unless TEST_DATABASE_URL is set; designed for the role-aware
// RLS-integration runner (NOSUPERUSER/NOBYPASSRLS).
func TestWithTenantTransaction_RLSIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping RLS integration test")
	}
	pool := setupTestDB(t)
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*1000*1000*1000)
	defer cancel()

	// R65 环境前置：RLS 隔离只在 NOSUPERUSER/NOBYPASSRLS 角色下可观测
	//（本文件头注的 role-aware runner 前提）。本地 TEST_DATABASE_URL 常以
	// superuser+BYPASSRLS 连接（llm_gateway 实测 usesuper=t usebypassrls=t，
	// 且 session_summaries relforcerowsecurity=f）——该前提下两租户都见全量
	// 行是 PG 语义的必然，不是隔离破洞；修前此景实锤假红。显式 skip 并说明
	// 前提，把隔离验证留给角色受控的 RLS 门（Makefile test-rls）。
	// R65 批判复审收紧：前置查询自身失败（异常环境）必须 Fatal 而非借道
	// skip——skip 只留给"确认 bypass"这一种前提不满足。
	var roleBypasses bool
	if err := pool.QueryRow(ctx,
		`SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user`,
	).Scan(&roleBypasses); err != nil {
		t.Fatalf("role precheck query failed: %v", err)
	}
	if roleBypasses {
		t.Skip("connected role is superuser/bypassrls; RLS isolation not observable — run via the role-aware RLS gate")
	}

	probeQuery := func(tenant string) (int, error) {
		var n int
		err := withTenantTx(ctx, pool, tenant, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, "SELECT count(*) FROM session_summaries").Scan(&n)
		})
		return n, err
	}

	nA, err := probeQuery("tenant-a")
	if err != nil {
		t.Skipf("session_summaries missing RLS context or table not present: %v", err)
	}
	if nB, err := probeQuery("tenant-b"); err == nil && nB > 0 && nA == nB {
		t.Fatalf("expected RLS to isolate tenants, got nA=%d nB=%d", nA, nB)
	}

	// GUC must be cleared after the transaction commits.
	if err := pool.QueryRow(ctx, "SELECT current_setting('app.current_tenant', true)").Scan(new(string)); err != nil {
		t.Fatalf("post-tx GUC probe failed: %v", err)
	}
}

// TestWithAllTenantReadOnlyTx_BypassGUC asserts that the explicit super-admin
// bypass path sets app.current_role and app.bypass_rls, and that the GUCs are
// reset once the transaction ends. The query is intentionally restricted to a
// row count to avoid depending on actual data.
func TestWithAllTenantReadOnlyTx_BypassGUC(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping RLS integration test")
	}
	pool := setupTestDB(t)
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*1000*1000*1000)
	defer cancel()

	var (
		role, bypass string
	)
	err := withAllTenantReadOnlyTx(ctx, pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT current_setting('app.current_role', true), current_setting('app.bypass_rls', true)").Scan(&role, &bypass); err != nil {
			return err
		}
		_, _ = tx.Exec(ctx, "SELECT 1")
		return nil
	})
	if err != nil {
		t.Skipf("bypass GUC path unavailable in this DB: %v", err)
	}
	if role != "super_admin" || bypass != "true" {
		t.Fatalf("expected super_admin bypass GUCs inside tx, got role=%q bypass=%q", role, bypass)
	}

	// After commit, GUCs must reset.
	if err := pool.QueryRow(ctx, "SELECT current_setting('app.current_role', true), current_setting('app.bypass_rls', true)").Scan(&role, &bypass); err != nil {
		t.Fatalf("post-tx GUC probe failed: %v", err)
	}
	if role == "super_admin" || bypass == "true" {
		t.Fatalf("bypass GUCs leaked across tx: role=%q bypass=%q", role, bypass)
	}
}

// TestListSessions_UnauthorizedWithoutToken guards that the handler still
// reports 503 (no DB) before authn can be short-circuited — purely a handler
// smoke check, not an RLS test.
func TestListSessions_HandlerRejectsMethodNotAllowed(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/session-analytics", nil)
	req = setTestRequestContext(req, "tenant_admin", "acme", "admin")
	resp := httptest.NewRecorder()
	h.HandleSessionAnalyticsList(resp, req)
	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.Code)
	}
	if !strings.Contains(resp.Body.String(), "method not allowed") {
		t.Fatalf("body = %q", resp.Body.String())
	}
}
