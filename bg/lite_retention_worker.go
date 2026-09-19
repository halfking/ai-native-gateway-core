package bg

// LiteRetentionWorker：lite 模式 SQLite 行级保留期清理（R46 F6）。
//
// 背景：Lite.Retention.RequestLogsDays 此前是死配置——文件侧有
// CacheTrimmer/BodiesTrimmer，行侧（request_logs/sessions/session_turns）
// 没有任何消费者，lite 长跑进程的 SQLite 单文件无界增长，配置给运维
// "7 天会清"的错误预期。本 worker 对齐 Trimmer 模式补上行级 TTL。
//
// 删除语义：
//   - request_logs 按 ts 逐行清理（ts 为 Unix 秒）；分批删除压短单条
//     DELETE 的写锁持有时长（裸 ts 谓词走 idx_logs_ts）。
//   - sessions/session_turns 以会话为单位清理（updated_at 超龄的会话
//     连同其全部 turns 一并删除）——不按 turn.ts 逐行删，避免误删
//     活跃会话的历史轮次。两步在同一事务内执行：SQLite 写锁在事务
//     期间串行化并发写，updated_at 谓词在删除时刻求值——缝隙内被新
//     turn 复活的会话整体豁免，不会出现"会话在、历史轮次全没"
//     （R47 修复旧实现的 SELECT→删 turns→删 sessions 三段竞态）。
//
// 与具体存储实现解耦：只持有 *sql.DB，不 import storage 包（对齐
// CacheTrimmer 的"只操作目录"边界）。

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"
)

const (
	defaultLiteRetentionInterval = 6 * time.Hour
	// liteRetentionDeleteBatch 是 request_logs 单批删除行数上限：把一次
	// 大清扫切成多个短写事务，避免与请求路径写入争 SQLite 写锁直到
	// busy_timeout（R47）。
	liteRetentionDeleteBatch = 500
)

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
	slog.Info("lite retention worker 已启动",
		"retention", w.retention.String(),
		"interval", w.interval.String())

	// 启动立即执行一次，排空历史积压的过期行（F6 前的存量）。
	if _, _, _, err := w.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("lite retention initial sweep failed", "error", err)
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("lite retention worker 已停止")
			return
		case <-ticker.C:
			if _, _, _, err := w.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("lite retention sweep failed", "error", err)
			}
		}
	}
}

// RunOnce 执行一轮清理，返回各类删除行数（供测试与日志断言）。
func (w *LiteRetentionWorker) RunOnce(ctx context.Context) (logsDeleted, sessionsDeleted, turnsDeleted int64, err error) {
	cutoff := time.Now().Add(-w.retention).Unix()

	// request_logs 按 ts 逐行清理，分批执行（R47：此前单条 DELETE 裸 ts
	// 谓词无索引可用（复合索引首列均为 tenant_id/session_id）全表扫描，
	// 且整条语句持 SQLite 写锁——与请求路径写入共池时会挤到 busy_timeout）。
	for {
		if err := ctx.Err(); err != nil {
			return logsDeleted, 0, 0, err
		}
		res, err := w.db.ExecContext(ctx,
			`DELETE FROM request_logs WHERE rowid IN (
				SELECT rowid FROM request_logs WHERE ts < ? LIMIT ?
			)`, cutoff, liteRetentionDeleteBatch)
		if err != nil {
			return logsDeleted, 0, 0, err
		}
		n, _ := res.RowsAffected()
		logsDeleted += n
		if n < liteRetentionDeleteBatch {
			break
		}
	}

	// 会话为单位：turns 与 sessions 两步同一事务（R47 TOCTOU 修复，见
	// 文件头注释）。EXISTS 子查询按删除时刻的 updated_at 求值，复活即豁免。
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return logsDeleted, 0, 0, err
	}
	defer tx.Rollback() // Commit 成功后为 no-op

	res, err := tx.ExecContext(ctx, `DELETE FROM session_turns WHERE EXISTS (
		SELECT 1 FROM sessions s
		WHERE s.tenant_id = session_turns.tenant_id
		  AND s.id = session_turns.session_id
		  AND s.updated_at < ?
	)`, cutoff)
	if err != nil {
		return logsDeleted, 0, 0, err
	}
	turnsDeleted, _ = res.RowsAffected()

	res, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE updated_at < ?`, cutoff)
	if err != nil {
		return logsDeleted, 0, turnsDeleted, err
	}
	sessionsDeleted, _ = res.RowsAffected()

	if err := tx.Commit(); err != nil {
		return logsDeleted, 0, turnsDeleted, err
	}

	if logsDeleted+sessionsDeleted+turnsDeleted > 0 {
		slog.Info("lite retention sweep",
			"request_logs_deleted", logsDeleted,
			"sessions_deleted", sessionsDeleted,
			"session_turns_deleted", turnsDeleted,
			"retention", w.retention.String())
	}
	return logsDeleted, sessionsDeleted, turnsDeleted, nil
}
