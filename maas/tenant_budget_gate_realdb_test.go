package maas

// 租户月度预算门（W1-Q1，迁移 996）真库 e2e：在真实 schema 上验证
//  1. 未配置预算（无行）⇒ (nil, nil) —— 不是 0 预算也不是故障；
//  2. monthly_budget_cents=0 ⇒ (nil, nil)（938「0=不设预算」口径）；
//  3. warn 超限 ⇒ verdict.Over=true 且 mode=warn（调用方只记日志）；
//  4. hard 超限 ⇒ verdict.Over=true 且 mode=hard（调用方 429）；
//  5. hard 未超限 ⇒ Over=false（放行）；
//  6. default 租户豁免在 Service 层生效（nil, nil）；
//  7. UpsertTenantBudget enforcement_mode 闭集校验 + 落库回读。
//
// 环境要求：TEST_MAAS_BUDGET_GATE_DATABASE_URL 指向已应用全部 startup
// 迁移（含 938 + 1000）的专用 *_test 库；未配置 ⇒ Skip。
// ★ 月界竞态注记：门按「当前 UTC 月」求和，种子桶用同口径 monthStart；
//   恰在月界最后一秒跑测试时理论上可能跨月（窗口微秒级，复跑即可）。

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTenantBudgetGate_RealDB(t *testing.T) {
	dsn := os.Getenv("TEST_MAAS_BUDGET_GATE_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_MAAS_BUDGET_GATE_DATABASE_URL not set — tenant budget gate e2e needs a dedicated *_test database")
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
		t.Fatalf("refusing to write tenant_budgets in %q; TEST_MAAS_BUDGET_GATE_DATABASE_URL must point at a dedicated *_test database", database)
	}

	svc := NewService(pool)

	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	stamp := now.UnixNano()
	// 未配置预算的对照租户（不插 tenant_budgets 行）。
	tenantUnset := fmt.Sprintf("t_gate_unset_%d", stamp)
	tenantWarn := fmt.Sprintf("t_gate_warn_%d", stamp)
	tenantHard := fmt.Sprintf("t_gate_hard_%d", stamp)
	tenantHardUnder := fmt.Sprintf("t_gate_hard_under_%d", stamp)

	seedBudget := func(tenant string, budgetCents int64, mode string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO tenant_budgets (tenant_id, monthly_budget_cents, enforcement_mode)
			VALUES ($1, $2, $3)
			ON CONFLICT (tenant_id) DO UPDATE
			   SET monthly_budget_cents = EXCLUDED.monthly_budget_cents,
			       enforcement_mode = EXCLUDED.enforcement_mode
		`, tenant, budgetCents, mode); err != nil {
			t.Fatalf("seed tenant_budgets %s: %v", tenant, err)
		}
	}
	seedBudget(tenantWarn, 100, BudgetEnforcementWarn) // 花费 300 cents（150 credits × 2）⇒ 超
	seedBudget(tenantHard, 100, BudgetEnforcementHard) // 同上 ⇒ 超且 hard
	seedBudget(tenantHardUnder, 10000, BudgetEnforcementHard)
	seedBudget(tenantUnset, 0, BudgetEnforcementWarn) // 0 = 不设预算
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		for _, tn := range []string{tenantUnset, tenantWarn, tenantHard, tenantHardUnder} {
			_, _ = pool.Exec(cctx, `DELETE FROM tenant_budgets WHERE tenant_id = $1`, tn)
			_, _ = pool.Exec(cctx, `DELETE FROM maas_credit_consumption_buckets WHERE tenant_id = $1`, tn)
		}
	})

	// 本月桶：150 credits；cents_per_credit 取库内现值（maas_settings seed）。
	seedBucket := func(tenant string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO maas_credit_consumption_buckets (tenant_id, bucket_start, credits, request_count)
			VALUES ($1, $2, 100, 2), ($1, $3, 50, 1)
		`, tenant, monthStart.Add(time.Hour), monthStart.Add(2*time.Hour)); err != nil {
			t.Fatalf("seed buckets %s: %v", tenant, err)
		}
	}
	for _, tn := range []string{tenantWarn, tenantHard, tenantHardUnder} {
		seedBucket(tn)
	}

	settings, err := svc.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if settings.CentsPerCredit <= 0 {
		t.Fatalf("测试库 cents_per_credit=%v ≤ 0 ⇒ 门惰性算不出超限（请检查 maas_settings 种子）", settings.CentsPerCredit)
	}

	// 1) 未配置预算（真无行）⇒ (nil, nil)
	if v, err := svc.CheckMonthlyBudget(ctx, tenantUnset+"_no_row_at_all"); v != nil || err != nil {
		t.Fatalf("无行租户应 (nil,nil)，got verdict=%v err=%v", v, err)
	}
	// 2) 0 预算 ⇒ (nil, nil)
	if v, err := svc.CheckMonthlyBudget(ctx, tenantUnset); v != nil || err != nil {
		t.Fatalf("0 预算租户应 (nil,nil)，got verdict=%v err=%v", v, err)
	}
	// 3) default 租户豁免（Service 层）
	if v, err := svc.CheckMonthlyBudget(ctx, "default"); v != nil || err != nil {
		t.Fatalf("default 租户应豁免 (nil,nil)，got verdict=%v err=%v", v, err)
	}

	// 4/5/6) warn 超限 / hard 超限 / hard 未超限
	assertVerdict := func(tenant, wantMode string, wantOver bool, budgetCents int64) {
		t.Helper()
		v, err := svc.CheckMonthlyBudget(ctx, tenant)
		if err != nil {
			t.Fatalf("CheckMonthlyBudget(%s): %v", tenant, err)
		}
		if v == nil {
			t.Fatalf("verdict 不应为 nil（tenant=%s budget>0）", tenant)
		}
		if v.EnforcementMode != wantMode {
			t.Fatalf("mode=%q want %q（enforcement_mode 回读失败？）", v.EnforcementMode, wantMode)
		}
		if v.Over != wantOver {
			t.Fatalf("over=%v want %v（tenant=%s spent=%d budget=%d）",
				v.Over, wantOver, tenant, v.SpentCents, v.MonthlyBudgetCents)
		}
		if v.MonthlyBudgetCents != budgetCents {
			t.Fatalf("budget 回读=%d want %d", v.MonthlyBudgetCents, budgetCents)
		}
		if v.SpentCents <= 0 && wantOver {
			t.Fatalf("花费=%d 但期望超限（本月桶种子没算进来？monthStart=%v）", v.SpentCents, monthStart)
		}
	}
	assertVerdict(tenantWarn, BudgetEnforcementWarn, true, 100)
	assertVerdict(tenantHard, BudgetEnforcementHard, true, 100)
	assertVerdict(tenantHardUnder, BudgetEnforcementHard, false, 10000)

	// 7) Upsert 闭集校验 + 落库回读（enforcement_mode 贯通）
	if _, err := svc.UpsertTenantBudget(ctx, tenantWarn, 100, 0.2, 0.8, nil, "enforce"); err == nil {
		t.Fatalf("非法 enforcement_mode 必须被拒（闭集 {warn,hard}）")
	}
	up, err := svc.UpsertTenantBudget(ctx, tenantWarn, 200, 0.2, 0.8, nil, BudgetEnforcementHard)
	if err != nil {
		t.Fatalf("UpsertTenantBudget(hard): %v", err)
	}
	if up.EnforcementMode != BudgetEnforcementHard || up.MonthlyBudgetCents != 200 {
		t.Fatalf("upsert 回读漂移: %+v", up)
	}
	got, err := svc.GetTenantBudget(ctx, tenantWarn)
	if err != nil {
		t.Fatalf("GetTenantBudget: %v", err)
	}
	if got.EnforcementMode != BudgetEnforcementHard {
		t.Fatalf("GET 回读 enforcement_mode=%q want %q（W1-Q1 贯通面断裂）", got.EnforcementMode, BudgetEnforcementHard)
	}
	// 空串 = 落默认 warn（服务端同口径）
	up2, err := svc.UpsertTenantBudget(ctx, tenantWarn, 200, 0.2, 0.8, nil, "")
	if err != nil {
		t.Fatalf("UpsertTenantBudget(empty): %v", err)
	}
	if up2.EnforcementMode != BudgetEnforcementWarn {
		t.Fatalf("空串 enforcement_mode 应落默认 warn，got %q", up2.EnforcementMode)
	}
}
