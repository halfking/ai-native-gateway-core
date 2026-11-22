package maas

// 团队月度预算执行门（W2-Q2，迁移 055-058）。
//
// 复用 996 的 enforcement_mode ∈ {warn, hard} 执行开关，但聚合粒度改为团队：
//   warn（默认）= 仅告警不拦截；
//   hard        = 当月团队花费达 monthly_limit_credits × cents_per_credit ⇒ 429
//
// ★★ ⚠ 本门**尚未接线**（2026-11-23 12h 修订审计轮登记）：
//
//	CheckTeamMonthlyBudget 当前**零调用点**（生产 0、测试 0）——
//	⇒ 团队预算**不生效**，任何请求都不会被它拦下。
//	⇒ 迁移 055-058（teams / user_teams / api_keys.team_id / team_budgets）
//	  已上线 ⇒ 看到「团队预算表已建」就以为预算已生效，是**假保证**。
//	⇒ 接线的前置条件（属产品裁决，不在本轮范围）：
//	  ① 真源选择（见下「口径」段）需要 owner 拍板；
//	  ② fail-open / fail-closed 语义要与租户门一致地落到四个协议面
//	    （chat/completions + messages + responses + audio）；
//	  ③ 需要一条「团队预算已生效」的端到端钉测。
//	⇒ 在接线之前，任何依赖团队预算的承诺都不成立。
//
// ★ 口径（2026-11-23 审计轮**订正**了原注释的错误声明）：
//
//	原注释写「花费与租户预算门同源 …… 同一换算，禁止另起聚合」——
//	这句话**不成立**：租户门读的是 maas_credit_consumption_buckets，
//	而该表**只有租户粒度**（PK(tenant_id, bucket_start)），
//	**没有 api_key_id、也没有 period_start** ⇒
//	  ① 按「api_keys.team_id 聚合 buckets」在真实 schema 上**跑不起来**
//	     （SQLSTATE 42703 column does not exist）；
//	  ② 即使把列名改对，租户粒度的 buckets 也**无法拆出团队粒度**——
//	     那样做会把**整个租户**的消费记到每个团队头上，比不实现更危险。
//	⇒ 因此团队真源改用 **usage_facts**（537）：
//	   唯一同时具备「按 key 归因」与「credits 计费」的真源；
//	   domains/stats/reconciliation.go:42 自称 usage_facts 为 source of truth；
//	   写侧 cmd/gateway/main.go 把 keyInfo.ID 落进 api_key_id，
//	   domains/stats/inbox_consumer.go:507 落进 usage_facts。
//
//	与租户门**仍然同源的部分**（这三项才是「不能打架」的真实含义）：
//	  ① 换算单位相同：credits × cents_per_credit（AmountCentsFromCredits）；
//	  ② 窗口定义相同：UTC 自然月，半开区间 [月初, 次月初)；
//	  ③ 口径由同一套勾稽覆盖：usage_facts ↔ stats_usage_daily（stats 对账），
//	     buckets ↔ bill（maas 三方勾稽）。
//	★ **不同的部分（必须如实知道，不是缺陷）**：两张表是两个事实源，
//	  团队门读 usage_facts、租户门读 buckets ⇒ 同一笔消费在两侧的可见性
//	  取决于各自事实源的完整性（例如 usage_facts 的 api_key_id 为 NULL
//	  ⇒ 该笔消费**不计入任何团队**；这与「未归因的请求不属于任何团队」一致）。
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
	TeamID              int64  // 团队 ID
	EnforcementMode     string // warn/hard
	MonthlyLimitCredits int64  // 月度预算额度（credits）
	SpentCredits        int64  // 已花费（credits）
	SpentCents          int64  // 已花费（cents，用于展示）
	Over                bool   // 是否超限
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
//
// 真源＝usage_facts（537）。★ **不是** maas_credit_consumption_buckets：
// 后者只有租户粒度（tenant_id, bucket_start），没有 api_key_id 也没有
// period_start，按团队聚合在真实 schema 上跑不起来（42703），
// 且即使改对列名也只能得到「整个租户」的消费。
//
// 口径三件（与租户门同源的部分）：单位=credits、窗口=[start,end) 半开、换算同函数。
// 归属：api_key_id → api_keys.id → api_keys.team_id。
// ★ 跨租户防串：额外要求 ak.tenant_id = t.tenant_id（键与其所属团队必须同租户），
//
//	否则一个挂在别租户团队下的 key 会把消费串进本团队预算。
//
// ★ 未归因消费（api_key_id IS NULL）不计入任何团队——INNER JOIN 天然排除。
func (s *Service) sumTeamPeriodConsumption(ctx context.Context, teamID int64, start, end time.Time) (int64, error) {
	var totalCredits int64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(f.credits_charged), 0)
		  FROM usage_facts f
		  JOIN api_keys ak ON ak.id = f.api_key_id
		  JOIN teams t     ON t.id = ak.team_id
		 WHERE t.id = $1
		   AND ak.tenant_id = t.tenant_id
		   AND f.occurred_at >= $2
		   AND f.occurred_at < $3
	`, teamID, start, end).Scan(&totalCredits)
	if err != nil {
		return 0, fmt.Errorf("sum team consumption from usage_facts: %w", err)
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
