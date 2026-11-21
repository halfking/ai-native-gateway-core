package maas

// 租户月度预算执行门（W1-Q1，迁移 996）。
//
// 938 建的 tenant_budgets 只做「预警 + 通知」，欠费默认放行（A-15）；
// 本文件给同一张表加 enforcement_mode ∈ {warn, hard} 执行开关：
//   warn（默认）= 仅告警不拦截 —— 存量行为零变化；
//   hard        = 当月花费达 monthly_budget_cents ⇒ 网关请求面 429
//                 tenant_budget_exceeded（default 租户与内部回环 key 豁免
//                 由调用方 domains/streaming 把守，与 MaaS credits
//                 pre-check 的豁免口径同一处）。
//
// ★ 口径锚点：花费与 maas_monthly_bills.amount_cents 同源 ——
//   sumPeriodConsumption（本月 buckets 求和）× cents_per_credit
//   （AmountCentsFromCredits）。门与账单不能打架：同一函数、同一换算，
//   禁止另起一套聚合（若账单口径变更，本门自动跟随）。

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Enforcement modes for tenant_budgets.enforcement_mode（迁移 996 闭集，
// DB 侧 CHECK tenant_budgets_enforcement_mode_check 与此同口径）。
const (
	BudgetEnforcementWarn = "warn" // 仅告警不拦截（默认，存量行为零变化）
	BudgetEnforcementHard = "hard" // 达限 429 tenant_budget_exceeded
)

// TenantBudgetExceededError: hard 模式下当月花费已达月度预算。
// 调用方（domains/streaming）据此返回 429 tenant_budget_exceeded。
type TenantBudgetExceededError struct {
	TenantID    string
	BudgetCents int64
	SpentCents  int64
}

func (e *TenantBudgetExceededError) Error() string {
	return fmt.Sprintf("tenant monthly budget exceeded: tenant=%s spent=%d cents >= budget=%d cents",
		e.TenantID, e.SpentCents, e.BudgetCents)
}

// TenantBudgetCheckError wraps a failed gate evaluation. Mode tells the
// caller whether the budget CONFIG was already known when the failure hit:
//
//	Mode == hard ⇒ 门适用性已确认，检查失败 =「不知道超没超」⇒ 调用方可
//	               fail-closed（与 api key 预算门 R89-133 同纪律）；
//	Mode == ""   ⇒ 预算行本身都没读到（适用性未知）⇒ 只能放行 + 响亮记日志
//	               （fail-closed 需要知道门对谁生效，否则误伤全量租户）。
type TenantBudgetCheckError struct {
	Mode string // BudgetEnforcementHard 或 ""（行未读到）
	Err  error
}

func (e *TenantBudgetCheckError) Error() string {
	if e.Mode == "" {
		return fmt.Sprintf("tenant budget gate unavailable (applicability unknown): %v", e.Err)
	}
	return fmt.Sprintf("tenant budget gate check failed (mode=%s): %v", e.Mode, e.Err)
}

func (e *TenantBudgetCheckError) Unwrap() error { return e.Err }

// MonthlyBudgetVerdict is one tenant monthly-budget evaluation. Returned in
// both modes so the caller can warn (log) without blocking.
type MonthlyBudgetVerdict struct {
	TenantID           string
	EnforcementMode    string
	MonthlyBudgetCents int64
	SpentCents         int64
	Over               bool // spent >= budget（budget>0 才可能有 verdict）
}

// CheckMonthlyBudget evaluates the tenant's monthly budget gate.
//
// 返回 (verdict, nil)：
//   - 租户无预算行 / monthly_budget_cents=0（未设预算）/ 服务未启用 ⇒
//     (nil, nil)：调用方直接放行，零行为变化；
//   - verdict.EnforcementMode == warn ⇒ 调用方仅记日志；
//   - verdict.EnforcementMode == hard 且 verdict.Over ⇒ 调用方拦 429；
//
// DB 故障 ⇒ (_, *TenantBudgetCheckError)：Mode==hard 时调用方 fail-closed
// （与 api key 预算门 R89-133 同纪律 —— 「不知道超没超」≠「没超」）；
// Mode==""（预算行没读到，适用性未知）与 warn 模式的故障由调用方吞掉
// + 响亮记日志（本门默认不承重，故障不得改变放行行为）。
//
// maas_settings 缺行/换算率 ≤0 时 spent=0（AmountCentsFromCredits 的
// 既有口径）⇒ 门惰性 —— 与账单本身的算术一致，门账永不打架。
func (s *Service) CheckMonthlyBudget(ctx context.Context, tenantID string) (*MonthlyBudgetVerdict, error) {
	if !s.Enabled() || tenantID == "" || tenantID == "default" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// 往返 1：预算行 + 换算率（maas_settings 单行表 LEFT JOIN，缺行 ⇒
	// cps=0 ⇒ 花费算 0，见上）。enforcement_mode 空串防御性归 warn
	// （列 DEFAULT 'warn'，历史行/外部写入的兜底口径）。
	var budgetCents int64
	var mode string
	var centsPerCredit float64
	err := s.pool.QueryRow(ctx, `
		SELECT b.monthly_budget_cents,
		       COALESCE(NULLIF(b.enforcement_mode, ''), $2),
		       COALESCE(ms.cents_per_credit::float8, 0)
		  FROM tenant_budgets b
		  LEFT JOIN maas_settings ms ON ms.id = 1
		 WHERE b.tenant_id = $1
	`, tenantID, BudgetEnforcementWarn).Scan(&budgetCents, &mode, &centsPerCredit)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // 未配置预算 —— 不是 0 预算，也不是故障
	}
	if err != nil {
		return nil, &TenantBudgetCheckError{Mode: "", Err: fmt.Errorf("query tenant budget: %w", err)}
	}
	if budgetCents <= 0 {
		return nil, nil // 0 = 不设预算（938 口径）
	}

	// 往返 2：本月花费 —— 与 IssueMonthlyBill 同一 Go 函数
	// （sumPeriodConsumption over maas_credit_consumption_buckets）。
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	cons, err := s.sumPeriodConsumption(ctx, tenantID, monthStart, monthStart.AddDate(0, 1, 0))
	if err != nil {
		return nil, &TenantBudgetCheckError{Mode: mode, Err: fmt.Errorf("sum month consumption: %w", err)}
	}
	return evaluateMonthlyBudget(tenantID, mode, budgetCents, cons.Credits, centsPerCredit), nil
}

// evaluateMonthlyBudget is the pure decision arithmetic of the gate
// (same as IssueMonthlyBill's amount_cents: AmountCentsFromCredits).
// budgetCents > 0 由调用方保证（0/负数预算在上游已短路为未设预算）。
func evaluateMonthlyBudget(tenantID, mode string, budgetCents, monthCredits int64, centsPerCredit float64) *MonthlyBudgetVerdict {
	spent := AmountCentsFromCredits(monthCredits, centsPerCredit)
	v := &MonthlyBudgetVerdict{
		TenantID:           tenantID,
		EnforcementMode:    mode,
		MonthlyBudgetCents: budgetCents,
		SpentCents:         spent,
	}
	v.Over = spent >= budgetCents
	return v
}
