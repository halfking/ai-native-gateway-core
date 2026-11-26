package maas

import (
	"context"
	"fmt"
	"log/slog"
)

// DeductCredits 从钱包扣减积分（P0-3方案C，2026-11-26）
//
// 此方法仅扣减钱包余额，不计算积分。用于handler异步扣减场景：
//   1. handler同步计算积分（CalculateCredits）
//   2. 积分写入telemetry（与cost_usd同时写入）
//   3. 异步调用此方法扣减钱包（失败不影响请求完成）
//
// 幂等性保证：
//   - 使用request_id作为幂等键
//   - 如果该请求已扣费，返回已扣除的积分（不重复扣减）
//
// 参数：
//   - ctx: 上下文
//   - tenantID: 租户ID
//   - ownerUser: 请求归属用户
//   - requestID: 请求ID（幂等键）
//   - credits: 要扣减的积分数（必须>0）
//
// 返回：
//   - 实际扣除的积分（如果已扣过，返回之前扣除的数量）
//   - error: 扣减失败时的错误
func (s *Service) DeductCredits(ctx context.Context, tenantID, ownerUser, requestID string, credits int64) (int64, error) {
	if !s.Enabled() {
		return 0, nil
	}

	if tenantID == "" || tenantID == "default" {
		return 0, nil
	}

	if credits <= 0 {
		return 0, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	//nolint:errcheck // deferred rollback, best-effort
	defer tx.Rollback(ctx)

	// 1. 幂等性检查：是否已扣费
	if requestID != "" {
		lockKey := fmt.Sprintf("%d:%s:%s", len(tenantID), tenantID, requestID)
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
			return 0, err
		}

		var alreadyCharged int64
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(-amount), 0)::bigint
			FROM (
				SELECT DISTINCT ON (id) amount FROM credit_ledger_with_current_month
				WHERE tenant_id = $1 AND ref_type = 'request' AND ref_id = $2
				  AND entry_type = 'consume' AND amount < 0
				ORDER BY id, created_at DESC
			) posted
		`, tenantID, requestID).Scan(&alreadyCharged); err != nil {
			return 0, err
		}

		if alreadyCharged > 0 {
			slog.Info("request already charged, skipping",
				"request_id", requestID,
				"tenant_id", tenantID,
				"already_charged", alreadyCharged)
			if err := tx.Commit(ctx); err != nil {
				return 0, err
			}
			return alreadyCharged, nil
		}
	}

	// 2. 确保钱包存在
	if err := s.ensureWallet(ctx, tx, tenantID); err != nil {
		return 0, err
	}

	// 3. 查询钱包余额结构（granted/purchased）
	var granted, purchased int64
	err = tx.QueryRow(ctx, `
		SELECT granted_balance, purchased_balance 
		FROM tenant_credit_wallets 
		WHERE tenant_id = $1 
		FOR UPDATE
	`, tenantID).Scan(&granted, &purchased)
	if err != nil {
		return 0, err
	}

	// 4. 查询订阅配额
	subID, quota, err := s.activeSubscriptionQuota(ctx, tx, tenantID)
	if err != nil {
		return 0, err
	}

	// 5. 按优先级扣减：订阅配额 → 赠送余额 → 购买余额
	remaining := credits
	var subConsumed, grantedConsumed, purchasedConsumed int64

	// 5.1 优先扣减订阅配额
	if subID > 0 && quota > 0 {
		consume := remaining
		if consume > quota {
			consume = quota
		}
		subConsumed = consume
		remaining -= consume

		// 写入订阅消费记录
		if _, err := tx.Exec(ctx, `
			INSERT INTO credit_ledger 
			(tenant_id, owner_user, entry_type, amount, ref_type, ref_id, subscription_id, created_at)
			VALUES ($1, $2, 'consume', $3, 'request', $4, $5, NOW())
		`, tenantID, ownerUser, -subConsumed, requestID, subID); err != nil {
			return 0, err
		}

		// 更新订阅配额
		if _, err := tx.Exec(ctx, `
			UPDATE tenant_subscriptions
			SET quota_used = quota_used + $1
			WHERE id = $2
		`, subConsumed, subID); err != nil {
			return 0, err
		}
	}

	// 5.2 扣减赠送余额
	if remaining > 0 && granted > 0 {
		consume := remaining
		if consume > granted {
			consume = granted
		}
		grantedConsumed = consume
		remaining -= consume

		// 写入赠送消费记录
		if _, err := tx.Exec(ctx, `
			INSERT INTO credit_ledger 
			(tenant_id, owner_user, entry_type, amount, ref_type, ref_id, created_at)
			VALUES ($1, $2, 'consume', $3, 'request', $4, NOW())
		`, tenantID, ownerUser, -grantedConsumed, requestID); err != nil {
			return 0, err
		}

		// 更新赠送余额
		if _, err := tx.Exec(ctx, `
			UPDATE tenant_credit_wallets
			SET granted_balance = granted_balance - $1
			WHERE tenant_id = $2
		`, grantedConsumed, tenantID); err != nil {
			return 0, err
		}
	}

	// 5.3 扣减购买余额
	if remaining > 0 && purchased > 0 {
		consume := remaining
		if consume > purchased {
			consume = purchased
		}
		purchasedConsumed = consume
		remaining -= consume

		// 写入购买消费记录
		if _, err := tx.Exec(ctx, `
			INSERT INTO credit_ledger 
			(tenant_id, owner_user, entry_type, amount, ref_type, ref_id, created_at)
			VALUES ($1, $2, 'consume', $3, 'request', $4, NOW())
		`, tenantID, ownerUser, -purchasedConsumed, requestID); err != nil {
			return 0, err
		}

		// 更新购买余额
		if _, err := tx.Exec(ctx, `
			UPDATE tenant_credit_wallets
			SET purchased_balance = purchased_balance - $1
			WHERE tenant_id = $2
		`, purchasedConsumed, tenantID); err != nil {
			return 0, err
		}
	}

	// 5.4 如果余额不足（允许负债），记录到购买余额
	if remaining > 0 {
		purchasedConsumed += remaining

		// 写入消费记录（进入负债）
		if _, err := tx.Exec(ctx, `
			INSERT INTO credit_ledger 
			(tenant_id, owner_user, entry_type, amount, ref_type, ref_id, created_at)
			VALUES ($1, $2, 'consume', $3, 'request', $4, NOW())
		`, tenantID, ownerUser, -remaining, requestID); err != nil {
			return 0, err
		}

		// 更新购买余额（变为负数）
		if _, err := tx.Exec(ctx, `
			UPDATE tenant_credit_wallets
			SET purchased_balance = purchased_balance - $1
			WHERE tenant_id = $2
		`, remaining, tenantID); err != nil {
			return 0, err
		}
	}

	// 6. 提交事务
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}

	slog.Info("credits deducted from wallet",
		"request_id", requestID,
		"tenant_id", tenantID,
		"total_credits", credits,
		"subscription_consumed", subConsumed,
		"granted_consumed", grantedConsumed,
		"purchased_consumed", purchasedConsumed)

	return credits, nil
}
