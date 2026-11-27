package maas

// 三级配额制（租户→组织→用户）的组织/用户级余额预检（2026-11-27）。
//
// 范式 = balance_precheck.go 的 PreCheckBalance（租户级）逐字对齐：
//   - 语义三层判定：余额≥0 放行 → 余额<0 且在信用额度内按 policy（warn 放行负债
//     / block 阻断）→ 超信用额度强制阻断；
//   - fail-open：钱包查不到（无行 = 该层未配置额度 = 不限制）与查询失败一律放行；
//   - best-effort 审计旁路：billing_precheck_log 落库，带 scope/subject 维度，
//     panic 防护 + 5s 超时（P2-E 同款）。
//
// 调用点（四面共用判定体的接线见 domains/streaming/handler.go 的
// applyTieredBalanceGate）：优先级 租户>组织>用户 逐级 AND —— 每一级独立判定、
// 任一级 block 即阻断；org/user 钱包行不存在 = 该层不设限 = 跳过。
//
// ★ 调用点判据（gate-with-call-site 纪律，team_budget_gate 教训）：本文件的
//   导出判定体必须被四面入口真实调用，TestTieredBalanceGateHasCallSites 钉住。

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// PrecheckScope 预检层级：org / user（tenant 级走原 PreCheckBalance）
type PrecheckScope string

const (
	PrecheckScopeOrg  PrecheckScope = "org"
	PrecheckScopeUser PrecheckScope = "user"
)

// resolveUserOrg 解析 ownerUser 的 (users.id, users.org_id)。
// 返回 (0, 0, nil) = 用户不存在或未关联组织 ⇒ 两层都跳过。
func (s *Service) resolveUserOrg(ctx context.Context, ownerUser string) (userID, orgID int, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT u.id, COALESCE(u.org_id, 0)
		FROM users u
		WHERE u.username = $1 AND u.enabled
		LIMIT 1
	`, ownerUser).Scan(&userID, &orgID)
	return
}

// PreCheckWalletBalance 单层（org 或 user）余额预检。
//
// 钱包行不存在（pgx.ErrNoRows）⇒ 该层未配置额度 ⇒ 放行（Policy=skipped）。
// 查询失败 ⇒ fail-open 放行（同租户级纪律）。判定语义与租户级逐字对齐。
func (s *Service) PreCheckWalletBalance(ctx context.Context, scope PrecheckScope, subjectID int, estimatedCostCredits int64) (*BillingDecision, error) {
	if !s.Enabled() || subjectID <= 0 {
		return &BillingDecision{Allow: true, Policy: "skipped", Reason: "scope disabled or subject unset"}, nil
	}

	table := "org_credit_wallets"
	idCol := "org_id"
	switch scope {
	case PrecheckScopeOrg:
		table, idCol = "org_credit_wallets", "org_id"
	case PrecheckScopeUser:
		table, idCol = "user_credit_wallets", "user_id"
	default:
		return &BillingDecision{Allow: true, Policy: "skipped", Reason: "unknown scope"}, nil
	}

	var balance, creditLimit int64
	var policy string
	// credit_limit_credits NULL = 不限制：读成「无限大」等价数（MaxInt64），
	// 使 available 恒 ≥ estimated ⇒ 判定自然放行，与租户级共享同一套比较逻辑。
	err := s.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT balance_credits, COALESCE(credit_limit_credits, 9223372036854775807), credit_policy
		FROM %s WHERE %s = $1
	`, table, idCol), subjectID).Scan(&balance, &creditLimit, &policy)

	if err != nil {
		// 无钱包行 = 该层不限额（正常形态，Debug 级）；真查询失败 = fail-open（Warn）。
		slog.Debug("tiered precheck: wallet row absent or query failed, fail-open",
			"scope", scope, "subject_id", subjectID, "error", err)
		return &BillingDecision{Allow: true, Policy: "warn", Reason: "wallet absent or query failed, fail-open"}, nil
	}

	availableCredit := balance + creditLimit
	decision := &BillingDecision{
		BalanceBefore:   balance,
		CreditLimit:     creditLimit,
		EstimatedCost:   estimatedCostCredits,
		AvailableCredit: availableCredit,
	}

	if balance >= 0 {
		decision.Allow = true
		decision.Policy = "allow"
		decision.Reason = "sufficient balance"
	} else if availableCredit >= estimatedCostCredits {
		if policy == "block" {
			decision.Allow = false
			decision.Policy = "block"
			decision.Reason = fmt.Sprintf("balance negative (policy=block), balance=%d", balance)
		} else {
			decision.Allow = true
			decision.Policy = "warn"
			decision.Reason = fmt.Sprintf("using credit line, balance=%d, available=%d", balance, availableCredit)
		}
	} else {
		decision.Allow = false
		decision.Policy = "block"
		decision.Reason = fmt.Sprintf("over credit limit, balance=%d, limit=%d, available=%d < estimated=%d",
			balance, creditLimit, availableCredit, estimatedCostCredits)
	}

	// 审计旁路（best-effort，panic 防护 + 5s 超时，P2-E 同款）。
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Warn("tiered precheck log goroutine panicked", "panic", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.logTieredPrecheck(ctx, scope, subjectID, decision)
	}()

	return decision, nil
}

// logTieredPrecheck 落库 billing_precheck_log（scope 列区分层级；
// tenant_id 列沿用：org 行带其 tenant_id，user 层带用户所在 tenant_id）。
func (s *Service) logTieredPrecheck(ctx context.Context, scope PrecheckScope, subjectID int, decision *BillingDecision) {
	decisionStr := "allow"
	if !decision.Allow {
		decisionStr = "reject"
	}
	tenantID := fmt.Sprintf("%s:%d", scope, subjectID)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO billing_precheck_log
		(request_id, tenant_id, balance_before_credits, credit_limit_credits,
		 estimated_cost_credits, policy, decision, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, "", tenantID, decision.BalanceBefore, decision.CreditLimit,
		decision.EstimatedCost, decision.Policy, decisionStr, decision.Reason)
	if err != nil {
		slog.Warn("failed to log tiered precheck", "error", err)
	}
}

// PreCheckOrgAndUserForOwner 组合入口：按 ownerUser 解析 (user, org) 后逐级预检。
//
// 返回第一个 block 的 decision（Allow=false）；全放行返回 nil decision（无阻断）。
// 解析失败/未关联组织 ⇒ 两层跳过（fail-open，与「无钱包行=不限」同口径）。
func (s *Service) PreCheckOrgAndUserForOwner(ctx context.Context, ownerUser string, estimatedCostCredits int64) (*BillingDecision, error) {
	if !s.Enabled() || ownerUser == "" {
		return nil, nil
	}
	userID, orgID, err := s.resolveUserOrg(ctx, ownerUser)
	if err != nil {
		slog.Debug("tiered precheck: owner not resolved to users row, skip",
			"owner_user", ownerUser, "error", err)
		return nil, nil
	}
	if orgID > 0 {
		d, err := s.PreCheckWalletBalance(ctx, PrecheckScopeOrg, orgID, estimatedCostCredits)
		if err != nil {
			return nil, err
		}
		if d != nil && !d.Allow {
			return d, nil
		}
	}
	if userID > 0 {
		d, err := s.PreCheckWalletBalance(ctx, PrecheckScopeUser, userID, estimatedCostCredits)
		if err != nil {
			return nil, err
		}
		if d != nil && !d.Allow {
			return d, nil
		}
	}
	return nil, nil
}
