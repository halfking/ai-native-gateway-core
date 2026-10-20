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
//   - sessions/session_turns/session_turn_details 以会话为单位清理
//     （updated_at 超龄的会话连同其全部 turns 与特征行一并删除）——
//     不按 turn.ts 逐行删，避免误删活跃会话的历史轮次。三步在同一事务
//     内执行：SQLite 写锁在事务期间串行化并发写，updated_at 谓词在删除
//     时刻求值——缝隙内被新 turn 复活的会话整体豁免，不会出现"会话在、
//     历史轮次全没"（R47 修复旧实现的 SELECT→删 turns→删 sessions
//     三段竞态）。
//
// R78：session_turn_details 曾遗漏。它是 session_turns 的特征层投影
// （schema.go:56-79），每轮追加一行，且与 session_turns 之间没有外键
// （PRIMARY KEY/UNIQUE 均不含 FK），所以删除会话后特征行永久残留——
// 该表建了 idx_turn_details_ts（设计时就预期按 ts 查询）却无任何
// DELETE 路径，使本 worker "lite 长跑 SQLite 无界增长"的修复目标部分
// 落空。R78 补进同一事务、共用同一 EXISTS 谓词。
//
// 与具体存储实现解耦：只持有 *sql.DB，不 import storage 包（对齐
// CacheTrimmer 的"只操作目录"边界）。
//
// 可观测性契约（2026-10-18）：**每一轮都必须出声**，包括零删除的那一轮。
// 见 RunOnce 末尾的说明——沉默不能同时表示「无事可做」与「没在跑」。

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
	if _, _, _, _, err := w.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
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
			if _, _, _, _, err := w.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("lite retention sweep failed", "error", err)
			}
		}
	}
}

// RunOnce 执行一轮清理，返回各类删除行数（供测试与日志断言）。
func (w *LiteRetentionWorker) RunOnce(ctx context.Context) (logsDeleted, sessionsDeleted, turnsDeleted, detailsDeleted int64, err error) {
	cutoff := time.Now().Add(-w.retention).Unix()

	// request_logs 按 ts 逐行清理，分批执行（R47：此前单条 DELETE 裸 ts
	// 谓词无索引可用（复合索引首列均为 tenant_id/session_id）全表扫描，
	// 且整条语句持 SQLite 写锁——与请求路径写入共池时会挤到 busy_timeout）。
	//
	// ★ 2026-10-20（迁移 890，v1 族退役）：表名写成**带 schema 限定**的
	// main.request_logs——本 worker 操作的是 **SQLite** 库（storage/sqlite 的
	// request_logs 表），与被 890 删掉的 PostgreSQL v1 族是**两张不同的表**。
	// 限定写法在 SQLite 里语义完全等价（默认库就是 main），但消除了「同名
	// 不同库」的歧义，也让「v1 族已退役」这句话在本文件里成立。
	for {
		if err := ctx.Err(); err != nil {
			return logsDeleted, 0, 0, 0, err
		}
		res, err := w.db.ExecContext(ctx,
			`DELETE FROM main.request_logs WHERE rowid IN (
				SELECT rowid FROM main.request_logs WHERE ts < ? LIMIT ?
			)`, cutoff, liteRetentionDeleteBatch)
		if err != nil {
			return logsDeleted, 0, 0, 0, err
		}
		n, _ := res.RowsAffected()
		logsDeleted += n
		if n < liteRetentionDeleteBatch {
			break
		}
	}

	// 会话为单位：details/turns/sessions 三步同一事务（R47 TOCTOU 修复，见
	// 文件头注释）。EXISTS 子查询按删除时刻的 updated_at 求值，复活即豁免。
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return logsDeleted, 0, 0, 0, err
	}
	defer tx.Rollback() // Commit 成功后为 no-op

	// 特征层先行：它与 session_turns 无外键，删除顺序无约束，但先删
	// 子层可以让"删到一半失败"时的残留偏向可重建的一侧。
	res, err := tx.ExecContext(ctx, `DELETE FROM session_turn_details WHERE EXISTS (
		SELECT 1 FROM sessions s
		WHERE s.tenant_id = session_turn_details.tenant_id
		  AND s.id = session_turn_details.session_id
		  AND s.updated_at < ?
	)`, cutoff)
	if err != nil {
		return logsDeleted, 0, 0, 0, err
	}
	detailsDeleted, _ = res.RowsAffected()

	// 孤儿特征行回收：上面那条 EXISTS 谓词要求 sessions 里还有对应行，
	// 而本缺陷在修复前已经让「会话行被删、特征行留下」的组合大量产生。
	// 这些行永远等不到一个可匹配的会话行——只补 EXISTS 谓词等于只挡新
	// 垃圾、存量永远不清，那不是修复。故再补一条按特征行自身 ts 的兜底：
	// 超龄且无对应会话行 ⇒ 确定是垃圾。
	//
	// 写序安全性：sink 的 ensureSession 先于 WriteTurnDetails
	// （cmd/gateway/lite_telemetry_sink.go:172 → :214），故「有特征行必有
	// 会话行」是写侧不变量，这条 NOT EXISTS 不会与在途写入竞态；再加上 ts
	// 谓词兜底，即便未来写序变化也只会漏删、不会误删近期数据。走
	// idx_turn_details_ts。
	res, err = tx.ExecContext(ctx, `DELETE FROM session_turn_details
		WHERE ts < ?
		  AND NOT EXISTS (
			SELECT 1 FROM sessions s
			WHERE s.tenant_id = session_turn_details.tenant_id
			  AND s.id = session_turn_details.session_id
		)`, cutoff)
	if err != nil {
		return logsDeleted, 0, 0, detailsDeleted, err
	}
	orphanDeleted, _ := res.RowsAffected()
	detailsDeleted += orphanDeleted

	res, err = tx.ExecContext(ctx, `DELETE FROM session_turns WHERE EXISTS (
		SELECT 1 FROM sessions s
		WHERE s.tenant_id = session_turns.tenant_id
		  AND s.id = session_turns.session_id
		  AND s.updated_at < ?
	)`, cutoff)
	if err != nil {
		return logsDeleted, 0, 0, detailsDeleted, err
	}
	turnsDeleted, _ = res.RowsAffected()

	res, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE updated_at < ?`, cutoff)
	if err != nil {
		return logsDeleted, 0, turnsDeleted, detailsDeleted, err
	}
	sessionsDeleted, _ = res.RowsAffected()

	if err := tx.Commit(); err != nil {
		return logsDeleted, 0, turnsDeleted, detailsDeleted, err
	}

	// 2026-10-18：这一轮「什么都没删到」同样出声。
	//
	// 旧实现只在总删除数 > 0 时打这行，于是「worker 活着但稳态无事可做」
	// 与「worker 根本没在跑」渲染成同一种沉默。周期 6h、默认 7d 保留期，
	// 稳态下**绝大多数轮次删除数必然为 0** ⇒ 沉默是常态而非异常信号，
	// 而这几行 slog 是本 worker 唯一的可观测面（无任何指标通道）。
	// 与 URSM SnapshotRetentionWorker 的同族修复（2026-10-14 批 116）同形：
	// 用 mode 字段区分「真删到了」与「这轮无事可做」，保留原有 msg 与
	// 计数键名，既有基于日志的断言不受影响。
	total := logsDeleted + sessionsDeleted + turnsDeleted + detailsDeleted
	mode := "noop"
	if total > 0 {
		mode = "reclaimed"
	}
	slog.Info("lite retention sweep",
		"mode", mode,
		"request_logs_deleted", logsDeleted,
		"sessions_deleted", sessionsDeleted,
		"session_turns_deleted", turnsDeleted,
		"session_turn_details_deleted", detailsDeleted,
		"retention", w.retention.String(),
		"interval", w.interval.String())
	return logsDeleted, sessionsDeleted, turnsDeleted, detailsDeleted, nil
}
