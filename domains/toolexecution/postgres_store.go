package toolexecution

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// PostgresStore 基于 *sql.DB 的 Store 实现。
//
// 使用 lib/pq 驱动；表的 schema 详见 migrations/134_tool_execution.sql。
//
// 线程安全：所有方法通过 *sql.DB 的内部连接池实现并发安全。
type PostgresStore struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewPostgresStore 构造一个 PostgreSQL 实现的 Store。
// logger 为 nil 时使用 slog.Default()。
func NewPostgresStore(db *sql.DB, logger *slog.Logger) *PostgresStore {
	if logger == nil {
		logger = slog.Default()
	}
	return &PostgresStore{db: db, logger: logger}
}

// Save 插入一条新记录。ExecutionID 必须唯一。
func (s *PostgresStore) Save(ctx context.Context, exec *ToolExecution) error {
	if exec == nil {
		return fmt.Errorf("toolexecution: nil exec")
	}
	if exec.ExecutionID == "" {
		return fmt.Errorf("toolexecution: empty ExecutionID")
	}
	if exec.Status == "" {
		exec.Status = StatusPending
	}
	if exec.CreatedAt.IsZero() {
		exec.CreatedAt = time.Now().UTC()
	}
	if exec.StartedAt.IsZero() {
		exec.StartedAt = exec.CreatedAt
	}
	exec.ComputeDuration()

	// 存储优化方案 v2 S1a（migration 706）+ R30 审计修正（2026-09-16）：
	// 写链落 8h hot 表 session_tools_hot（134 的 tool_executions 形状 +
	// turn_no + partition_date），由 promote_session_tools_hot_to_partition
	// 批量搬运进月分区母表。"更新/删除只能发生在 hot 表"是本存储架构的
	// 不变量：此前直写母表使 hot 表与 promote 成为空转死设备，且行一旦
	// promote 进 columnar 月分区，UPDATE 会直接报错。partition_date 取
	// UTC 调用日，与会话族 calendarDate 语义一致。
	partitionDate := exec.StartedAt.UTC()
	partitionDate = time.Date(partitionDate.Year(), partitionDate.Month(), partitionDate.Day(), 0, 0, 0, 0, time.UTC)

	const q = `
		INSERT INTO public.session_tools_hot (
			execution_id, session_id, request_id, tenant_id,
			tool_name, tool_call_id, arguments, result,
			status, error_message, error_type,
			started_at, completed_at, duration_ms,
			identity_hash, model, created_at, partition_date
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8,
			$9, $10, $11,
			$12, $13, $14,
			$15, $16, $17, $18
		)
	`
	_, err := s.db.ExecContext(ctx, q,
		exec.ExecutionID, exec.SessionID, exec.RequestID, exec.TenantID,
		exec.ToolName, nullString(exec.ToolCallID), nullJSON(exec.Arguments), nullJSON(exec.Result),
		string(exec.Status), nullString(exec.ErrorMessage), nullString(exec.ErrorType),
		exec.StartedAt, nullTime(exec.CompletedAt), exec.DurationMs,
		nullString(exec.IdentityHash), nullString(exec.Model), exec.CreatedAt,
		partitionDate,
	)
	if err != nil {
		return fmt.Errorf("toolexecution: insert: %w", err)
	}
	return nil
}

// Get 按 execution_id 获取单条记录。行在 8h 内位于 hot 表，promote 后位于
// 月分区母表；先查 hot（最新写入），未命中再查母表。
func (s *PostgresStore) Get(ctx context.Context, executionID string) (*ToolExecution, error) {
	const qHot = `
		SELECT execution_id, session_id, request_id, tenant_id,
		       tool_name, COALESCE(tool_call_id, ''), arguments, result,
		       status, COALESCE(error_message, ''), COALESCE(error_type, ''),
		       started_at, completed_at, COALESCE(duration_ms, 0),
		       COALESCE(identity_hash, ''), COALESCE(model, ''), created_at
		FROM public.session_tools_hot
		WHERE execution_id = $1
	`
	var exec ToolExecution
	var argsJSON, resultJSON []byte
	var status string
	var completedAt sql.NullTime

	err := s.db.QueryRowContext(ctx, qHot, executionID).Scan(
		&exec.ExecutionID, &exec.SessionID, &exec.RequestID, &exec.TenantID,
		&exec.ToolName, &exec.ToolCallID, &argsJSON, &resultJSON,
		&status, &exec.ErrorMessage, &exec.ErrorType,
		&exec.StartedAt, &completedAt, &exec.DurationMs,
		&exec.IdentityHash, &exec.Model, &exec.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return s.getFromParent(ctx, executionID)
	}
	if err != nil {
		return nil, fmt.Errorf("toolexecution: get: %w", err)
	}
	return scanExecution(&exec, argsJSON, resultJSON, status, completedAt), nil
}

const selectExecutionCols = `
		SELECT execution_id, session_id, request_id, tenant_id,
		       tool_name, COALESCE(tool_call_id, ''), arguments, result,
		       status, COALESCE(error_message, ''), COALESCE(error_type, ''),
		       started_at, completed_at, COALESCE(duration_ms, 0),
		       COALESCE(identity_hash, ''), COALESCE(model, ''), created_at
	`

func (s *PostgresStore) getFromParent(ctx context.Context, executionID string) (*ToolExecution, error) {
	var exec ToolExecution
	var argsJSON, resultJSON []byte
	var status string
	var completedAt sql.NullTime

	err := s.db.QueryRowContext(ctx,
		selectExecutionCols+` FROM public.session_tools WHERE execution_id = $1`, executionID).Scan(
		&exec.ExecutionID, &exec.SessionID, &exec.RequestID, &exec.TenantID,
		&exec.ToolName, &exec.ToolCallID, &argsJSON, &resultJSON,
		&status, &exec.ErrorMessage, &exec.ErrorType,
		&exec.StartedAt, &completedAt, &exec.DurationMs,
		&exec.IdentityHash, &exec.Model, &exec.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("toolexecution: get: %w", err)
	}
	return scanExecution(&exec, argsJSON, resultJSON, status, completedAt), nil
}

func scanExecution(exec *ToolExecution, argsJSON, resultJSON []byte, status string, completedAt sql.NullTime) *ToolExecution {
	exec.Status = ExecutionStatus(status)
	if completedAt.Valid {
		exec.CompletedAt = completedAt.Time
	}
	exec.Arguments = argsJSON
	exec.Result = resultJSON
	return exec
}

// Update 读-改-写一次执行记录。
//
// 该实现直接执行一次 UPDATE，依赖传入的 updater 自行决定如何计算
// 终态字段（Status/Result/ErrorMessage/...）。这样做的好处：
//   - 减少一次 SELECT 的网络往返
//   - 避免应用层 SELECT 与 UPDATE 之间的 TOCTOU 竞争
//
// 若需要在执行中同时使用"读取后再修改"语义（如补偿任务），调用方
// 可显式先 Get 再 Save。
func (s *PostgresStore) Update(ctx context.Context, executionID string, updater func(*ToolExecution) error) error {
	if updater == nil {
		return fmt.Errorf("toolexecution: nil updater")
	}
	exec, err := s.Get(ctx, executionID)
	if err != nil {
		return err
	}
	if err := updater(exec); err != nil {
		return err
	}
	exec.ComputeDuration()

	// R30 审计修正（2026-09-16）：UPDATE 只打 hot 表——promote 进 columnar
	// 月分区的行不可更新（架构不变量）。行已被 promote 时（>8h 的迟到终态
	// 回写）按未命中处理，与旧行为的 ErrNotFound 语义一致。
	const q = `
		UPDATE public.session_tools_hot
		SET status        = $1,
		    result        = $2,
		    error_message = $3,
		    error_type    = $4,
		    completed_at  = $5,
		    duration_ms   = $6
		WHERE execution_id = $7
	`
	res, err := s.db.ExecContext(ctx, q,
		string(exec.Status), nullJSON(exec.Result),
		nullString(exec.ErrorMessage), nullString(exec.ErrorType),
		nullTime(exec.CompletedAt), exec.DurationMs,
		executionID,
	)
	if err != nil {
		return fmt.Errorf("toolexecution: update: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("toolexecution: rows affected: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// ListBySession 返回会话的所有执行记录。
// hot ∪ 母表联合读：正常时一行只存在于其中一侧（promote 是搬移不是复制），
// NOT EXISTS 兜底 promote ON CONFLICT 残留的双行边界（优先 hot 侧最新值）。
func (s *PostgresStore) ListBySession(ctx context.Context, sessionID string) ([]*ToolExecution, error) {
	const q = selectExecutionCols + ` FROM public.session_tools_hot WHERE session_id = $1
		UNION ALL
		` + selectExecutionCols + ` FROM public.session_tools p
		WHERE p.session_id = $1
		  AND NOT EXISTS (SELECT 1 FROM public.session_tools_hot h WHERE h.execution_id = p.execution_id)
		ORDER BY started_at DESC
	`
	return s.queryExecutions(ctx, q, sessionID)
}

// ListByIdentity 返回某客户端最近 limit 条执行。
func (s *PostgresStore) ListByIdentity(ctx context.Context, identityHash string, limit int) ([]*ToolExecution, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	const q = selectExecutionCols + ` FROM public.session_tools_hot WHERE identity_hash = $1
		UNION ALL
		` + selectExecutionCols + ` FROM public.session_tools p
		WHERE p.identity_hash = $1
		  AND NOT EXISTS (SELECT 1 FROM public.session_tools_hot h WHERE h.execution_id = p.execution_id)
		ORDER BY started_at DESC
		LIMIT $2
	`
	return s.queryExecutions(ctx, q, identityHash, limit)
}

// ListByToolName 返回某工具在时间窗口内的所有执行。
func (s *PostgresStore) ListByToolName(ctx context.Context, toolName string, startTime, endTime time.Time) ([]*ToolExecution, error) {
	const q = selectExecutionCols + ` FROM public.session_tools_hot
		WHERE tool_name = $1 AND started_at >= $2 AND started_at < $3
		UNION ALL
		` + selectExecutionCols + ` FROM public.session_tools p
		WHERE p.tool_name = $1 AND p.started_at >= $2 AND p.started_at < $3
		  AND NOT EXISTS (SELECT 1 FROM public.session_tools_hot h WHERE h.execution_id = p.execution_id)
		ORDER BY started_at ASC
	`
	return s.queryExecutions(ctx, q, toolName, startTime, endTime)
}

// ListByTenant 返回某租户在时间窗口内的执行记录（按时间倒序）。
func (s *PostgresStore) ListByTenant(ctx context.Context, tenantID string, startTime, endTime time.Time, limit int) ([]*ToolExecution, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	const q = selectExecutionCols + ` FROM public.session_tools_hot
		WHERE tenant_id = $1 AND started_at >= $2 AND started_at < $3
		UNION ALL
		` + selectExecutionCols + ` FROM public.session_tools p
		WHERE p.tenant_id = $1 AND p.started_at >= $2 AND p.started_at < $3
		  AND NOT EXISTS (SELECT 1 FROM public.session_tools_hot h WHERE h.execution_id = p.execution_id)
		ORDER BY started_at DESC
		LIMIT $4
	`
	return s.queryExecutions(ctx, q, tenantID, startTime, endTime, limit)
}

// SaveStats upsert 一条聚合统计。
func (s *PostgresStore) SaveStats(ctx context.Context, stats *ToolUsageStats) error {
	if stats == nil {
		return fmt.Errorf("toolexecution: nil stats")
	}
	if stats.ToolName == "" {
		return fmt.Errorf("toolexecution: empty ToolName")
	}
	date := stats.Date.UTC().Truncate(24 * time.Hour)
	if stats.TopUsers == nil {
		stats.TopUsers = []UserUsage{}
	}
	topUsersJSON, err := json.Marshal(stats.TopUsers)
	if err != nil {
		return fmt.Errorf("toolexecution: marshal top_users: %w", err)
	}
	now := time.Now().UTC()

	// INSERT directly targets tool_usage_stats_hot (the canonical
	// write target per the 2026-07 data-lifecycle architecture). All
	// INSERT/UPDATE/DELETE on tool_usage_stats goes through *_default —
	// the parent's auto-routing is intentionally bypassed so writes
	// never land in a non-default partition that cannot be
	// UPDATEd/DELETEd later.
	const q = `
		INSERT INTO tool_usage_stats_hot (
			tool_name, date,
			total_calls, success_calls, failed_calls, timeout_calls,
			avg_duration_ms, p50_duration_ms, p95_duration_ms, p99_duration_ms,
			unique_users, unique_sessions, top_users,
			created_at, updated_at
		) VALUES (
			$1, $2,
			$3, $4, $5, $6,
			$7, $8, $9, $10,
			$11, $12, $13,
			$14, $15
		)
		ON CONFLICT (tool_name, date) DO UPDATE SET
			total_calls     = EXCLUDED.total_calls,
			success_calls   = EXCLUDED.success_calls,
			failed_calls    = EXCLUDED.failed_calls,
			timeout_calls   = EXCLUDED.timeout_calls,
			avg_duration_ms = EXCLUDED.avg_duration_ms,
			p50_duration_ms = EXCLUDED.p50_duration_ms,
			p95_duration_ms = EXCLUDED.p95_duration_ms,
			p99_duration_ms = EXCLUDED.p99_duration_ms,
			unique_users    = EXCLUDED.unique_users,
			unique_sessions = EXCLUDED.unique_sessions,
			top_users       = EXCLUDED.top_users,
			updated_at      = EXCLUDED.updated_at
		RETURNING id, created_at, updated_at
	`
	row := s.db.QueryRowContext(ctx, q,
		stats.ToolName, date,
		stats.TotalCalls, stats.SuccessCalls, stats.FailedCalls, stats.TimeoutCalls,
		stats.AvgDurationMs, stats.P50DurationMs, stats.P95DurationMs, stats.P99DurationMs,
		stats.UniqueUsers, stats.UniqueSessions, topUsersJSON,
		now, now,
	)
	if err := row.Scan(&stats.ID, &stats.CreatedAt, &stats.UpdatedAt); err != nil {
		return fmt.Errorf("toolexecution: upsert stats: %w", err)
	}
	stats.Date = date
	return nil
}

// GetStats 获取某工具某天的统计。当日聚合先落 hot 表（promote 前），
// 先查 hot、未命中再查母表。
func (s *PostgresStore) GetStats(ctx context.Context, toolName string, date time.Time) (*ToolUsageStats, error) {
	day := date.UTC().Truncate(24 * time.Hour)
	const qHot = `
		SELECT id, tool_name, date,
		       total_calls, success_calls, failed_calls, timeout_calls,
		       COALESCE(avg_duration_ms, 0), COALESCE(p50_duration_ms, 0),
		       COALESCE(p95_duration_ms, 0), COALESCE(p99_duration_ms, 0),
		       COALESCE(unique_users, 0), COALESCE(unique_sessions, 0),
		       COALESCE(top_users, '[]'::jsonb), created_at, updated_at
		FROM tool_usage_stats_hot
		WHERE tool_name = $1 AND date = $2
	`
	var stats ToolUsageStats
	var topUsersJSON []byte
	err := s.db.QueryRowContext(ctx, qHot, toolName, day).Scan(
		&stats.ID, &stats.ToolName, &stats.Date,
		&stats.TotalCalls, &stats.SuccessCalls, &stats.FailedCalls, &stats.TimeoutCalls,
		&stats.AvgDurationMs, &stats.P50DurationMs, &stats.P95DurationMs, &stats.P99DurationMs,
		&stats.UniqueUsers, &stats.UniqueSessions, &topUsersJSON, &stats.CreatedAt, &stats.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return s.getStatsFromParent(ctx, toolName, day)
	}
	if err != nil {
		return nil, fmt.Errorf("toolexecution: get stats: %w", err)
	}
	return decodeStats(&stats, topUsersJSON)
}

func (s *PostgresStore) getStatsFromParent(ctx context.Context, toolName string, day time.Time) (*ToolUsageStats, error) {
	const q = `
		SELECT id, tool_name, date,
		       total_calls, success_calls, failed_calls, timeout_calls,
		       COALESCE(avg_duration_ms, 0), COALESCE(p50_duration_ms, 0),
		       COALESCE(p95_duration_ms, 0), COALESCE(p99_duration_ms, 0),
		       COALESCE(unique_users, 0), COALESCE(unique_sessions, 0),
		       COALESCE(top_users, '[]'::jsonb), created_at, updated_at
		FROM tool_usage_stats
		WHERE tool_name = $1 AND date = $2
	`
	var stats ToolUsageStats
	var topUsersJSON []byte
	err := s.db.QueryRowContext(ctx, q, toolName, day).Scan(
		&stats.ID, &stats.ToolName, &stats.Date,
		&stats.TotalCalls, &stats.SuccessCalls, &stats.FailedCalls, &stats.TimeoutCalls,
		&stats.AvgDurationMs, &stats.P50DurationMs, &stats.P95DurationMs, &stats.P99DurationMs,
		&stats.UniqueUsers, &stats.UniqueSessions, &topUsersJSON, &stats.CreatedAt, &stats.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("toolexecution: get stats: %w", err)
	}
	return decodeStats(&stats, topUsersJSON)
}

func decodeStats(stats *ToolUsageStats, topUsersJSON []byte) (*ToolUsageStats, error) {
	if len(topUsersJSON) > 0 {
		if err := json.Unmarshal(topUsersJSON, &stats.TopUsers); err != nil {
			return nil, fmt.Errorf("toolexecution: unmarshal top_users: %w", err)
		}
	}
	return stats, nil
}

// ListStats 返回某工具在时间窗口内的统计列表（按日期倒序）。
// toolName 为空时返回所有工具的统计。
func (s *PostgresStore) ListStats(ctx context.Context, toolName string, startTime, endTime time.Time, limit int) ([]*ToolUsageStats, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	args := []interface{}{}
	// hot ∪ 母表联合读；NOT EXISTS 以 (tool_name, date) 去重 promote 残留
	// 双行（promote ON CONFLICT DO NOTHING 时 hot 侧不删），优先 hot 最新值。
	q := `
		SELECT id, tool_name, date,
		       total_calls, success_calls, failed_calls, timeout_calls,
		       COALESCE(avg_duration_ms, 0), COALESCE(p50_duration_ms, 0),
		       COALESCE(p95_duration_ms, 0), COALESCE(p99_duration_ms, 0),
		       COALESCE(unique_users, 0), COALESCE(unique_sessions, 0),
		       COALESCE(top_users, '[]'::jsonb), created_at, updated_at
		FROM tool_usage_stats_hot
		WHERE date >= $1 AND date < $2
		UNION ALL
		SELECT id, tool_name, date,
		       total_calls, success_calls, failed_calls, timeout_calls,
		       COALESCE(avg_duration_ms, 0), COALESCE(p50_duration_ms, 0),
		       COALESCE(p95_duration_ms, 0), COALESCE(p99_duration_ms, 0),
		       COALESCE(unique_users, 0), COALESCE(unique_sessions, 0),
		       COALESCE(top_users, '[]'::jsonb), created_at, updated_at
		FROM tool_usage_stats p
		WHERE p.date >= $1 AND p.date < $2
		  AND NOT EXISTS (
		      SELECT 1 FROM tool_usage_stats_hot h
		      WHERE h.tool_name = p.tool_name AND h.date = p.date
		  )
	`
	args = append(args, startTime, endTime)
	if toolName != "" {
		q = `SELECT * FROM (` + q + `) u WHERE u.tool_name = $3`
		args = append(args, toolName)
		q += fmt.Sprintf(" ORDER BY date DESC LIMIT $%d", len(args)+1)
	} else {
		q = `SELECT * FROM (` + q + `) u`
		q += " ORDER BY date DESC, tool_name ASC"
		q += fmt.Sprintf(" LIMIT $%d", len(args)+1)
	}
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("toolexecution: list stats: %w", err)
	}
	defer rows.Close()

	var result []*ToolUsageStats
	for rows.Next() {
		var s ToolUsageStats
		var topUsersJSON []byte
		if err := rows.Scan(
			&s.ID, &s.ToolName, &s.Date,
			&s.TotalCalls, &s.SuccessCalls, &s.FailedCalls, &s.TimeoutCalls,
			&s.AvgDurationMs, &s.P50DurationMs, &s.P95DurationMs, &s.P99DurationMs,
			&s.UniqueUsers, &s.UniqueSessions, &topUsersJSON, &s.CreatedAt, &s.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("toolexecution: scan stats: %w", err)
		}
		if len(topUsersJSON) > 0 {
			if err := json.Unmarshal(topUsersJSON, &s.TopUsers); err != nil {
				return nil, fmt.Errorf("toolexecution: unmarshal top_users: %w", err)
			}
		}
		result = append(result, &s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("toolexecution: rows stats: %w", err)
	}
	return result, nil
}

// ListToolNamesWithActivity 返回 [startTime, endTime) 区间内有过调用的所有工具名。
func (s *PostgresStore) ListToolNamesWithActivity(ctx context.Context, startTime, endTime time.Time) ([]string, error) {
	const q = `
		SELECT DISTINCT tool_name FROM (
			SELECT tool_name FROM public.session_tools_hot
			WHERE started_at >= $1 AND started_at < $2
			UNION ALL
			SELECT p.tool_name FROM public.session_tools p
			WHERE p.started_at >= $1 AND p.started_at < $2
			  AND NOT EXISTS (SELECT 1 FROM public.session_tools_hot h WHERE h.execution_id = p.execution_id)
		) u
		ORDER BY tool_name
	`
	rows, err := s.db.QueryContext(ctx, q, startTime, endTime)
	if err != nil {
		return nil, fmt.Errorf("toolexecution: list tool names: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("toolexecution: scan tool name: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("toolexecution: rows tool names: %w", err)
	}
	return names, nil
}

// queryExecutions 公共查询逻辑。
func (s *PostgresStore) queryExecutions(ctx context.Context, q string, args ...interface{}) ([]*ToolExecution, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("toolexecution: query: %w", err)
	}
	defer rows.Close()

	var result []*ToolExecution
	for rows.Next() {
		var exec ToolExecution
		var argsJSON, resultJSON []byte
		var status string
		var completedAt sql.NullTime

		if err := rows.Scan(
			&exec.ExecutionID, &exec.SessionID, &exec.RequestID, &exec.TenantID,
			&exec.ToolName, &exec.ToolCallID, &argsJSON, &resultJSON,
			&status, &exec.ErrorMessage, &exec.ErrorType,
			&exec.StartedAt, &completedAt, &exec.DurationMs,
			&exec.IdentityHash, &exec.Model, &exec.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("toolexecution: scan: %w", err)
		}
		exec.Status = ExecutionStatus(status)
		if completedAt.Valid {
			exec.CompletedAt = completedAt.Time
		}
		exec.Arguments = argsJSON
		exec.Result = resultJSON
		result = append(result, &exec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("toolexecution: rows: %w", err)
	}
	return result, nil
}

// ──────────────────────────────────────────────────────────────────
// Nullable 辅助
// ──────────────────────────────────────────────────────────────────

func nullString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func nullJSON(b []byte) interface{} {
	if len(b) == 0 {
		return nil
	}
	return b
}

func nullTime(t time.Time) interface{} {
	if t.IsZero() {
		return nil
	}
	return t
}
