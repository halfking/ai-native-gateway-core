// Package bg — auto index refresher.
//
// AutoIndexRefresher is a background worker that periodically rebuilds
// the autoroute candidate pool from PostgreSQL. It serves two purposes:
//
//  1. Populate credential_model_index with live per-credential metrics
//     (success rate, p95 latency, concurrency, pressure) so the
//     auto-route decider has a hot-path lookup table.
//
//  2. Compute per-task-type aggregates into model_task_index for
//     observability (which model wins which task type).
//
// Refresh cadence:
//   - credential_model_index : every 5 minutes (matches the bg
//     concurrency peak collector's bucket size)
//   - model_task_index       : every 5 minutes (same cadence, different
//     aggregate level)
//
// Failure handling:
//   - Transient DB errors: log + retry next interval. The index keeps
//     the previous snapshot (stale-while-error).
//   - Fatal errors (pool closed, schema missing): stop the worker and
//     require operator intervention (slog.Error with shutdown context).
//
// Co-authored-by: Cursor <cursoragent@cursor.com>
package bg

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/autoroute"
)

// Default refresh cadence. Override via env vars at startup.
const (
	defaultRefreshInterval = 5 * time.Minute
	defaultRefreshTimeout  = 30 * time.Second
)

// AutoIndexRefresher runs the periodic credential_model_index and
// model_task_index rollups.
type AutoIndexRefresher struct {
	db  *pgxpool.Pool
	idx *autoroute.Index

	// RefreshInterval controls how often the rollup runs.
	// Default: 5 minutes.
	RefreshInterval time.Duration

	// RefreshTimeout caps each rollup's wall-clock time. Prevents
	// runaway DB queries from blocking subsequent refreshes.
	// Default: 30 seconds.
	RefreshTimeout time.Duration

	// OnRollupComplete is an optional callback invoked after each
	// successful rollup. Used by tests and admin metrics endpoints.
	OnRollupComplete func(bucket time.Time, credentialRows, taskRows int)

	// 2026-10-04 新增：singleflight 互斥。
	//
	// 为什么需要（本文件 2026-09-10 那条注释已经点出了病根，只是当时选择绕过）：
	// RefreshOnce 有**三个并发触发源** —— 5 分钟 ticker、auto_route_refresh 的
	// LISTEN 监听、以及 admin 手动接口（admin/auto_route.go:799）。实测生产上
	// 触发频率是**每 8.8 秒一次**，而设计间隔是 5 分钟 ⇒ **约 34 倍**。
	// 而 DELETE+INSERT 这一对**不是原子的**（注释已记录：两个交错的 run 会变成
	// DELETE(A) → DELETE(B) → INSERT(A) → INSERT(B)，第二次 INSERT 撞
	// duplicate key，2026-09-10 04:18 真实发生过）。
	//
	// 当时的修法是给 INSERT 加 ON CONFLICT —— 那是**让交错变得无害**，
	// 没有减少交错的次数。现在实测的 409 万次删除/11.35 天就是这 34 倍的后果：
	// 同一个 5 分钟 bucket 在 5 分钟内被反复 DELETE+INSERT 约 34 遍。
	//
	// 这里加的是**源头**治理：同一时刻只允许一个 rollup 在跑。
	//
	// ★ 为什么是「跳过」而不是「排队」：调用方是**事件触发**（路由配置变了、
	//  有人点了刷新），不是作业队列。排队意味着把积压的 N-1 次过期触发
	//  挨个执行一遍 —— 而它们算的是**同一个 bucket**，结果只会覆盖成一样的东西。
	//  跳过时数据本来就是新的（正在跑的那次会以更晚的时刻重算同一个 bucket）。
	//
	// ★ 为什么这不构成契约变更：返回值语义不变（跳过返回 nil = 没有错误），
	//   数据新鲜度不降低（正在跑的那次会以更晚的时刻重算同一个 bucket），
	//   消除的是**并发交错**这个已发生过真实故障的缺陷。
	//
	// 2026-10-05 P2（R44 移交 §R44/移交.2）对上一段声明的两条订正：
	//   1. 「返回值语义不变」正是缺陷 1 的病根 —— 跳过返回 nil error 后，
	//      LISTEN 监听器把「没执行」当成「已处理」清掉 pending，该 NOTIFY
	//      永不再重试；而跑着的那次 rollup 的 SELECT 早于该 NOTIFY 对应的
	//      提交 ⇒ 内存索引丢一次路由配置直到下个 ticker。跳过必须以
	//      skipped 语义上浮（RefreshOnceStatus），让调用方区分执行/跳过。
	//   2. 「数据新鲜度不降低」不成立 —— 见上条。且 4315a598c 只加了互斥
	//      没加节流：若 rollup 耗时 < 触发间隔，34 倍触发会**串行全部执行**，
	//      DELETE 次数不降。本结构体现在同时做同 bucket 触发合并（缺陷 2）。
	refreshMu sync.Mutex
	inFlight  bool

	// lastBucket：最近一次**成功**执行的 bucket（同 bucket 合并节流的记忆，
	// 2026-10-05 P2 缺陷 2）。失败**不**记账 —— 失败后的下一次触发必须重试
	// （与既有失败重试语义一致），不能被同 bucket 合并挡住。
	lastBucket time.Time

	// busyDeferred：有触发因 singleflight 忙被推迟、尚未被任何一次执行覆盖。
	// 在跑那次的 SELECT 早于该触发的数据提交，属其盲区；若把它与同 bucket
	// 重复触发一样合并掉，缺陷 1 的「丢一次路由配置直到下个 bucket」会借
	// 合并节流借尸还魂。下一次 tryBeginRefresh 消费它并在本 bucket 内补执行。
	busyDeferred bool

	// CoalesceWindow 是同 bucket 触发合并的窗口。默认与 RefreshInterval 一致
	// （5 分钟 bucket）—— 同一 bucket 内的重复触发算的是同一份 bucket 键，
	// 串行执行只是重复 DELETE+INSERT（实测 34 倍）。独立成字段是为了可调与
	// 可测：离线测试把窗口调小即可验证合并与兜底，不必等真 5 分钟。
	// 最终一致性：bucket 滚动后的下一次触发必然放行（5 分钟 ticker 的 run
	// 循环就是这个必然到来的触发），空闲后必有执行兜底。
	CoalesceWindow time.Duration

	// nowFn 时钟缝：默认 time.Now。离线测试用它确定性地推进 bucket
	// （不引入真实等待）。
	nowFn func() time.Time

	// executeFn 执行体缝：默认 nil 走真 rollup（需要真库）。离线测试注入
	// 假执行体，端到端计数「N 次触发合并为几次执行」。
	executeFn func(ctx context.Context, bucket time.Time) error

	cancel context.CancelFunc
	done   chan struct{}
}

// NewAutoIndexRefresher wires the refresher. idx must be a fully
// initialised autoroute.Index (call autoroute.NewIndex() then
// idx.SetPool(db)).
func NewAutoIndexRefresher(db *pgxpool.Pool, idx *autoroute.Index) *AutoIndexRefresher {
	return &AutoIndexRefresher{
		db:              db,
		idx:             idx,
		RefreshInterval: defaultRefreshInterval,
		RefreshTimeout:  defaultRefreshTimeout,
		CoalesceWindow:  defaultRefreshInterval,
		nowFn:           time.Now,
		done:            make(chan struct{}),
	}
}

// Start spawns the background goroutine and triggers an initial refresh
// in the foreground (so the index is hot before serving the first request).
func (r *AutoIndexRefresher) Start(ctx context.Context) {
	cctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	Go("auto_index_refresher.run", func() { r.run(cctx) })
	slog.Info("auto index refresher started",
		"interval", r.RefreshInterval.String(),
		"timeout", r.RefreshTimeout.String(),
	)

	// Initial refresh in foreground — blocks until done or ctx cancelled.
	// Safe to fail: the goroutine will retry next interval.
	if err := r.RefreshOnce(ctx); err != nil {
		slog.Warn("auto index initial refresh failed", "error", err)
	}
}

// Stop terminates the goroutine and waits for it.
func (r *AutoIndexRefresher) Stop() {
	if r.cancel != nil {
		r.cancel()
	}
	<-r.done
}

// run is the periodic refresh loop.
func (r *AutoIndexRefresher) run(ctx context.Context) {
	defer close(r.done)
	t := time.NewTicker(r.RefreshInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := r.RefreshOnce(ctx); err != nil {
				slog.Warn("auto index refresh failed", "error", err)
			}
		}
	}
}

// beginRefresh 尝试占住刷新槽。返回 false 表示已有人在跑。
//
// 用非阻塞的「占-放」而不是排队锁：调用方是事件触发而不是作业队列，
// 排队只会让积压的 N-1 次过期触发挨个跑一遍，而它们算的是同一个 bucket。
func (r *AutoIndexRefresher) beginRefresh() bool {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	if r.inFlight {
		return false
	}
	r.inFlight = true
	return true
}

func (r *AutoIndexRefresher) endRefresh() {
	r.refreshMu.Lock()
	r.inFlight = false
	r.refreshMu.Unlock()
}

// coalesceWindow 返回同 bucket 合并窗口。零值结构体（测试直接构造）回退到
// RefreshInterval / 默认值，避免 Truncate(0) panic。
func (r *AutoIndexRefresher) coalesceWindow() time.Duration {
	if r.CoalesceWindow > 0 {
		return r.CoalesceWindow
	}
	if r.RefreshInterval > 0 {
		return r.RefreshInterval
	}
	return defaultRefreshInterval
}

// rollupInterval 返回 rollup bucket 的截断粒度（SQL 语义：DELETE WHERE
// bucket = $1 的粒度）。与 coalesceWindow 分开：前者是数据形态、后者是
// 触发节奏，生产配置下两者同为 RefreshInterval（5 分钟）。
func (r *AutoIndexRefresher) rollupInterval() time.Duration {
	if r.RefreshInterval > 0 {
		return r.RefreshInterval
	}
	return defaultRefreshInterval
}

// tryBeginRefresh 是 RefreshOnceStatus 的准入闸门，原子地合并三个判定：
//
//  1. singleflight 互斥（4315a598c 原语义）：已有刷新在跑 ⇒ 跳过，但置
//     busyDeferred 欠账。这是缺陷 1 的修法的一半：跑着那次的 SELECT 早于
//     本次触发的数据提交，本次触发是它的盲区，必须在它结束后补一次执行，
//     而不是当成「已处理」。
//  2. 同 bucket 触发合并（2026-10-05 P2 缺陷 2）：lastBucket == 当前
//     bucket 的重复触发直接跳过 —— 它们算的是同一份 bucket 键，串行执行
//     只是重复 DELETE+INSERT（实测同 bucket 被反复重写约 34 遍）。
//  3. busyDeferred 消费：同 bucket 但带着忙跳过欠账的触发放行并清账 ——
//     合并节流不得吞掉缺陷 1 的补执行。
//
// 返回 false 表示本次触发被跳过（没有执行），调用方必须以 skipped 语义上报，
// 不得当作「已处理」。最终一致性由两处兜底：bucket 滚动后的下一次触发必然
// 放行（5 分钟 ticker 的 run 循环必然到来），以及 LISTEN 监听器对 skipped
// 保持 pending 逐窗重试。
func (r *AutoIndexRefresher) tryBeginRefresh(bucket time.Time) bool {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	if r.inFlight {
		r.busyDeferred = true
		return false
	}
	if !r.lastBucket.IsZero() && bucket.Equal(r.lastBucket) && !r.busyDeferred {
		return false
	}
	r.busyDeferred = false
	r.inFlight = true
	return true
}

// RefreshOnce runs one refresh cycle: credential_model_index rollup,
// then model_task_index rollup, then in-memory Index refresh.
//
// Returns the first error encountered. The In-memory Index is updated
// even if downstream rollups fail (so a degraded model_task_index
// doesn't lock out routing decisions).
//
// 2026-10-05 P2：本方法保留 error 单返回值形态 —— admin/handler.go 以
// `RefreshOnce(ctx) error` 的结构接口静态持有本类型并转手注入
// （SetAutoIndexRefresher → SetIndexRefresher），改签名会把不可动的装配点
// 拉进编译。需要区分「执行 / 跳过」的调用方（LISTEN 监听器、admin /refresh）
// 改用 RefreshOnceStatus；ticker 的 run 循环语义不变（跳过 = 等下一个 tick，
// 天然是兜底位）。
func (r *AutoIndexRefresher) RefreshOnce(ctx context.Context) error {
	_, err := r.RefreshOnceStatus(ctx)
	return err
}

// RefreshOnceStatus 是 RefreshOnce 的跳过感知形态：skipped=true 表示本次
// 触发**没有执行**（singleflight 忙 / 同 bucket 重复触发被合并），此时
// err 必为 nil —— 跳过不是错误，但也绝不是「已处理」。
//
// 2026-10-04: 新增 singleflight 互斥。三个并发触发源（5 分钟 ticker、
// auto_route_refresh 的 LISTEN 监听、admin 手动接口 admin/auto_route.go:799）
// 在实测中产生每 8.8 秒一次的调用，是设计间隔的 34 倍；而 DELETE+INSERT
// **非原子**，交错会撞 duplicate key（2026-09-10 04:18 真实发生过）。
// 2026-10-05 P2（R44 移交 §R44/移交.2、.3）：在互斥之上加两件事 ——
// 跳过以上浮的 skipped 语义上报（不再伪装成成功），以及同 bucket 触发合并
// （互斥只消除交错、不减少执行次数，rollup 快于触发间隔时 34 倍触发会
// 串行全部执行）。准入判定见 tryBeginRefresh。
func (r *AutoIndexRefresher) RefreshOnceStatus(ctx context.Context) (skipped bool, err error) {
	if r == nil {
		return false, nil
	}

	now := time.Now
	if r.nowFn != nil {
		now = r.nowFn
	}
	gateBucket := now().UTC().Truncate(r.coalesceWindow())
	if !r.tryBeginRefresh(gateBucket) {
		slog.Debug("auto index refresh: 触发被合并跳过（已有刷新在跑 / 同 bucket 重复触发）",
			"coalesce_window", r.coalesceWindow().String())
		return true, nil
	}
	defer r.endRefresh()
	// 只有成功才记账 lastBucket：失败后的下一次触发必须重试，不能被同
	// bucket 合并挡住（与既有失败重试语义一致）。
	defer func() {
		if err == nil {
			r.refreshMu.Lock()
			r.lastBucket = gateBucket
			r.refreshMu.Unlock()
		}
	}()

	timeoutCtx, cancel := context.WithTimeout(ctx, r.RefreshTimeout)
	defer cancel()

	bucket := now().UTC().Truncate(r.rollupInterval())

	// 测试缝：注入假执行体时走这里，让离线环境（无真库）也能端到端
	// 计数「N 次触发合并为几次执行」。
	if r.executeFn != nil {
		if err := r.executeFn(timeoutCtx, bucket); err != nil {
			return false, err
		}
		if r.OnRollupComplete != nil {
			r.OnRollupComplete(bucket, 0, 0)
		}
		return false, nil
	}

	credRows, err := r.rollupCredentialModelIndex(timeoutCtx, bucket)
	if err != nil {
		return false, fmt.Errorf("rollup credential_model_index: %w", err)
	}

	taskRows, err := r.rollupModelTaskIndex(timeoutCtx, bucket)
	if err != nil {
		// Don't fail the whole refresh — model_task_index is best-effort
		slog.Warn("model_task_index rollup failed", "error", err)
	}

	// Refresh the in-memory index after the DB rollups succeed.
	if err := r.idx.Refresh(timeoutCtx); err != nil {
		return false, fmt.Errorf("in-memory index refresh: %w", err)
	}

	slog.Info("auto index refreshed",
		"bucket", bucket.Format(time.RFC3339),
		"credential_rows", credRows,
		"task_rows", taskRows,
	)

	if r.OnRollupComplete != nil {
		r.OnRollupComplete(bucket, credRows, taskRows)
	}
	return false, nil
}

// rollupCredentialModelIndex inserts the latest per-credential × per-model
// snapshot into credential_model_index.
//
// Source of truth (last 5 min bucket from credential_model_peak_1m +
// historical request_logs). One row per (credential_id, raw_model).
//
// Pre-computes the three profile scores (smart, speed_first, cost_first)
// inline so the hot path (Decider.Decide) doesn't need to recompute.
//
// 2026-06-28 (provider-314 incident): the underlying SELECT is a
// UNION ALL of two halves:
//   - half 1: traffic-derived (request_logs last 5 min) — original path
//   - half 2: cold-start fallback (v_routable_credential_models bindings
//     not already covered by half 1)
//
// Both halves share the same ON CONFLICT DO UPDATE suffix so live
// metrics always overwrite the conservative half-2 baseline as soon as
// a credential gets real traffic.
// credentialModelIndexRollupSQLs returns the (delete, insert) statement pair
// that refreshes credential_model_index_hot for one bucket. Exposed for the
// SQL-shape regression tests (2026-09-10: an extra ")" in half-2 plus an
// ungrouped outer reference in half-1 kept the rollup failing on every tick).
//
// 2026-09-10 (PG log audit): RefreshOnce runs from BOTH the 5-min ticker and
// the auto_route_refresh LISTEN listener with no mutual exclusion. The
// DELETE+INSERT pair is not atomic, so two overlapping runs interleave as
// DELETE(A) → DELETE(B) → INSERT(A) → INSERT(B) and the second INSERT hits
//
//	duplicate key value violates unique constraint "idx_credential_model_index_hot_unique"
//
// (observed 2026-09-10 04:18 CST). Re-adding ON CONFLICT DO UPDATE makes each
// INSERT idempotent against rows a concurrent run already committed. The
// 2026-07-20 P2-#6 reason ON CONFLICT was originally dropped — "cannot affect
// row a second time" (21000) within ONE statement — cannot recur here: the
// DISTINCT ON (bucket, credential_id, raw_model) tail already guarantees a
// single row per conflict key per statement, so the clause now only ever
// resolves against rows committed by the *other* run.
//
// 2026-09-25 (252 PG log audit): the DELETE used to wrap the full rollup
// SELECT in an IN(...) subquery, so every tick executed the heaviest query in
// the data plane TWICE (once to find the rows to delete, once to insert them)
// across all gateway instances — 29 DELETEs in a 15-min window on 252, mean
// 1.8s each. Every row the rollup produces carries bucket = $1 (both halves
// select $1::timestamptz AS bucket), so "delete exactly the fresh set" is
// equivalent to "delete the whole current bucket and re-insert the fresh set"
// — and the bucket form additionally drops stale current-bucket rows the
// fresh set no longer contains. The delete now rides the unique index prefix
// instead of re-running the rollup.
func credentialModelIndexRollupSQLs() (deleteSQL, insertSQL string) {
	deleteSQL = `DELETE FROM credential_model_index_hot
			WHERE bucket = $1`
	insertSQL = `INSERT INTO credential_model_index_hot (
	    bucket, credential_id, raw_model, canonical_id,
	    billing_mode, unit_price_in_per_1m, unit_price_out_per_1m, context_window,
	    success_rate, p95_latency_ms, active_sessions, concurrency_limit, pressure_ratio,
	    score_smart, score_speed_first, score_cost_first
	) ` + rollupCredentialModelIndexSQL + rollupCredentialModelIndexONCONFLICT
	return deleteSQL, insertSQL
}

func (r *AutoIndexRefresher) rollupCredentialModelIndex(ctx context.Context, bucket time.Time) (int, error) {
	// 2026-07-20 P2-#6: split into DELETE + INSERT to avoid
	// "ON CONFLICT DO UPDATE command cannot affect row a second time"
	// (SQLSTATE 21000) when half-1 (traffic) and half-2 (cold-start) of
	// the UNION ALL produce the same (bucket, credential_id, raw_model)
	// triple. Two-statement version preserves the original semantics
	// (live metrics overwrite baseline) but does it via DELETE-then-INSERT
	// instead of ON CONFLICT.
	deleteSQL, insertSQL := credentialModelIndexRollupSQLs()
	if _, err := r.db.Exec(ctx, deleteSQL, bucket); err != nil {
		return 0, fmt.Errorf("delete: %w", err)
	}
	tag, err := r.db.Exec(ctx, insertSQL, bucket)
	if err != nil {
		return 0, fmt.Errorf("insert: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// rollupModelTaskIndex inserts the latest per-canonical × per-task_type
// snapshot into model_task_index.
//
// Source: request_logs in the last 5 minutes, classified by task_type
// (heuristic, persisted in request_logs.task_type from auto requests).
// If no auto requests yet, the table stays empty — that's fine.
func (r *AutoIndexRefresher) rollupModelTaskIndex(ctx context.Context, bucket time.Time) (int, error) {
	tag, err := r.db.Exec(ctx, rollupModelTaskIndexSQL, bucket)
	if err != nil {
		return 0, fmt.Errorf("exec: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// rollupCredentialModelIndexSQL is the single statement that materialises
// the credential_model_index snapshot.
//
// Logic (two halves joined by UNION ALL):
//
//	(1) Traffic-derived rows (the original path, unchanged):
//	    For each (credential_id, raw_model) active in the last 5 minutes,
//	    gather:
//	      - success rate over the period
//	      - avg/p95 latency
//	      - cost (sum(cost_usd) / sum(total_tokens))
//	      - live active_sessions from credential_model_peak_1m latest bucket
//	      - concurrency_limit from credentials.concurrency_limit
//	    Join credentials + model_offers for static attributes.
//	    Pre-compute the 3 profile scores (smart/speed_first/cost_first)
//	    using autoroute.Score with a representative task signal
//	    (long_context as the worst-case fit).
//
//	(2) Cold-start fallback rows (added 2026-06-28):
//	    For every (credential_id, raw_model) that is_routable=TRUE in the
//	    unified v_routable_credential_models view BUT was not produced by
//	    the request_logs rollup above, insert a baseline row with neutral
//	    scores (success_rate=0.9, p95=1000, scores=50). This closes the
//	    "manual disable → traffic stops → credential_model_index drains
//	    → auto-route forgets the credential → admin re-enables → still
//	    invisible" loop (provider-314/gpt-5.4 incident 2026-06-28).
//	    Default values are conservative: a restored credential sits at
//	    cohort-average quality until the first real request_logs row
//	    arrives, at which point the ON CONFLICT DO UPDATE branch takes
//	    over and replaces the baseline with live metrics.
//
// Cost note: this query is the heaviest in the v2.0 data plane. It runs
// every 5 minutes and touches ~3k rows for a typical 5-min window.
// Mitigations if it gets slow:
//   - Add covering index on request_logs(credential_id, ts)
//   - Materialise request_logs into a per-minute summary table
//   - Run in a separate "reporting" DB replica
//
// NOTE: The profile-score pre-computation in SQL is intentionally rough.
// For v2.0 we use a simple weighted formula in SQL (10×speed + 5×stability
// + 3×success - 0.5×price) — close enough for ranking, and avoids
// spinning up the full autoroute.Score() formula in PL/pgSQL.
// The Decider still calls autoroute.Score() at request time using fresh
// signals, so the SQL scores are advisory.
const rollupCredentialModelIndexSQL = `
WITH fresh AS (
-- ── Half 1: traffic-derived rows (5-min window from request_logs) ─────────
-- 2026-09-10 修复：peak 子查询改为关联内层 q 的分组列。原来子查询里引用
-- COALESCE(rl.outbound_model, rl.client_model)，它只是 GROUP BY 表达式而非
-- 裸列，PG 报 "subquery uses ungrouped column"。聚合下推到 q，外层再取
-- (credential_id, raw_model) 的最新 peak 桶。
SELECT
    q.bucket,
    q.credential_id,
    q.raw_model,
    q.canonical_id,
    q.billing_mode,
    q.unit_price_in_per_1m,
    q.unit_price_out_per_1m,
    q.context_window,
    q.success_rate,
    q.p95_latency_ms,
    -- active_sessions 从 credential_model_peak_1m 的最新桶拉取（dispatch 的
    -- PeakCollector 每分钟写入），驱动 autoroute 的并发感知压力评分。没数据
    -- 时回落 0（保持与 cold-start baseline 的安全行为一致）。
    COALESCE((
      SELECT GREATEST(p.peak_concurrent, 0)
      FROM credential_model_peak_1m p
      WHERE p.credential_id = q.credential_id
        AND p.raw_model     = q.raw_model
        AND p.bucket <= $1
      ORDER BY p.bucket DESC
      LIMIT 1
    ), 0) AS active_sessions,
    q.concurrency_limit,
    -- pressure_ratio 从 active_sessions / concurrency_limit 计算（NULL/0 安全）。
    CASE
      WHEN q.concurrency_limit IS NULL OR q.concurrency_limit <= 0 THEN 0
      ELSE LEAST(1.0, COALESCE((
        SELECT GREATEST(p.peak_concurrent, 0)::numeric
        FROM credential_model_peak_1m p
        WHERE p.credential_id = q.credential_id
          AND p.raw_model     = q.raw_model
          AND p.bucket <= $1
        ORDER BY p.bucket DESC
        LIMIT 1
      ), 0) / q.concurrency_limit)
    END AS pressure_ratio,
    q.score_smart,
    q.score_speed_first,
    q.score_cost_first,
    1 AS half_no
FROM (
SELECT
    $1::timestamptz AS bucket,
    rl.credential_id,
    COALESCE(rl.outbound_model, rl.client_model) AS raw_model,
    mo.canonical_id,
    mo.billing_mode,
    AVG(mo.unit_price_in_per_1m)  AS unit_price_in_per_1m,
    AVG(mo.unit_price_out_per_1m) AS unit_price_out_per_1m,
    MAX(mc.context_window)        AS context_window,
    -- success rate over the bucket window
    COALESCE(AVG(CASE WHEN rl.success THEN 1.0 ELSE 0.0 END), 0.9) AS success_rate,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY rl.latency_ms)::int, 1000) AS p95_latency_ms,
    MAX(cr.concurrency_limit)  AS concurrency_limit,
    -- Simplified pre-computed scores (no subquery; pressure assumed 0).
    -- smart weights: price=25 speed=25 stab=20 match=25 pressure=10 ctx=15
    (COALESCE(100 * (1 - LEAST(1.0, AVG(mo.unit_price_in_per_1m + mo.unit_price_out_per_1m) / 20.0)), 50) * 0.25
   + COALESCE(100 - LEAST(100, percentile_cont(0.95) WITHIN GROUP (ORDER BY rl.latency_ms) / 30), 50) * 0.25
   + COALESCE(AVG(CASE WHEN rl.success THEN 1.0 ELSE 0.0 END), 0.9) * 100 * 0.20
   + 50 * 0.25
   + 100 * 0.10
   + 80 * 0.15)::numeric(8,4) AS score_smart,
    -- speed_first weights: price=10 speed=50 stab=20 match=15 pressure=5 ctx=10
    (COALESCE(100 * (1 - LEAST(1.0, AVG(mo.unit_price_in_per_1m + mo.unit_price_out_per_1m) / 20.0)), 50) * 0.10
   + COALESCE(100 - LEAST(100, percentile_cont(0.95) WITHIN GROUP (ORDER BY rl.latency_ms) / 30), 50) * 0.50
   + COALESCE(AVG(CASE WHEN rl.success THEN 1.0 ELSE 0.0 END), 0.9) * 100 * 0.20
   + 50 * 0.15
   + 100 * 0.05
   + 80 * 0.10)::numeric(8,4) AS score_speed_first,
    -- cost_first weights: price=50 speed=10 stab=15 match=20 pressure=5 ctx=10
    (COALESCE(100 * (1 - LEAST(1.0, AVG(mo.unit_price_in_per_1m + mo.unit_price_out_per_1m) / 20.0)), 50) * 0.50
   + COALESCE(100 - LEAST(100, percentile_cont(0.95) WITHIN GROUP (ORDER BY rl.latency_ms) / 30), 50) * 0.10
   + COALESCE(AVG(CASE WHEN rl.success THEN 1.0 ELSE 0.0 END), 0.9) * 100 * 0.15
    + 50 * 0.20
    + 100 * 0.05
    + 80 * 0.10)::numeric(8,4) AS score_cost_first
FROM request_logs_hot rl
JOIN credentials cr ON cr.id = rl.credential_id
LEFT JOIN model_offers mo
  ON mo.credential_id = rl.credential_id
 AND (mo.outbound_model_name = COALESCE(rl.outbound_model, rl.client_model)
   OR mo.raw_model_name = COALESCE(rl.outbound_model, rl.client_model))
LEFT JOIN models_canonical mc ON mc.id = mo.canonical_id
WHERE rl.ts >= NOW() - INTERVAL '5 minutes'
  AND rl.ts < NOW()
  AND rl.credential_id IS NOT NULL
  AND COALESCE(cr.status, 'active') = 'active'
  AND COALESCE(cr.lifecycle_status, 'active') = 'active'
GROUP BY rl.credential_id, COALESCE(rl.outbound_model, rl.client_model),
         mo.canonical_id, mo.billing_mode
) q

UNION ALL

-- ── Half 2: cold-start fallback (added 2026-06-28, provider-314 incident) ─
-- For every routable binding that the request_logs rollup above did NOT
-- already produce, write a baseline row so the auto-route can see it
-- even with zero recent traffic. Baseline values are intentionally
-- conservative (success=0.9, p95=1000, scores=50) — the first real
-- request through the credential will produce a request_logs row that
-- the half-1 rollup will overwrite via ON CONFLICT DO UPDATE below.
--
-- Anti-join clause: skip pairs that already appeared in half 1 so we
-- don't churn the ON CONFLICT path. Note we deliberately do NOT
-- re-derive rl.ts in the anti-join's filter — half 1 itself filters on
-- the same 5-min window, so equality is sufficient.
SELECT
    $1::timestamptz                     AS bucket,
    v.credential_id,
    pm.raw_model_name                   AS raw_model,
    pm.canonical_id,
    cmb.billing_mode,
    cmb.unit_price_in_per_1m,
    cmb.unit_price_out_per_1m,
    mc.context_window,
    0.9::numeric(5,4)                   AS success_rate,
    1000::int                           AS p95_latency_ms,
    -- 2026-09-09 P0：cold-start baseline 也从 credential_model_peak_1m 拉
    -- 最新并发；若冷启动凭据此前无流量，回落 0（与原行为一致）。
    COALESCE((
      SELECT GREATEST(p.peak_concurrent, 0)
      FROM credential_model_peak_1m p
      WHERE p.credential_id = v.credential_id
        AND p.raw_model = pm.raw_model_name
        AND p.bucket <= $1
      ORDER BY p.bucket DESC
      LIMIT 1
    ), 0)::int AS active_sessions,
    c.concurrency_limit,
    -- 同步计算 pressure_ratio，与 traffic 半保持对称。
    CASE
      WHEN c.concurrency_limit IS NULL OR c.concurrency_limit <= 0 THEN 0::numeric(5,4)
      ELSE LEAST(1.0, COALESCE((
        SELECT GREATEST(p.peak_concurrent, 0)::numeric
        FROM credential_model_peak_1m p
        WHERE p.credential_id = v.credential_id
          AND p.raw_model = pm.raw_model_name
          AND p.bucket <= $1
        ORDER BY p.bucket DESC
        LIMIT 1
      ), 0) / c.concurrency_limit)::numeric(5,4)
    END AS pressure_ratio,
    50::numeric(8,4)                    AS score_smart,
    50::numeric(8,4)                    AS score_speed_first,
    50::numeric(8,4)                    AS score_cost_first,
    2 AS half_no
FROM v_routable_credential_models v
JOIN credentials c                  ON c.id = v.credential_id
JOIN credential_model_bindings cmb ON cmb.id = v.binding_id
JOIN provider_models pm             ON pm.id = v.provider_model_id
LEFT JOIN models_canonical mc       ON mc.id = pm.canonical_id
WHERE v.is_routable = TRUE
  AND pm.raw_model_name IS NOT NULL
  AND NOT EXISTS (
      SELECT 1 FROM request_logs_hot rl
      WHERE rl.credential_id = v.credential_id
        AND (rl.outbound_model = pm.raw_model_name
          OR rl.client_model  = pm.raw_model_name)
        AND rl.ts >= NOW() - INTERVAL '5 minutes'
  )
)
-- ── Dedup: skip rows whose (credential_id, raw_model) had IDENTICAL
-- metrics (success_rate, p95, all 3 scores) in the most recent prior
-- bucket. 2026-07-13 incident: 97.1% of inserts were pure duplicates.
--
-- 2026-07-13 dedup scope fix: was a single-bucket lookup. Now it checks
-- the LATEST row overall — so when traffic is stable for 6 hours we
-- collapse 72 hourly buckets into 1, instead of only avoiding duplicate
-- writes to the immediately previous bucket.
-- Genuine metric changes (success_rate 0.9 -> 0.85 -> 0.9) are still
-- recorded because the comparison is against the most-recent row
-- (whose metrics would already match the new row if nothing changed).
SELECT DISTINCT ON (f.bucket, f.credential_id, f.raw_model)
       f.bucket, f.credential_id, f.raw_model, f.canonical_id,
       f.billing_mode, f.unit_price_in_per_1m, f.unit_price_out_per_1m, f.context_window,
       f.success_rate, f.p95_latency_ms, f.active_sessions, f.concurrency_limit, f.pressure_ratio,
       f.score_smart, f.score_speed_first, f.score_cost_first
FROM fresh f
WHERE NOT EXISTS (
    SELECT 1 FROM credential_model_index_hot prev
    WHERE prev.credential_id = f.credential_id
      AND prev.raw_model      = f.raw_model
      AND prev.bucket = (
          SELECT MAX(bucket) FROM credential_model_index_hot prev2
          WHERE prev2.credential_id = f.credential_id
            AND prev2.raw_model      = f.raw_model
            AND prev2.bucket        < f.bucket
            AND prev2.bucket        > f.bucket - INTERVAL '7 days'
      )
      AND prev.success_rate        IS NOT DISTINCT FROM f.success_rate
      AND prev.p95_latency_ms      IS NOT DISTINCT FROM f.p95_latency_ms
      AND prev.score_smart         IS NOT DISTINCT FROM f.score_smart
      AND prev.score_speed_first   IS NOT DISTINCT FROM f.score_speed_first
      AND prev.score_cost_first    IS NOT DISTINCT FROM f.score_cost_first
)
-- ORDER BY 必须以 DISTINCT ON 的列开头；末尾 half_no 保证 traffic 行(half_no=1)
-- 优先于 cold-start baseline(half_no=2)，避免 baseline 覆盖真实指标。
ORDER BY f.bucket, f.credential_id, f.raw_model, f.half_no
`

// rollupCredentialModelIndexONCONFLICT is the ON CONFLICT clause for the
// rollup INSERT. Lives in a separate const because UNION ALL writes have
// to combine the SELECT list with this suffix, and Go string concatenation
// is the cleanest way to express it without making the main SELECT
// unreadable. ON CONFLICT DO UPDATE ensures the live half-1 metrics
// always overwrite the half-2 baseline, and vice-versa when both halves
// target the same (bucket, credential_id, raw_model).
const rollupCredentialModelIndexONCONFLICT = `
ON CONFLICT (bucket, credential_id, raw_model) DO UPDATE SET
    canonical_id         = EXCLUDED.canonical_id,
    billing_mode         = EXCLUDED.billing_mode,
    unit_price_in_per_1m = EXCLUDED.unit_price_in_per_1m,
    unit_price_out_per_1m= EXCLUDED.unit_price_out_per_1m,
    context_window       = EXCLUDED.context_window,
    success_rate         = EXCLUDED.success_rate,
    p95_latency_ms       = EXCLUDED.p95_latency_ms,
    active_sessions      = EXCLUDED.active_sessions,
    concurrency_limit    = EXCLUDED.concurrency_limit,
    pressure_ratio       = EXCLUDED.pressure_ratio,
    score_smart          = EXCLUDED.score_smart,
    score_speed_first    = EXCLUDED.score_speed_first,
    score_cost_first     = EXCLUDED.score_cost_first,
    updated_at           = NOW()
`

// rollupModelTaskIndexSQL aggregates request_logs into per-task-type
// performance buckets. Used by the admin auto-route dashboard
// ("which model wins which task type?") and future analytics.
//
// Bucket key: (bucket, canonical_id, task_type). Filtered to auto-route
// requests only (is_auto_request = true) so we measure actual routing
// outcomes rather than pre-auto usage.
const rollupModelTaskIndexSQL = `
INSERT INTO model_task_index (
    bucket, canonical_id, task_type,
    sample_count, success_rate,
    avg_latency_ms, p95_latency_ms, avg_cost_per_1k_usd,
    primary_credential_id
)
SELECT
    $1 AS bucket,
    rl.canonical_id,
    COALESCE(rl.task_type, 'chat') AS task_type,
    COUNT(*) AS sample_count,
    COALESCE(AVG(CASE WHEN rl.success THEN 1.0 ELSE 0.0 END), 0.9) AS success_rate,
    COALESCE(AVG(rl.latency_ms), 0)::int AS avg_latency_ms,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY rl.latency_ms), 1000)::int AS p95_latency_ms,
    CASE WHEN SUM(rl.total_tokens) > 0
         THEN (SUM(rl.cost_usd) / SUM(rl.total_tokens)) * 1000
         ELSE 0
    END AS avg_cost_per_1k_usd,
    MODE() WITHIN GROUP (ORDER BY rl.credential_id) AS primary_credential_id
FROM request_logs_hot rl
WHERE rl.ts >= NOW() - INTERVAL '5 minutes'
  AND rl.ts < $1
  AND rl.is_auto_request = TRUE
  AND rl.canonical_id IS NOT NULL
GROUP BY rl.canonical_id, rl.task_type
ON CONFLICT (bucket, canonical_id, task_type) DO UPDATE SET
    sample_count        = EXCLUDED.sample_count,
    success_rate        = EXCLUDED.success_rate,
    avg_latency_ms      = EXCLUDED.avg_latency_ms,
    p95_latency_ms      = EXCLUDED.p95_latency_ms,
    avg_cost_per_1k_usd = EXCLUDED.avg_cost_per_1k_usd,
    primary_credential_id = EXCLUDED.primary_credential_id,
    updated_at          = NOW()
`
