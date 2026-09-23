// hostedtask/store.go — hosted_tasks 三表的唯一写入方（§6.1）。
//
// 模式参照 routeincident/store.go：事务内 SELECT … FOR UPDATE 行锁 +
// revision CAS；事件追加在同一事务内以 COALESCE(MAX(seq),0)+1 分配，
// 行锁保证 seq 连续不冲突。租户路径置 app.current_tenant，worker 路径置
// app.bypass_rls（迁移 711 RLS 策略的两种放行形态，§6.1 bypass 仅 worker）。
//
// 本 store 是投影写入方，不是执行 owner（D4）：所有 ACC 语义（重试=同键
// 重放、租约、围栏）都在 ACC/companion 侧。
package hostedtask

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store 是 hosted_tasks / hosted_task_events / hosted_task_callbacks 的
// 持久层。并发安全：每次迁移持自身行锁。
type Store struct {
	pool *pgxpool.Pool
}

// NewStore 构造 Store。pool 必须非 nil。
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// taskColumns 是 Task 行扫描的列清单（单点维护）。sse_cursor 列自 R65 起
// 停用（SSE 订阅移除，轮询为状态权威——设计文档 §10 D7），列保留不迁移。
const taskColumns = `
	id, tenant_id, api_key_id, goal, done_when, context, environment,
	status, acc_command_id, acc_run_id, dispatch_key, dispatch_attempts,
	gw_session_id, callback_url_hash,
	result, result_version, revision, idempotency_key, request_hash,
	deadline_at, created_at, updated_at, completed_at
`

func scanTask(row pgx.Row) (*Task, error) {
	var t Task
	var contextJSON, envJSON, resultJSON []byte
	var completedAt *time.Time
	if err := row.Scan(
		&t.ID, &t.TenantID, &t.APIKeyID, &t.Goal, &t.DoneWhen,
		&contextJSON, &envJSON,
		&t.Status, &t.AccCommandID, &t.AccRunID, &t.DispatchKey, &t.DispatchAttempts,
		&t.GwSessionID, &t.CallbackURLHash,
		&resultJSON, &t.ResultVersion, &t.Revision, &t.IdempotencyKey, &t.RequestHash,
		&t.DeadlineAt, &t.CreatedAt, &t.UpdatedAt, &completedAt,
	); err != nil {
		return nil, err
	}
	if len(contextJSON) > 0 {
		_ = json.Unmarshal(contextJSON, &t.Context)
	}
	if len(envJSON) > 0 {
		_ = json.Unmarshal(envJSON, &t.Environment)
	}
	if len(resultJSON) > 0 {
		_ = json.Unmarshal(resultJSON, &t.Result)
	}
	t.CompletedAt = completedAt
	return &t, nil
}

// setTenant 在事务内置 app.current_tenant（handler 路径的 RLS 租户上下文）。
func setTenant(ctx context.Context, tx pgx.Tx, tenantID string) {
	if tenantID == "" {
		return
	}
	_, _ = tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tenantID)
}

// setWorkerBypass 在事务内置 app.bypass_rls=true（reconciler/deliverer 路径；
// 迁移 711 策略：bypass 仅 worker 角色）。
func setWorkerBypass(ctx context.Context, tx pgx.Tx) {
	_, _ = tx.Exec(ctx, "SELECT set_config('app.bypass_rls', 'true', true)")
}

// begin 开启一个事务并打上 worker 放行 GUC。
func (s *Store) begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	setWorkerBypass(ctx, tx)
	return tx, nil
}

// appendEvent 在已持行锁的事务内追加事件（seq = max+1）。
func appendEvent(ctx context.Context, tx pgx.Tx, taskID string, ev EventType, payload map[string]any) (int64, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshal event payload: %w", err)
	}
	var seq int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO hosted_task_events (task_id, seq, event_type, payload)
		SELECT $1, COALESCE(MAX(seq), 0) + 1, $2, $3::jsonb
		FROM hosted_task_events WHERE task_id = $1
		RETURNING seq
	`, taskID, string(ev), string(payloadJSON)).Scan(&seq); err != nil {
		return 0, fmt.Errorf("append event %s: %w", ev, err)
	}
	return seq, nil
}

// ─── 创建（§3.1 ①：幂等；同键同体重放、同键异体 409）────────────────────

// CreateTask 创建委托任务：Tx{ INSERT hosted_tasks(delegated) +
// hosted_task_events(accepted) + hosted_task_callbacks }。
// 幂等：同 (tenant_id, idempotency_key) 已存在时，request_hash 一致 → 返回
// 原任务 (created=false)；不一致 → ErrIdempotencyConflict。
func (s *Store) CreateTask(ctx context.Context, in CreateInput) (*Task, bool, error) {
	if in.IdempotencyKey == "" || in.TenantID == "" || in.Goal == "" {
		return nil, false, fmt.Errorf("hostedtask: tenant/idempotency_key/goal required")
	}
	if in.Deadline.IsZero() {
		in.Deadline = time.Now().UTC().Add(time.Hour)
	}
	taskID, err := newTaskID()
	if err != nil {
		return nil, false, fmt.Errorf("generate task id: %w", err)
	}
	if in.GwSessionID == "" {
		in.GwSessionID = "gw_" + taskID
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, false, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	setTenant(ctx, tx, in.TenantID)

	contextJSON, err := json.Marshal(orEmptyMap(in.Context))
	if err != nil {
		return nil, false, fmt.Errorf("marshal context: %w", err)
	}
	envJSON, err := json.Marshal(orEmptyMap(in.Environment))
	if err != nil {
		return nil, false, fmt.Errorf("marshal environment: %w", err)
	}

	row := tx.QueryRow(ctx, `
		INSERT INTO hosted_tasks (
			id, tenant_id, api_key_id, goal, done_when, context, environment,
			status, gw_session_id, callback_url_hash,
			idempotency_key, request_hash, deadline_at
		) VALUES (
			$1, $2, $3, $4, $5, $6::jsonb, $7::jsonb,
			'delegated', $8, $9,
			$10, $11, $12
		)
		ON CONFLICT (tenant_id, idempotency_key) DO NOTHING
		RETURNING `+taskColumns,
		taskID, in.TenantID, in.APIKeyID, in.Goal, in.DoneWhen,
		string(contextJSON), string(envJSON),
		in.GwSessionID, in.CallbackHash,
		in.IdempotencyKey, in.RequestHash, in.Deadline,
	)
	task, err := scanTask(row)
	if err == nil {
		// 新建：同事务追加 accepted 事件 + 回调台账行。
		if _, err := appendEvent(ctx, tx, task.ID, EventAccepted, map[string]any{
			"goal":        task.Goal,
			"deadline_at": task.DeadlineAt.UTC().Format(time.RFC3339),
			"callback":    task.CallbackURLHash != "",
		}); err != nil {
			return nil, false, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO hosted_task_callbacks (task_id, url_enc, secret_enc)
			VALUES ($1, $2, $3)
			ON CONFLICT (task_id) DO NOTHING
		`, task.ID, in.CallbackURLEnc, in.CallbackSecEnc); err != nil {
			return nil, false, fmt.Errorf("insert callback row: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, false, fmt.Errorf("commit create: %w", err)
		}
		return task, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("insert hosted_task: %w", err)
	}

	// 幂等重放：取原行并比对 request_hash（同键同体 200 / 异体 409）。
	existing, err := scanTask(tx.QueryRow(ctx, `
		SELECT `+taskColumns+` FROM hosted_tasks
		WHERE tenant_id = $1 AND idempotency_key = $2
	`, in.TenantID, in.IdempotencyKey))
	if err != nil {
		return nil, false, fmt.Errorf("load existing task: %w", err)
	}
	if existing.RequestHash != in.RequestHash {
		return nil, false, ErrIdempotencyConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit replay: %w", err)
	}
	return existing, false, nil
}

func orEmptyMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// ─── 读取（§4.1：跨租户/不存在统一 ErrNotFound → 404）────────────────────

// GetTask 按 id 读取，强制租户作用域。
func (s *Store) GetTask(ctx context.Context, tenantID, id string) (*Task, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	setTenant(ctx, tx, tenantID)

	task, err := scanTask(tx.QueryRow(ctx, `
		SELECT `+taskColumns+` FROM hosted_tasks WHERE id = $1 AND tenant_id = $2
	`, id, tenantID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get hosted_task: %w", err)
	}
	return task, nil
}

// ListEvents 返回事件时间线（升序），强制租户作用域。
func (s *Store) ListEvents(ctx context.Context, tenantID, taskID string, limit int) ([]Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	setTenant(ctx, tx, tenantID)

	rows, err := tx.Query(ctx, `
		SELECT e.task_id, e.seq, e.event_type, e.payload, e.created_at
		FROM hosted_task_events e
		JOIN hosted_tasks t ON t.id = e.task_id
		WHERE e.task_id = $1 AND t.tenant_id = $2
		ORDER BY e.seq ASC
		LIMIT $3
	`, taskID, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var payloadJSON []byte
		var evType string
		if err := rows.Scan(&e.TaskID, &e.Seq, &evType, &payloadJSON, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Type = EventType(evType)
		if len(payloadJSON) > 0 {
			_ = json.Unmarshal(payloadJSON, &e.Payload)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ─── 取消（§3.2：CAS 终态抢占 → 事件 cancel_requested + cancelled）────────

// cancelRowInTx 在已持行锁的事务内做 cancelled 终态抢占 + cancel_requested/
// cancelled 事件（cancel 端点与 recall 共用；调用方保证 cur 非终态且迁移
// 合法）。
func cancelRowInTx(ctx context.Context, tx pgx.Tx, cur *Task) (*Task, error) {
	task, err := scanTask(tx.QueryRow(ctx, `
		UPDATE hosted_tasks
		SET status = 'cancelled', revision = revision + 1,
		    completed_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND revision = $2
		RETURNING `+taskColumns,
		cur.ID, cur.Revision,
	))
	if err != nil {
		return nil, fmt.Errorf("cancel cas: %w", err)
	}
	for _, ev := range []EventType{EventCancelRequested, EventCancelled} {
		if _, err := appendEvent(ctx, tx, cur.ID, ev, map[string]any{
			"actor": "tenant", "from_status": string(cur.Status),
		}); err != nil {
			return nil, err
		}
	}
	return task, nil
}

// CancelTask 尝试把任务置为 cancelled（终态 sticky 抢占）。返回
// (task, won)：won=false 表示任务已终态（调用方回 409）。与 complete 的
// 竞争由行锁 + 终态检查保证只活一个（矩阵 H）。
func (s *Store) CancelTask(ctx context.Context, tenantID, id string) (*Task, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, false, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	setTenant(ctx, tx, tenantID)

	cur, err := scanTask(tx.QueryRow(ctx, `
		SELECT `+taskColumns+` FROM hosted_tasks WHERE id = $1 AND tenant_id = $2 FOR UPDATE
	`, id, tenantID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, ErrNotFound
		}
		return nil, false, fmt.Errorf("lock task: %w", err)
	}
	if cur.Status.Terminal() {
		return cur, false, nil
	}
	if !CanTransition(cur.Status, StatusCancelled) {
		return cur, false, nil
	}
	task, err := cancelRowInTx(ctx, tx, cur)
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit cancel: %w", err)
	}
	return task, true, nil
}

// ─── 召回（§3.3 轻量快照路径，R65 P1 子集）─────────────────────────────────

// RecallStatus 是 RecallTask 的召回路径标注。
type RecallStatus string

const (
	// RecallCancelled 表示本轮完成了非终态 → cancelled 的终态抢占。
	RecallCancelled RecallStatus = "cancelled"
	// RecallAlreadyTerminal 表示召回时任务已终态（只快照 + 补事件）。
	RecallAlreadyTerminal RecallStatus = "already_terminal"
)

// RecallOutcome 是召回事务的结果。
type RecallOutcome struct {
	Task         *Task
	EventSeq     int64                   // recalled 事件 seq（回调 event_id 用）
	RecallStatus RecallStatus            // 本轮是否做了终态抢占
	Packet       StructuredHandoffPacket // 最终任务行构建的移交包（§3.3 ④）
}

// RecallTask 召回（§3.3 轻量路径，单事务原子）：
//  1. 非终态 → cancelled 终态抢占（与 cancel 端点同款语义，含
//     cancel_requested/cancelled 事件；不动 ACC 权威状态，ACC cancel 由
//     handler 在提交后尽力补发）；
//  2. 以抢占后的最终任务行构建 StructuredHandoffPacket（§3.3 ④）；
//  3. 追加 recalled 事件（payload 携带 recall_status + handoff_packet，
//     前端凭包续跑）；
//  4. 回调台账置 pending，event_id 复用固定幂等键 EventID(taskID, seq)
//     （§6.1，与 SettleTask 同款；重投由接收方按 event_id 幂等）。
//
// 终态任务不抢占，只补 recalled 事件 + 重置回调（每次召回交付一次最新包）。
// 幂等性：同任务重复 recall 各自追加一条 recalled 事件（append-only 审计），
// 返回包内容一致。跨租户/不存在 → ErrNotFound。
func (s *Store) RecallTask(ctx context.Context, tenantID, id string) (*RecallOutcome, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	setTenant(ctx, tx, tenantID)

	cur, err := scanTask(tx.QueryRow(ctx, `
		SELECT `+taskColumns+` FROM hosted_tasks WHERE id = $1 AND tenant_id = $2 FOR UPDATE
	`, id, tenantID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("lock task: %w", err)
	}

	status := RecallAlreadyTerminal
	if !cur.Status.Terminal() && CanTransition(cur.Status, StatusCancelled) {
		if _, err := cancelRowInTx(ctx, tx, cur); err != nil {
			return nil, err
		}
		status = RecallCancelled
	}

	// 以抢占后的最终行构建移交包（与事件同事务，保证包与台账一致）。
	task, err := scanTask(tx.QueryRow(ctx, `
		SELECT `+taskColumns+` FROM hosted_tasks WHERE id = $1
	`, id))
	if err != nil {
		return nil, fmt.Errorf("reload recalled task: %w", err)
	}
	packet := buildHandoffPacket(task)

	payload := map[string]any{
		"recall_status":  string(status),
		"next_owner":     "recall_caller",
		"handoff_packet": packet,
	}
	seq, err := appendEvent(ctx, tx, id, EventRecalled, payload)
	if err != nil {
		return nil, err
	}
	// 回调入队：event_id 复用 EventID(taskID, seq) 幂等键（§6.1）。
	if _, err := tx.Exec(ctx, `
		UPDATE hosted_task_callbacks
		SET status = 'pending', event_id = $2, event_seq = $3,
		    next_attempt_at = NOW(), attempts = 0, last_error = '', updated_at = NOW()
		WHERE task_id = $1 AND url_enc != ''
	`, id, EventID(id, seq), seq); err != nil {
		return nil, fmt.Errorf("enqueue recall callback: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit recall: %w", err)
	}
	return &RecallOutcome{Task: task, EventSeq: seq, RecallStatus: status, Packet: packet}, nil
}

// ─── dispatch 投影（§3.1 ②）───────────────────────────────────────────────

// BeginDispatch 认领一轮 dispatch：delegated→dispatching；已处 dispatching
// 的重试轮也返回 true。返回 false = 任务不处于可 dispatch 状态。
func (s *Store) BeginDispatch(ctx context.Context, id string) (bool, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM hosted_tasks WHERE id = $1 FOR UPDATE`, id).Scan(&status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("lock for dispatch: %w", err)
	}
	st := Status(status)
	if st.Terminal() || (st != StatusDelegated && st != StatusDispatching) {
		return false, nil
	}
	if st == StatusDelegated {
		if _, err := tx.Exec(ctx, `
			UPDATE hosted_tasks SET status='dispatching', revision=revision+1, updated_at=NOW()
			WHERE id = $1
		`, id); err != nil {
			return false, fmt.Errorf("begin dispatch: %w", err)
		}
	}
	return true, tx.Commit(ctx)
}

// RecordDispatchSuccess 记录 ACC dispatch 202：dispatching→running，落
// acc_command_id/acc_run_id/dispatch_key，并追加 running 事件。
func (s *Store) RecordDispatchSuccess(ctx context.Context, id, commandID, runID, dispatchKey string) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := scanTask(tx.QueryRow(ctx, `SELECT `+taskColumns+` FROM hosted_tasks WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock for dispatch success: %w", err)
	}
	if cur.Status.Terminal() {
		return nil // 迟到的 dispatch 成功不打断终态（CAS 0 行语义）
	}
	if _, err := tx.Exec(ctx, `
		UPDATE hosted_tasks
		SET status = 'running', acc_command_id = $2, acc_run_id = $3,
		    dispatch_key = $4, dispatch_attempts = dispatch_attempts + 1,
		    last_dispatch_at = NOW(), revision = revision + 1, updated_at = NOW()
		WHERE id = $1
	`, id, commandID, runID, dispatchKey); err != nil {
		return fmt.Errorf("record dispatch: %w", err)
	}
	if _, err := appendEvent(ctx, tx, id, EventRunning, map[string]any{
		"acc_command_id": commandID, "acc_run_id": runID, "dispatch_key": dispatchKey,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RecordDispatchDegraded 记录一轮 dispatch 失败（§3.1 ②：持续失败 → 事件
// dispatch_degraded），任务停留 dispatching 等待同键重放。
func (s *Store) RecordDispatchDegraded(ctx context.Context, id string, attempt int, reason string) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		UPDATE hosted_tasks
		SET dispatch_attempts = $2, last_dispatch_at = NOW(), updated_at = NOW()
		WHERE id = $1
	`, id, attempt); err != nil {
		return fmt.Errorf("record dispatch degraded: %w", err)
	}
	if _, err := appendEvent(ctx, tx, id, EventDispatchDegraded, map[string]any{
		"attempt": attempt, "reason": truncErr(reason),
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ListDispatchDue 返回到期需要 dispatch 轮次的任务（delegated 首轮 /
// dispatching 同键重放轮），跳过已过期的（归 reaper）。
func (s *Store) ListDispatchDue(ctx context.Context, now time.Time, retryAfter time.Duration, limit int) ([]Task, error) {
	if limit <= 0 {
		limit = 50
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT `+taskColumns+` FROM hosted_tasks
		WHERE status IN ('delegated', 'dispatching')
		  AND deadline_at > $1
		  AND (last_dispatch_at IS NULL OR last_dispatch_at <= $2)
		ORDER BY deadline_at ASC
		LIMIT $3
	`, now, now.Add(-retryAfter), limit)
	if err != nil {
		return nil, fmt.Errorf("list dispatch due: %w", err)
	}
	defer rows.Close()
	return scanTasks(rows)
}

// ─── reconciler 投影（§3.1 ④：CAS revision + 终态 sticky）────────────────

// SettleInput 是终态落定的输入（§3.1 ④/⑤：终态 + result + 事件 + 回调入队
// 原子提交）。
type SettleInput struct {
	To        Status         // completed | failed | needs_review | expired
	EventType EventType      // completed | failed | expired
	Result    map[string]any // PG 权威结果（可为 nil）
	Payload   map[string]any // 事件附注
}

// SettleTask 终态落定：CAS（非终态才生效，迟到回写 0 行 → settled=false，
// 矩阵 H）；同事务追加终态事件、写 result（result_version 单调 +1）、并把
// 回调台账置 pending（§3.1 ⑤）。返回终态事件的 seq（回调 event_id 用）。
func (s *Store) SettleTask(ctx context.Context, id string, in SettleInput) (settled bool, seq int64, err error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return false, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := scanTask(tx.QueryRow(ctx, `SELECT `+taskColumns+` FROM hosted_tasks WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, 0, ErrNotFound
		}
		return false, 0, fmt.Errorf("lock for settle: %w", err)
	}
	if cur.Status.Terminal() || !CanTransition(cur.Status, in.To) {
		return false, 0, nil
	}

	resultJSON := []byte{}
	if in.Result != nil {
		resultJSON, err = json.Marshal(in.Result)
		if err != nil {
			return false, 0, fmt.Errorf("marshal result: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE hosted_tasks
		SET status = $2, completed_at = NOW(), updated_at = NOW(),
		    revision = revision + 1,
		    result = COALESCE($3::jsonb, result),
		    result_version = result_version + CASE WHEN $3::jsonb IS NOT NULL THEN 1 ELSE 0 END
		WHERE id = $1 AND revision = $4
	`, id, string(in.To), string(resultJSON), cur.Revision); err != nil {
		return false, 0, fmt.Errorf("settle cas: %w", err)
	}
	seq, err = appendEvent(ctx, tx, id, in.EventType, in.Payload)
	if err != nil {
		return false, 0, err
	}
	// 回调入队（有回调配置才置 pending；崩溃在 commit 前则不入队，事件不会丢——
	// 事件先于回调）。
	if _, err := tx.Exec(ctx, `
		UPDATE hosted_task_callbacks
		SET status = 'pending', event_id = $2, event_seq = $3,
		    next_attempt_at = NOW(), attempts = 0, last_error = '', updated_at = NOW()
		WHERE task_id = $1 AND url_enc != ''
	`, id, EventID(id, seq), seq); err != nil {
		return false, 0, fmt.Errorf("enqueue callback: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, 0, fmt.Errorf("commit settle: %w", err)
	}
	return true, seq, nil
}

// RecordProgress 追加 progress 事件（非终态投影；不迁移状态）。
func (s *Store) RecordProgress(ctx context.Context, id string, payload map[string]any) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		UPDATE hosted_tasks SET updated_at = NOW() WHERE id = $1
		  AND status NOT IN ('completed','failed','needs_review','cancelled','expired')
	`, id); err != nil {
		return fmt.Errorf("touch task: %w", err)
	}
	if _, err := appendEvent(ctx, tx, id, EventProgress, payload); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ListActiveRuns 返回需要轮询投影的非终态任务（有 acc_run_id）。
func (s *Store) ListActiveRuns(ctx context.Context, limit int) ([]Task, error) {
	if limit <= 0 {
		limit = 100
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		SELECT `+taskColumns+` FROM hosted_tasks
		WHERE acc_run_id != ''
		  AND status NOT IN ('completed','failed','needs_review','cancelled','expired')
		ORDER BY deadline_at ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list active runs: %w", err)
	}
	defer rows.Close()
	return scanTasks(rows)
}

// ─── deadline reaper（§4.2：任意非终态 → expired）─────────────────────────

// ClaimExpiredTasks 认领已过 deadline 的非终态任务（SKIP LOCKED 防多 worker
// 争抢），逐个过期落定（复用 SettleTask 的 CAS + 事件 + 回调入队）。
func (s *Store) ClaimExpiredTasks(ctx context.Context, now time.Time, limit int) (int, error) {
	if limit <= 0 {
		limit = 50
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return 0, err
	}
	var ids []string
	rows, err := tx.Query(ctx, `
		SELECT id FROM hosted_tasks
		WHERE deadline_at <= $1
		  AND status NOT IN ('completed','failed','needs_review','cancelled','expired')
		ORDER BY deadline_at ASC
		LIMIT $2
		FOR UPDATE SKIP LOCKED
	`, now, limit)
	if err != nil {
		_ = tx.Rollback(ctx)
		return 0, fmt.Errorf("claim expired scan: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			_ = tx.Rollback(ctx)
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		_ = tx.Rollback(ctx)
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}

	expired := 0
	for _, id := range ids {
		ok, _, err := s.SettleTask(ctx, id, SettleInput{
			To:        StatusExpired,
			EventType: EventExpired,
			Payload:   map[string]any{"reason": "deadline_exceeded"},
		})
		if err != nil {
			return expired, err
		}
		if ok {
			expired++
		}
	}
	return expired, nil
}

// GetEvent 按 (task_id, seq) 读取单条事件（回调投递重建 payload 用：
// CallbackJob.EventSeq 精确定位 recalled 事件的 recall_status/handoff_packet，
// §3.3）。强制租户作用域，跨租户/不存在 → ErrNotFound。
func (s *Store) GetEvent(ctx context.Context, tenantID, taskID string, seq int64) (*Event, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	setTenant(ctx, tx, tenantID)

	var e Event
	var payloadJSON []byte
	var evType string
	err = tx.QueryRow(ctx, `
		SELECT e.task_id, e.seq, e.event_type, e.payload, e.created_at
		FROM hosted_task_events e
		JOIN hosted_tasks t ON t.id = e.task_id
		WHERE e.task_id = $1 AND t.tenant_id = $2 AND e.seq = $3
	`, taskID, tenantID, seq).Scan(&e.TaskID, &e.Seq, &evType, &payloadJSON, &e.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get hosted_task_event: %w", err)
	}
	e.Type = EventType(evType)
	if len(payloadJSON) > 0 {
		_ = json.Unmarshal(payloadJSON, &e.Payload)
	}
	return &e, nil
}

// ─── 回调台账（§3.1 ⑤：deliverer 认领/回写）──────────────────────────────

// CallbackJob 是 deliverer 的一次投递任务（URL/secret 为加密信封，解密在
// callbacks.go 的 deliverer 侧完成）。
type CallbackJob struct {
	TaskID      string
	TenantID    string
	EventID     string
	EventSeq    int64
	Attempts    int
	MaxAttempts int
	URLEnc      string
	SecretEnc   string
}

// ClaimDueCallbacks 认领到期回调（SKIP LOCKED）。P0 单 deliverer goroutine；
// 若未来多 worker，投递中崩溃 → 行仍 pending → 重投，接收方以 event_id 幂等
// （矩阵 E）。
func (s *Store) ClaimDueCallbacks(ctx context.Context, now time.Time, limit int) ([]CallbackJob, error) {
	if limit <= 0 {
		limit = 20
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		SELECT c.task_id, t.tenant_id, c.event_id, c.event_seq,
		       c.attempts, c.max_attempts, c.url_enc, c.secret_enc
		FROM hosted_task_callbacks c
		JOIN hosted_tasks t ON t.id = c.task_id
		WHERE c.status = 'pending' AND c.next_attempt_at <= $1 AND c.url_enc != ''
		ORDER BY c.next_attempt_at ASC
		LIMIT $2
		FOR UPDATE OF c SKIP LOCKED
	`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("claim callbacks: %w", err)
	}
	defer rows.Close()
	var jobs []CallbackJob
	for rows.Next() {
		var j CallbackJob
		if err := rows.Scan(&j.TaskID, &j.TenantID, &j.EventID, &j.EventSeq,
			&j.Attempts, &j.MaxAttempts, &j.URLEnc, &j.SecretEnc); err != nil {
			return nil, err
		}
		if j.MaxAttempts <= 0 {
			j.MaxAttempts = 8
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// CallbackOutcome 是一次投递尝试的结论（deliverer → store 回写）。
type CallbackOutcome struct {
	Delivered  bool // 2xx
	Retryable  bool // 网络错误/5xx=true；4xx=false（不重试直 DLQ，矩阵 E）
	StatusCode int  // 0=网络错误
	Err        string
	NextAfter  time.Duration // Retryable 时生效（deliverer 计算的退避）
}

// RecordCallbackOutcome 回写一次投递结论：delivered / 退避重试 / DLQ，
// 并追加 callback_delivered / callback_dlq 事件（§4.3）。
func (s *Store) RecordCallbackOutcome(ctx context.Context, job CallbackJob, out CallbackOutcome) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	switch {
	case out.Delivered:
		if _, err := tx.Exec(ctx, `
			UPDATE hosted_task_callbacks
			SET status='delivered', delivered_at=NOW(), attempts=attempts+1,
			    last_status_code=$2, last_error='', next_attempt_at=NULL, updated_at=NOW()
			WHERE task_id=$1
		`, job.TaskID, out.StatusCode); err != nil {
			return fmt.Errorf("record delivered: %w", err)
		}
		if _, err := appendEvent(ctx, tx, job.TaskID, EventCallbackDone, map[string]any{
			"event_id": job.EventID, "status_code": out.StatusCode,
		}); err != nil {
			return err
		}
	case !out.Retryable || job.Attempts+1 >= job.MaxAttempts:
		if _, err := tx.Exec(ctx, `
			UPDATE hosted_task_callbacks
			SET status='dlq', dlq_at=NOW(), attempts=attempts+1,
			    last_status_code=$2, last_error=$3, next_attempt_at=NULL, updated_at=NOW()
			WHERE task_id=$1
		`, job.TaskID, out.StatusCode, truncErr(out.Err)); err != nil {
			return fmt.Errorf("record dlq: %w", err)
		}
		if _, err := appendEvent(ctx, tx, job.TaskID, EventCallbackDLQ, map[string]any{
			"event_id": job.EventID, "status_code": out.StatusCode,
			"reason": truncErr(out.Err), "attempts": job.Attempts + 1,
		}); err != nil {
			return err
		}
	default:
		if _, err := tx.Exec(ctx, `
			UPDATE hosted_task_callbacks
			SET attempts=attempts+1, last_status_code=$2, last_error=$3,
			    next_attempt_at=$4, updated_at=NOW()
			WHERE task_id=$1
		`, job.TaskID, out.StatusCode, truncErr(out.Err), time.Now().UTC().Add(out.NextAfter)); err != nil {
			return fmt.Errorf("record retry: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// ─── 工具 ─────────────────────────────────────────────────────────────────

func scanTasks(rows pgx.Rows) ([]Task, error) {
	out := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func truncErr(s string) string {
	if len(s) > 512 {
		return s[:512]
	}
	return s
}

// newTaskID 生成 hosted_task_id：ht_ + 16 字节 hex。
func newTaskID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "ht_" + hex.EncodeToString(raw[:]), nil
}
