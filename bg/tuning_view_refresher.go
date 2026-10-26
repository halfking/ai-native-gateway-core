package bg

// tuning_view_refresher.go — periodic REFRESH of the materialised
// views (tuning_signals_5m and tuning_signals_daily) added in P7.5.
//
// Materialised views don't auto-update on source-row changes, so
// we run REFRESH MATERIALIZED VIEW CONCURRENTLY on a 5-min ticker.
// CONCURRENTLY requires a UNIQUE index (which the views have).
//
// ── 2026-10-20（R51 96h 轮）：跨实例协调 + 可观测 ─────────────────────
//
// **根因**：本 worker 此前**没有任何跨实例协调**，且 ticker 是**进程启动相位**
// （time.NewTicker）⇒ 每个网关实例各刷各的，集群刷新频率 = 实例数 × 12 次/小时。
//
// 实测（252 生产库只读，pg_stat_statements 窗口 25.69h + 逐实例日志对账）：
// tuning_signals_5m **24.4 次/小时**（= 2.04×，正好两个实例：154 full role
// + 245 data-plane —— 245 虽跑在 data-plane 模式，但本 worker **不在**
// `!bgDataPlaneOnly` 门内，照刷不误）。
//
// ⚠ 归因方法（别重犯 R51 的错）：pg_stat_statements **不记录"谁发的"**，
// 光看 calls 只能得到"2×"这个**现象**，得不到**归因**。真正的归因是
// **逐实例日志行计数**，且要求各实例合计与 pg_stat_statements 的 calls
// **完全对上**（本轮 tuning 628/628、routing_analytics_7d 76/76，两项均完全一致）。
//
// 单次刷新成本（252 生产实测，同一窗口）：
//
//	view                   calls   mean      max        total
//	tuning_signals_5m        685   217.4ms   4517.6ms   148.9s
//	tuning_signals_daily     685   126.0ms   8616.8ms    86.3s
//
// ⇒ 合计 235.2s / 25.69h = 9.2s/小时（≈0.25% 墙钟）。2× 消掉可省约
// 4.6s/小时（均值口径）；max 口径单轮两视图合计可达 13.1s。
//
// ⚠ 旧文件头宣称的 "~30ms / ~50ms" **与实测差 7 倍**，已按实测改写。
// ⇒ 视图本身极小（168 kB / 80 kB）且 temp_blks_written = 0 ⇒ **不溢写**，
// 无需像 materialized_view_refresher 那样抬 work_mem。
//
// **修法**：复用 materialized_view_refresher 的成熟范式（三件套）：
//
//  1. **墙钟对齐**：nextAlignedWait(now, 5m) ⇒ 所有实例在同一瞬间醒来。
//     锁只在"重叠窗口"内互斥（见 materialized_view_refresher.go 文件头），
//     相位随机时锁去重不掉 2× —— 必须先对齐，锁才收得住。
//  2. **Redis 令牌桶**（首选）+ **Postgres advisory lock**（兜底）：
//     每窗口全局恰好一次刷新，没抢到的**跳过**而非排队。
//  3. **指标**：`gateway_mv_refresh_total{view,outcome}` 此前**闭集里
//     没有 tuning 标签** ⇒ tuning 侧完全无可观测性，协调效果也无尺子。
//     本轮把两个 tuning 视图纳入该指标族（见 bg/metrics.go 的闭集注释）。
//
// ⚠ **advisory 锁键必须独立于 materialized_view_refresher 的 mvRefreshLockKey**：
// 本周期 5min 与 routing 周期 30min、session 周期 60min **互为整数倍**，
// 且三者都对齐墙钟 ⇒ 每个 30min/60min 边界必然撞在同一把锁上。
// 2026-10-12 已实测过这个坑：session 那一轮整轮 skipped_no_lock，
// MV 数据陈旧 2 小时（见 materialized_view_refresher.go 的
// SessionViewLockRetryBudget 注释）。⇒ 这里另开一把锁键
// （tuningViewAdvisoryLockKey）+ 另一个 Redis 逻辑名。
//
// Failure handling: stale-while-error — the previous view content
// stays valid for the next 5 min even if a refresh fails.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/admin/distlock"
)

const (
	// TuningViewRefreshInterval 是两个 tuning 物化视图的刷新周期。
	// 视图本身是 5 分钟桶（tuning_signals_5m）与 1 天桶（daily），
	// 周期与桶宽对齐 ⇒ 不能为了省负载把周期拉长（那样最新桶会陈旧）。
	// 省负载的正确做法是**跨实例只刷一次**（见文件头）。
	TuningViewRefreshInterval = 5 * time.Minute

	// tuningViewDistLockNamespace/Logical 构成 Redis 令牌桶的键。
	//
	// ⚠ logical 必须与 materialized_view_refresh /
	// materialized_view_refresh_session **都不同**：三个周期节奏不同
	// （5min / 30min / 60min），共用一个 token 会互相抢票
	// （session 周期是 30min 周期的整数倍，边界必然相撞）。
	tuningViewDistLockNamespace = "bg"
	tuningViewDistLockLogical   = "tuning_view_refresh"

	// tuningViewDistLockTTL 是 leader 持有令牌的最长时长。
	//
	// ⚠ 必须 **小于** TuningViewRefreshInterval（5min）：否则上一窗口的
	// 令牌会跨窗口存活，把下一窗口的刷新也顶掉 ⇒ 变成"隔一轮才刷一次"，
	// 视图陈旧度翻倍且从指标上看不出来。
	//
	// 取 90s 的理由：两台实例的抢票是 Redis SETNX 原子操作，毫秒级完成；
	// 90s 远大于抢票窗口，又远小于 5min 窗口。distlock 会在 ttl/3 处
	// 自动续租，所以存活进程不会丢租约；ttl 实际是"崩溃恢复上界"。
	tuningViewDistLockTTL = 90 * time.Second

	// tuningViewAdvisoryLockKey 是本 worker 自己的 advisory 锁键。
	// ⚠ 必须与 mvRefreshLockKey 独立，见文件头的整数倍撞锁说明。
	tuningViewAdvisoryLockKey int64 = 915_2026_10_20
)

// TuningViews 是本 worker 负责的两个物化视图。
//
// ⚠ 这是**白名单**：REFRESH 语句必须把视图名拼进 SQL（PG 不支持参数化
// 视图名），白名单把注入面关掉。见 isTuningView。
var TuningViews = []string{"tuning_signals_5m", "tuning_signals_daily"}

// TuningViewRefresher periodically calls REFRESH MATERIALIZED VIEW
// CONCURRENTLY on the two pre-aggregated views.
type TuningViewRefresher struct {
	pool     *pgxpool.Pool
	tick     time.Duration
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once

	// 2026-07-27 concurrency fix: the previous "select on done with a
	// default" probe could not distinguish "never started" from "still
	// running" — it returned immediately while the goroutine was still
	// mid-refresh. An explicit started flag makes the distinction exact.
	started atomic.Bool

	// distLock 是可选的 Redis 令牌桶管理器（跨实例 leader 选举）。
	// nil 或 Enabled()==false ⇒ 退到 Postgres advisory lock。
	// 必须在 Start() 之前设置：run() 的 goroutine 会无锁读它。
	distLock distlock.Manager

	// refreshViewFn 是 refreshView 的可替换实现（仅测试用）。
	// nil ⇒ 走真实实现。生产代码不设置它。
	//
	// 为什么要这个缝：follower 跳过这条腿是本轮的核心判据，
	// 而"follower 不碰数据库"这件事**必须**用一个能观测"数据库被碰过
	// 了吗"的量具来判。真实 refreshView 在 pool==nil 时会安静返回，
	// 于是"协调坏了"和"协调正常"读数完全一样 ⇒ 恒绿门。
	// （与 materialized_view_refresher 的 retryOnLockHeld(attempt func())
	// 同一思路：把策略参数化，判据就能在无数据库的条件下驱动它。）
	//
	// 指标**不在** refreshView 里记，而在上层的 refreshCoordinated 记：
	// 这样"哪个视图被刷了、刷了多久"是**周期层**的事实，
	// 判据可以用这个缝驱动它，而不必真的连数据库。
	refreshViewFn func(ctx context.Context, view string) (tuningRefreshOutcome, time.Duration, error)
}

// tuningRefreshOutcome 是一次单视图刷新的结果分类。
type tuningRefreshOutcome uint8

const (
	// tuningRefreshed：视图真的被刷新了。
	tuningRefreshed tuningRefreshOutcome = iota
	// tuningSkippedMissingView：视图不存在（迁移未应用）⇒ 跳过。
	tuningSkippedMissingView
	// tuningSkippedLockHeld：advisory 锁被别的实例拿着 ⇒ 跳过。
	tuningSkippedLockHeld
)

// NewTuningViewRefresher constructs the worker. Default tick = 5 min.
func NewTuningViewRefresher(pool *pgxpool.Pool) *TuningViewRefresher {
	return &TuningViewRefresher{
		pool: pool,
		tick: TuningViewRefreshInterval,
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
}

// SetDistLock wires the Redis-backed distributed lock manager used for
// cross-instance leader election. Optional: when never called, or called
// with a manager whose Enabled() is false, the refresher transparently
// falls back to the Postgres advisory lock.
// Call before Start(): the field is read by the refresh loop goroutine
// without a lock, so a post-Start call is a data race.
func (r *TuningViewRefresher) SetDistLock(mgr distlock.Manager) {
	r.distLock = mgr
}

// Start spawns the background goroutine. Returns immediately.
func (r *TuningViewRefresher) Start(ctx context.Context) {
	if !r.started.CompareAndSwap(false, true) {
		// Already started; a second run() would double-close r.done.
		return
	}
	Go("tuning_view_refresher.run", func() { r.run(ctx) })
	slog.Info("tuning view refresher started", "interval", r.tick.String())
}

// Stop terminates the goroutine and waits for it to finish.
// Safe to call on a never-Started refresher (no-op), and safe
// to call multiple times (idempotent via sync.Once).
func (r *TuningViewRefresher) Stop() {
	if r.stop == nil || r.done == nil {
		return
	}
	r.stopOnce.Do(func() {
		close(r.stop)
	})
	// Wait for the goroutine, but only if it ever started.
	// A noop Stop (never Started) returns immediately.
	if !r.started.Load() {
		return
	}
	<-r.done
}

// RefreshOnce triggers an immediate refresh.
//
// Since 2026-10-20 it goes through the **cross-instance coordination**
// path: when another instance holds this window's token, this call is a
// no-op (the shared database is already being refreshed by the leader —
// all instances point at the same DB, so "who refreshes" only affects
// database load, not correctness).
func (r *TuningViewRefresher) RefreshOnce(ctx context.Context) error {
	if r == nil || r.pool == nil {
		return nil
	}
	return r.refreshCoordinated(ctx)
}

// nextCycleWait returns how long to wait until the next wall-clock
// multiple of the refresh interval.
//
// 2026-10-20：改用 nextAlignedWait（Unix 纪元对齐），**不再**用
// time.NewTicker 的"进程启动相位"。原因见文件头：令牌桶/advisory 锁
// 只在"同一瞬间"才收得住 2× 放大；相位随机时两台实例各刷各的。
//
// 抽成方法是为了让"是否墙钟对齐"可被单测判定（对齐 ⇒ 落点秒数是
// 5min 的整数倍；用普通 ticker ⇒ 落点是"上次唤醒 + 5min"，与墙钟无关）。
func (r *TuningViewRefresher) nextCycleWait(now time.Time) time.Duration {
	return nextAlignedWait(now, r.tick)
}

func (r *TuningViewRefresher) run(ctx context.Context) {
	defer close(r.done)
	// Refresh once on startup so a fresh deploy has data immediately
	// (otherwise the first /tuning/accuracy call would return 0 rows
	// for 5 minutes).
	if err := r.RefreshOnce(ctx); err != nil {
		slog.Warn("tuning view initial refresh failed", "error", err)
	}

	timer := time.NewTimer(r.nextCycleWait(time.Now()))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stop:
			return
		case <-timer.C:
			if err := r.RefreshOnce(ctx); err != nil {
				slog.Warn("tuning view periodic refresh failed", "error", err)
			}
			timer.Reset(r.nextCycleWait(time.Now()))
		}
	}
}

// refreshCoordinated runs one coordination-aware refresh cycle.
//
// 顺序（与 materialized_view_refresher.refreshAll 同形）：
//  1. Redis 令牌桶抢票 —— 抢到的继续，抢不到的**跳过**（记 skipped_follower）。
//  2. 逐视图取 Postgres advisory 锁 —— 取不到的跳过（记 skipped_no_lock）。
//     这层是 Redis 不可用时的兜底；两实例在墙钟同一瞬间醒来时它也能收住。
//  3. 执行 REFRESH ... CONCURRENTLY —— 成功记 success，失败记 failed。
func (r *TuningViewRefresher) refreshCoordinated(ctx context.Context) error {
	if r == nil {
		return nil
	}

	if handle := r.acquireDistLock(ctx, tuningViewDistLockLogical); handle != nil {
		defer handle.Release(context.WithoutCancel(ctx))
		if !handle.IsLeader() {
			slog.Info("tuning view refresh skipped, redis token held by another instance")
			for _, v := range TuningViews {
				recordMVRefreshSkipped(v, "skipped_follower")
			}
			return nil
		}
		slog.Info("tuning view refresh: redis leader token acquired")
	}

	fn := r.refreshViewFn
	if fn == nil {
		fn = r.refreshView
	}

	var firstErr error
	for _, view := range TuningViews {
		outcome, elapsed, err := fn(ctx, view)
		switch {
		case err != nil:
			recordMVRefreshFailure(view)
			slog.Error("failed to refresh tuning materialized view",
				"view", view, "error", err, "elapsed", elapsed)
			if firstErr == nil {
				firstErr = err
			}
		case outcome == tuningRefreshed:
			recordMVRefreshSuccess(view, elapsed.Seconds())
			slog.Info("tuning view refreshed", "view", view, "elapsed", elapsed)
		case outcome == tuningSkippedMissingView:
			recordMVRefreshSkipped(view, "skipped_missing_view")
		case outcome == tuningSkippedLockHeld:
			recordMVRefreshSkipped(view, "skipped_no_lock")
		}
	}
	return firstErr
}

// acquireDistLock attempts the Redis token-bucket leader election for one
// refresh cycle. Returns nil whenever Redis coordination was not usable —
// no manager wired, the manager reports Enabled()==false, or Acquire itself
// errored (e.g. Redis outage) — so the caller falls back to the Postgres
// advisory lock. Returns a non-nil handle (leader OR follower) whenever the
// Redis round-trip succeeded; the caller must Release() it either way and
// only proceeds with the refresh when handle.IsLeader() is true.
func (r *TuningViewRefresher) acquireDistLock(ctx context.Context, logical string) *distlock.Handle {
	if r == nil || r.distLock == nil || !r.distLock.Enabled() {
		return nil
	}
	h, err := r.distLock.Acquire(ctx, distlock.AcquireOpts{
		Key:   distlock.BuildKey(tuningViewDistLockNamespace, logical),
		TTL:   tuningViewDistLockTTL,
		Mode:  distlock.ModeWaitFollower,
		Scope: "tuning_refresh",
	})
	if err != nil {
		if !errors.Is(err, distlock.ErrNotEnabled) {
			slog.Warn("tuning view refresh: redis lock acquire failed, falling back to postgres advisory lock",
				"error", err)
		}
		return nil
	}
	return h
}

// refreshView refreshes a single materialized view using CONCURRENTLY.
// A missing view (e.g. the migration has not been applied on this
// database) is a skip, not an error.
//
// The returned outcome reports whether the view was actually refreshed, so
// the caller can distinguish it from a skip in metrics and logs.
//
// ⚠ statement_timeout：252 生产 llm_gateway 角色级上限 30s。本视图实测
// max 8.6s（mean 126ms）⇒ 余量 3.5×，本轮**不抬**该上限（不引入连接级
// SET/RESET 的额外风险）。若将来实测尾部越过 30s，应按
// materialized_view_refresher 的 mvRefreshStatementTimeout 同样做法抬一次，
// 并把实测数字补进本注释。
func (r *TuningViewRefresher) refreshView(ctx context.Context, view string) (tuningRefreshOutcome, time.Duration, error) {
	// 白名单校验必须在拼 SQL 之前：PG 不支持参数化视图名。
	if !isTuningView(view) {
		return tuningSkippedMissingView, 0,
			fmt.Errorf("refusing to refresh non-whitelisted materialized view %q", view)
	}
	if r.pool == nil {
		return tuningSkippedMissingView, 0, nil
	}

	// 视图存在性检查：未应用迁移的库上应当跳过而不是刷错误日志。
	var exists bool
	if err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_matviews
			WHERE schemaname = 'public'
			  AND matviewname = $1
		)
	`, view).Scan(&exists); err != nil {
		return tuningSkippedMissingView, 0, err
	}
	if !exists {
		slog.Warn("tuning materialized view does not exist, skipping refresh",
			"view", view)
		return tuningSkippedMissingView, 0, nil
	}

	// Pin one connection for the whole lock/refresh/unlock sequence: the
	// advisory lock is session-scoped and pool.Exec may hop connections.
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return tuningSkippedMissingView, 0, err
	}
	defer conn.Release()

	var locked bool
	if err := conn.QueryRow(ctx,
		`SELECT pg_try_advisory_lock($1)`, tuningViewAdvisoryLockKey).Scan(&locked); err != nil {
		return tuningSkippedMissingView, 0, err
	}
	if !locked {
		slog.Info("tuning view refresh skipped, another instance holds the lock",
			"view", view)
		return tuningSkippedLockHeld, 0, nil
	}
	defer func() {
		// Unlock even when ctx is done, or the lock sticks to the pooled
		// connection until it is recycled.
		_, _ = conn.Exec(context.WithoutCancel(ctx),
			`SELECT pg_advisory_unlock($1)`, tuningViewAdvisoryLockKey)
	}()

	start := time.Now()
	if _, err := conn.Exec(ctx, "REFRESH MATERIALIZED VIEW CONCURRENTLY "+view); err != nil {
		return tuningSkippedMissingView, time.Since(start), err
	}
	return tuningRefreshed, time.Since(start), nil
}

// isTuningView reports whether view belongs to this worker's whitelist.
func isTuningView(view string) bool {
	for _, v := range TuningViews {
		if v == view {
			return true
		}
	}
	return false
}
