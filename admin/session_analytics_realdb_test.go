package admin

import (
	"context"
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
	if _, err := conn.Exec(ctx,
		`INSERT INTO session_dim (gw_session_id, tenant_id, owner_user, created_at)
		 VALUES ($1,$2,$3,NOW())
		 ON CONFLICT (gw_session_id) DO UPDATE SET tenant_id=EXCLUDED.tenant_id, owner_user=EXCLUDED.owner_user`,
		gwSessionID, tenant, owner,
	); err != nil {
		t.Fatalf("seed session_dim: %v", err)
	}
	if _, err := conn.Exec(ctx,
		`INSERT INTO session_summaries (session_key, tenant_id, first_request_at, last_request_at, updated_at)
		 VALUES ($1,$2,NOW(),NOW(),NOW())
		 ON CONFLICT (session_key) DO UPDATE SET tenant_id=EXCLUDED.tenant_id`,
		gwSessionID, tenant,
	); err != nil {
		t.Fatalf("seed session_summaries: %v", err)
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(ctx, `DELETE FROM session_summaries WHERE session_key=$1`, gwSessionID)
		_, _ = conn.Exec(ctx, `DELETE FROM session_dim WHERE gw_session_id=$1`, gwSessionID)
	}
}

// TestRLS_TenantIsolationWithFixture verifies that withTenantTx blocks
// cross-tenant reads when the GUC is set to a single tenant. This is the
// minimum defense-in-depth contract; the application-layer WHERE in handlers
// is expected to align with it.
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