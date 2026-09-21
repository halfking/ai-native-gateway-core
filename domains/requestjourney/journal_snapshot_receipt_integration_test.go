//go:build integration

package requestjourney

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const receiptIntegrationIsolationEnv = "TEST_PG_CONTRACTS_ISOLATED"

func openReceiptIntegrationPool(t *testing.T, env string) *pgxpool.Pool {
	t.Helper()
	if os.Getenv(receiptIntegrationIsolationEnv) != "1" {
		t.Skipf("%s=1 is required; this test mutates an isolated PostgreSQL database", receiptIntegrationIsolationEnv)
	}
	dsn := os.Getenv(env)
	if dsn == "" {
		t.Skipf("%s is not set; skipping PostgreSQL contract test", env)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New(%s): %v", env, err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("%s is unreachable", env)
	}
	var database string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		t.Fatalf("read database identity: %v", err)
	}
	if !strings.HasSuffix(database, "_test") {
		t.Fatalf("refusing mutating PostgreSQL contract test against database %q; use a dedicated *_test database", database)
	}
	return pool
}

func requireReceiptSchema(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.journal_snapshot_receipts') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatalf("receipt table probe: %v", err)
	}
	if !exists {
		t.Fatalf("journal_snapshot_receipts is missing; apply migration 618 to the isolated database")
	}
	var forced bool
	if err := pool.QueryRow(ctx, `
		SELECT c.relforcerowsecurity
		FROM pg_class c
		JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname='public' AND c.relname='journal_snapshot_receipts'`).Scan(&forced); err != nil {
		t.Fatalf("receipt RLS probe: %v", err)
	}
	if !forced {
		t.Fatal("journal_snapshot_receipts must use FORCE ROW LEVEL SECURITY")
	}
	var writerBypass bool
	if err := pool.QueryRow(ctx, `SELECT pg_has_role(current_user, 'llm_gateway_rls_bypass', 'member')`).Scan(&writerBypass); err != nil {
		t.Fatalf("read writer bypass role membership: %v", err)
	}
	if !writerBypass {
		t.Fatal("TEST_DATABASE_URL role must be a member of llm_gateway_rls_bypass")
	}
}

func TestJournalSnapshotReceiptRealPG(t *testing.T) {
	pool := openReceiptIntegrationPool(t, "TEST_DATABASE_URL")
	requireReceiptSchema(t, pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	suffix := time.Now().UTC().Format("20060102150405.000000000")
	alphaTenant := "pg-it-receipt-alpha-" + suffix
	betaTenant := "pg-it-receipt-beta-" + suffix
	requestID := "pg-it-receipt-request-" + suffix
	version := int64(1)
	hash := "hash-" + suffix

	cleanup := func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		tx, err := pool.Begin(cleanupCtx)
		if err != nil {
			t.Errorf("cleanup begin: %v", err)
			return
		}
		defer tx.Rollback(context.Background()) //nolint:errcheck
		if _, err := tx.Exec(cleanupCtx, `SELECT set_config('app.bypass_rls', 'true', true)`); err != nil {
			t.Errorf("cleanup visibility: %v", err)
			return
		}
		if _, err := tx.Exec(cleanupCtx, `DELETE FROM journal_snapshot_receipts WHERE tenant_id IN ($1,$2)`, alphaTenant, betaTenant); err != nil {
			t.Errorf("cleanup receipts: %v", err)
			return
		}
		if err := tx.Commit(cleanupCtx); err != nil {
			t.Errorf("cleanup commit: %v", err)
		}
	}
	t.Cleanup(cleanup)

	storeA := NewPostgresJournalSnapshotReceiptStore(pool, "receipt-it-a")
	storeB := NewPostgresJournalSnapshotReceiptStore(pool, "receipt-it-b")
	claimA, err := storeA.Claim(ctx, alphaTenant, requestID, version, hash)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if !claimA.Claimed || claimA.AlreadyCompleted || claimA.Owner != "receipt-it-a" {
		t.Fatalf("first claim = %+v, want claimed by receipt-it-a", claimA)
	}

	liveClaim, err := storeB.Claim(ctx, alphaTenant, requestID, version, hash)
	if err != nil {
		t.Fatalf("live second claim: %v", err)
	}
	if liveClaim.Claimed || liveClaim.AlreadyCompleted {
		t.Fatalf("live second claim = %+v, want unclaimed", liveClaim)
	}
	if err := storeB.Complete(ctx, liveClaim); !errors.Is(err, ErrSnapshotReceiptLeaseLost) {
		t.Fatalf("non-owner completion = %v, want ErrSnapshotReceiptLeaseLost", err)
	}

	if _, err := storeB.Claim(ctx, alphaTenant, requestID, version, "different-"+hash); !errors.Is(err, ErrSnapshotReceiptConflict) {
		t.Fatalf("hash conflict = %v, want ErrSnapshotReceiptConflict", err)
	}
	if err := storeA.Complete(ctx, claimA); err != nil {
		t.Fatalf("complete first claim: %v", err)
	}
	completed, err := storeB.Claim(ctx, alphaTenant, requestID, version, hash)
	if err != nil {
		t.Fatalf("completed replay claim: %v", err)
	}
	if !completed.AlreadyCompleted || completed.Claimed {
		t.Fatalf("completed replay claim = %+v, want already completed", completed)
	}

	// Reclaim a different identity after expiring the original lease and fence
	// the stale owner without waiting for the production one-minute lease.
	reclaimRequest := requestID + "-reclaim"
	reclaimA, err := storeA.Claim(ctx, alphaTenant, reclaimRequest, version, hash)
	if err != nil || !reclaimA.Claimed {
		t.Fatalf("reclaim setup claim = %+v, err=%v", reclaimA, err)
	}
	adminTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lease expiry: %v", err)
	}
	if _, err := adminTx.Exec(ctx, `SELECT set_config('app.bypass_rls', 'true', true)`); err != nil {
		adminTx.Rollback(context.Background())
		t.Fatalf("set lease expiry bypass: %v", err)
	}
	if _, err := adminTx.Exec(ctx, `UPDATE journal_snapshot_receipts SET claim_until=now()-interval '1 second' WHERE tenant_id=$1 AND request_id=$2 AND snapshot_version=$3`, alphaTenant, reclaimRequest, version); err != nil {
		adminTx.Rollback(context.Background())
		t.Fatalf("expire lease: %v", err)
	}
	if err := adminTx.Commit(ctx); err != nil {
		t.Fatalf("commit lease expiry: %v", err)
	}
	reclaimB, err := storeB.Claim(ctx, alphaTenant, reclaimRequest, version, hash)
	if err != nil || !reclaimB.Claimed || reclaimB.Owner != "receipt-it-b" {
		t.Fatalf("reclaim claim = %+v, err=%v", reclaimB, err)
	}
	if err := storeA.Complete(ctx, reclaimA); !errors.Is(err, ErrSnapshotReceiptLeaseLost) {
		t.Fatalf("stale completion after reclaim = %v, want ErrSnapshotReceiptLeaseLost", err)
	}
	if err := storeB.Complete(ctx, reclaimB); err != nil {
		t.Fatalf("complete reclaimed receipt: %v", err)
	}

	seedReceipt := func(tenant, req string) {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin seed receipt: %v", err)
		}
		defer tx.Rollback(context.Background()) //nolint:errcheck
		if _, err := tx.Exec(ctx, `SELECT set_config('app.bypass_rls', 'true', true)`); err != nil {
			t.Fatalf("seed bypass: %v", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO journal_snapshot_receipts (tenant_id,request_id,snapshot_version,payload_hash,status,claim_owner,claim_until) VALUES ($1,$2,1,$3,'processing','seed',now()+interval '1 hour')`, tenant, req, hash); err != nil {
			t.Fatalf("seed receipt: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit seed receipt: %v", err)
		}
	}
	seedReceipt(alphaTenant, requestID+"-rls")
	seedReceipt(betaTenant, requestID+"-rls")
	verifyReceiptRLS(t, alphaTenant, betaTenant, requestID+"-rls")
}

func verifyReceiptRLS(t *testing.T, alphaTenant, betaTenant, requestID string) {
	t.Helper()
	pool := openReceiptIntegrationPool(t, "TEST_TENANT_DATABASE_URL")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var superuser, bypassRLS, tenantBypass bool
	if err := pool.QueryRow(ctx, `SELECT rolsuper, rolbypassrls, pg_has_role(current_user, 'llm_gateway_rls_bypass', 'member') FROM pg_roles WHERE rolname=current_user`).Scan(&superuser, &bypassRLS, &tenantBypass); err != nil {
		t.Fatalf("read tenant role flags: %v", err)
	}
	if superuser || bypassRLS || tenantBypass {
		t.Fatalf("TEST_TENANT_DATABASE_URL role must be NOSUPERUSER, NOBYPASSRLS, and outside llm_gateway_rls_bypass; got superuser=%t bypassrls=%t role_member=%t", superuser, bypassRLS, tenantBypass)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin receipt RLS probe: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, alphaTenant); err != nil {
		tx.Rollback(context.Background())
		t.Fatalf("set receipt tenant: %v", err)
	}
	var alphaCount, betaCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE tenant_id=$1), count(*) FILTER (WHERE tenant_id=$2) FROM journal_snapshot_receipts WHERE request_id=$3`, alphaTenant, betaTenant, requestID).Scan(&alphaCount, &betaCount); err != nil {
		tx.Rollback(context.Background())
		t.Fatalf("receipt tenant RLS query: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit receipt RLS probe: %v", err)
	}
	if alphaCount != 1 || betaCount != 0 {
		t.Fatalf("receipt RLS saw alpha=%d beta=%d, want 1/0", alphaCount, betaCount)
	}

	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin unset receipt probe: %v", err)
	}
	var total int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM journal_snapshot_receipts WHERE request_id=$1`, requestID).Scan(&total); err != nil {
		tx.Rollback(context.Background())
		t.Fatalf("unset receipt RLS query: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit unset receipt probe: %v", err)
	}
	if total != 0 {
		t.Fatalf("unset receipt tenant saw %d rows, want 0", total)
	}
}
