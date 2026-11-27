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
//
// ★ 2026-11-27 12h 审计轮（W12）：新增 DeductCreditsWithDetail——detail 是
//   可解释账单五因子 JSON（{rate_in,rate_out,tokens,discount,rate_multiplier}，
//   与旧路径 ledgerDetailJSON 同形，service.go:777），随 consume 流水落
//   credit_ledger.detail，让「我为什么被扣这些积分」能对到行。本签名保留为
//   薄委托（detail=nil，重试队列通道无用量上下文），调用方尽量用 WithDetail。
func (s *Service) DeductCredits(ctx context.Context, tenantID, ownerUser, requestID string, credits int64) (int64, error) {
	return s.DeductCreditsWithDetail(ctx, tenantID, ownerUser, requestID, credits, nil)
}

// DeductCreditsWithDetail 同 DeductCredits，额外把五因子 detail 写进每条
// credit_ledger 流水（★ W12：审计「为什么扣这些积分」信息不再丢失）。
func (s *Service) DeductCreditsWithDetail(ctx context.Context, tenantID, ownerUser, requestID string, credits int64, detail []byte) (int64, error) {
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
	//
	// ★ 2026-11-27 12h 审计轮（P0-A 根修）：此前 UPDATE 引用不存在的
	//   quota_used 列（tenant_subscriptions 只有 quota_remaining，
	//   007_maas_billing.sql:57-68 + census 实证）⇒ 订阅租户每次扣费
	//   事务直接 SQL 报错回滚 → ledger 也不写 → 进重试队列 10 次全败
	//   → 标 failed，订阅租户用量持续漏计费。改回与旧路径
	//   （service.go ChargeRequestMultimodalWithMultiplier）一致写法：
	//   quota_remaining 原子递减 + WHERE 守卫（配额不足时不消费订阅，
	//   溢出部分自然落进 5.2/5.3 的钱包扣减）。
	if subID > 0 && quota > 0 {
		consume := remaining
		if consume > quota {
			consume = quota
		}

		tag, err := tx.Exec(ctx, `
			UPDATE tenant_subscriptions
			   SET quota_remaining = quota_remaining - $1, updated_at = now()
			 WHERE id = $2 AND quota_remaining >= $1
		`, consume, subID)
		if err != nil {
			return 0, err
		}
		if tag.RowsAffected() == 0 {
			// 并发下配额已被别处扣空：本次不消费订阅，全部走钱包。
			consume = 0
		}
		subConsumed = consume
		remaining -= consume

		if subConsumed > 0 {
			// 写入订阅消费记录
			if _, err := tx.Exec(ctx, `
				INSERT INTO credit_ledger
				(tenant_id, owner_user, entry_type, amount, ref_type, ref_id, subscription_id, detail, created_at)
				VALUES ($1, $2, 'consume', $3, 'request', $4, $5, $6, NOW())
			`, tenantID, ownerUser, -subConsumed, requestID, subID, detail); err != nil {
				return 0, err
			}
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
			(tenant_id, owner_user, entry_type, amount, ref_type, ref_id, detail, created_at)
			VALUES ($1, $2, 'consume', $3, 'request', $4, $5, NOW())
		`, tenantID, ownerUser, -grantedConsumed, requestID, detail); err != nil {
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
			(tenant_id, owner_user, entry_type, amount, ref_type, ref_id, detail, created_at)
			VALUES ($1, $2, 'consume', $3, 'request', $4, $5, NOW())
		`, tenantID, ownerUser, -purchasedConsumed, requestID, detail); err != nil {
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
			(tenant_id, owner_user, entry_type, amount, ref_type, ref_id, detail, created_at)
			VALUES ($1, $2, 'consume', $3, 'request', $4, $5, NOW())
		`, tenantID, ownerUser, -remaining, requestID, detail); err != nil {
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

	// 6. 同事务回写 balance_credits 派生列 + 消费分桶（对齐旧路径语义）
	//
	// ★ 2026-11-27 12h 审计轮（P0-B 根修）：此前只改 granted/purchased 两个
	//   分量、不回写 balance_credits ⇒ PreCheckBalance（读 balance_credits，
	//   balance_precheck.go:48）的读数永不下降 ⇒ 余额≥0 分支恒命中，
	//   P0-1 的负余额/信用额度阻断对走本路径的流量永不触发，钱包/账单展示
	//   口径漂移。旧路径同一事务内 balance_credits = granted+purchased
	//   （service.go:272-280），此处保持同一口径。
	//
	// ★ 2026-11-27 12h 审计轮（P1-C 补齐）：月账单出账数据源
	//   maas_credit_consumption_buckets 此前断流（旧路径同事务 UPSERT，
	//   maas/bills.go 与 credit_buckets.go 均读它）⇒ 账单金额系统性偏低。
	//   补同事务 UPSERT（与扣费同一事务时钟）。
	if subConsumed+grantedConsumed+purchasedConsumed > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE tenant_credit_wallets
			   SET balance_credits = granted_balance + purchased_balance,
			       updated_at = now()
			 WHERE tenant_id = $1
		`, tenantID); err != nil {
			return 0, err
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO maas_credit_consumption_buckets
				(tenant_id, bucket_start, credits, request_count, updated_at)
			VALUES
				($1, date_trunc('hour', now(), 'UTC'), $2, 1, now())
			ON CONFLICT (tenant_id, bucket_start) DO UPDATE
				SET credits       = maas_credit_consumption_buckets.credits + EXCLUDED.credits,
				    request_count = maas_credit_consumption_buckets.request_count + 1,
				    updated_at    = now()
		`, tenantID, subConsumed+grantedConsumed+purchasedConsumed); err != nil {
			return 0, fmt.Errorf("post request consumption bucket: %w", err)
		}
	}

	// 7. 提交事务
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
