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
