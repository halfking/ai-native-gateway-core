package routeincident

// 2026-10-18：route_incident_events 的行级留存 worker（**默认不启用**）。
//
// # 为什么需要它
//
// 生产实测（runbook §41/§43/§49）：这张表 170 万行 / 1,004 MB，
// 没有任何留存路径；无 `created_at` 首列索引 ⇒ 任何按时间的删除都会退化成
// 全索引扫描，在服务端 `statement_timeout = 30s` 下**第一条语句就跑不完**
// （实测冷缓存 25.7 s，贴着 30 s 线）。
//
// 本 worker 只做**行级 TTL**，且**严格要求前置索引存在**（见 hasTimeIndex）。
// 配套的索引（DML 用 CONCURRENTLY 建，避免阻塞网关写入）必须先落地。
//
// # 三条设计约束
//
//  1. **跨实例互斥**：与 URSM 快照留存同款 —— 事务级
//     `pg_try_advisory_xact_lock`（不是会话级，随事务自动释放）。
//     拿不到锁就是「对端在做」，本轮跳过而不是排队。
//  2. **分批删除**：本地 1,683,550 行基准克隆实测，20,000 行/批时
//     单批 121~206 ms（中位 ~133），且**单批耗时与总量解耦**。
//     一次性 DELETE 40 万行不可控：长时间持锁 + 大量死元组。
//  3. **每轮必出声**（含零回收）：6 小时一轮、保留期按天，
//     稳态下绝大多数轮次删除数必然为 0。
//     只在删到东西时打日志，会让「活着无事可做」与「根本没在跑」
//     渲染成同一种沉默 —— 这正是 bg/lite_retention_worker 在 §42 修掉的同类缺口。
//
// # 口径提醒（runbook §48）
//
// 本 worker 按 `created_at` 一刀切，**不区分探针来源**。
// 实测 65.2% 的存量行、39.0% 的增量行是 `probe_direct_*`（探针自身噪声）。
// 那是收益更大但风险也更高的另一件事（会改写入语义与在线可见性口径），
// 未对齐前不并进来。本 worker 与它**正交**：先封顶存量，不改变增量来源。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// advisoryLockKeyBase 是本 worker 的 advisory lock 键。
// 取一个与 URSM 快照留存（632_2026_10_14）**明确不同**的值，
// 避免两套留存互相抢锁（它们作用于完全不同的表，抢锁没有意义）。
const advisoryLockKeyBase int64 = 815_2026_10_18

// defaultRetentionInterval 是清理周期。留存语义本身就是小时级，
// 与 URSM 快照留存（每小时）刻意不同：事件证据的窗口以天计。
const defaultRetentionInterval = 6 * time.Hour

// defaultDeleteBatch 是单批删除行数上限。
// 取值依据：本地基准克隆实测 20,000 行/批 → 单批 121~206 ms。
const defaultDeleteBatch = 20_000

// errHeldByPeer 表示 advisory lock 被同网另一台实例持有 ⇒ 本轮跳过。
var errHeldByPeer = errors.New("routeincident: retention lock held by peer")

// EventRetentionResult 是一轮清理的计数，供测试与日志断言使用。
type EventRetentionResult struct {
	EventsDeleted  int64
	Batches        int
	YieldedToPeer  bool
	SkippedNoIndex bool
	Duration       time.Duration
}

// EventRetentionWorker 删除超过保留期的 route_incident_events 行。
//
// 默认**不启用**：调用方必须显式构造并 Start，
// 且 retention <= 0 时 CleanupOnce 直接跳过（不删任何东西）。
type EventRetentionWorker struct {
	pool      *pgxpool.Pool
	retention time.Duration
	interval  time.Duration
	batchSize int
}

// NewEventRetentionWorker 构造 worker。pool 必须非 nil；
// retention <= 0 表示未启用（CleanupOnce 会直接返回零结果）。
func NewEventRetentionWorker(pool *pgxpool.Pool, retention time.Duration) *EventRetentionWorker {
	return &EventRetentionWorker{
		pool:      pool,
		retention: retention,
		interval:  defaultRetentionInterval,
		batchSize: defaultDeleteBatch,
	}
}

// WithInterval 覆盖清理周期（主要供测试），返回自身以便链式调用。
func (w *EventRetentionWorker) WithInterval(d time.Duration) *EventRetentionWorker {
	if d > 0 {
		w.interval = d
	}
	return w
}

// WithBatchSize 覆盖单批行数（主要供测试），返回自身以便链式调用。
func (w *EventRetentionWorker) WithBatchSize(n int) *EventRetentionWorker {
	if n > 0 {
		w.batchSize = n
	}
	return w
}

// Start 阻塞式运行清理循环（由调用方 go 启动）。
//
// 契约：每轮都出声，包括「未启用」「缺索引」「对端在做」「零回收」四种。
func (w *EventRetentionWorker) Start(ctx context.Context) {
	slog.Info("route incident event retention worker 已启动",
		"retention", w.retention.String(),
		"interval", w.interval.String(),
		"batch", w.batchSize,
		"enabled", w.retention > 0)

	// 启动立即跑一轮，排空历史积压（与 bg/lite_retention_worker 同契约）。
	w.runOnceLogged(ctx)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("route incident event retention worker 已停止")
			return
		case <-ticker.C:
			w.runOnceLogged(ctx)
		}
	}
}

func (w *EventRetentionWorker) runOnceLogged(ctx context.Context) {
	res, err := w.CleanupOnce(ctx)
	switch {
	case errors.Is(err, context.Canceled):
		return
	case errors.Is(err, errHeldByPeer):
		// 对端在做：这是良性结果，不是故障，但**必须出声**，
		// 否则「本机该做没做」在日志上与「做完了」无法区分。
		slog.Info("route incident event retention 本轮让给对端",
			"retention", w.retention.String(), "interval", w.interval.String())
		return
	case err != nil:
		slog.Warn("route incident event retention sweep failed", "error", err)
		return
	}

	mode := "noop"
	switch {
	case !w.retentionEnabled():
		mode = "disabled"
	case res.SkippedNoIndex:
		mode = "skipped_no_index"
	case res.EventsDeleted > 0:
		mode = "reclaimed"
	}
	slog.Info("route incident event retention sweep",
		"mode", mode,
		"events_deleted", res.EventsDeleted,
		"batches", res.Batches,
		"retention", w.retention.String(),
		"interval", w.interval.String(),
		"duration_ms", res.Duration.Milliseconds())
}

func (w *EventRetentionWorker) retentionEnabled() bool { return w.retention > 0 }

// hasTimeIndex 检查 created_at 上是否存在可用的时间有序访问路径。
//
// ★ 这是本 worker 的**前置条件**，不是优化项：
// 没有以 created_at 打头的索引时，子查询 `WHERE created_at < $1 LIMIT $2`
// 只能全索引扫描（生产实测 1,699,257 行冷缓存 25.7 s vs statement_timeout 30 s）
// ⇒ 单批就可能超时，而超时的 DELETE 会持有行锁并留下死元组。
// 宁可这一轮明确跳过并出声，也不要制造一个「删不掉且更膨胀」的结果。
func (w *EventRetentionWorker) hasTimeIndex(ctx context.Context) (bool, error) {
	// 只认「首列就是 created_at」或「以 created_at 为表达式首列」的索引。
	// BRIN 同样可用（范围扫描），所以按 amname 一并不限定 btree。
	const q = `
		SELECT EXISTS (
			SELECT 1
			FROM pg_index i
			JOIN pg_class ic ON ic.oid = i.indexrelid
			JOIN pg_am am ON am.oid = ic.relam
			WHERE i.indrelid = 'public.route_incident_events'::regclass
			  AND i.indisvalid
			  AND i.indisready
			  AND (
			      (SELECT array_agg(a.attname ORDER BY k.ord)
			         FROM unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
			         JOIN pg_attribute a
			           ON a.attrelid = i.indrelid AND a.attnum = k.attnum)[1]
			      = 'created_at'
			  )
		)`
	var ok bool
	if err := w.pool.QueryRow(ctx, q).Scan(&ok); err != nil {
		return false, fmt.Errorf("probe route_incident_events time index: %w", err)
	}
	return ok, nil
}

// CleanupOnce 执行一轮行级留存，返回删除计数。
//
// 错误语义：
//   - context.Canceled：调用方关停，非故障；
//   - errHeldByPeer：advisory lock 被同网另一台实例持有 ⇒ 本轮跳过；
//   - 其他：错误。
func (w *EventRetentionWorker) CleanupOnce(ctx context.Context) (EventRetentionResult, error) {
	started := time.Now()
	res := EventRetentionResult{}
	if !w.retentionEnabled() {
		res.Duration = time.Since(started)
		return res, nil
	}
	if w.pool == nil {
		return res, errors.New("routeincident: retention worker has nil pool")
	}

	ok, err := w.hasTimeIndex(ctx)
	if err != nil {
		return res, err
	}
	if !ok {
		res.SkippedNoIndex = true
		res.Duration = time.Since(started)
		return res, nil
	}

	cutoff := time.Now().Add(-w.retention)

	for {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		n, err := w.deleteBatch(ctx, cutoff)
		if err != nil {
			return res, err
		}
		res.EventsDeleted += n
		if n > 0 {
			res.Batches++
		}
		if n < int64(w.batchSize) {
			break // 已删干净：最后一批不满 batchSize
		}
		// 每批之间让出，避免在关停前把预算一次性耗尽。
		if err := ctx.Err(); err != nil {
			return res, err
		}
	}

	res.Duration = time.Since(started)
	return res, nil
}

// deleteBatch 删除至多 batchSize 行超龄事件。
//
// 事务级 advisory lock 在**每批内**取一次：批次之间刻意不放锁，
// 让另一台实例有机会接手剩余批次，而不是两台从同一位置各删一半。
func (w *EventRetentionWorker) deleteBatch(ctx context.Context, cutoff time.Time) (int64, error) {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin retention batch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// try 语义必须立刻返回：临时把 lock_timeout 置 0，
	// 否则拿不到锁时本端会白等一整个 lock_timeout。
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '0'`); err != nil {
		return 0, fmt.Errorf("set lock_timeout=0 for advisory lock: %w", err)
	}
	var gotLock bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, advisoryLockKeyBase).
		Scan(&gotLock); err != nil {
		return 0, fmt.Errorf("acquire incident retention lock: %w", err)
	}
	if !gotLock {
		return 0, errHeldByPeer
	}

	// 恢复一个有限的 lock_timeout：DELETE 要拿行锁，
	// 无上限等待会让关停时的删除无限期占着锁。
	const deleteLockTimeout = 5_000 * time.Millisecond
	if _, err := tx.Exec(ctx, fmt.Sprintf(`SET LOCAL lock_timeout = '%dms'`, deleteLockTimeout.Milliseconds())); err != nil {
		return 0, fmt.Errorf("restore lock_timeout for delete: %w", err)
	}

	// 子查询只取 id 与 created_at，走 created_at 首列索引；
	// 外层按主键删行，避免 DELETE 自身再去回表定位。
	const del = `
		DELETE FROM route_incident_events
		WHERE id IN (
			SELECT id FROM route_incident_events
			WHERE created_at < $1
			LIMIT $2
		)`
	tag, err := tx.Exec(ctx, del, cutoff, w.batchSize)
	if err != nil {
		return 0, fmt.Errorf("delete expired incident events: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit retention batch: %w", err)
	}
	return tag.RowsAffected(), nil
}
