package maas

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/schemaobj"
)

// Each test uses its own schema in a dedicated *_test database. Catalog DDL
// comes from the real objects/migrations; no production tables are modified.
func commerceAuditDB(t *testing.T) (*Service, context.Context) {
	t.Helper()
	dsn := os.Getenv("TEST_MAAS_BILLS_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_MAAS_BILLS_DATABASE_URL unset; commerce audit requires a dedicated *_test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(cfg.ConnConfig.Database, "_test") {
		t.Fatal("commerce audit refuses a database without the _test suffix")
	}
	schema := fmt.Sprintf("commerce_audit_%d", time.Now().UnixNano())
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := pool.Exec(context.WithoutCancel(ctx), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		if err != nil {
			t.Errorf("cleanup commerce schema: %v", err)
		}
	})
	ddl := schemaobj.Table(t, "../sql/objects/tables/schema_migrations.sql",
		"../sql/objects/constraints/schema_migrations_schema_migrations_pkey.sql")
	for _, table := range []string{"subscription_plans", "topup_packages", "billing_orders", "tenant_credit_wallets", "credit_ledger_hot", "credit_ledger"} {
		if table == "credit_ledger_hot" {
			// The per-object sequence file only contains OWNED BY; its CREATE
			// lives in the baseline. Identity values do not affect these tests.
			ddl += "CREATE SEQUENCE public.credit_ledger_partitioned_id_seq;"
		}
		ddl += schemaobj.Table(t, "../sql/objects/tables/"+table+".sql")
	}
	ddl += schemaobj.Table(t, "../sql/objects/constraints/tenant_credit_wallets_tenant_credit_wallets_pkey.sql")
	ddl += `CREATE FUNCTION public.get_current_tenant() RETURNS text LANGUAGE sql AS 'SELECT current_setting(''app.tenant_id'', true)';`
	if _, err := pool.Exec(ctx, strings.ReplaceAll(ddl, "public.", schema+".")); err != nil {
		t.Fatalf("commerce objects: %v", err)
	}
	for _, migration := range []string{"934_subscription_plan_commerce_columns.sql", "935_credit_ledger_owner_campaign_detail.sql", "937_promotion_campaigns.sql", "952_model_price_versions.sql"} {
		b, err := os.ReadFile("../sql/migrations/startup/" + migration)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, strings.ReplaceAll(string(b), "public.", schema+".")); err != nil {
			t.Fatalf("migration %s: %v", migration, err)
		}
	}
	return NewService(pool), ctx
}

func TestCommerceAuditGiftBudget_RealDB(t *testing.T) {
	svc, ctx := commerceAuditDB(t)
	_, err := svc.pool.Exec(ctx, `INSERT INTO promotion_campaigns
		(code, name, benefit_type, benefit_value, status, total_budget_credits)
		VALUES ('gift-budget', 'Gift', 'gift', '{"credits":60}', 'active', 100)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RedeemCampaign(ctx, "gift-budget", "tenant-a", "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RedeemCampaign(ctx, "gift-budget", "tenant-b", "owner"); err == nil {
		t.Fatal("gift redemption exceeded the 100-credit budget")
	}
	var granted, ledger, wallets int64
	if err := svc.pool.QueryRow(ctx, `SELECT granted_credits,
		(SELECT sum(amount) FROM credit_ledger_hot),
		(SELECT count(*) FROM tenant_credit_wallets)
		FROM promotion_campaigns WHERE code = 'gift-budget'`).Scan(&granted, &ledger, &wallets); err != nil {
		t.Fatal(err)
	}
	if granted != 60 || ledger != 60 || wallets != 1 {
		t.Fatalf("rejected gift changed balances: granted=%d ledger=%d wallets=%d", granted, ledger, wallets)
	}
	for _, accounting := range []struct{ budget, granted int64 }{{100, -1}, {-1, 0}, {100, 1<<63 - 1}} {
		if _, err := svc.pool.Exec(ctx, `UPDATE promotion_campaigns SET total_budget_credits=$1, granted_credits=$2`, accounting.budget, accounting.granted); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.RedeemCampaign(ctx, "gift-budget", "tenant-c", "owner"); err == nil {
			t.Fatalf("invalid or exhausted accounting accepted: %+v", accounting)
		}
	}
}

func TestCommerceAuditFirstPurchaseConfirm_RealDB(t *testing.T) {
	svc, ctx := commerceAuditDB(t)
	_, err := svc.pool.Exec(ctx, `INSERT INTO topup_packages
		(id, code, tier, name, price_cents, credits_amount, first_time_only, bonus_credits)
		VALUES (1, 'first', 'small', 'First', 100, 1000, true, 100);
		INSERT INTO billing_orders
		(id, order_no, tenant_id, order_type, amount_cents, credits, package_id, expires_at)
		VALUES (1, 'order-1', 'tenant-a', 'topup', 100, 1000, 1, now()+interval '1 hour'),
		(2, 'order-2', 'tenant-a', 'topup', 100, 1000, 1, now()+interval '1 hour')`)
	if err != nil {
		t.Fatal(err)
	}
	// Two pre-existing pending orders must not both claim first-purchase credit,
	// including concurrent manual/payment confirmations.
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []int64{1, 2} {
		wg.Add(1)
		go func() { defer wg.Done(); results <- svc.ConfirmOrder(ctx, id, "audit") }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !strings.Contains(err.Error(), "one purchase per tenant") {
			t.Errorf("unexpected confirmation failure: %v", err)
		}
	}
	var paid, balance, entries int64
	if err := svc.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM billing_orders WHERE status='paid'),
		COALESCE((SELECT purchased_balance FROM tenant_credit_wallets WHERE tenant_id='tenant-a'),0),
		(SELECT count(*) FROM credit_ledger_hot)`).Scan(&paid, &balance, &entries); err != nil {
		t.Fatal(err)
	}
	if successes != 1 || paid != 1 || balance != 1100 || entries != 2 {
		t.Fatalf("first purchase: successful=%d paid=%d balance=%d ledger=%d; want 1/1/1100/2", successes, paid, balance, entries)
	}
	// Reconfirming the paid order is idempotent. A regular package still
	// permits the other order once the first-purchase restriction is removed.
	var paidID, pendingID int64
	if err := svc.pool.QueryRow(ctx, `SELECT id FROM billing_orders WHERE status='paid'`).Scan(&paidID); err != nil {
		t.Fatal(err)
	}
	if err := svc.pool.QueryRow(ctx, `SELECT id FROM billing_orders WHERE status='pending'`).Scan(&pendingID); err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfirmOrder(ctx, paidID, "again"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.pool.Exec(ctx, `UPDATE topup_packages SET first_time_only=false WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfirmOrder(ctx, pendingID, "regular"); err != nil {
		t.Fatalf("ordinary package repeat purchase: %v", err)
	}
	if err := svc.pool.QueryRow(ctx, `SELECT purchased_balance FROM tenant_credit_wallets WHERE tenant_id='tenant-a'`).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 2200 {
		t.Fatalf("repeat confirmation double-credited or ordinary purchase failed: balance=%d", balance)
	}
}
