// store.go — GoalRunStore：GoalRun 持久编排账本的 PostgreSQL repository（设计 13 §6.2，Wave 2-A）。
//
// 关键语义：
//   - CreateGoalRun：单事务「插入 GoalRun + root step + created->queued 事件」，
//     幂等键为 (tenant_id, root_request_id)；
//   - 所有更新携带 (lease_owner, version) CAS 条件，更新 0 行即租约失效或版本冲突；
//   - CreateStep 使用单调 sequence 和 (goal_run_id, sequence) 唯一约束；
//   - 终态 sticky：cancel/terminal 优先于迟到 worker；
//   - 一个 GoalRun 同时只有一个有效 successor（通过 CAS 保证）。
package goalrun

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB 是 Store 的最小依赖接口：*pgxpool.Pool 与 pgxmock.PgxPoolIface 均满足。
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Store 是 goal_runs / goal_run_steps / goal_run_actions 的 repository。
// 并发安全：无共享可变状态，所有方法独立开事务。
type Store struct {
	db DB
	// now 供测试注入时钟；nil 用 time.Now。
	now func() time.Time
}

// NewStore 构造 GoalRunStore。
func NewStore(db DB) *Store {
	return &Store{db: db}
}

func (s *Store) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// CreateGoalRun 首次创建 GoalRun 并初始化 root step（设计 13 §6.2，W2-B）。
// 单事务插入 GoalRun（status='queued'、version=1、lease_owner/lease_until）
// 并写 root step（sequence=0）和 created->queued 事件。
// 幂等键为 (tenant_id, root_request_id)；重复调用返回 ErrDuplicateGoalRun。
func (s *Store) CreateGoalRun(ctx context.Context, input NewGoalRunInput) (*GoalRun, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}

	goalRunID := "gr_" + uuid.NewString()
	now := s.clock()

	run := &GoalRun{
		ID:                         goalRunID,
		TenantID:                   input.TenantID,
		APIKeyID:                   input.APIKeyID,
		RootGoalID:                 input.RootGoalID,
		RootSessionID:              input.RootSessionID,
		CurrentSessionID:           input.RootSessionID,
		RootRequestID:              input.RootRequestID,
		LastRequestID:              input.RootRequestID,
		Status:                     StatusQueued,
		PolicyVersion:              input.PolicyVersion,
		PolicySnapshot:             input.PolicySnapshot,
		InstructionHash:            input.InstructionHash,
		RedactedInstructionSummary: input.RedactedInstructionSummary,
		DeadlineAt:                 input.DeadlineAt,
		LeaseOwner:                 input.LeaseOwner,
		LeaseUntil:                 input.LeaseUntil,
		Version:                    1,
		CreatedAt:                  now,
		UpdatedAt:                  now,
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("goalrun: begin: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck

	// 插入 goal_runs
	if _, err := tx.Exec(ctx, `
		INSERT INTO goal_runs (
			id, tenant_id, api_key_id, root_goal_id,
			root_session_id, current_session_id,
			root_request_id, last_request_id,
			status, policy_version, policy_snapshot,
			instruction_hash, redacted_instruction_summary,
			turn_count, follow_up_count, retry_count,
			model_switch_count, handoff_count, tokens_used,
			deadline_at, lease_owner, lease_until, version,
			created_at, updated_at
		) VALUES (
			$1, $2, $3, $4,
			$5, $6,
			$7, $8,
			'queued', $9, $10,
			$11, $12,
			0, 0, 0,
			0, 0, 0,
			$13, $14, $15, 1,
			$16, $16
		)`,
		run.ID, input.TenantID, input.APIKeyID, input.RootGoalID,
		input.RootSessionID, input.RootSessionID,
		input.RootRequestID, input.RootRequestID,
		input.PolicyVersion, jsonbOrNull(input.PolicySnapshot),
		input.InstructionHash, input.RedactedInstructionSummary,
		input.DeadlineAt, input.LeaseOwner, input.LeaseUntil,
		now,
	); err != nil {
		if isUniqueViolation(err) {
			return nil, ErrDuplicateGoalRun
		}
		return nil, fmt.Errorf("goalrun: insert run: %w", err)
	}

	// 插入 root step (sequence=0)
	rootStep := &GoalRunStep{
		GoalRunID: goalRunID,
		Sequence:  0,
		RequestID: input.RootRequestID,
		SessionID: input.RootSessionID,
		Action:    ActionTypeContinue,
		Status:    StepStatusPending,
		CreatedAt: now,
	}
	if err := s.insertStep(ctx, tx, rootStep); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("goalrun: commit create: %w", err)
	}

	return run, nil
}

// GetGoalRun 按 ID 查询 GoalRun（带 tenant RLS）。
func (s *Store) GetGoalRun(ctx context.Context, goalRunID string) (*GoalRun, error) {
	row := s.db.QueryRow(ctx, `
		SELECT
			id, tenant_id, api_key_id, root_goal_id,
			root_session_id, current_session_id,
			root_request_id, last_request_id, last_durable_task_id,
			status, policy_version, policy_snapshot,
			instruction_hash, redacted_instruction_summary,
			turn_count, follow_up_count, retry_count,
			model_switch_count, handoff_count, tokens_used,
			last_progress_hash,
			deadline_at, lease_owner, lease_until, version,
			terminal_reason, created_at, updated_at, completed_at
		FROM goal_runs
		WHERE id = $1
	`, goalRunID)

	run := &GoalRun{}
	var policySnapshot []byte
	var lastDurableTaskID, lastProgressHash, leaseOwner, terminalReason string
	var leaseUntil, completedAt time.Time

	err := row.Scan(
		&run.ID, &run.TenantID, &run.APIKeyID, &run.RootGoalID,
		&run.RootSessionID, &run.CurrentSessionID,
		&run.RootRequestID, &run.LastRequestID, &lastDurableTaskID,
		&run.Status, &run.PolicyVersion, &policySnapshot,
		&run.InstructionHash, &run.RedactedInstructionSummary,
		&run.TurnCount, &run.FollowUpCount, &run.RetryCount,
		&run.ModelSwitchCount, &run.HandoffCount, &run.TokensUsed,
		&lastProgressHash,
		&run.DeadlineAt, &leaseOwner, &leaseUntil, &run.Version,
		&terminalReason, &run.CreatedAt, &run.UpdatedAt, &completedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrGoalRunNotFound
		}
		return nil, fmt.Errorf("goalrun: get run: %w", err)
	}

	run.PolicySnapshot = policySnapshot
	run.LastDurableTaskID = lastDurableTaskID
	run.LastProgressHash = lastProgressHash
	run.LeaseOwner = leaseOwner
	run.LeaseUntil = leaseUntil
	run.TerminalReason = terminalReason
	run.CompletedAt = completedAt

	return run, nil
}

// UpdateStatus 更新 GoalRun 状态，带 CAS (lease_owner, version) 门禁。
// 更新成功返回新 version；租约失效或版本冲突返回 ErrLeaseLost/ErrVersionConflict。
func (s *Store) UpdateStatus(ctx context.Context, goalRunID, leaseOwner string, expectedVersion int64, newStatus Status, terminalReason string) (int64, error) {
	if goalRunID == "" {
		return 0, ErrGoalRunIDRequired
	}

	now := s.clock()
	newVersion := expectedVersion + 1

	var completedAt any
	if IsTerminalStatus(newStatus) {
		completedAt = now
	}

	result, err := s.db.Exec(ctx, `
		UPDATE goal_runs
		SET status = $1,
		    terminal_reason = $2,
		    version = $3,
		    updated_at = $4,
		    completed_at = COALESCE(completed_at, $5)
		WHERE id = $6
		  AND lease_owner = $7
		  AND version = $8
		  AND status NOT IN ('completed', 'failed', 'canceled', 'expired', 'manual_required')
	`, newStatus, terminalReason, newVersion, now, completedAt,
		goalRunID, leaseOwner, expectedVersion)

	if err != nil {
		return 0, fmt.Errorf("goalrun: update status: %w", err)
	}

	if result.RowsAffected() == 0 {
		return 0, ErrLeaseLost
	}

	return newVersion, nil
}

// CreateStep 创建新 step，带单调 sequence 约束（设计 13 §6.2）。
// sequence 冲突返回 ErrSequenceConflict。
func (s *Store) CreateStep(ctx context.Context, step *GoalRunStep) error {
	if step.GoalRunID == "" {
		return ErrGoalRunIDRequired
	}
	if step.RequestID == "" {
		return ErrRequestIDRequired
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("goalrun: begin: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck

	step.CreatedAt = s.clock()
	if err := s.insertStep(ctx, tx, step); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("goalrun: commit step: %w", err)
	}

	return nil
}

func (s *Store) insertStep(ctx context.Context, tx pgx.Tx, step *GoalRunStep) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO goal_run_steps (
			goal_run_id, sequence, request_id, parent_request_id,
			session_id, durable_task_id, action, status,
			response_hash, result_version, created_at
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8,
			$9, $10, $11
		)`,
		step.GoalRunID, step.Sequence, step.RequestID, step.ParentRequestID,
		step.SessionID, step.DurableTaskID, step.Action, step.Status,
		step.ResponseHash, step.ResultVersion, step.CreatedAt,
	); err != nil {
		if isUniqueViolation(err) {
			return ErrSequenceConflict
		}
		return fmt.Errorf("goalrun: insert step: %w", err)
	}
	return nil
}

// CreateAction 创建可幂等 action（设计 13 §6.2）。
// idempotency_key 冲突时返回既有 action（幂等）。
func (s *Store) CreateAction(ctx context.Context, action *GoalRunAction) error {
	if action.GoalRunID == "" {
		return ErrGoalRunIDRequired
	}
	if action.ActionType == "" {
		return ErrActionTypeRequired
	}
	if action.IdempotencyKey == "" {
		return ErrIdempotencyKeyRequired
	}

	if action.ActionID == "" {
		action.ActionID = "act_" + uuid.NewString()
	}
	now := s.clock()
	action.CreatedAt = now
	action.UpdatedAt = now

	_, err := s.db.Exec(ctx, `
		INSERT INTO goal_run_actions (
			action_id, goal_run_id, causation_id, action_type,
			idempotency_key, expected_version, status,
			retry_at, attempts, last_error, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7,
			$8, $9, $10, $11, $12
		)
		ON CONFLICT (goal_run_id, idempotency_key) DO NOTHING
	`,
		action.ActionID, action.GoalRunID, action.CausationID, action.ActionType,
		action.IdempotencyKey, action.ExpectedVersion, action.Status,
		action.RetryAt, action.Attempts, action.LastError, now, now,
	)

	if err != nil {
		return fmt.Errorf("goalrun: insert action: %w", err)
	}

	return nil
}

// GetAction 按 idempotency_key 查询 action（幂等键查询）。
func (s *Store) GetAction(ctx context.Context, goalRunID, idempotencyKey string) (*GoalRunAction, error) {
	row := s.db.QueryRow(ctx, `
		SELECT
			action_id, goal_run_id, causation_id, action_type,
			idempotency_key, expected_version, status,
			retry_at, attempts, last_error, created_at, updated_at
		FROM goal_run_actions
		WHERE goal_run_id = $1 AND idempotency_key = $2
	`, goalRunID, idempotencyKey)

	action := &GoalRunAction{}
	err := row.Scan(
		&action.ActionID, &action.GoalRunID, &action.CausationID, &action.ActionType,
		&action.IdempotencyKey, &action.ExpectedVersion, &action.Status,
		&action.RetryAt, &action.Attempts, &action.LastError, &action.CreatedAt, &action.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrActionNotFound
		}
		return nil, fmt.Errorf("goalrun: get action: %w", err)
	}

	return action, nil
}

// jsonbOrNull 空切片落 NULL，非空 JSON 原样传递。
func jsonbOrNull(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// isUniqueViolation 识别 PG 23505。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
