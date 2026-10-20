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
	"github.com/kaixuan/llm-gateway-go/db"
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
		<-r.done
	}
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
// historical _request_logs). One row per (credential_id, raw_model).
//
// Pre-computes the three profile scores (smart, speed_first, cost_first)
// inline so the hot path (Decider.Decide) doesn't need to recompute.
//
// 2026-06-28 (provider-314 incident): the underlying SELECT is a
// UNION ALL of two halves:
//   - half 1: traffic-derived (_request_logs last 5 min) — original path
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

// credentialModelIndexRollupLockKey 是本 rollup 的**跨进程**互斥键。
//
// ★ 2026-10-20（退役轮 R69）：进程内互斥（tryBeginRefresh / singleflight）
// 只覆盖**本进程**的触发源（5 分钟 ticker / LISTEN 监听 / admin 手动接口）。
// 2026-10-20 本机实测暴露出进程间同库并存时的数据损坏：
//
//	8781（新构建，rollup 正确）  INSERT 75 行（bucket=B）
//	8782（旧构建，rollup 42703）  DELETE bucket=B 成功 → INSERT 42703 失败
//	⇒ hot 表被「删掉且没补回」⇒ credential_model_index_hot 恒空
//
// 后果不是「指标陈旧」而是**功能缺失**：autoroute 候选池读的是视图
// credential_model_index_with_current_month（hot ∪ 月分区），新出现的
// (credential, raw_model) 组合在月分区里要等 8h 排水窗口才可见 ⇒ 在此
// 之前该凭据/模型对候选池**根本不存在** ⇒ 路由层 model_not_found /
// no_candidate ⇒ 100% 流量失败。
//
// 为什么用 advisory lock（而不是只把两条语句放进一个事务）：
//   - 单事务只保证**本进程**这一对语句不撕裂；另一个实例的 DELETE 仍然
//     会把本进程刚提交的行删掉（对方不需要知道我们的锁）。
//   - advisory lock 让**同库的所有实例**在同一把锁上排队 ⇒ 蓝绿部署
//     窗口（候选槽 + 旧槽同时对着同一个 PG，见
//     scripts/deploy-seamless-docker.sh）也不会互删。
//
// ⚠ 边界（如实记录）：advisory lock 只对**同样会取锁的构建**有效。
// 旧构建（未修 rollup 42703 的那个）不会取锁，它照样能把我们的行删掉
// —— 那种情况只能靠把对方升级到含修复的构建来消除（实测：旧构建的
// DELETE 每 5s 执行一次，hot 表 n_tup_ins=1036 / n_tup_del=968 /
// n_live_tup=0，n_live_tup=0 就是「写进去又被删掉」的直接证据）。
//
// 键的取法沿用仓内既有约定 settings/session_write_safety.go：
// hashtextextended('<literal>', 0) —— 同一 PG 上所有实例算出同一个值，
// 不依赖进程内状态，也不需要新增迁移。
const credentialModelIndexRollupLockKey = "llmgw:credential-model-index-rollup"

// rollupCredentialModelIndex 把当前 bucket 的快照写进 credential_model_index_hot。
//
// ★ 2026-10-20（R69）：DELETE 与 INSERT 现在在**同一个事务**里跑，且事务
// 先取跨进程 advisory lock（credentialModelIndexRollupLockKey）。
//
// 改之前：两条语句各走一次 r.db.Exec（= 两个隐式事务）⇒
//  1. 中间态对读者可见（读者能在 DELETE 与 INSERT 之间看到 hot 表空）；
//  2. 另一个实例可以在两条语句之间插进来，把我们写的行删掉（见
//     credentialModelIndexRollupLockKey 的注释里的本机实测）。
//
// 改之后：两条语句同事务提交 ⇒ 读者只看到「旧态」或「新态」；且同库实例
// 之间用 advisory lock 串行 ⇒ 不再互删。
//
// 2026-07-20 P2-#6: split into DELETE + INSERT to avoid
// "ON CONFLICT DO UPDATE command cannot affect row a second time"
// (SQLSTATE 21000) when half-1 (traffic) and half-2 (cold-start) of
// the UNION ALL produce the same (bucket, credential_id, raw_model)
// triple. Two-statement version preserves the original semantics
// (live metrics overwrite baseline) but does it via DELETE-then-INSERT
// instead of ON CONFLICT.
//
// ⚠ 事务边界带来的一个正向副作用：INSERT 失败（例如旧构建的 42703）时，
// DELETE 会随事务一起回滚 ⇒ **失败不再留下「删掉了却没补回」的坑**。
func (r *AutoIndexRefresher) rollupCredentialModelIndex(ctx context.Context, bucket time.Time) (int, error) {
	deleteSQL, insertSQL := credentialModelIndexRollupSQLs()

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	// 回滚是幂等的：commit 之后调用它返回 ErrTxClosed，忽略即可。
	defer func() { _ = tx.Rollback(ctx) }()

	// 锁必须**先于** DELETE 取：否则两个实例仍可能同时删同一 bucket。
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		credentialModelIndexRollupLockKey,
	); err != nil {
		return 0, fmt.Errorf("advisory lock: %w", err)
	}

	if _, err := tx.Exec(ctx, deleteSQL, bucket); err != nil {
		return 0, fmt.Errorf("delete: %w", err)
	}
	tag, err := tx.Exec(ctx, insertSQL, bucket)
	if err != nil {
		return 0, fmt.Errorf("insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// rollupModelTaskIndex inserts the latest per-canonical × per-task_type
// snapshot into model_task_index.
//
// Source: the session family (session_turns_hot), last 5 minutes, classified
// by task_type. If no auto requests yet, the table stays empty — that's fine.
//
// ★ 2026-10-20（迁移 890，v1 族退役）：源从 `public._request_logs` 换成
// **会话族**（db.SessionFamilyTurnsHotSourceSQL()）。
//
// # 总体（population）变了，而且变得更**对**
//
// 旧登记（2026-10-13 S3 波 3 实测，「不迁」）写的是：auto 请求按设计不镜像进
// 会话族 —— 本地真库 3 天窗 v1 侧 3,420 条 is_auto_request 行、会话侧 0 条。
//
// 那条测量的两侧口径**不同**，这才是它看起来矛盾的原因：
//
//   - v1 侧那 3,420 条里含**内部** auto 流量（探针 / 内部回环）；
//   - 会话族**按设计**把内部 auto 条目排除（internal/sessionv2mirror/hook.go 的
//     4 臂分类：IsInternalAutoEntry 命中即 return，**不镜像进 session_turns 家族**），
//     但**业务** auto 请求是镜像的 —— 镜像侧写 is_auto_request
//     （internal/sessionv2mirror/s1a_fields.go:75-76）。
//
// ⇒ 改指之后，这个索引的总体从「v1 的全部 auto 行（含内部探针）」收窄成
// **业务 auto 请求**——而后者正是「per model per task_type 的自动路由性能」
// 这个索引本该统计的东西（内部探针的延迟与成功率本来就代表不了业务任务）。
// ⇒ 「不迁」这条登记在 890 之后**已经没有对象**：v1 族不存在了，不存在
// 「换个源继续按老口径统计」的选项。
//
// # 两个必须一起说清的后果
//
//  1. **本语句没有 DELETE**（只有 INSERT … ON CONFLICT DO UPDATE）⇒ 窗口里
//     没有业务 auto 请求时，本轮**不写任何行**，既有行**保留**（表冻结在
//     上一轮有样本的样子），**不是**被清空。
//  2. 若某个环境**从未**产生过业务 auto 请求，表会一直是空的；
//     admin 侧对此已有话术（admin/analytics.go:645：
//     "model_task_index is empty; awaiting first bg worker refresh"）。
//
// ⚠ 口径前提（本轮未改，但会决定这个索引有没有内容）：
//
//	业务 auto 请求必须**真的**流经网关并带上 is_auto_request —— 实测本地真库
//	session_turns_hot 535 行里 is_auto_request 非空 0 行、origin_actor 非空
//	0 行 ⇒ 本地环境**从未产生过业务 auto 流量**（不是映射坏了）。
//	读这个索引之前先确认那件事，否则会把「本地没有 auto 流量」读成「自动
//	路由坏了」。
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
//	    the _request_logs rollup above, insert a baseline row with neutral
//	    scores (success_rate=0.9, p95=1000, scores=50). This closes the
//	    "manual disable → traffic stops → credential_model_index drains
//	    → auto-route forgets the credential → admin re-enables → still
//	    invisible" loop (provider-314/gpt-5.4 incident 2026-06-28).
//	    Default values are conservative: a restored credential sits at
//	    cohort-average quality until the first real _request_logs row
//	    arrives, at which point the ON CONFLICT DO UPDATE branch takes
//	    over and replaces the baseline with live metrics.
//
// Cost note: this query is the heaviest in the v2.0 data plane. It runs
// every 5 minutes and touches ~3k rows for a typical 5-min window.
// Mitigations if it gets slow:
//   - Add covering index on _request_logs(credential_id, ts)
//   - Materialise _request_logs into a per-minute summary table
//   - Run in a separate "reporting" DB replica
//
// NOTE: The profile-score pre-computation in SQL is intentionally rough.
// For v2.0 we use a simple weighted formula in SQL (10×speed + 5×stability
// + 3×success - 0.5×price) — close enough for ranking, and avoids
// spinning up the full autoroute.Score() formula in PL/pgSQL.
// The Decider still calls autoroute.Score() at request time using fresh
// signals, so the SQL scores are advisory.
var rollupCredentialModelIndexSQL = `
WITH fresh AS (
-- ── Half 1: traffic-derived rows (5-min window from the session family) ───
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
    rl.model_key AS raw_model,
    rl.mo_canonical_id AS canonical_id,
    rl.mo_billing_mode AS billing_mode,
    AVG(rl.mo_price_in)  AS unit_price_in_per_1m,
    AVG(rl.mo_price_out) AS unit_price_out_per_1m,
    MAX(mc.context_window)        AS context_window,
    -- success rate over the bucket window
    COALESCE(AVG(CASE WHEN rl.success THEN 1.0 ELSE 0.0 END), 0.9) AS success_rate,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY rl.latency_ms)::int, 1000) AS p95_latency_ms,
    MAX(cr.concurrency_limit)  AS concurrency_limit,
    -- Simplified pre-computed scores (no subquery; pressure assumed 0).
    -- smart weights: price=25 speed=25 stab=20 match=25 pressure=10 ctx=15
    (COALESCE(100 * (1 - LEAST(1.0, AVG(rl.mo_price_in + rl.mo_price_out) / 20.0)), 50) * 0.25
   + COALESCE(100 - LEAST(100, percentile_cont(0.95) WITHIN GROUP (ORDER BY rl.latency_ms) / 30), 50) * 0.25
   + COALESCE(AVG(CASE WHEN rl.success THEN 1.0 ELSE 0.0 END), 0.9) * 100 * 0.20
    + 50 * 0.25
    + 100 * 0.10
    + 80 * 0.15)::numeric(8,4) AS score_smart,
    -- speed_first weights: price=10 speed=50 stab=20 match=15 pressure=5 ctx=10
    (COALESCE(100 * (1 - LEAST(1.0, AVG(rl.mo_price_in + rl.mo_price_out) / 20.0)), 50) * 0.10
   + COALESCE(100 - LEAST(100, percentile_cont(0.95) WITHIN GROUP (ORDER BY rl.latency_ms) / 30), 50) * 0.50
   + COALESCE(AVG(CASE WHEN rl.success THEN 1.0 ELSE 0.0 END), 0.9) * 100 * 0.20
    + 50 * 0.15
    + 100 * 0.05
    + 80 * 0.10)::numeric(8,4) AS score_speed_first,
    -- cost_first weights: price=50 speed=10 stab=15 match=20 pressure=5 ctx=10
    (COALESCE(100 * (1 - LEAST(1.0, AVG(rl.mo_price_in + rl.mo_price_out) / 20.0)), 50) * 0.50
   + COALESCE(100 - LEAST(100, percentile_cont(0.95) WITHIN GROUP (ORDER BY rl.latency_ms) / 30), 50) * 0.10
   + COALESCE(AVG(CASE WHEN rl.success THEN 1.0 ELSE 0.0 END), 0.9) * 100 * 0.15
    + 50 * 0.20
    + 100 * 0.05
    + 80 * 0.10)::numeric(8,4) AS score_cost_first
-- ── 2026-10-13 S3 波 3（停写切换）双源化 ──
-- v1 腿原样保留（写门开 = 生产/回滚期它就是全部流量）；新增会话腿只收
-- 冻结点（db.V1FreezeCutoffSQL = v1 两表 max(ts)）以上的行——停写后 v1
-- hot 排空，真实 success/latency 由会话腿继续供数，autoroute 候选池不再
-- 退化为 half-2 cold-start 基线。
-- 会话腿的 offers 匹配不走路由名精确等值：session 侧 t.raw_model_name 是
-- 剥 provider 前缀的归一化名（真库实测 3 天双写对里 v1 名匹配 96.7%、
-- 会话 raw 名仅 54%），改走 alias 链 lower(t.model) → model_aliases →
-- canonical_id → model_offers.canonical_id，实测恢复到 96.5%（与 v1 持平）。
-- 输出名对齐：匹配行用 offers 自己的 raw_model_name（与 half-2 基线键、
-- credential_model_peak_1m.raw_model 同名空间），未匹配行回落 t.model
-- （与 v1 腿未匹配行的失序幅度同级）。
--
-- ★ 2026-10-20（迁移 890，v1 族退役）：**v1 腿整条删除**，只剩会话腿。
-- 理由：v1 族被 DROP 后，同一条 FROM 里提到 v1 关系会让**整条语句** 42P01
-- （不是「那条腿失败」）⇒ half-1 与 half-2 一起挂。删除不损失供数能力：
-- 停写后 v1 腿本就恒空（v1 hot 排空），会话腿已经是承重腿。
-- ⚠ 冻结点守卫（db.V1FreezeCutoffSQL）保留：890 之后它是常数
-- '-infinity'（见 db/v1_freeze_cutoff.go），条件恒真、零成本，但它把
-- 「本查询与 admin 侧同款分界」这件事保持在同一形状上。
FROM (
    -- ⚠⚠ 这 8 个投影**必须带别名**：外层（rl.*）按 v1 腿的列名引用它们
    -- （credential_id / model_key / mo_canonical_id / mo_billing_mode /
    --   mo_price_in / mo_price_out / success / latency_ms）。
    -- 890 之前这条子查询的列名来自 v1 族的真实列；删掉 v1 腿、换成会话腿
    -- 之后，若不补别名，PG 会把无别名表达式列命名为 "?column?" ⇒ 外层
    -- rl.credential_id 直接 42703（column rl.credential_id does not exist）
    -- ⇒ 整条 rollup 失败 ⇒ autoroute 每 5s 刷新报错、凭据健康/压力指标停更。
    -- （2026-10-20 实测：活库日志每 5s 一条 42703。）
    SELECT (CASE WHEN t.credential_id ~ '^[0-9]+$' THEN t.credential_id::bigint END) AS credential_id,
           COALESCE(mo.raw_model_name, mo.outbound_model_name, t.model)      AS model_key,
           mo.canonical_id                                                   AS mo_canonical_id,
           mo.billing_mode                                                   AS mo_billing_mode,
           mo.unit_price_in_per_1m                                           AS mo_price_in,
           mo.unit_price_out_per_1m                                          AS mo_price_out,
           t.success                                                         AS success,
           t.latency_ms                                                      AS latency_ms
    FROM session_turns_hot t
    LEFT JOIN model_offers mo
      ON mo.credential_id = (CASE WHEN t.credential_id ~ '^[0-9]+$' THEN t.credential_id::bigint END)
     AND mo.canonical_id IN (SELECT ma.canonical_id FROM model_aliases ma
                             WHERE ma.raw_name = lower(t.model) AND ma.status = 'active')
    WHERE t.ts >= NOW() - INTERVAL '5 minutes'
      AND t.ts < NOW()
      AND t.credential_id IS NOT NULL
      AND t.credential_id <> ''
      -- 5 分钟窗 ≪ hot 保留窗（默认 8h），会话腿只读 hot 表即可；
      -- 冻结点守卫防双写期双计（镜像 ts 是落库侧时刻，晚于 v1 行 ts）。
      AND t.ts > ` + db.V1FreezeCutoffSQL + `
) rl
JOIN credentials cr ON cr.id = rl.credential_id
LEFT JOIN models_canonical mc ON mc.id = rl.mo_canonical_id
WHERE COALESCE(cr.status, 'active') = 'active'
  AND COALESCE(cr.lifecycle_status, 'active') = 'active'
GROUP BY rl.credential_id, rl.model_key,
         rl.mo_canonical_id, rl.mo_billing_mode
) q

UNION ALL

-- ── Half 2: cold-start fallback (added 2026-06-28, provider-314 incident) ─
-- For every routable binding that the _request_logs rollup above did NOT
-- already produce, write a baseline row so the auto-route can see it
-- even with zero recent traffic. Baseline values are intentionally
-- conservative (success=0.9, p95=1000, scores=50) — the first real
-- request through the credential will produce a _request_logs row that
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
  -- S3 波 3（2026-10-13）：同源会话腿抑制——停写后 v1 NOT EXISTS 恒空，
  -- 没有这条腿的话 half-2 的 cold-start 基线会每 5 分钟经 ON CONFLICT
  -- 覆盖 half-1 刚写入的真实指标（half-1/half-2 必须同迁，见文件头）。
  -- 匹配键与 half-1 会话腿同一 alias 链（canonical 粒度）；pm.canonical_id
  -- 为 NULL 时 IN 集为空 = 不抑制 = 写基线，与 v1 名失配时的行为同级。
  -- ★ 2026-10-20（迁移 890，v1 族退役）：原先成对存在的 v1 抑制臂删除
  -- （v1 族已 DROP，提及即 42P01 带走整条语句）；会话腿成为唯一抑制臂。
  AND NOT EXISTS (
      SELECT 1
      FROM session_turns_hot t
      WHERE (CASE WHEN t.credential_id ~ '^[0-9]+$' THEN t.credential_id::bigint END) = v.credential_id
        AND t.ts >= NOW() - INTERVAL '5 minutes'
        AND t.ts > ` + db.V1FreezeCutoffSQL + `
        AND lower(t.model) IN (SELECT ma.raw_name FROM model_aliases ma
                               WHERE ma.canonical_id = pm.canonical_id
                                 AND ma.status = 'active')
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

// rollupModelTaskIndexSQL aggregates the session-turn family into per-task-type
// performance buckets. Used by the admin auto-route dashboard
// ("which model wins which task type?") and future analytics.
//
// Bucket key: (bucket, canonical_id, task_type). Filtered to auto-route
// requests only (is_auto_request = true) so we measure actual routing
// outcomes rather than pre-auto usage.
//
// ★ 2026-10-20（迁移 890，v1 族退役）：源从 v1 hot 表换成**会话族原生源**
// （db.SessionFamilyTurnsHotSourceSQL：session_turns_hot ∪ 特征层 LEFT JOIN，
// 投影出与 v1 同名列的 118 列契约）。选 hot 腿而不是 hot ∪ parent 的理由：
// 窗口是 5 分钟，远小于 hot 保留期（默认 8h）⇒ 窗内的行只可能落在 hot 侧
// （与同文件 half-1 会话腿、bg/candidate_failure_monitor.go 的 auto-cool 腿
// 同款论证）。投影带来的列名对等让下面这份 SELECT 一字未改。
// ⚠ 它是 **var** 不是 const：投影源由 db.SessionFamilyTurnsHotSourceSQL() 生成，
// 那不是编译期常量（const 表达式里不能调函数）。
var rollupModelTaskIndexSQL = `
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
FROM ` + db.SessionFamilyTurnsHotSourceSQL() + ` rl
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
