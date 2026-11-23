package maas

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestChargeRequest_IdempotentConcurrentReplay_RealDB verifies that two
// terminal replays for one tenant/request debit once and return the same
// aggregate even when the first debit is split across ledger pools.
func TestChargeRequest_IdempotentConcurrentReplay_RealDB(t *testing.T) {
	dsn := os.Getenv("TEST_MAAS_CHARGE_IDEMPOTENCY_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_MAAS_CHARGE_IDEMPOTENCY_DATABASE_URL not set — charge idempotency requires a dedicated *_test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	var database string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		t.Fatalf("current_database: %v", err)
	}
	if !strings.HasSuffix(database, "_test") {
		t.Fatalf("refusing to write billing rows in %q; dedicated *_test database required", database)
	}

	stamp := time.Now().UnixNano()
	tenant := fmt.Sprintf("t_charge_replay_%d", stamp)
	requestID := fmt.Sprintf("req_charge_replay_%d", stamp)
	if _, err := pool.Exec(ctx, `INSERT INTO tenants (code, name) VALUES ($1, $1)`, tenant); err != nil {
		t.Fatalf("create test tenant: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO tenant_credit_wallets (tenant_id, granted_balance, purchased_balance, balance_credits)
		VALUES ($1, 1000000, 1000000, 2000000)
	`, tenant); err != nil {
		t.Fatalf("create test wallet: %v", err)
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer ccancel()
		_, _ = pool.Exec(cctx, `DELETE FROM credit_ledger_hot WHERE tenant_id = $1 AND ref_type = 'request' AND ref_id = $2`, tenant, requestID)
		_, _ = pool.Exec(cctx, `DELETE FROM maas_credit_consumption_buckets WHERE tenant_id = $1`, tenant)
		_, _ = pool.Exec(cctx, `DELETE FROM tenant_credit_wallets WHERE tenant_id = $1`, tenant)
		_, _ = pool.Exec(cctx, `DELETE FROM tenants WHERE code = $1`, tenant)
	})

	svc := NewService(pool)
	usage := TokenUsage{PromptTokens: 100000}
	var wg sync.WaitGroup
	results := make(chan int64, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			charged, err := svc.ChargeRequestMultimodal(ctx, tenant, requestID, "", usage)
			results <- charged
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("charge replay: %v", err)
		}
	}
	var first int64
	for charged := range results {
		if charged <= 0 {
			t.Fatalf("charged=%d, want positive amount", charged)
		}
		if first == 0 {
			first = charged
		} else if charged != first {
			t.Fatalf("replay returned %d, first charge was %d", charged, first)
		}
	}

	var posted int64
	var rows int
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(-amount), 0)::bigint, COUNT(*)
		FROM credit_ledger_with_current_month
		WHERE tenant_id = $1 AND ref_type = 'request' AND ref_id = $2
		  AND entry_type = 'consume' AND amount < 0
	`, tenant, requestID).Scan(&posted, &rows); err != nil {
		t.Fatalf("read posted ledger: %v", err)
	}
	if posted != first {
		t.Fatalf("posted total=%d, returned=%d", posted, first)
	}
	if rows == 0 || rows > 2 {
		t.Fatalf("consume rows=%d; want one or legitimate pool split (max two for this fixture)", rows)
	}
	var balance int64
	if err := pool.QueryRow(ctx, `SELECT balance_credits FROM tenant_credit_wallets WHERE tenant_id = $1`, tenant).Scan(&balance); err != nil {
		t.Fatalf("read balance: %v", err)
	}
	if balance != 2000000-first {
		t.Fatalf("balance=%d; want initial balance minus one charge (%d)", balance, 2000000-first)
	}
}
