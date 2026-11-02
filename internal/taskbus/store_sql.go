// store_sql.go — taskbus 的 SQL Store（P1-1 S1 后半）。
//
// 复刻 MemoryStore 的语义（internal/taskbus/store_memory.go）到 PG：
//   - Enqueue 幂等：(task_type, idempotency_key) 唯一，重复入队返回已有行；
//   - ClaimBatch：`FOR UPDATE SKIP LOCKED` 拿 pending/failed 且
//     next_retry_at 到期的行，置 claimed_at（跨进程即锁）；
//   - MarkSent/MarkFailed/MarkDLQ：状态迁移 + completed_at（终态）；
//   - CountByStatus：运维计数；
//   - Sweep：删超保留窗口的终态行（幂等去重窗口，同 MemoryStore 语义）。
//
// 与 MemoryStore 的两个刻意差异（SQL 形态的天然优势）：
//   - claim 用行锁而非进程内 map ⇒ 多副本安全；
//   - claimed 超时回收：进程崩溃后 claimed_at 超时（默认 5 分钟）的行
//     重新可 claim（MemoryStore 进程内无此问题，进程死了状态也死了）。
package taskbus

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultSQLStoreClaimTimeout 是 claimed 行的僵尸回收阈值：claim 后这么
// 久没有 Mark* 的行视为持有者已死，重新可 claim（对齐 ASM inbox 的
// 5 分钟 lease 惯例）。
const DefaultSQLStoreClaimTimeout = 5 * time.Minute

// SQLStore 是 task_events 表的 Store 实现。
type SQLStore struct {
	pool         *pgxpool.Pool
	claimTimeout time.Duration
}

// NewSQLStore 构造。pool 不可为 nil。
func NewSQLStore(pool *pgxpool.Pool) *SQLStore {
	return &SQLStore{pool: pool, claimTimeout: DefaultSQLStoreClaimTimeout}
}

// Enqueue implements Store. (task_type, idempotency_key) 冲突时返回已有行。
func (s *SQLStore) Enqueue(ctx context.Context, taskType, idempotencyKey string, schemaVersion int, payload []byte) (Task, error) {
	if taskType == "" || idempotencyKey == "" {
		return Task{}, fmt.Errorf("taskbus.SQLStore.Enqueue: taskType and idempotencyKey required")
	}
	if schemaVersion <= 0 {
		return Task{}, fmt.Errorf("taskbus.SQLStore.Enqueue: schemaVersion must be > 0")
	}
	const q = `
		INSERT INTO public.task_events (task_type, payload, payload_version, idempotency_key, status)
		VALUES ($1, $2, $3, $4, 'pending')
		ON CONFLICT (task_type, idempotency_key) DO NOTHING
		RETURNING id, created_at`
	var id int64
	var createdAt time.Time
	err := s.pool.QueryRow(ctx, q, taskType, payload, schemaVersion, idempotencyKey).Scan(&id, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// 冲突路径：读回已有行（幂等去重窗口语义）。
		return s.getByKey(ctx, taskType, idempotencyKey)
	}
	if err != nil {
		return Task{}, fmt.Errorf("taskbus.SQLStore.Enqueue: %w", err)
	}
	return Task{
		ID: id, TaskType: taskType, Payload: payload, PayloadVersion: schemaVersion,
		IdempotencyKey: idempotencyKey, Status: StatusPending, CreatedAt: createdAt,
	}, nil
}

func (s *SQLStore) getByKey(ctx context.Context, taskType, key string) (Task, error) {
	const q = `
		SELECT id, task_type, payload, payload_version, idempotency_key,
		       status, attempts, COALESCE(next_retry_at, '1970-01-01'), COALESCE(last_error,''),
		       created_at, COALESCE(completed_at, '1970-01-01')
		FROM public.task_events
		WHERE task_type = $1 AND idempotency_key = $2`
	return s.scanOne(s.pool.QueryRow(ctx, q, taskType, key))
}

func (s *SQLStore) scanOne(row pgx.Row) (Task, error) {
	var t Task
	var nextRetry, completed time.Time
	err := row.Scan(&t.ID, &t.TaskType, &t.Payload, &t.PayloadVersion, &t.IdempotencyKey,
		&t.Status, &t.Attempts, &nextRetry, &t.LastError, &t.CreatedAt, &completed)
	if err != nil {
		return Task{}, fmt.Errorf("taskbus.SQLStore: %w", err)
	}
	if !nextRetry.IsZero() && nextRetry.Year() > 1970 {
		t.NextRetryAt = nextRetry
	}
	if !completed.IsZero() && completed.Year() > 1970 {
		t.CompletedAt = completed
	}
	return t, nil
}

// ClaimBatch implements Store. 行锁 claim + 僵尸回收。
func (s *SQLStore) ClaimBatch(ctx context.Context, limit int, now time.Time) ([]Task, error) {
	if limit <= 0 {
		limit = 32
	}
	// 单语句：可 claim 的行（未 claim 或 claim 超时）置 claimed_at 并返回。
	// SKIP LOCKED 让多副本互不争抢；僵尸回收=claimed_at < now-timeout。
	const q = `
		WITH claimable AS (
			SELECT id FROM public.task_events
			WHERE status IN ('pending','failed')
			  AND (next_retry_at IS NULL OR next_retry_at <= $1)
			  AND (claimed_at IS NULL OR claimed_at < $2)
			ORDER BY id
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		)
		UPDATE public.task_events t
		SET claimed_at = $1, updated_at = $1
		FROM claimable
		WHERE t.id = claimable.id
		RETURNING t.id, t.task_type, t.payload, t.payload_version, t.idempotency_key,
		          t.status, t.attempts, COALESCE(t.next_retry_at, '1970-01-01'),
		          COALESCE(t.last_error,''), t.created_at, COALESCE(t.completed_at, '1970-01-01')`
	rows, err := s.pool.Query(ctx, q, now, now.Add(-s.claimTimeout), limit)
	if err != nil {
		return nil, fmt.Errorf("taskbus.SQLStore.ClaimBatch: %w", err)
	}
	defer rows.Close()
	out := make([]Task, 0, limit)
	for rows.Next() {
		t, err := s.scanOne(rows)
		if err != nil {
			return out, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// MarkSent implements Store.
func (s *SQLStore) MarkSent(ctx context.Context, id int64) error {
	return s.markTerminal(ctx, id, StatusSent, "")
}

// MarkDLQ implements Store.
func (s *SQLStore) MarkDLQ(ctx context.Context, id int64, lastErr string) error {
	return s.markTerminal(ctx, id, StatusDLQ, lastErr)
}

func (s *SQLStore) markTerminal(ctx context.Context, id int64, status, lastErr string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE public.task_events
		SET status = $2, last_error = NULLIF($3,''), completed_at = now(),
		    claimed_at = NULL, updated_at = now()
		WHERE id = $1`, id, status, lastErr)
	if err != nil {
		return fmt.Errorf("taskbus.SQLStore.markTerminal: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("taskbus.SQLStore: id %d not found", id)
	}
	return nil
}

// MarkFailed implements Store.
func (s *SQLStore) MarkFailed(ctx context.Context, id int64, attempts int, nextRetry time.Time, lastErr string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE public.task_events
		SET status = 'failed', attempts = $2, next_retry_at = $3,
		    last_error = $4, claimed_at = NULL, updated_at = now()
		WHERE id = $1`, id, attempts, nextRetry, lastErr)
	if err != nil {
		return fmt.Errorf("taskbus.SQLStore.MarkFailed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("taskbus.SQLStore: id %d not found", id)
	}
	return nil
}

// CountByStatus implements Store.
func (s *SQLStore) CountByStatus(ctx context.Context, taskType, status string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM public.task_events
		WHERE ($1 = '' OR task_type = $1) AND status = $2`,
		taskType, status).Scan(&n)
	return n, err
}

// Sweep 删除超保留窗口的终态行（sent/dlq），返回删除数。
// pending/failed 永不回收（同 MemoryStore 语义）。
func (s *SQLStore) Sweep(ctx context.Context, retention time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM public.task_events
		WHERE status IN ('sent','dlq') AND completed_at < now() - $1::interval`,
		fmt.Sprintf("%d seconds", int(retention.Seconds())))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

var _ Store = (*SQLStore)(nil)
