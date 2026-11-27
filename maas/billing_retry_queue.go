package maas

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// BillingRetryItem 计费重试队列项
type BillingRetryItem struct {
	ID              int64
	TenantID        string
	OwnerUser       string
	RequestID       string
	Credits         int64
	RetryType       string // deduct_credits / refund_credits
	Attempts        int
	MaxAttempts     int
	LastError       string
	LastAttemptedAt *time.Time
	NextRetryAt     time.Time
	CreatedAt       time.Time
	CompletedAt     *time.Time
	Status          string // pending / retrying / completed / failed
}

// EnqueueCreditDeduction 将失败的积分扣减加入重试队列
//
// P0-3阶段2：当异步扣减失败时，将任务写入 billing_retry_queue，
// 由后台worker定期重试。
//
// 参数：
//   - ctx: 上下文
//   - tenantID: 租户ID
//   - ownerUser: 请求归属用户
//   - requestID: 请求ID（幂等键）
//   - credits: 要扣减的积分数
//   - lastError: 失败原因
//
// 返回：
//   - error: 入队失败时的错误
func (s *Service) EnqueueCreditDeduction(ctx context.Context, tenantID, ownerUser, requestID string, credits int64, lastError string) error {
	if !s.Enabled() {
		return nil
	}

	// 检查是否已存在相同请求的待重试任务
	var existingID int64
	err := s.pool.QueryRow(ctx, `
		SELECT id FROM billing_retry_queue
		WHERE request_id = $1
		  AND status IN ('pending', 'retrying')
		LIMIT 1
	`, requestID).Scan(&existingID)

	if err == nil {
		// 已存在，更新错误信息
		_, err := s.pool.Exec(ctx, `
			UPDATE billing_retry_queue
			SET last_error = $1,
			    last_attempted_at = NOW()
			WHERE id = $2
		`, lastError, existingID)

		if err != nil {
			slog.Warn("failed to update existing retry queue item",
				"request_id", requestID,
				"error", err)
		}

		return nil // 已存在，不重复入队
	}

	// 插入新的重试任务。
	// ★ 2026-11-27 12h 审计轮（P2-A）：上面「查无 → 插入」是 check-then-act
	//   竞态——双网关同时入队同一 requestID 会插两行。此前无唯一约束兜底，
	//   重复行各自走完重试（重复扣减被 DeductCredits 的 advisory-lock 幂等
	//   检查兜住，但队列语义坏掉）。migration 1002 已补部分唯一索引
	//   (request_id) WHERE status IN ('pending','retrying')，这里改用
	//   ON CONFLICT 幂等插入：撞行时退化为更新错误信息，与上方已存在分支
	//   同语义。
	_, err = s.pool.Exec(ctx, `
		INSERT INTO billing_retry_queue
		(tenant_id, owner_user, request_id, credits, retry_type,
		 attempts, max_attempts, last_error, next_retry_at, status)
		VALUES ($1, $2, $3, $4, 'deduct_credits',
		        0, 10, $5, NOW() + INTERVAL '1 minute', 'pending')
		ON CONFLICT (request_id) WHERE status IN ('pending', 'retrying')
		DO UPDATE SET last_error = EXCLUDED.last_error,
		             last_attempted_at = NOW()
	`, tenantID, ownerUser, requestID, credits, lastError)

	if err != nil {
		return fmt.Errorf("failed to enqueue credit deduction: %w", err)
	}

	slog.Info("credit deduction enqueued for retry",
		"request_id", requestID,
		"tenant_id", tenantID,
		"credits", credits,
		"next_retry_at", "1 minute later")

	return nil
}

// ProcessRetryQueue 处理重试队列（由后台worker调用）
//
// 查询所有到期的待重试任务，逐个执行扣减。
// 成功：标记为completed
// 失败：增加attempts，计算下次重试时间（指数退避）
// 超过最大重试次数：标记为failed
//
// 参数：
//   - ctx: 上下文
//   - batchSize: 每批处理的任务数（默认100）
//
// 返回：
//   - processed: 处理的任务数
//   - succeeded: 成功的任务数
//   - failed: 失败的任务数
func (s *Service) ProcessRetryQueue(ctx context.Context, batchSize int) (processed, succeeded, failed int) {
	if !s.Enabled() {
		return 0, 0, 0
	}

	if batchSize <= 0 {
		batchSize = 100
	}

	// 查询待重试任务。
	// ★ 2026-11-27 12h 审计轮（P2-B）：FOR UPDATE SKIP LOCKED —— 多副本网关
	//   各自跑 BillingRetryWorker 时，此前裸 SELECT 会让两个 worker 捞到同一
	//   批任务并发重试（同一 requestID 的重复执行被 DeductCredits 幂等兜住，
	//   但会放大 DB 压力并让 attempts 双计）。行锁 + 跳过已锁行 = 消费互斥、
	//   副本间互不阻塞（与 metricsstore processor 同一模式）。LIMIT 在锁外
	//   语义不变：锁不到的行被跳过，批次可能小于 batchSize，可接受。
	rows, err := s.pool.Query(ctx, `
		SELECT id, tenant_id, owner_user, request_id, credits,
		       retry_type, attempts, max_attempts, last_error
		FROM billing_retry_queue
		WHERE status IN ('pending', 'retrying')
		  AND next_retry_at <= NOW()
		ORDER BY next_retry_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, batchSize)

	if err != nil {
		slog.Error("failed to query retry queue", "error", err)
		return 0, 0, 0
	}
	defer rows.Close()

	var items []BillingRetryItem
	for rows.Next() {
		var item BillingRetryItem
		var lastError *string

		err := rows.Scan(
			&item.ID, &item.TenantID, &item.OwnerUser, &item.RequestID,
			&item.Credits, &item.RetryType, &item.Attempts, &item.MaxAttempts,
			&lastError,
		)
		if err != nil {
			slog.Error("failed to scan retry queue item", "error", err)
			continue
		}

		if lastError != nil {
			item.LastError = *lastError
		}

		items = append(items, item)
	}
	// ★ rowsguard：循环后必须有 rows.Err() 终检（2026-11-27 一周审计轮补）。
	//   迭代中途的网络/驱动错误此前会被静默吞掉 ⇒ 批次半截仍按全量处理。
	if err := rows.Err(); err != nil {
		slog.Error("retry queue iteration error", "error", err)
		return 0, 0, 0
	}

	// 逐个处理
	for _, item := range items {
		processed++

		// 标记为retrying
		s.pool.Exec(ctx, `
			UPDATE billing_retry_queue
			SET status = 'retrying',
			    last_attempted_at = NOW()
			WHERE id = $1
		`, item.ID)

		// 执行扣减。
		// ★ 2026-11-27 12h 审计轮（P2-C）：refund_credits 此前不走任何执行
		//   分支 ⇒ execErr 恒 nil ⇒ 空跑也会被标 completed（队列说退了、
		//   积分没退）。当前仓内无 refund 入队方（死枚举值），但为防未来
		//   接线即踩雷：未知/未实现类型按失败处理走退避重试路径，超限进
		//   failed 可见，不再静默假完成。
		var execErr error
		switch item.RetryType {
		case "deduct_credits":
			_, execErr = s.DeductCredits(ctx, item.TenantID, item.OwnerUser, item.RequestID, item.Credits)
		default:
			execErr = fmt.Errorf("unsupported retry_type %q (no executor wired)", item.RetryType)
		}

		if execErr == nil {
			// 成功：标记为completed
			_, err := s.pool.Exec(ctx, `
				UPDATE billing_retry_queue
				SET status = 'completed',
				    completed_at = NOW(),
				    attempts = attempts + 1
				WHERE id = $1
			`, item.ID)

			if err != nil {
				slog.Error("failed to mark retry as completed", "id", item.ID, "error", err)
			} else {
				succeeded++
				slog.Info("retry succeeded",
					"id", item.ID,
					"request_id", item.RequestID,
					"attempts", item.Attempts+1)
			}
		} else {
			// 失败：增加attempts，计算下次重试时间
			newAttempts := item.Attempts + 1

			if newAttempts >= item.MaxAttempts {
				// 超过最大重试次数：标记为failed
				_, err := s.pool.Exec(ctx, `
					UPDATE billing_retry_queue
					SET status = 'failed',
					    attempts = $1,
					    last_error = $2
					WHERE id = $3
				`, newAttempts, execErr.Error(), item.ID)

				if err != nil {
					slog.Error("failed to mark retry as failed", "id", item.ID, "error", err)
				} else {
					failed++
					slog.Warn("retry failed permanently",
						"id", item.ID,
						"request_id", item.RequestID,
						"attempts", newAttempts,
						"error", execErr)
				}
			} else {
				// 计算下次重试时间（指数退避：1min -> 2min -> 4min -> ... 最多60min）
				backoffMinutes := 1 << uint(newAttempts) // 2^attempts
				if backoffMinutes > 60 {
					backoffMinutes = 60
				}

				_, err := s.pool.Exec(ctx, `
					UPDATE billing_retry_queue
					SET status = 'pending',
					    attempts = $1,
					    last_error = $2,
					    next_retry_at = NOW() + $3 * INTERVAL '1 minute'
					WHERE id = $4
				`, newAttempts, execErr.Error(), backoffMinutes, item.ID)

				if err != nil {
					slog.Error("failed to update retry schedule", "id", item.ID, "error", err)
				} else {
					slog.Info("retry scheduled",
						"id", item.ID,
						"request_id", item.RequestID,
						"attempts", newAttempts,
						"next_retry_minutes", backoffMinutes,
						"error", execErr)
				}
			}
		}
	}

	return processed, succeeded, failed
}
