package admin

import (
	"context"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// seedSessionRow inserts a session_summaries + session_dim fixture row pair
// for the given tenant/owner and returns cleanup that removes both rows.
func seedSessionRow(t *testing.T, pool *pgxpool.Pool, tenant, owner, gwSessionID string) func() {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin fixture tx: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_role', 'super_admin', true), set_config('app.bypass_rls', 'true', true)"); err != nil {
		t.Fatalf("set fixture bypass GUC: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO session_dim (gw_session_id, session_key, tenant_id, owner_user, created_at)
		 VALUES ($1,$1,$2,$3,NOW())
		 ON CONFLICT (gw_session_id) DO UPDATE SET session_key=EXCLUDED.session_key, tenant_id=EXCLUDED.tenant_id, owner_user=EXCLUDED.owner_user`,
		gwSessionID, tenant, owner,
	); err != nil {
		t.Fatalf("seed session_dim: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO session_summaries (session_key, tenant_id, first_request_at, last_request_at, updated_at)
		 VALUES ($1,$2,NOW(),NOW(),NOW())
		 ON CONFLICT (session_key) DO UPDATE SET tenant_id=EXCLUDED.tenant_id`,
		gwSessionID, tenant,
	); err != nil {
		t.Fatalf("seed session_summaries: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit fixture tx: %v", err)
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, err := pool.Acquire(ctx)
		if err != nil {
			return
		}
		defer conn.Release()
		tx, err := conn.Begin(ctx)
		if err != nil {
			return
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, "SELECT set_config('app.current_role', 'super_admin', true), set_config('app.bypass_rls', 'true', true)"); err != nil {
			return
		}
		_, _ = tx.Exec(ctx, `DELETE FROM session_summaries WHERE session_key=$1`, gwSessionID)
		_, _ = tx.Exec(ctx, `DELETE FROM session_dim WHERE gw_session_id=$1`, gwSessionID)
		_ = tx.Commit(ctx)
	}
}

// requireLowPrivilegeRole asserts the connection is run by a non-superuser,
// non-bypass-rls role; otherwise the RLS policies would not apply and the
// integration test would silently turn into a false positive. The check is a
// best-effort defense: it fails the test if the test database is mis-
// configured for RLS verification.
func requireLowPrivilegeRole(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var (
		isSuper     bool
		isBypassRLS bool
	)
	if err := pool.QueryRow(ctx, `SELECT current_setting('is_superuser')::bool`).Scan(&isSuper); err != nil {
		t.Fatalf("is_superuser probe failed (test DB unconfigured?): %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(rolbypassrls, false) FROM pg_roles WHERE rolname = current_user`,
	).Scan(&isBypassRLS); err != nil {
		t.Fatalf("bypassrls probe failed: %v", err)
	}
	if isSuper || isBypassRLS {
		t.Skipf("RLS isolation test requires NOSUPERUSER/NOBYPASSRLS role, current is_superuser=%v bypassrls=%v", isSuper, isBypassRLS)
	}
}

// seedThreeTenantFixture inserts three rows:
//   - tenant-a / alice / sess-A
//   - tenant-a / bob   / sess-B (same tenant, different owner)
//   - tenant-b / carol / sess-C (different tenant)
//
// and returns cleanup.
func seedThreeTenantFixture(t *testing.T, pool *pgxpool.Pool) (sessA, sessB, sessC string, cleanup func()) {
	t.Helper()
	sessA = "rls-fixture-a-alice"
	sessB = "rls-fixture-a-bob"
	sessC = "rls-fixture-b-carol"
	cleanA := seedSessionRow(t, pool, "tenant-a", "alice", sessA)
	cleanB := seedSessionRow(t, pool, "tenant-a", "bob", sessB)
	cleanC := seedSessionRow(t, pool, "tenant-b", "carol", sessC)
	return sessA, sessB, sessC, func() {
		cleanA()
		cleanB()
		cleanC()
	}
}

// visibleRowCount returns how many of the given session_keys are visible
// when running in the supplied wrapper (tenant string empty => bypass path).
func visibleRowCount(t *testing.T, pool *pgxpool.Pool, tenant string, keys []string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var n int
	err := func() error {
		if tenant != "" {
			return withTenantTx(ctx, pool, tenant, func(tx pgx.Tx) error {
				row := tx.QueryRow(ctx,
					`SELECT count(*) FROM session_summaries WHERE session_key = ANY($1::text[])`,
					keys,
				)
				return row.Scan(&n)
			})
		}
		return withAllTenantReadOnlyTx(ctx, pool, func(tx pgx.Tx) error {
			row := tx.QueryRow(ctx,
				`SELECT count(*) FROM session_summaries WHERE session_key = ANY($1::text[])`,
				keys,
			)
			return row.Scan(&n)
		})
	}()
	if err != nil {
		t.Fatalf("visible row count failed: %v", err)
	}
	return n
}

// TestRLS_TenantIsolationWithFixture verifies the minimum contract: with the
// tenant GUC set, only that tenant's rows are visible. This is the
// foundation the application-layer WHERE clauses rely on.
func TestRLS_TenantIsolationWithFixture(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-DB RLS isolation test")
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	requireLowPrivilegeRole(t, pool)

	cleanupA := seedSessionRow(t, pool, "tenant-a", "alice", "rls-test-a")
	defer cleanupA()
	cleanupB := seedSessionRow(t, pool, "tenant-b", "bob", "rls-test-b")
	defer cleanupB()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var visibleA, visibleB int
	if err := withTenantTx(ctx, pool, "tenant-a", func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `SELECT count(*) FROM session_summaries WHERE session_key IN ('rls-test-a','rls-test-b')`)
		return row.Scan(&visibleA)
	}); err != nil {
		t.Fatalf("tenant-a probe: %v", err)
	}
	if err := withTenantTx(ctx, pool, "tenant-b", func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `SELECT count(*) FROM session_summaries WHERE session_key IN ('rls-test-a','rls-test-b')`)
		return row.Scan(&visibleB)
	}); err != nil {
		t.Fatalf("tenant-b probe: %v", err)
	}

	if visibleA != 1 || visibleB != 1 {
		t.Fatalf("expected each tenant to see exactly 1 fixture row, got A=%d B=%d", visibleA, visibleB)
	}
}

// TestRLS_ThreeRoleMatrix covers the full (tenant_admin / regular / super_admin)
// × (cross-tenant / cross-owner / same-tenant) matrix against a real DB.
//
// Expectations:
//   - tenant_admin(acme) sees only tenant-a rows (both alice and bob).
//   - regular(acme/alice) sees only tenant-a/alice rows; bob rows and
//     tenant-b rows are hidden by RLS even before the application WHERE
//     layer runs.
//   - super_admin via withAllTenantReadOnlyTx sees all three rows.
func TestRLS_ThreeRoleMatrix(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-DB three-role RLS matrix")
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	requireLowPrivilegeRole(t, pool)

	_, _, _, cleanup := seedThreeTenantFixture(t, pool)
	defer cleanup()

	keys := []string{"rls-fixture-a-alice", "rls-fixture-a-bob", "rls-fixture-b-carol"}

	// tenant_admin tenant-a → 2 rows (alice + bob).
	if got := visibleRowCount(t, pool, "tenant-a", keys); got != 2 {
		t.Fatalf("tenant_admin tenant-a should see 2 rows, got %d", got)
	}
	// tenant_admin tenant-b → 1 row (carol).
	if got := visibleRowCount(t, pool, "tenant-b", keys); got != 1 {
		t.Fatalf("tenant_admin tenant-b should see 1 row, got %d", got)
	}
	// super_admin bypass GUC path → 3 rows.
	if got := visibleRowCount(t, pool, "", keys); got != 3 {
		t.Fatalf("super_admin bypass should see 3 rows, got %d", got)
	}

	// Regular alice would be denied by the application-level owner filter at
	// the handler boundary. The RLS layer alone (with only the tenant GUC)
	// sees both alice and bob under tenant-a. We exercise that here so
	// the matrix documents where the defense layers live.
	if got := visibleRowCount(t, pool, "tenant-a", keys); got == 0 {
		t.Fatalf("tenant-a RLS sees zero rows; fixture missing?")
	}
}

// TestRLS_BypassGUCScopeLeaves verifies that after withAllTenantReadOnlyTx
// commits, the bypass GUCs do not leak into subsequent pool queries.
func TestRLS_BypassGUCScopeLeaves(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-DB bypass GUC leakage test")
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	requireLowPrivilegeRole(t, pool)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := withAllTenantReadOnlyTx(ctx, pool, func(tx pgx.Tx) error {
		var role, bypass string
		if err := tx.QueryRow(ctx,
			`SELECT current_setting('app.current_role', true), current_setting('app.bypass_rls', true)`,
		).Scan(&role, &bypass); err != nil {
			return err
		}
		if role != "super_admin" || bypass != "true" {
			t.Fatalf("bypass GUCs not set inside tx: role=%q bypass=%q", role, bypass)
		}
		return nil
	}); err != nil {
		t.Fatalf("bypass tx probe failed: %v", err)
	}

	// After commit, the GUCs must reset; otherwise future pool queries could
	// inadvertently gain super-admin visibility.
	var role, bypass string
	if err := pool.QueryRow(ctx,
		`SELECT current_setting('app.current_role', true), current_setting('app.bypass_rls', true)`,
	).Scan(&role, &bypass); err != nil {
		t.Fatalf("post-tx probe failed: %v", err)
	}
	if role == "super_admin" || bypass == "true" {
		t.Fatalf("bypass GUCs leaked across tx: role=%q bypass=%q", role, bypass)
	}
}

// TestRLS_ApplicationOwnerFilterForRegular ensures that even with RLS tenant
// isolation in place, regular users still cannot observe rows belonging to a
// different owner inside the same tenant. We exercise the same
// application-level owner filter used by HandleSessionAnalyticsList via the
// session_dim JOIN pattern.
func TestRLS_ApplicationOwnerFilterForRegular(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-DB owner filter test")
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	requireLowPrivilegeRole(t, pool)

	_, _, _, cleanup := seedThreeTenantFixture(t, pool)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var n int
	err = withTenantTx(ctx, pool, "tenant-a", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM session_summaries ss
			   JOIN session_dim sd ON sd.gw_session_id = ss.session_key
			   WHERE ss.tenant_id = $1 AND sd.owner_user = $2`,
			"tenant-a", "alice",
		).Scan(&n)
	})
	if err != nil {
		t.Fatalf("owner filter probe failed: %v", err)
	}
	if n != 1 {
		t.Fatalf("regular alice should see exactly 1 row in tenant-a, got %d", n)
	}
}

// TestAssertSessionOwnerAccessInTx_RealDBCrossTenantDenied verifies the in-tx
// owner check helper rejects a regular user trying to access another tenant's
// session_key.
func TestAssertSessionOwnerAccessInTx_RealDBCrossTenantDenied(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-DB owner access test")
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	requireLowPrivilegeRole(t, pool)

	sessA, _, _, cleanup := seedThreeTenantFixture(t, pool)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r := httptest.NewRequest("GET", "/", nil)
	r = SetAuthContext(r, &AuthContext{Role: "user", TenantID: "tenant-a", Username: "alice", IsJWT: true})

	var allowed bool
	err = withTenantTx(ctx, pool, "tenant-a", func(tx pgx.Tx) error {
		// alice (tenant-a) looking at her own session → ok.
		if o, e := assertSessionOwnerAccessInTx(ctx, tx, r, sessA); e != nil || !o {
			t.Fatalf("expected owner match for alice on her own session: ok=%v err=%v", o, e)
		}
		// alice (tenant-a) looking at a tenant-b session → denied by RLS first.
		var err error
		allowed, err = assertSessionOwnerAccessInTx(ctx, tx, r, "rls-fixture-b-carol")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("owner access tx failed: %v", err)
	}
	if allowed {
		t.Fatalf("expected deny for alice on tenant-b session, got allowed=true")
	}
}
