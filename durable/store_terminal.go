// store_terminal.go — write-ahead checkpoint / 原子终态 / reapers
// （doc 18 §11.3，SR-09）。
//
//   - CheckpointCommitState：语义帧写网络前的 DB 预写门禁；携带
//     (lease_owner, fencing_token)，commit_state 只允许前进；
//   - CommitTerminal：终态在单 PG 事务内原子提交（status + durable-result
//     域加密结果 + hash/version/completed_at），带 fencing 条件防终态竞态，
//     终态不可回退；成功后返回 PendingStore 投影载荷（SR-10）；
//   - ReapDeadlines / ReapUnsafeCheckpointed：deadline reaper 与
//     safety reaper，行锁 + fencing 原子迁移，与 worker 完成竞态时只有
//     一方形成终态。
package durable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// ReasonSurvivalExpired 是 deadline 过期的稳定 error code（doc 18 §11.3）。
const ReasonSurvivalExpired = "survival_expired"

// commitStateRankSQL 是 SQL 内的 commit_state 单调序（与 CommitStateRank 对齐）。
const commitStateRankSQL = `CASE commit_state
	WHEN 'none' THEN 0 WHEN 'metadata' THEN 1 WHEN 'content' THEN 2
	WHEN 'tool_call' THEN 3 ELSE 4 END`

// CheckpointParams 是 write-ahead checkpoint 输入。Payload 是 tool call
// checkpoint 载荷（tool call ID/类型/序号/参数摘要 hash，doc 18 §11.3）。
type CheckpointParams struct {
	TaskID       string
	LeaseOwner   string
	FencingToken int64
	State        CommitState
	Payload      []byte
}

// CheckpointCommitState 以 (task_id, lease_owner, fencing_token) 写
// commit_state checkpoint：只有 rank 前进才生效；0 行（token 失效/回退/
// 非 running）返回 ErrLeaseLost，调用方不得写网络语义帧（doc 18 §11.3
// write-ahead commit）。
func (s *Store) CheckpointCommitState(ctx context.Context, p CheckpointParams) error {
	if CommitStateRank(p.State) <= 0 && p.State != CommitStateNone {
		return fmt.Errorf("durable: unknown commit state %q", p.State)
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE durable_llm_tasks
		SET commit_state = $5,
		    checkpoint_payload = COALESCE($6::jsonb, checkpoint_payload),
		    semantic_content_committed = semantic_content_committed
		        OR ($5 = 'content' OR $5 = 'tool_call' OR $5 = 'terminal'),
		    updated_at = $7
		WHERE id = $1 AND lease_owner = $2 AND fencing_token = $3 AND status = 'running'
		  AND `+commitStateRankSQL+` < $4`,
		p.TaskID, p.LeaseOwner, p.FencingToken,
		CommitStateRank(p.State), string(p.State), jsonbOrNull(p.Payload), s.clock())
	if err != nil {
		return fmt.Errorf("durable: checkpoint: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}

// TerminalCommit 是终态提交输入。
type TerminalCommit struct {
	Task *Task
	// Outcome 必须是终态（completed/failed/expired/canceled）。
	Outcome Status
	// Body 是规范化最终结果明文；completed 必填，失败终态可空。
	Body []byte
	// ContentType：text/event-stream | application/json 等。
	ContentType string
	// ReasonCode / ErrorKind：失败终态的稳定错误码与分类。
	ReasonCode string
	ErrorKind  string
	// Attempt 是本次终态时的 attempt 序号（写事件审计）。
	Attempt int
}

// TerminalProjection 是终态提交成功后的 PendingStore 投影载荷（SR-10，
// doc 18 §11.4：携带 task_id/fencing_token/result_version/result_hash）。
// Committed=false 表示被 fencing 拒绝（终态竞态败者），不得投影。
type TerminalProjection struct {
	Committed     bool
	TaskID        string
	TenantID      string
	RequestID     string
	SessionID     string
	RequestHash   string
	Status        Status
	Body          string
	ContentType   string
	ReasonCode    string
	FencingToken  int64
	ResultVersion int64
	ResultHash    string
	CompletedAt   time.Time
	// ExpiresAt 是 Redis 投影 TTL 基准（deadline + result_read_window）。
	ExpiresAt time.Time
}

// CommitTerminal 在单个 PG 事务内原子写终态：加密结果（durable-result 域
// AAD）+ status + result_hash/version + completed_at + commit_state=
// 'terminal' + 终态事件。WHERE 携带 (lease_owner, fencing_token) 且排除
// 已终态行——与 reaper/其他 worker 并发时只有先取得行锁并校验 fencing 的
// 一方成功，另一方 0 行返回 Committed=false（doc 18 §11.3）。
func (s *Store) CommitTerminal(ctx context.Context, c TerminalCommit) (*TerminalProjection, error) {
	if c.Task == nil {
		return nil, errors.New("durable: terminal commit requires task")
	}
	if !IsTerminalStatus(c.Outcome) {
		return nil, fmt.Errorf("durable: outcome %q is not terminal", c.Outcome)
	}
	if c.Outcome == StatusCompleted && len(c.Body) == 0 {
		return nil, errors.New("durable: completed terminal requires result body")
	}
	if s.kr == nil {
		return nil, ErrNoKeyring
	}

	hash := sha256.Sum256(c.Body)
	resultHash := hex.EncodeToString(hash[:])

	// 结果与请求同级敏感（doc 18 §11.2）：durable-result 域加密，AAD 绑定
	// (tenant, task, request hash)。失败终态无正文时密文为 NULL。
	var ciphertext any
	if len(c.Body) > 0 {
		binding := secret.AADBinding{
			TenantID:    c.Task.TenantID,
			TaskID:      c.Task.ID,
			RequestHash: c.Task.RequestHash,
		}
		env, _, err := secret.EncryptWithAAD(c.Body, s.kr, secret.AADDomainDurableResult, binding)
		if err != nil {
			return nil, fmt.Errorf("durable: encrypt result: %w", err)
		}
		ciphertext = env
	}

	now := s.clock()
	proj := &TerminalProjection{
		TaskID:       c.Task.ID,
		TenantID:     c.Task.TenantID,
		RequestID:    c.Task.RequestID,
		SessionID:    c.Task.SessionID,
		RequestHash:  c.Task.RequestHash,
		Status:       c.Outcome,
		Body:         string(c.Body),
		ContentType:  c.ContentType,
		ReasonCode:   c.ReasonCode,
		FencingToken: c.Task.FencingToken,
		ResultHash:   resultHash,
		CompletedAt:  now,
		ExpiresAt:    c.Task.ExpiresAt,
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("durable: begin terminal: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck

	err = tx.QueryRow(ctx, `
		UPDATE durable_llm_tasks
		SET status = $4,
		    result_ciphertext = $5,
		    result_hash = $6,
		    content_type = $7,
		    completed_at = $8,
		    reason_code = $9,
		    error_kind = $10,
		    result_version = COALESCE(result_version, 0) + 1,
		    commit_state = 'terminal',
		    semantic_content_committed = TRUE,
		    attempt_count = $11,
		    lease_owner = NULL, lease_until = NULL,
		    updated_at = $12
		WHERE id = $1 AND lease_owner = $2 AND fencing_token = $3
		  AND status NOT IN ('completed', 'failed', 'expired', 'canceled')
		RETURNING result_version`,
		c.Task.ID, c.Task.LeaseOwner, c.Task.FencingToken,
		string(c.Outcome), ciphertext, resultHash, c.ContentType,
		now, c.ReasonCode, c.ErrorKind, c.Attempt, now,
	).Scan(&proj.ResultVersion)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 终态竞态败者：租约失效或任务已是终态，丢弃结果（§11.3）。
			proj.Committed = false
			return proj, nil
		}
		return nil, fmt.Errorf("durable: terminal update: %w", err)
	}
	proj.Committed = true

	if _, err := tx.Exec(ctx, `
			INSERT INTO durable_llm_task_events (
				task_id, request_id, session_id, tenant_id,
				attempt, from_status, to_status, reason, fencing_token, created_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		c.Task.ID, c.Task.RequestID, c.Task.SessionID, c.Task.TenantID,
		c.Attempt, string(c.Task.Status), string(c.Outcome),
		orDefault(c.ReasonCode, string(c.Outcome)), c.Task.FencingToken, now,
	); err != nil {
		return nil, fmt.Errorf("durable: terminal event: %w", err)
	}
	if err := enqueuePendingOutbox(ctx, tx, c.Task.ID, c.Task.TenantID, proj.ResultVersion, now); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("durable: commit terminal: %w", err)
	}
	return proj, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// reapedTask 是 reaper 返回的最小投影（足够构造 PendingStore 投影与审计）。
type reapedTask struct {
	ID            string
	TenantID      string
	RequestID     string
	SessionID     string
	RequestHash   string
	Status        Status
	FencingToken  int64
	ResultVersion int64
	ExpiresAt     time.Time
	ContentType   string
	ReasonCode    string
}

// ReapDeadlines 是 deadline reaper（doc 18 §11.3）：扫描所有非终态且
// deadline_at <= now 的任务（含 waiting_recovery/retry_scheduled/崩溃后的
// running），在单事务内以行锁 + fencing 原子迁移为 expired。
// 已越过语义检查点（commit_state content/tool_call）的任务被排除——
// 它们只能由 ReapUnsafeCheckpointed 终态化为 resume_safety_blocked，
// 打成干净的 expired 会误报「无副作用」（doc 18 §11.3 验收 19）：
// fencing_token+1、撤销 lease（旧 worker 后续更新因 token 失效而失败）、
// reason=survival_expired、completed_at、result_version+1，并写事件。
// 与 worker 完成并发时只有一方形成终态。
func (s *Store) ReapDeadlines(ctx context.Context, limit int, now time.Time) ([]*ReapedTaskInfo, error) {
	if now.IsZero() {
		now = s.clock()
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("durable: begin deadline reap: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck

	rows, err := reapSelect(ctx, tx, `
		SELECT id, status FROM durable_llm_tasks
		WHERE deadline_at <= $2
		  AND status NOT IN ('completed', 'failed', 'expired', 'canceled')
			  AND commit_state IN ('none', 'metadata')
			  AND NOT EXISTS (SELECT 1 FROM durable_task_settlement_intents si WHERE si.task_id = durable_llm_tasks.id)
			ORDER BY deadline_at
		FOR UPDATE SKIP LOCKED
		LIMIT $1`, limit, now)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("durable: commit deadline reap(empty): %w", err)
		}
		return nil, nil
	}

	var reaped []*ReapedTaskInfo
	for _, row := range rows {
		var rt reapedTask
		err := tx.QueryRow(ctx, `
			UPDATE durable_llm_tasks
			SET status = 'expired',
			    commit_state = 'terminal',
			    reason_code = $3, error_kind = $4,
			    fencing_token = fencing_token + 1,
			    lease_owner = NULL, lease_until = NULL,
			    completed_at = $2, updated_at = $2,
			    result_version = COALESCE(result_version, 0) + 1
			WHERE id = $1
			  AND status NOT IN ('completed', 'failed', 'expired', 'canceled')
			  AND commit_state IN ('none', 'metadata')
			RETURNING id, tenant_id, request_id, session_id, request_hash,
			          status, fencing_token, expires_at, COALESCE(content_type, ''),
			          reason_code, result_version`,
			row.id, now, ReasonSurvivalExpired, ReasonSurvivalExpired,
		).Scan(&rt.ID, &rt.TenantID, &rt.RequestID, &rt.SessionID, &rt.RequestHash,
			&rt.Status, &rt.FencingToken, &rt.ExpiresAt, &rt.ContentType,
			&rt.ReasonCode, &rt.ResultVersion)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				continue // 竞态败者（并发终态提交赢了一步）
			}
			return nil, fmt.Errorf("durable: deadline reap %s: %w", row.id, err)
		}
		if _, err := tx.Exec(ctx, `
				INSERT INTO durable_llm_task_events (
					task_id, request_id, session_id, tenant_id,
					attempt, from_status, to_status, reason, fencing_token, created_at
				) VALUES ($1, $2, $3, $4, 0, $5, 'expired', $6, $7, $8)`,
			rt.ID, rt.RequestID, rt.SessionID, rt.TenantID,
			row.fromStatus, ReasonSurvivalExpired, rt.FencingToken, now,
		); err != nil {
			return nil, fmt.Errorf("durable: deadline reap event: %w", err)
		}
		if err := enqueuePendingOutbox(ctx, tx, rt.ID, rt.TenantID, rt.ResultVersion, now); err != nil {
			return nil, err
		}
		reaped = append(reaped, &ReapedTaskInfo{reapedTask: rt, CompletedAt: now})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("durable: commit deadline reap: %w", err)
	}
	return reaped, nil
}

// ReapedTaskInfo 是 reaper 返回的任务信息（可投影 PendingStore failed 状态）。
type ReapedTaskInfo struct {
	reapedTask
	CompletedAt time.Time
}

// ReapUnsafeCheckpointed 是 safety reaper（doc 18 §11.3）：把**已弃置**（lease
// 过期或为空——持有者崩溃/断连）且非终态、commit_state IN
// ('content','tool_call','terminal') 的任务原子迁移到 resume_safety_blocked
// （fencing+1、撤租约），绝不进入 ExecuteAttempt。
//
// lease 护栏（2026-08-15 审计修正，doc 28 §1）：活跃前台持有者（lease 未
// 过期、持续续租中）绝不收割——它已向客户端提交语义内容，终态转换属于它
// 自己的 fenced 写（CommitTerminal）；否则前台随后的完成会被 fence 拒绝，
// 客户端拿到成功流但任务被记为 resume_safety_blocked 并投影失败。
func (s *Store) ReapUnsafeCheckpointed(ctx context.Context, limit int, now time.Time) ([]*Task, error) {
	if now.IsZero() {
		now = s.clock()
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("durable: begin safety reap: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck

	rows, err := reapSelect(ctx, tx, `
		SELECT id, status FROM durable_llm_tasks
		WHERE commit_state IN ('content', 'tool_call', 'terminal')
			  AND status NOT IN ('completed', 'failed', 'expired', 'canceled', 'resume_safety_blocked')
			  AND (lease_until IS NULL OR lease_until < $2)
			  AND NOT EXISTS (SELECT 1 FROM durable_task_settlement_intents si WHERE si.task_id = durable_llm_tasks.id)
			ORDER BY updated_at
		FOR UPDATE SKIP LOCKED
		LIMIT $1`, limit, now)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("durable: commit safety reap(empty): %w", err)
		}
		return nil, nil
	}

	var blocked []*Task
	for _, row := range rows {
		var (
			t            Task
			leaseOwner   pgtype.Text
			leaseUntil   pgtype.Timestamptz
			nextRetry    pgtype.Timestamptz
			completedAt  pgtype.Timestamptz
			errKind      pgtype.Text
			reasonCode   pgtype.Text
			resultVer    pgtype.Int8
			semCommitted bool
		)
		err := tx.QueryRow(ctx, `
				UPDATE durable_llm_tasks
				SET status = 'resume_safety_blocked',
				    reason_code = $3,
				    fencing_token = fencing_token + 1,
				    lease_owner = NULL, lease_until = NULL,
				    completed_at = $2,
				    result_version = COALESCE(result_version, 0) + 1,
				    updated_at = $2
				WHERE id = $1
				  AND status NOT IN ('completed', 'failed', 'expired', 'canceled', 'resume_safety_blocked')
				RETURNING `+taskColumns,
			row.id, now, ReasonResumeSafetyBlocked,
		).Scan(
			&t.ID, &t.TenantID, &t.RequestID, &t.SessionID, &t.Protocol, &t.Endpoint,
			&t.Status, &t.CommitState, &semCommitted,
			&errKind, &reasonCode, &t.AttemptCount, &t.FencingToken,
			&leaseOwner, &leaseUntil, &nextRetry, &t.DeadlineAt, &t.ExpiresAt,
			&t.RequestHash, &t.SnapshotVersion, &t.EncryptionKeyID,
			&resultVer, &t.CreatedAt, &t.UpdatedAt, &completedAt)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			return nil, fmt.Errorf("durable: safety reap %s: %w", row.id, err)
		}
		t.SemanticContentCommitted = semCommitted
		t.LeaseOwner = leaseOwner.String
		t.ErrorKind = errKind.String
		t.ReasonCode = reasonCode.String
		t.LeaseUntil = leaseUntil.Time
		t.NextRetryAt = nextRetry.Time
		t.CompletedAt = completedAt.Time
		t.ResultVersion = resultVer.Int64

		if _, err := tx.Exec(ctx, `
				INSERT INTO durable_llm_task_events (
					task_id, request_id, session_id, tenant_id,
					attempt, from_status, to_status, reason, fencing_token, created_at
				) VALUES ($1, $2, $3, $4, $5, $6, 'resume_safety_blocked', $7, $8, $9)`,
			t.ID, t.RequestID, t.SessionID, t.TenantID,
			t.AttemptCount, row.fromStatus, ReasonResumeSafetyBlocked, t.FencingToken, now,
		); err != nil {
			return nil, fmt.Errorf("durable: safety reap event: %w", err)
		}
		if err := enqueuePendingOutbox(ctx, tx, t.ID, t.TenantID, t.ResultVersion, now); err != nil {
			return nil, err
		}
		blocked = append(blocked, &t)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("durable: commit safety reap: %w", err)
	}
	return blocked, nil
}

// ReasonResumeSafetyBlocked 是安全阻断的稳定 reason code。
const ReasonResumeSafetyBlocked = "resume_safety_blocked"

// reapRow 是 reaper SELECT 阶段抓到的 (id, from_status)。
type reapRow struct {
	id         string
	fromStatus string
}

// reapSelect 在调用方事务内完成 SELECT ... FOR UPDATE SKIP LOCKED，行锁
// 保持到事务提交（doc 18 §11.3：行锁 + fencing 双重保护）。
func reapSelect(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]reapRow, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("durable: reap select: %w", err)
	}
	var out []reapRow
	for rows.Next() {
		var r reapRow
		if err := rows.Scan(&r.id, &r.fromStatus); err != nil {
			rows.Close()
			return nil, fmt.Errorf("durable: reap select scan: %w", err)
		}
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("durable: reap select rows: %w", err)
	}
	return out, nil
}
