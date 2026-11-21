package maas

// 租户月度预算门（W1-Q1，迁移 996）单元测试：
//   1. Go 侧 enforcement_mode 闭集常量与 DB 侧 CHECK 同口径（迁移 996 的
//      sqlguard 交付门从字符串侧对拍，本测试从常量侧对拍 —— 两侧任一漂移
//      都会红）；
//   2. 判定算术（evaluateMonthlyBudget）：warn/hard、边界（spent==budget）、
//      换算率 ≤0 ⇒ 门惰性（与账单 AmountCentsFromCredits 同口径）。
//
// 真库 e2e（含 RLS/pool 语义与 buckets 求和）见
// tenant_budget_gate_realdb_test.go（专用 DSN 未配置时 Skip）。

import (
	"errors"
	"testing"
)

func TestBudgetEnforcementModeClosedSet(t *testing.T) {
	// 与迁移 996 的 CHECK (enforcement_mode IN ('warn','hard')) 逐字对拍。
	got := map[string]bool{
		BudgetEnforcementWarn: true,
		BudgetEnforcementHard: true,
	}
	if len(got) != 2 || !got["warn"] || !got["hard"] {
		t.Fatalf("enforcement mode 常量闭集漂移: %v", got)
	}
	if BudgetEnforcementWarn != "warn" || BudgetEnforcementHard != "hard" {
		t.Fatalf("常量字面量必须与迁移 996 CHECK 闭集逐字一致: warn=%q hard=%q",
			BudgetEnforcementWarn, BudgetEnforcementHard)
	}
}

func TestTenantBudgetCheckErrorUnwrap(t *testing.T) {
	inner := errors.New("boom")
	err := &TenantBudgetCheckError{Mode: BudgetEnforcementHard, Err: inner}
	if !errors.Is(err, inner) {
		t.Fatalf("TenantBudgetCheckError 必须 Unwrap 到内层错误（errors.As 分支判定依赖它）")
	}
	appUnknown := &TenantBudgetCheckError{Mode: "", Err: inner}
	if errors.Is(appUnknown, nil) || appUnknown.Mode != "" {
		t.Fatalf("行未读到的检查错误 Mode 必须为空串（调用方据此区分 fail-closed 与放行）")
	}
}

func TestEvaluateMonthlyBudgetModes(t *testing.T) {
	cases := []struct {
		name      string
		mode      string
		budget    int64
		credits   int64
		cps       float64 // cents_per_credit：1 credit 换多少 cents
		wantOver  bool
		wantSpent int64
	}{
		{"hard 达限（边界 spent==budget）", BudgetEnforcementHard, 1000, 500, 2.0, true, 1000},
		{"hard 未达限", BudgetEnforcementHard, 1000, 499, 2.0, false, 998},
		{"hard 刚超过（四舍五入跨限）", BudgetEnforcementHard, 1000, 501, 2.0, true, 1002},
		{"warn 超限只出 verdict 不改行为面", BudgetEnforcementWarn, 1000, 500, 2.0, true, 1000},
		{"换算率 0 ⇒ 花费 0 ⇒ 永不超（门惰性）", BudgetEnforcementHard, 1000, 999999, 0, false, 0},
		{"换算率负 ⇒ 同上", BudgetEnforcementHard, 1000, 42, -1, false, 0},
		{"零花费", BudgetEnforcementWarn, 1000, 0, 2.0, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := evaluateMonthlyBudget("acme", tc.mode, tc.budget, tc.credits, tc.cps)
			if v == nil {
				t.Fatalf("verdict 不应为 nil（budget>0 由调用方保证）")
			}
			if v.Over != tc.wantOver || v.SpentCents != tc.wantSpent {
				t.Fatalf("over=%v spent=%d，want over=%v spent=%d（mode=%s budget=%d credits=%d cps=%v）",
					v.Over, v.SpentCents, tc.wantOver, tc.wantSpent, tc.mode, tc.budget, tc.credits, tc.cps)
			}
			if v.EnforcementMode != tc.mode || v.MonthlyBudgetCents != tc.budget || v.TenantID != "acme" {
				t.Fatalf("verdict 字段回填不全: %+v", v)
			}
		})
	}
}

func TestTenantBudgetExceededErrorMessage(t *testing.T) {
	e := &TenantBudgetExceededError{TenantID: "acme", BudgetCents: 1000, SpentCents: 1200}
	if e.Error() == "" {
		t.Fatalf("错误信息不能为空")
	}
}
