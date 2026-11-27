package maas

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// BillingDecision 计费预检决策结果
type BillingDecision struct {
	Allow           bool   // 是否允许请求继续
	Policy          string // 应用的策略：block / warn / skipped
	Reason          string // 决策原因（用于日志和用户提示）
	BalanceBefore   int64  // 预检前的余额（积分）
	CreditLimit     int64  // 信用额度（积分）
	EstimatedCost   int64  // 预估成本（积分）
	AvailableCredit int64  // 可用额度 = 余额 + 信用额度
}

// PreCheckBalance 预检租户余额是否足够支付预估成本
//
// 判定逻辑：
//  1. 余额 >= 0：允许使用
//  2. 余额 < 0 但在信用额度内：
//     - policy=warn：允许使用（进入负债）
//     - policy=block：阻断
//  3. 超过信用额度：强制阻断
//
// 失败策略（fail-open）：
// - 如果查询余额失败，默认允许继续（记录警告）
// - 避免计费系统故障导致服务完全不可用
func (s *Service) PreCheckBalance(ctx context.Context, tenantID string, estimatedCostCredits int64) (*BillingDecision, error) {
	return s.PreCheckBalanceWithRequestID(ctx, tenantID, "", estimatedCostCredits)
}

// PreCheckBalanceWithRequestID 同 PreCheckBalance，额外把 requestID 透传进
// billing_precheck_log（★ 2026-11-27 12h 审计轮 W6：此前 request_id 恒空串，
// 审计时无法把预检决策关联到具体请求——空串在 NOT NULL 列上可通过但丢失
// 了关联能力）。
func (s *Service) PreCheckBalanceWithRequestID(ctx context.Context, tenantID, requestID string, estimatedCostCredits int64) (*BillingDecision, error) {
	// 0. MaaS未启用：跳过预检
	if !s.Enabled() {
		return &BillingDecision{
			Allow:  true,
			Policy: "skipped",
			Reason: "maas disabled",
		}, nil
	}

	// 1. 查询租户余额、信用额度、策略
	var balance, creditLimit int64
	var policy string

	err := s.pool.QueryRow(ctx, `
		SELECT balance_credits, credit_limit_credits, credit_policy
		FROM tenant_credit_wallets
		WHERE tenant_id = $1
	`, tenantID).Scan(&balance, &creditLimit, &policy)

	if err != nil {
		slog.Warn("failed to query tenant wallet for precheck",
			"tenant_id", tenantID,
			"error", err)

		// fail-open：查询失败时允许继续（记录警告）
		// 原因：避免计费系统故障导致服务完全不可用
		return &BillingDecision{
			Allow:  true,
			Policy: "warn",
			Reason: "wallet query failed, fail-open",
		}, nil
	}

	// 2. 计算可用额度
	availableCredit := balance + creditLimit

	// 3. 构建决策对象
	decision := &BillingDecision{
		BalanceBefore:   balance,
		CreditLimit:     creditLimit,
		EstimatedCost:   estimatedCostCredits,
		AvailableCredit: availableCredit,
	}

	// 4. 执行判定逻辑
	if balance >= 0 {
		// 场景1: 余额充足
		decision.Allow = true
		decision.Policy = "allow"
		decision.Reason = "sufficient balance"
	} else if availableCredit >= estimatedCostCredits {
		// 场景2: 余额不足，但在信用额度内
		if policy == "block" {
			// block模式：即使在信用额度内也阻断
			decision.Allow = false
			decision.Policy = "block"
			decision.Reason = fmt.Sprintf("balance negative (policy=block), balance=%d", balance)
		} else {
			// warn模式：允许使用，进入负债
			decision.Allow = true
			decision.Policy = "warn"
			decision.Reason = fmt.Sprintf("using credit line, balance=%d, available=%d", balance, availableCredit)
		}
	} else {
		// 场景3: 超过信用额度，强制阻断
		decision.Allow = false
		decision.Policy = "block"
		decision.Reason = fmt.Sprintf("over credit limit, balance=%d, limit=%d, available=%d < estimated=%d",
			balance, creditLimit, availableCredit, estimatedCostCredits)
	}

	// 5. 异步记录预检日志（不阻塞主流程）
	// ★ 2026-11-27 12h 审计轮（P2-E）：日志 goroutine 加 panic 防护与 5s
	//   超时——裸 go + context.Background() 下，Exec panic 会崩掉整个进程
	//   （既往教训：裸 go 无 recover 崩进程），DB 变慢时 goroutine 会无界
	//   堆积。日志是 best-effort 旁路：失败/超时只降级为 Warn，绝不影响
	//   预检决策路径。
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Warn("billing precheck log goroutine panicked", "panic", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.logBillingPrecheck(ctx, tenantID, requestID, decision)
	}()

	return decision, nil
}

// logBillingPrecheck 记录预检日志到 billing_precheck_log 表
func (s *Service) logBillingPrecheck(ctx context.Context, tenantID, requestID string, decision *BillingDecision) {
	decisionStr := "allow"
	if !decision.Allow {
		decisionStr = "reject"
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO billing_precheck_log 
		(request_id, tenant_id, balance_before_credits, credit_limit_credits, 
		 estimated_cost_credits, policy, decision, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, requestID, tenantID, decision.BalanceBefore, decision.CreditLimit,
		decision.EstimatedCost, decision.Policy, decisionStr, decision.Reason)

	if err != nil {
		slog.Warn("failed to log billing precheck", "error", err)
	}
}

// GetTenantCreditInfo 获取租户的积分信息（用于展示）
func (s *Service) GetTenantCreditInfo(ctx context.Context, tenantID string) (balance, creditLimit int64, policy string, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT balance_credits, credit_limit_credits, credit_policy
		FROM tenant_credit_wallets
		WHERE tenant_id = $1
	`, tenantID).Scan(&balance, &creditLimit, &policy)

	return balance, creditLimit, policy, err
}

// UpdateTenantCreditPolicy 更新租户的信用策略
func (s *Service) UpdateTenantCreditPolicy(ctx context.Context, tenantID, newPolicy string) error {
	if newPolicy != "block" && newPolicy != "warn" {
		return fmt.Errorf("invalid policy: %s (must be 'block' or 'warn')", newPolicy)
	}

	_, err := s.pool.Exec(ctx, `
		UPDATE tenant_credit_wallets
		SET credit_policy = $1
		WHERE tenant_id = $2
	`, newPolicy, tenantID)

	if err != nil {
		return fmt.Errorf("failed to update credit policy: %w", err)
	}

	slog.Info("tenant credit policy updated",
		"tenant_id", tenantID,
		"new_policy", newPolicy)

	return nil
}

// UpdateTenantCreditLimit 更新租户的信用额度
func (s *Service) UpdateTenantCreditLimit(ctx context.Context, tenantID string, newLimit int64) error {
	if newLimit < 0 {
		return fmt.Errorf("invalid credit limit: %d (must be >= 0)", newLimit)
	}

	_, err := s.pool.Exec(ctx, `
		UPDATE tenant_credit_wallets
		SET credit_limit_credits = $1
		WHERE tenant_id = $2
	`, newLimit, tenantID)

	if err != nil {
		return fmt.Errorf("failed to update credit limit: %w", err)
	}

	slog.Info("tenant credit limit updated",
		"tenant_id", tenantID,
		"new_limit", newLimit)

	return nil
}
