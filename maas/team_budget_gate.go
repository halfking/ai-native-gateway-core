package maas

// 团队月度预算执行门（W2-Q2，迁移 055-058）。
//
// 复用 996 的 enforcement_mode ∈ {warn, hard} 执行开关，但聚合粒度改为团队：
//   warn（默认）= 仅告警不拦截；
//   hard        = 当月团队花费达 monthly_limit_credits × cents_per_credit ⇒ 429
//
// ★ 口径锚点：花费与租户预算门同源 ——
//   sumTeamPeriodConsumption（本月团队所有 API keys 的 buckets 求和）
//   × cents_per_credit（AmountCentsFromCredits）。
//   门与账单不能打架：同一换算，禁止另起聚合。
//
// 团队预算聚合范围：api_keys.team_id = ? 的所有 keys 的消耗。
// 个人 key（team_id IS NULL）不计入任何团队预算。

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// TeamBudgetVerdict 团队月度预算判决
type TeamBudgetVerdict struct {
	TeamID                int64  // 团队 ID
	EnforcementMode       string // warn/hard
	MonthlyLimitCredits   int64  // 月度预算额度（credits）
	SpentCredits          int64  // 已花费（credits）
	SpentCents            int64  // 已花费（cents，用于展示）
	Over                  bool   // 是否超限
}

// TeamBudgetCheckError 团队预算检查错误（DB 故障等）
type TeamBudgetCheckError struct {
	Mode string // warn/hard（用于调用方决定 fail-open 或 fail-closed）
	Err  error
}

func (e *TeamBudgetCheckError) Error() string {
	return fmt.Sprintf("team budget check failed (mode=%s): %v", e.Mode, e.Err)
}

func (e *TeamBudgetCheckError) Unwrap() error {
	return e.Err
}

// CheckTeamMonthlyBudget 检查团队月度预算
//
// 输入：teamID（团队 ID）
// 输出：
//   - (*TeamBudgetVerdict, nil)：预算判决成功
//     verdict.EnforcementMode == hard 且 verdict.Over ⇒ 调用方拦 429；
//   - (nil, nil)：未配置团队预算 或 teamID 无效；
//   - (nil, *TeamBudgetCheckError)：DB 故障等
//     Mode==hard 时调用方 fail-closed（与租户预算门同纪律）；
//     Mode==""（预算行没读到）与 warn 模式故障由调用方吞掉 + 响亮记日志。
func (s *Service) CheckTeamMonthlyBudget(ctx context.Context, teamID int64) (*TeamBudgetVerdict, error) {
	if !s.Enabled() || teamID <= 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// 往返 1：团队预算行 + 换算率（maas_settings 单行表 LEFT JOIN）
	var limitCredits float64 // 用 float64 接收 numeric(20,6)
	var mode string
	var centsPerCredit float64
	err := s.pool.QueryRow(ctx, `
		SELECT tb.monthly_limit_credits,
		       COALESCE(NULLIF(tb.enforcement_mode, ''), $2),
		       COALESCE(ms.cents_per_credit::float8, 0)
		  FROM team_budgets tb
		  LEFT JOIN maas_settings ms ON ms.id = 1
		 WHERE tb.team_id = $1
	`, teamID, BudgetEnforcementWarn).Scan(&limitCredits, &mode, &centsPerCredit)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // 未配置预算 —— 不是 0 预算，也不是故障
	}
	if err != nil {
		return nil, &TeamBudgetCheckError{Mode: "", Err: fmt.Errorf("query team budget: %w", err)}
	}
	if limitCredits <= 0 {
		return nil, nil // 0 或负数 = 不设预算
	}

	// 往返 2：本月团队花费 —— 聚合该团队所有 API keys 的消耗
	// （与 sumPeriodConsumption 同口径，但按 team_id 聚合）
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	monthEnd := monthStart.AddDate(0, 1, 0)

	teamCredits, err := s.sumTeamPeriodConsumption(ctx, teamID, monthStart, monthEnd)
	if err != nil {
		return nil, &TeamBudgetCheckError{Mode: mode, Err: fmt.Errorf("sum team month consumption: %w", err)}
	}

	return evaluateTeamMonthlyBudget(teamID, mode, int64(limitCredits), teamCredits, centsPerCredit), nil
}

// sumTeamPeriodConsumption 聚合团队在指定时间段的消耗（credits）
// 聚合范围：api_keys.team_id = teamID 的所有 keys 的 consumption buckets
func (s *Service) sumTeamPeriodConsumption(ctx context.Context, teamID int64, start, end time.Time) (int64, error) {
	var totalCredits int64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(ccb.credits), 0)
		  FROM maas_credit_consumption_buckets ccb
		 INNER JOIN api_keys ak ON ccb.api_key_id = ak.id
		 WHERE ak.team_id = $1
		   AND ccb.period_start >= $2
		   AND ccb.period_start < $3
	`, teamID, start, end).Scan(&totalCredits)
	if err != nil {
		return 0, fmt.Errorf("sum team consumption buckets: %w", err)
	}
	return totalCredits, nil
}

// evaluateTeamMonthlyBudget 团队月度预算判决算术（纯函数）
// limitCredits > 0 由调用方保证（0/负数预算在上游已短路）
func evaluateTeamMonthlyBudget(teamID int64, mode string, limitCredits, spentCredits int64, centsPerCredit float64) *TeamBudgetVerdict {
	spentCents := AmountCentsFromCredits(spentCredits, centsPerCredit)
	limitCents := AmountCentsFromCredits(limitCredits, centsPerCredit)

	v := &TeamBudgetVerdict{
		TeamID:              teamID,
		EnforcementMode:     mode,
		MonthlyLimitCredits: limitCredits,
		SpentCredits:        spentCredits,
		SpentCents:          spentCents,
	}
	v.Over = spentCents >= limitCents
	return v
}
