package bg

// LiteRetentionWorker：lite 模式 SQLite 行级保留期清理（R46 F6）。
//
// 背景：Lite.Retention.RequestLogsDays 此前是死配置——文件侧有
// CacheTrimmer/BodiesTrimmer，行侧（request_logs/sessions/session_turns）
// 没有任何消费者，lite 长跑进程的 SQLite 单文件无界增长，配置给运维
// "7 天会清"的错误预期。本 worker 对齐 Trimmer 模式补上行级 TTL。
//
// 删除语义：
//   - request_logs 按 ts 逐行清理（ts 为 Unix 秒）；
//   - sessions/session_turns 以会话为单位清理（updated_at 超龄的会话
//     连同其全部 turns 一并删除）——不按 turn.ts 逐行删，避免误删
//     活跃会话的历史轮次。
//
// 与具体存储实现解耦：只持有 *sql.DB，不 import storage 包（对齐
// CacheTrimmer 的"只操作目录"边界）。

import (
	"context"
	"database/sql"
	"log/slog"
	"time"
)

const defaultLiteRetentionInterval = 6 * time.Hour

type LiteRetentionWorker struct {
	db        *sql.DB
	retention time.Duration // request_logs 按行、sessions 按会话的保留时长
	interval  time.Duration // 清理周期，默认 6 小时（测试可经 WithInterval 覆盖）
}

func NewLiteRetentionWorker(db *sql.DB, retention time.Duration) *LiteRetentionWorker {
	return &LiteRetentionWorker{
		db:        db,
		retention: retention,
		interval:  defaultLiteRetentionInterval,
	}
}

// WithInterval 覆盖默认清理周期（主要供测试使用），返回自身以便链式调用。
func (w *LiteRetentionWorker) WithInterval(d time.Duration) *LiteRetentionWorker {
	if d > 0 {
		w.interval = d
	}
	return w
}

// Start 阻塞式运行清理循环（启动即先跑一轮；由调用方 go 启动，对齐
// CacheTrimmer/BodiesTrimmer 的 Start 契约）。
func (w *LiteRetentionWorker) Start(ctx context.Context) {
	if _, _, _, err := w.RunOnce(ctx); err != nil {
		slog.Warn("lite retention initial sweep failed", "error", err)
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, _, _, err := w.RunOnce(ctx); err != nil {
				slog.Warn("lite retention sweep failed", "error", err)
			}
		}
	}
}

// RunOnce 执行一轮清理，返回各类删除行数（供测试与日志断言）。
func (w *LiteRetentionWorker) RunOnce(ctx context.Context) (logsDeleted, sessionsDeleted, turnsDeleted int64, err error) {
	cutoff := time.Now().Add(-w.retention).Unix()

	res, err := w.db.ExecContext(ctx, `DELETE FROM request_logs WHERE ts < ?`, cutoff)
	if err != nil {
		return 0, 0, 0, err
	}
	logsDeleted, _ = res.RowsAffected()

	// 会话为单位：先删其 turns，再删 sessions 行。
	rows, err := w.db.QueryContext(ctx,
		`SELECT tenant_id, id FROM sessions WHERE updated_at < ?`, cutoff)
	if err != nil {
		return logsDeleted, 0, 0, err
	}
	type oldSession struct{ tenantID, sessionID string }
	old := make([]oldSession, 0, 64)
	for rows.Next() {
		var s oldSession
		if err := rows.Scan(&s.tenantID, &s.sessionID); err != nil {
			rows.Close()
			return logsDeleted, 0, 0, err
		}
		old = append(old, s)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return logsDeleted, 0, 0, err
	}
	rows.Close()

	for _, s := range old {
		res, err := w.db.ExecContext(ctx,
			`DELETE FROM session_turns WHERE tenant_id = ? AND session_id = ?`,
			s.tenantID, s.sessionID)
		if err != nil {
			return logsDeleted, 0, turnsDeleted, err
		}
		n, _ := res.RowsAffected()
		turnsDeleted += n
	}
	res, err = w.db.ExecContext(ctx, `DELETE FROM sessions WHERE updated_at < ?`, cutoff)
	if err != nil {
		return logsDeleted, 0, turnsDeleted, err
	}
	sessionsDeleted, _ = res.RowsAffected()

	if logsDeleted+sessionsDeleted+turnsDeleted > 0 {
		slog.Info("lite retention sweep",
			"request_logs_deleted", logsDeleted,
			"sessions_deleted", sessionsDeleted,
			"session_turns_deleted", turnsDeleted,
			"retention", w.retention.String())
	}
	return logsDeleted, sessionsDeleted, turnsDeleted, nil
}
