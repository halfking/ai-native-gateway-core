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

// TestIssueMonthlyBill_RealDB 是 07（月账单）的真库 e2e：在真实 schema 上验证
//  1. 首次出账的金额 = 期内用量 × 单价（期外桶不计入）；
//  2. 重复出账幂等（同一 id，第二次 created=false）；
//  3. 并发出账只产生一条账单（ON CONFLICT 仲裁）；
//  4. paid 之后再出账不改写金额与状态。
//
// 破坏性（写入 maas_* 表）：只在库名以 _test 结尾时运行，并只清理本测试的租户。
//
// 使用专用变量 TEST_MAAS_BILLS_DATABASE_URL，而不是事实源统一下发的 TEST_DATABASE_URL：
// 后者若指向非 _test 库，本用例会静默死腿（守卫 DatasourceEnvTargetGuard 据此判红）。
// 变量已配置却库名不合规 ⇒ Fatal（显式失败，不静默跳过）；未配置 ⇒ Skip。
func TestIssueMonthlyBill_RealDB(t *testing.T) {
	dsn := os.Getenv("TEST_MAAS_BILLS_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_MAAS_BILLS_DATABASE_URL not set — monthly bill e2e needs a dedicated *_test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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
		t.Fatalf("refusing to write maas tables in %q; TEST_MAAS_BILLS_DATABASE_URL must point at a dedicated *_test database", database)
	}

	svc := NewService(pool)
	if svc == nil {
		t.Fatal("NewService returned nil for a live pool")
	}
	settings, err := svc.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings on a fresh install failed — maas_settings id=1 is not seeded: %v", err)
	}

	period := "2026-08"
	start, _, err := PeriodBounds(period, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UnixNano()
	tenantA := fmt.Sprintf("t_bill_rdb_a_%d", stamp)
	tenantB := fmt.Sprintf("t_bill_rdb_b_%d", stamp)
	// maas_monthly_bills.tenant_id 外键指向 tenants(code)：先建测试租户行。
	for _, tn := range []string{tenantA, tenantB} {
		if _, err := pool.Exec(ctx, `INSERT INTO tenants (code, name) VALUES ($1, $1)`, tn); err != nil {
			t.Fatalf("create test tenant %s: %v", tn, err)
		}
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		for _, tn := range []string{tenantA, tenantB} {
			_, _ = pool.Exec(cctx, `DELETE FROM maas_monthly_bills WHERE tenant_id = $1`, tn)
			_, _ = pool.Exec(cctx, `DELETE FROM maas_credit_consumption_buckets WHERE tenant_id = $1`, tn)
			_, _ = pool.Exec(cctx, `DELETE FROM tenants WHERE code = $1`, tn)
		}
	})

	// 期内 100+250 = 350 credits / 2+5 = 7 requests；期外 999 / 9 必须被排除。
	seed := func(tenant string) {
		t.Helper()
		_, err := pool.Exec(ctx, `
			INSERT INTO maas_credit_consumption_buckets (tenant_id, bucket_start, credits, request_count)
			VALUES ($1, $2, 100, 2), ($1, $3, 250, 5), ($1, $4, 999, 9)
		`, tenant, start.Add(time.Hour), start.AddDate(0, 0, 5), start.AddDate(0, 1, 0).Add(time.Hour))
		if err != nil {
			t.Fatalf("seed buckets: %v", err)
		}
	}
	seed(tenantA)

	// 1) 首次出账
	first, err := svc.IssueMonthlyBill(ctx, tenantA, period)
	if err != nil {
		t.Fatalf("first issue: %v", err)
	}
	if !first.Created {
		t.Fatal("first issue must report created=true")
	}
	if first.TotalCredits != 350 || first.TotalRequests != 7 {
		t.Fatalf("period consumption = %d credits / %d requests, want 350 / 7 (期外桶未被排除？)",
			first.TotalCredits, first.TotalRequests)
	}
	if want := AmountCentsFromCredits(350, settings.CentsPerCredit); first.AmountCents != want {
		t.Fatalf("amount_cents = %d, want %d (350 credits × cents_per_credit=%v)",
			first.AmountCents, want, settings.CentsPerCredit)
	}

	// 2) 幂等：同一租户同一账期再出一次 ⇒ 同一条账单
	again, err := svc.IssueMonthlyBill(ctx, tenantA, period)
	if err != nil {
		t.Fatalf("second issue: %v", err)
	}
	if again.ID != first.ID || again.Created {
		t.Fatalf("re-issue must be idempotent: id %d→%d created=%v", first.ID, again.ID, again.Created)
	}

	// 3) 并发：8 路同时出账同一新账期 ⇒ 只能有一条账单、且只有一路 created=true
	seed(tenantB)
	const workers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	createdCount := 0
	ids := map[int64]struct{}{}
	errs := []error{}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := svc.IssueMonthlyBill(ctx, tenantB, period)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			ids[b.ID] = struct{}{}
			if b.Created {
				createdCount++
			}
		}()
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("concurrent issue errors: %v", errs)
	}
	if len(ids) != 1 {
		t.Fatalf("concurrent issue produced %d distinct bills, want 1", len(ids))
	}
	if createdCount != 1 {
		t.Fatalf("concurrent issue created=true %d times, want exactly 1", createdCount)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM maas_monthly_bills WHERE tenant_id = $1`, tenantB).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("maas_monthly_bills rows for tenant B = %d, want 1", rows)
	}

	// 4) paid 之后：用量再涨，重新出账不得改写金额与状态
	if _, err := pool.Exec(ctx, `UPDATE maas_monthly_bills SET status = 'paid' WHERE tenant_id = $1`, tenantA); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO maas_credit_consumption_buckets (tenant_id, bucket_start, credits, request_count)
		VALUES ($1, $2, 1000, 10)
	`, tenantA, start.AddDate(0, 0, 9)); err != nil {
		t.Fatal(err)
	}
	after, err := svc.IssueMonthlyBill(ctx, tenantA, period)
	if err != nil {
		t.Fatalf("issue after paid: %v", err)
	}
	if after.Status != "paid" || after.TotalCredits != 350 || after.AmountCents != first.AmountCents {
		t.Fatalf("paid bill was rewritten: status=%s credits=%d amount=%d (want paid/350/%d)",
			after.Status, after.TotalCredits, after.AmountCents, first.AmountCents)
	}
}
