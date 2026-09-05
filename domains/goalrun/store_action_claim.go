// store_action_claim.go — goal_run_actions 调度面：claim/renew/complete/requeue
// （设计 13 §6.2，Wave 3-A：Durable Continuation Scheduler）。
//
// 模式对齐 durable/store_claim.go：
//   - 过期 lease 可被任何 scheduler worker 抢占；
//   - 所有写操作携带 (lease_owner, fencing_token) CAS 条件，0 行即
//     ErrActionLeaseLost，旧 worker 必须丢弃结果、停止副作用；
//   - claim 时 fencing_token 单调 +1（与 durable.ClaimRunnable 一致）；
//   - 完成 / 失败 / 重排均带 CAS，避免迟到 worker 越过状态边界写入。
//
// 关键不变量（设计 13 §6.2）：
//   - 一个 GoalRun 同时只有一个有效 successor（CAS + 跨 action 唯一性）；
//   - 终态 sticky：cancel/terminal 优先于迟到 worker。
package goalrun

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// ClaimActionOptions 是 worker claim 参数。
type ClaimActionOptions struct {
	// Owner 是领取者标识（gateway instance id）。
	Owner string
	// Lease 是租约时长（与 durable 对齐默认 60s）。
	Lease time.Duration
	// Batch 是单次 batch 上限。
	Batch int
	// Now 供测试注入；零值用 Store.clock。
	Now time.Time
}

// claimActionSelectSQL：claim path 的查找条件。
//
// 复用 migration 555 新建的 idx_goal_run_actions_schedulable 索引。
// 同时支持过期 lease 抢占：status='pending' AND retry_at <= now AND 租约空闲。
const claimActionSelectSQL = `
	SELECT action_id
	FROM goal_run_actions
	WHERE status = 'pending'
	  AND retry_at IS NOT NULL
	  AND retry_at <= $2
	  AND (lease_until IS NULL OR lease_until < $2)
	ORDER BY retry_at
	FOR UPDATE SKIP LOCKED
	LIMIT $1`

// claimActionUpdateSQL：CAS 更新到 running。
//
//   - status='running'：claim 成功的唯一路径；
//   - lease_owner=新 owner；
//   - lease_until=now+lease；
//   - fencing_token+1：单调序列号，旧 worker 即使 lease_owner 错配也无法写入；
//   - claimed_at=now：审计；
//   - attempts+1：执行计数；
//   - updated_at=now。
const claimActionUpdateSQL = `
	UPDATE goal_run_actions
	SET status = 'running',
	    lease_owner = $2,
	    lease_until = $3,
	    fencing_token = fencing_token + 1,
	    claimed_at = $4,
	    attempts = attempts + 1,
	    updated_at = $4
	WHERE action_id = $1
	  AND status = 'pending'
	  AND (lease_until IS NULL OR lease_until < $4)
	RETURNING
	    action_id, goal_run_id, causation_id, action_type,
	    idempotency_key, expected_version, status,
	    retry_at, attempts, last_error,
	    lease_owner, lease_until, fencing_token, claimed_at,
	    created_at, updated_at`

// actionColumns 是 GoalRunAction 行的统一 SELECT/RETURNING 列（与 scanAction 对齐）。
const actionColumns = `action_id, goal_run_id, causation_id, action_type,
	idempotency_key, expected_version, status,
	retry_at, attempts, last_error,
	lease_owner, lease_until, fencing_token, claimed_at,
	created_at, updated_at`

// scanAction 把 actionColumns 顺序的行扫描为 GoalRunAction。
func scanAction(scan interface{ Scan(dest ...any) error }) (*GoalRunAction, error) {
	var a GoalRunAction
	var (
		lastError                                            string
		retryAt, leaseUntil, claimedAt, createdAt, updatedAt pgtype.Timestamptz
	)
	err := scan.Scan(
		&a.ActionID, &a.GoalRunID, &a.CausationID, &a.ActionType,
		&a.IdempotencyKey, &a.ExpectedVersion, &a.Status,
		&retryAt, &a.Attempts, &lastError,
		&a.LeaseOwner, &leaseUntil, &a.FencingToken, &claimedAt,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	a.RetryAt = retryAt.Time
	a.LeaseUntil = leaseUntil.Time
	a.ClaimedAt = claimedAt.Time
	a.CreatedAt = createdAt.Time
	a.UpdatedAt = updatedAt.Time
	a.LastError = lastError
	return &a, nil
}

// ClaimRunnableActions 领取一批可执行 action：单事务内
// `SELECT ... FOR UPDATE SKIP LOCKED` + 逐行 UPDATE（status='running' +
// 新 lease + fencing_token+1）。
//
// 选项：
//   - Owner: claim 标识（gateway instance id）
//   - Lease: 租约时长；默认 60s（与 durable 对齐）
//   - Batch: 单次 batch 上限；默认 4
//   - Now: 测试注入时钟；零值用 Store.clock
func (s *Store) ClaimRunnableActions(ctx context.Context, opts ClaimActionOptions) ([]*GoalRunAction, error) {
	if opts.Owner == "" {
		return nil, fmt.Errorf("goalrun: claim owner required")
	}
	if opts.Batch <= 0 {
		opts.Batch = 4
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
		return nil, fmt.Errorf("goalrun: begin claim: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck

	rows, err := tx.Query(ctx, claimActionSelectSQL, opts.Batch, now)
	if err != nil {
		return nil, fmt.Errorf("goalrun: claim select: %w", err)
	}
	type candidate struct{ id string }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("goalrun: claim scan id: %w", err)
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("goalrun: claim rows: %w", err)
	}
	if len(candidates) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("goalrun: commit claim(empty): %w", err)
		}
		return nil, nil
	}

	claimed := make([]*GoalRunAction, 0, len(candidates))
	for _, c := range candidates {
		row := tx.QueryRow(ctx, claimActionUpdateSQL,
			c.id, opts.Owner, now.Add(opts.Lease), now)
		action, err := scanAction(row)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// SELECT 已锁定该 row，UPDATE 返回 0 行意味着另一 path
				// 已修改 lease（CAS 失败）；忽略本行继续。
				continue
			}
			return nil, fmt.Errorf("goalrun: claim update %s: %w", c.id, err)
		}
		claimed = append(claimed, action)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("goalrun: commit claim: %w", err)
	}
	return claimed, nil
}

// RenewActionLease 为 running action 续租。
//
// CAS 条件：(action_id, lease_owner, fencing_token, status='running')，
// 0 行 = lease 被夺 / token 失效 / 状态边界，返回 ErrActionLeaseLost。
func (s *Store) RenewActionLease(ctx context.Context, actionID, owner string, token int64, until time.Time) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE goal_run_actions
		SET lease_until = $4, updated_at = $5
		WHERE action_id = $1
		  AND lease_owner = $2
		  AND fencing_token = $3
		  AND status = 'running'`,
		actionID, owner, token, until, s.clock())
	if err != nil {
		return fmt.Errorf("goalrun: renew action lease: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrActionLeaseLost
	}
	return nil
}

// CompleteActionParams 是 action 完成参数。
type CompleteActionParams struct {
	ActionID     string
	LeaseOwner   string
	FencingToken int64
	NewStatus    ActionStatus
	LastError    string // 仅 failed 时填充
	Now          time.Time
}

// CompleteAction 把 running action CAS 到终态（completed/failed/canceled）。
//
// 0 行 → ErrActionLeaseLost（旧 worker / 状态边界）。
func (s *Store) CompleteAction(ctx context.Context, p CompleteActionParams) error {
	if p.ActionID == "" {
		return ErrGoalRunIDRequired
	}
	if p.LeaseOwner == "" {
		return fmt.Errorf("goalrun: complete action requires lease owner")
	}
	if p.NewStatus != ActionStatusCompleted && p.NewStatus != ActionStatusFailed && p.NewStatus != ActionStatusCanceled {
		return fmt.Errorf("goalrun: complete action must be terminal status, got %s", p.NewStatus)
	}
	now := p.Now
	if now.IsZero() {
		now = s.clock()
	}

	tag, err := s.db.Exec(ctx, `
		UPDATE goal_run_actions
		SET status = $1,
		    last_error = $2,
		    lease_owner = '',
		    lease_until = NULL,
		    updated_at = $3
		WHERE action_id = $4
		  AND lease_owner = $5
		  AND fencing_token = $6
		  AND status = 'running'`,
		p.NewStatus, p.LastError, now, p.ActionID, p.LeaseOwner, p.FencingToken)
	if err != nil {
		return fmt.Errorf("goalrun: complete action: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrActionLeaseLost
	}
	return nil
}

// RequeueActionParams 是 action 重排参数（worker 失败后可重试）。
type RequeueActionParams struct {
	ActionID     string
	LeaseOwner   string
	FencingToken int64
	RetryAt      time.Time
	LastError    string
}

// RequeueAction 把 running action CAS 回到 pending：
//   - 重置 lease_owner/lease_until；
//   - 写入新的 retry_at；
//   - attempts 由 claim 时已 +1，不再增加。
func (s *Store) RequeueAction(ctx context.Context, p RequeueActionParams) error {
	if p.ActionID == "" {
		return ErrGoalRunIDRequired
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE goal_run_actions
		SET status = 'pending',
		    last_error = $1,
		    retry_at = $2,
		    lease_owner = '',
		    lease_until = NULL,
		    updated_at = $3
		WHERE action_id = $4
		  AND lease_owner = $5
		  AND fencing_token = $6
		  AND status = 'running'`,
		p.LastError, p.RetryAt, s.clock(),
		p.ActionID, p.LeaseOwner, p.FencingToken)
	if err != nil {
		return fmt.Errorf("goalrun: requeue action: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrActionLeaseLost
	}
	return nil
}

// ExpireLeases 把超过 lease_until 的 running action 重置回 pending。
//
// 由 reaper-style 调用；返回被重置的行数。
// 0 rows 是正常状态。Fencing token 不重置（旧 worker 之后任何 CAS 都失败）。
func (s *Store) ExpireLeases(ctx context.Context, now time.Time, limit int) (int64, error) {
	if now.IsZero() {
		now = s.clock()
	}
	if limit <= 0 {
		limit = 256
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE goal_run_actions
		SET status = 'pending',
		    lease_owner = '',
		    lease_until = NULL,
		    updated_at = $2
		WHERE status = 'running'
		  AND lease_until IS NOT NULL
		  AND lease_until < $2
		  AND action_id IN (
		      SELECT action_id FROM goal_run_actions
		      WHERE status = 'running'
		        AND lease_until IS NOT NULL
		        AND lease_until < $2
		      ORDER BY lease_until
		      LIMIT $1
		  )`,
		limit, now)
	if err != nil {
		return 0, fmt.Errorf("goalrun: expire leases: %w", err)
	}
	return tag.RowsAffected(), nil
}
