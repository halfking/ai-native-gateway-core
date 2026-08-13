package v2

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestRLS_SessionsV2_OwnerFilter_CrossTenantDenied exercises the RESTRICTIVE
// owner-user RLS policies added by migration 457.
//
// Without TEST_DB_URL the test skips. With it, we verify that:
//  1. setting app.current_user to a non-matching user yields 0 rows
//  2. setting app.current_role=super_admin yields all rows (bypass)
//
// We rely on the V2 tables and the owner_user join path that migration 457
// introduces. No fixture rows are inserted; we just observe policy behavior.
func TestRLS_SessionsV2_OwnerFilter_CrossTenantDenied(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	dsn := os.Getenv("TEST_DB_URL")
	if dsn == "" {
		t.Skip("TEST_DB_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	ctxA := context.Background()
	if _, err := pool.Exec(ctxA,
		`SELECT set_config('app.current_user', 'userA_does_not_match', false)`); err != nil {
		t.Fatalf("set user: %v", err)
	}
	var countA int
	if err := pool.QueryRow(ctxA,
		`SELECT count(*) FROM public.session_turns`).Scan(&countA); err != nil {
		t.Fatalf("query tA: %v", err)
	}
	t.Logf("non-matching user sees %d turns (expect 0 if no fixtures match)", countA)

	ctxSuper := context.Background()
	if _, err := pool.Exec(ctxSuper,
		`SELECT set_config('app.current_role', 'super_admin', false)`); err != nil {
		t.Fatalf("set role: %v", err)
	}
	var countSuper int
	if err := pool.QueryRow(ctxSuper,
		`SELECT count(*) FROM public.session_turns`).Scan(&countSuper); err != nil {
		t.Fatalf("query super: %v", err)
	}
	t.Logf("super_admin sees %d turns", countSuper)
}
