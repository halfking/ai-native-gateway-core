// store_claim.go — worker claim / 续租 / 重排（doc 18 §11.3，SR-09）。
//
// 所有 runnable 转移 SQL 均携带全局 commit_state IN ('none','metadata')
// 门禁；完成/重排/续租更新携带 (lease_owner, fencing_token)，0 行即
// ErrLeaseLost，旧 worker 必须丢弃结果、停止执行。
package durable

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// taskColumns 是 Task 行的统一 SELECT/RETURNING 列（与 scanTask 对齐）。
const taskColumns = `id, tenant_id, request_id, session_id, protocol, endpoint,
	status, commit_state, semantic_content_committed,
	error_kind, reason_code, attempt_count, fencing_token,
	lease_owner, lease_until, next_retry_at, deadline_at, expires_at,
	request_hash, snapshot_version, encryption_key_id,
	result_version, created_at, updated_at, completed_at`

// scanTask 把 taskColumns 顺序的行扫描为 Task。
func scanTask(scan interface{ Scan(dest ...any) error }) (*Task, error) {
	var (
		t           Task
		leaseOwner  pgtypeText
		errKind     pgtypeText
		reasonCode  pgtypeText
		leaseUntil  pgtypeTimestamptz
		nextRetry   pgtypeTimestamptz
		completedAt pgtypeTimestamptz
		resultVer   pgtypeInt8
	)
	if err := scan.Scan(
		&t.ID, &t.TenantID, &t.RequestID, &t.SessionID, &t.Protocol, &t.Endpoint,
		&t.Status, &t.CommitState, &t.SemanticContentCommitted,
		&errKind, &reasonCode, &t.AttemptCount, &t.FencingToken,
		&leaseOwner, &leaseUntil, &nextRetry, &t.DeadlineAt, &t.ExpiresAt,
		&t.RequestHash, &t.SnapshotVersion, &t.EncryptionKeyID,
		&resultVer, &t.CreatedAt, &t.UpdatedAt, &completedAt,
	); err != nil {
		return nil, err
	}
	t.LeaseOwner = leaseOwner.String
	t.ErrorKind = errKind.String
	t.ReasonCode = reasonCode.String
	t.LeaseUntil = leaseUntil.Time
	t.NextRetryAt = nextRetry.Time
	t.CompletedAt = completedAt.Time
	t.ResultVersion = resultVer.Int64
	return &t, nil
}

// pgtype 薄别名（避免在签名里散落 pgtype 原名）。
type (
	pgtypeText        = pgtype.Text
	pgtypeTimestamptz = pgtype.Timestamptz
	pgtypeInt8        = pgtype.Int8
)

// ClaimOptions 是 worker claim 参数。
type ClaimOptions struct {
	// Owner 是领取者标识（gateway-instance/request）。
	Owner string
	// Lease 是租约时长（doc 18 §13.1 默认 60s）。
	Lease time.Duration
	// Batch 是单次批量上限。
	Batch int
	// Now 供测试注入；零值用 time.Now。
	Now time.Time
}

// claimSelectSQL 是 doc 18 §11.3 的 execution claim 语句原文：
// 全局 commit_state 门禁 + 过期 lease 可重领 + deadline/next_retry 条件。
const claimSelectSQL = `
	SELECT id, status FROM durable_llm_tasks
	WHERE commit_state IN ('none', 'metadata')
	  AND status IN ('waiting_recovery', 'retry_scheduled', 'running')
	  AND (status <> 'running' OR lease_until < $2)
	  AND next_retry_at IS NOT NULL AND next_retry_at <= $2 + INTERVAL '5 seconds'
	  AND deadline_at > $2
	  AND (lease_until IS NULL OR lease_until < $2)
	ORDER BY next_retry_at
	FOR UPDATE SKIP LOCKED
	LIMIT $1`

// ClaimRunnable 领取一批可执行任务：单事务内 SELECT FOR UPDATE SKIP
// LOCKED + 逐行 UPDATE（running + 新 lease + fencing_token+1）。领取者
// 必须在 lease 内续租或提交，否则任务可被重领。
func (s *Store) ClaimRunnable(ctx context.Context, opts ClaimOptions) ([]*Task, error) {
	if opts.Owner == "" {
		return nil, fmt.Errorf("durable: claim owner required")
	}
	if opts.Batch <= 0 {
		opts.Batch = 8
	}
	if opts.Lease <= 0 {
		opts.Lease = 60 * time.Second
	}
	now := opts.Now
	if now.IsZero() {
		now = s.clock()
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("durable: begin claim: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck

	rows, err := tx.Query(ctx, claimSelectSQL, opts.Batch, now)
	if err != nil {
		return nil, fmt.Errorf("durable: claim select: %w", err)
	}
	type claimCandidate struct {
		id, fromStatus string
	}
	var candidates []claimCandidate
	for rows.Next() {
		var candidate claimCandidate
		if err := rows.Scan(&candidate.id, &candidate.fromStatus); err != nil {
			rows.Close()
			return nil, fmt.Errorf("durable: claim scan id: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("durable: claim rows: %w", err)
	}
	if len(candidates) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("durable: commit claim(empty): %w", err)
		}
		return nil, nil
	}

	claimed := make([]*Task, 0, len(candidates))
	for _, candidate := range candidates {
		row := tx.QueryRow(ctx, `
			UPDATE durable_llm_tasks
			SET status = 'running', lease_owner = $2, lease_until = $3,
			    fencing_token = fencing_token + 1, updated_at = $4
			WHERE id = $1
			RETURNING `+taskColumns,
			candidate.id, opts.Owner, now.Add(opts.Lease), now)
		task, err := scanTask(row)
		if err != nil {
			return nil, fmt.Errorf("durable: claim update %s: %w", candidate.id, err)
		}
		if err := appendEvent(ctx, tx, task, Status(candidate.fromStatus), StatusRunning, "claim", task.AttemptCount, now); err != nil {
			return nil, err
		}
		claimed = append(claimed, task)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("durable: commit claim: %w", err)
	}
	return claimed, nil
}

// RenewLease 为 running 任务续租。必须携带 (lease_owner, fencing_token)；
// 0 行（租约被夺/token 失效/任务不在 running）返回 ErrLeaseLost。
func (s *Store) RenewLease(ctx context.Context, taskID, owner string, token int64, until time.Time) error {
	tag, err := s.db.Exec(ctx, `
			UPDATE durable_llm_tasks
			SET lease_until = $4, updated_at = $5
			WHERE id = $1 AND lease_owner = $2 AND fencing_token = $3 AND status = 'running'`,
		taskID, owner, token, until, s.clock())
	if err != nil {
		return fmt.Errorf("durable: renew lease: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}

// RescheduleParams 是重排到 retry_scheduled 的参数。
type RescheduleParams struct {
	TaskID       string
	LeaseOwner   string
	FencingToken int64
	NextRetryAt  time.Time
	ErrorKind    string
	Reason       string
	Attempt      int
}

// Reschedule 把 running 任务重排为 retry_scheduled：带 fencing 条件 +
// commit_state IN ('none','metadata') 全局门禁（已 checkpoint 任务拒绝
// 重排，0 行 → ErrLeaseLost），同事务写 retry 事件并撤销租约。
func (s *Store) Reschedule(ctx context.Context, p RescheduleParams) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("durable: begin reschedule: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck

	var (
		tenantID, requestID, sessionID string
		newToken                       int64
	)
	err = tx.QueryRow(ctx, `
		UPDATE durable_llm_tasks
		SET status = 'retry_scheduled', next_retry_at = $4,
		    error_kind = $5, reason_code = $6,
		    lease_owner = NULL, lease_until = NULL,
		    attempt_count = attempt_count + 1, updated_at = $4
		WHERE id = $1 AND lease_owner = $2 AND fencing_token = $3
		  AND status = 'running'
		  AND commit_state IN ('none', 'metadata')
		RETURNING tenant_id, request_id, session_id, fencing_token`,
		p.TaskID, p.LeaseOwner, p.FencingToken, p.NextRetryAt, p.ErrorKind, p.Reason,
	).Scan(&tenantID, &requestID, &sessionID, &newToken)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLeaseLost
		}
		return fmt.Errorf("durable: reschedule: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO durable_llm_task_events (
			task_id, request_id, session_id, tenant_id,
			attempt, from_status, to_status, reason, fencing_token, created_at
		) VALUES ($1, $2, $3, $4, $5, 'running', 'retry_scheduled', $6, $7, $8)`,
		p.TaskID, requestID, sessionID, tenantID,
		p.Attempt, p.Reason, newToken, s.clock(),
	); err != nil {
		return fmt.Errorf("durable: reschedule event: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("durable: commit reschedule: %w", err)
	}
	return nil
}
