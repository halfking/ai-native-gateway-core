// Package bg — materialized_view_refresher.go
//
// MaterializedViewRefresher periodically refreshes the routing analytics
// materialized views (migration 632) so the admin analytics endpoints can
// serve pre-aggregated data instead of Seq-Scanning 314K+ request_logs
// rows per request.
//
// Refresh strategy:
//   - routing_analytics_7d / routing_audit_summary_7d:
//     REFRESH MATERIALIZED VIEW CONCURRENTLY every RefreshInterval.
//   - Ticks are aligned to wall-clock boundaries (UTC multiples of
//     RefreshInterval), not to each process's start time. The token and
//     advisory locks below are mutual-exclusion WITHIN an overlapping
//     window only — they cannot dedup ticks that arrive minutes apart.
//     With phase-random tickers, every gateway instance on the shared
//     database refreshed on its own phase and the locks never fired
//     (252 production 2026-09-28: three instances → three fixed tick
//     phases → 3× REFRESH amplification, ~90s of matview maintenance per
//     10min). Aligned ticks make concurrent instances contend at the same
//     instant, which is exactly the case the locks DO collapse — one
//     refresh per window fleet-wide. Clock skew beyond one refresh cycle
//     degrades to the old behavior (correct, just duplicated).
//   - Cross-instance coordination is a token-bucket: every RefreshInterval
//     tick is one "token", and exactly one gateway instance should redeem
//     it. Two lock backends implement that mutual exclusion:
//     1. Redis (preferred) — admin/distlock SETNX-with-TTL leader
//     election (2026-09-01). Works across any number of instances
//     without touching Postgres, and self-heals if a leader dies
//     mid-refresh (TTL expiry, no manual unlock needed).
//     2. Postgres advisory lock (fallback) — used verbatim when the
//     distlock manager is nil/disabled (dev/local without Redis, or
//     Redis outage). Session-scoped pg_try_advisory_lock; canary and
//     prod instances share one database, and stacked REFRESHes would
//     otherwise serialize on the view lock and waste cycles.
//     The instance that fails to acquire either lock simply skips that
//     cycle — never blocks, never queues.
//   - Freshness contract with admin/analytics_materialized.go: consumers
//     only trust the views when their routing_mv_refresh_state row is within
//     15 minutes, so one missed cycle is invisible while a dead refresher
//     degrades callers back to the base-view queries. The timestamp is a
//     side-table row written by execRefresh, not a column of the view —
//     see execRefresh for why it cannot live in the target list (837).
package bg

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/admin/distlock"
	dbpkg "github.com/kaixuan/llm-gateway-go/db"
)

const (
	// RefreshInterval is how often we refresh the materialized views.
	// 10 minutes balances freshness against database load; consumers
	// tolerate up to 15 minutes of staleness (admin.mvFreshnessBudget).
	RefreshInterval = 10 * time.Minute

	// RefreshTimeout bounds a single refresh cycle. Must stay below
	// RefreshInterval so cycles cannot pile up.
	RefreshTimeout = 5 * time.Minute

	// InitialDelay lets startup migrations (which create and populate the
	// views) settle before the first refresh.
	InitialDelay = 30 * time.Second

	// mvRefreshLockKey is the advisory lock guarding REFRESH cycles across
	// gateway instances sharing one database. Arbitrary constant; only
	// uniqueness matters.
	mvRefreshLockKey int64 = 632_2026_08_31

	// mvRefreshDistLockNamespace/Logical build the Redis key backing the
	// token-bucket leader election (2026-09-01), via distlock.BuildKey so
	// it lands in one Redis Cluster hash slot regardless of deployment.
	mvRefreshDistLockNamespace = "bg"
	mvRefreshDistLockLogical   = "materialized_view_refresh"

	// mvRefreshDistLockTTL bounds how long a Redis-elected leader holds the
	// refresh token before the lease auto-expires. Must exceed RefreshTimeout so a
	// slow-but-alive refresh never loses its lease mid-cycle; distlock
	// auto-renews at ttl/3 while the process is alive, so this is really
	// just the crash-recovery bound (dead leader → lock free within TTL).
	mvRefreshDistLockTTL = 6 * time.Minute

	// mvDriftAlertCooldown prevents a persistent drift from sending an alert on
	// every ten-minute refresh cycle. Metrics remain updated on every check.
	mvDriftAlertCooldown = 30 * time.Minute

	// mvRefreshStatementTimeout lifts the connection-default statement_timeout
	// for the REFRESH statement only (2026-09-21, 252 PG 日志审计轮). The
	// llm_gateway role carries `statement_timeout=30s` (252 生产角色级配置，
	// 兜底所有应用语句)，而 routing_analytics_7d 的 REFRESH CONCURRENTLY
	// 单次物化 ~17M 行、均值 6.2s 但 ~50% 周期在 29.9s 被角色级上限击杀
	// （252 生产日志 6.3h 窗口：38 次超时 / 35 次慢完成）——陈旧度契约
	// （15min）被打破，admin 端点退化回基础视图重查询。客户端 ctx 上限
	// RefreshTimeout=5min 本就允许更久，这里把该连接的语句上限抬到
	// 180s（< 5min ctx，且 < RefreshInterval 的 2 倍，不会堆积周期），
	// 用后立刻 RESET，不污染连接池归还后的其他语句。
	mvRefreshStatementTimeout = "180s"

	// ── 357 会话分析物化视图族（2026-10-07）─────────────────────────────
	// 迁移 357 建了 session_client_stats / session_task_stats /
	// session_client_task_matrix 三个物化视图，admin session-analytics 端点
	// 直接读它们。**此前没有任何刷新排程**：迁移末尾的初始刷新跑过一次之后
	// 就再没人管（245 实测：2026-10-07 06:31:36 填的，16:07 时端点仍在返回
	// 这份数据，陈旧 9h37m，而 session_summaries 仍在持续写入）。
	//
	// 间隔按实测定，不是拍的（245，session_summaries 623,006 行 / 919MB）：
	//   session_client_stats          聚合  8.3s
	//   session_task_stats            聚合  4.8s
	//   session_client_task_matrix    聚合 10.9s
	// 三个合计 ≈24s；REFRESH CONCURRENTLY 还要另建堆并算唯一索引差，
	// 整轮量级 40-70s。源表增速约 200k 行/天，小时级绰绰有余。
	SessionViewsInterval = 60 * time.Minute

	// SessionViewsTimeout 绑定一轮 session 刷新。它必须**明显大于**实测的
	// 40-70s：被 ctx 掐断的表现不是报错，而是**后几个视图根本没刷新**，
	// 且前面已刷的会留下半轮状态 —— 比整体失败更难发现。
	SessionViewsTimeout = 10 * time.Minute

	// sessionViewsInitialDelay 比 InitialDelay 长：startup 迁移正在创建并
	// 首次填充这些视图，等它们落定再进第一轮，否则第一轮必然撞上
	// skipped_missing_view。
	sessionViewsInitialDelay = 2 * time.Minute

	// sessionViewsDistLockLogical 是独立的 Redis 键后缀。与 10min 的
	// routing 周期共用一个 token 会让两个节奏不同的周期互相抢票。
	sessionViewsDistLockLogical = "materialized_view_refresh_session"
)

// SessionAnalyticsViews 是迁移 357 建立、admin session-analytics 端点读取的
// 物化视图族。三个都带 UNIQUE 索引（idx_session_client_stats_tenant_client /
// idx_session_task_stats_tenant_task / idx_session_client_task_matrix_uq），
// 这是 REFRESH MATERIALIZED VIEW **CONCURRENTLY** 的硬前提 —— 缺了会直接报错，
// 而不是退化成普通 REFRESH。refreshView 对不存在的视图会安全跳过
// （skipped_missing_view），所以 357 未应用的库不会因此报错。
var SessionAnalyticsViews = []string{
	"session_client_stats",
	"session_task_stats",
	"session_client_task_matrix",
}

// MaterializedViewRefresher manages periodic refresh of routing analytics
// materialized views.
type MaterializedViewRefresher struct {
	db     *pgxpool.Pool
	cancel context.CancelFunc
	done   chan struct{}
	// Failure tracking for alerting (2026-09-01 P1-B)
	failureCount       int
	lastFailureTime    time.Time
	alertCallback      func(viewName string, consecutiveFailures int, err error)
	driftAlertCallback func(viewName string, breaches int, maxPct float64, maxAbs int64, summary string)
	lastDriftAlertTime time.Time

	// refreshMu serializes refresh cycles (2026-09-01 P2 race fix). The
	// periodic loop and TriggerRefresh (admin tools) can run concurrently;
	// without this mutex failureCount/lastFailureTime were written from two
	// goroutines — a data race that -race flags and that could also interleave
	// two full REFRESH cycles. Redis token-bucket leader election and the
	// Postgres advisory-lock fallback semantics are unchanged: the mutex only
	// dedups refreshAll entries within THIS process; cross-instance mutual
	// exclusion still rests on the distributed locks below.
	refreshMu sync.Mutex

	// distLock is the optional Redis-backed leader election manager
	// (2026-09-01). Nil (or Enabled()==false) makes refreshView fall back
	// to the Postgres advisory lock unconditionally.
	distLock distlock.Manager
}

// NewMaterializedViewRefresher creates a new refresher instance.
func NewMaterializedViewRefresher(db *pgxpool.Pool) *MaterializedViewRefresher {
	return &MaterializedViewRefresher{
		db:   db,
		done: make(chan struct{}),
	}
}

// SetDistLock wires the Redis-backed distributed lock manager used for
// cross-instance leader election (2026-09-01 — token-bucket refresh
// coordination). Optional: when never called, or called with a manager
// whose Enabled() is false, refreshView transparently falls back to the
// Postgres advisory lock. Call before Start(): the field is read by the
// refresh loop goroutine without a lock, so a post-Start call is a data
// race (current production wiring in main.go only calls it before Start).
func (r *MaterializedViewRefresher) SetDistLock(mgr distlock.Manager) {
	r.distLock = mgr
}

// Start begins the refresh loop in the background.
func (r *MaterializedViewRefresher) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel

	Go("materialized_view_refresher.refreshLoop", func() { r.refreshLoop(ctx) })
	slog.Info("materialized_view_refresher started",
		"interval", RefreshInterval.String(),
		"timeout", RefreshTimeout.String())
}

// Stop gracefully shuts down the refresh loop. It returns promptly even
// during the initial delay — the loop's waits are ctx-aware.
func (r *MaterializedViewRefresher) Stop() {
	if r.cancel != nil {
		r.cancel()
	}
	<-r.done
	slog.Info("materialized_view_refresher stopped")
}

// SetAlertCallback sets the callback function for refresh failure alerts.
// The callback is invoked when consecutive failures reach 2 or more.
// Call before Start(): alertCallback is read by the refresh loop goroutine
// without a lock, so a post-Start call is a data race.
func (r *MaterializedViewRefresher) SetAlertCallback(cb func(viewName string, consecutiveFailures int, err error)) {
	r.alertCallback = cb
}

// SetDriftAlertCallback wires the optional callback used when a consistency
// check finds sustained materialized-view drift. It is kept separate from the
// refresh-failure callback so operators can distinguish a successful refresh
// that produced stale data from a refresh that failed outright.
func (r *MaterializedViewRefresher) SetDriftAlertCallback(cb func(viewName string, breaches int, maxPct float64, maxAbs int64, summary string)) {
	r.driftAlertCallback = cb
}

// refreshLoop runs the periodic refresh cycle.
func (r *MaterializedViewRefresher) refreshLoop(ctx context.Context) {
	defer close(r.done)

	// Initial refresh after startup migrations settle; interruptible so
	// Stop() during deploy shutdown does not hang here.
	select {
	case <-ctx.Done():
		return
	case <-time.After(InitialDelay):
	}
	r.refreshAll(ctx)

	// 2026-09-28 252 PG 日志审计轮（R12-F1）：对齐墙钟边界，不用
	// time.NewTicker 的"进程启动相位"。根因实证：252 三台网关各持一个
	// 相位随机的 10min ticker，而 Redis token / advisory lock 都只是
	// "重叠窗口内互斥"——只去重并发刷新，不去重错峰 tick。生产日志
	// 50min 窗口 15 次 routing_analytics_7d REFRESH 呈三个固定相位
	// （:15/:25/:35…、:19/:29…、:23/:33…）= 三实例各刷各的，每轮
	// REFRESH 15-22s + 漂移核查，~90s/10min 持续烧在重复物化上。
	// 对齐后所有实例在同一边界瞬间竞争，既有互斥随之收敛为每窗口
	// 恰好一次 REFRESH。时钟偏移超过单轮刷新时长时退化为旧行为
	//（正确性无损，仅重复）。每轮重算等待时间，刷新耗时不会累积漂移。
	timer := time.NewTimer(nextAlignedWait(time.Now(), RefreshInterval))
	defer timer.Stop()

	// 2026-10-07：357 会话分析视图族接入。**不新起 goroutine**，而是在同一个
	// 循环里挂第二个定时器 —— Stop() 靠 refreshLoop 里的 close(r.done) 收口，
	// 多一个 goroutine 就得多一套等待语义，而这里并不需要并发。
	//
	// 刻意用**另一个定时器**而不是把三个视图塞进 refreshAll：两个周期的节奏
	// 与超时预算完全不同（10min/5min vs 60min/10min），共用一个 ctx 预算会让
	// routing 那一轮被 session 的耗时吃掉预算。陈旧度契约也是分开的
	// （15min vs sessionMvFreshnessBudget）。
	select {
	case <-ctx.Done():
		return
	case <-time.After(sessionViewsInitialDelay - InitialDelay):
	}
	r.refreshSessionViews(ctx)

	sessionTimer := time.NewTimer(nextAlignedWait(time.Now(), SessionViewsInterval))
	defer sessionTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			r.refreshAll(ctx)
			timer.Reset(nextAlignedWait(time.Now(), RefreshInterval))
		case <-sessionTimer.C:
			r.refreshSessionViews(ctx)
			sessionTimer.Reset(nextAlignedWait(time.Now(), SessionViewsInterval))
		}
	}
}

// nextAlignedWait returns how long to sleep until the next wall-clock
// multiple of interval, anchored to the Unix epoch in UTC so every gateway
// instance — regardless of local timezone or process start time — wakes on
// the same boundary (CST=UTC+8 is a whole multiple of 10min, so the
// boundary set is identical in local time). Landing exactly on a boundary
// yields a full interval, never a zero-length wait.
func nextAlignedWait(now time.Time, interval time.Duration) time.Duration {
	phase := time.Duration(now.UnixNano()) % interval
	if phase < 0 {
		phase += interval
	}
	return interval - phase
}

// refreshAll refreshes all materialized views.
func (r *MaterializedViewRefresher) refreshAll(parentCtx context.Context) {
	// 2026-09-01 P2 race fix: TriggerRefresh (admin) and the periodic loop can
	// enter refreshAll concurrently; serialize the whole cycle so the failure
	// counters below have a single writer and cycles never interleave.
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()

	ctx, cancel := context.WithTimeout(parentCtx, RefreshTimeout)
	defer cancel()

	// 2026-09-01: token-bucket cross-instance coordination for the whole
	// cycle (both views share one token — no reason to elect a leader
	// twice per tick). Redis is preferred: it works for any instance
	// count and self-heals on crash via TTL expiry with no manual unlock.
	// When Redis is nil/disabled/unreachable, the Postgres advisory lock
	// below runs exactly as before this change.
	//
	// 2026-09-23 252 SQL 日志审计轮（FIX-4）：advisory lock 升级为全局互斥
	// 后端，Redis leader 不再绕过它（原 useAdvisoryLock=false 删除）。
	// 根因实证：Redis 选举与 advisory 兜底互不可见——无 Redis 的实例
	// （252-dev 形态）走 advisory，Redis leader（154/245）裸奔 REFRESH，
	// 同一视图双发叠跑，252-dev 每 10min tick 饿死在 180s pin 上
	// （击杀时间轴 05:33/05:43/05:53/06:03 = tick 起点精确 +180s，watcher
	// 红手抓捕 252-dev 刷新 98s 仍在跑）。现在 advisory lock 是唯一真理：
	// 任一后端赢了选举，另一个实例 pg_try_advisory_lock 失败即跳过。
	useAdvisoryLock := true
	if handle := r.acquireDistLock(ctx, mvRefreshDistLockLogical); handle != nil {
		defer handle.Release(context.WithoutCancel(ctx))
		if !handle.IsLeader() {
			slog.Info("materialized view refresh skipped, redis token held by another instance")
			// 十七轮审计接线：coordination 指标族此前是零调用死代码，
			// R12-F1（墙钟对齐）的"每窗口收敛单刷新"回验只能靠 slog 肉眼
			// 对账。follower 跳过时两视图各记一次 skipped_follower。
			recordMVCoordination("redis_follower")
			recordMVRefreshSkipped("routing_analytics_7d", "skipped_follower")
			recordMVRefreshSkipped("routing_audit_summary_7d", "skipped_follower")
			return
		}
		slog.Info("materialized view refresh: redis leader token acquired")
		recordMVCoordination("redis_leader")
	} else {
		recordMVCoordination("advisory_only")
	}

	var hasError bool
	var lastErr error
	var failedView string

	start := time.Now()
	if skipped, err := r.refreshView(ctx, "routing_analytics_7d", useAdvisoryLock); err != nil {
		hasError = true
		lastErr = err
		failedView = "routing_analytics_7d"
		recordMVRefreshFailure("routing_analytics_7d")
		slog.Error("failed to refresh routing_analytics_7d",
			"error", err,
			"elapsed", time.Since(start))
	} else if !skipped {
		recordMVRefreshSuccess("routing_analytics_7d", time.Since(start).Seconds())
		slog.Info("refreshed routing_analytics_7d",
			"elapsed", time.Since(start))
		r.checkConsistency(ctx, MVDriftViewRoutingAnalytics7d)
	}

	auditStart := time.Now()
	if skipped, err := r.refreshView(ctx, "routing_audit_summary_7d", useAdvisoryLock); err != nil {
		hasError = true
		lastErr = err
		failedView = "routing_audit_summary_7d"
		recordMVRefreshFailure("routing_audit_summary_7d")
		slog.Error("failed to refresh routing_audit_summary_7d",
			"error", err,
			"elapsed", time.Since(auditStart))
	} else if !skipped {
		recordMVRefreshSuccess("routing_audit_summary_7d", time.Since(auditStart).Seconds())
		slog.Info("refreshed routing_audit_summary_7d",
			"elapsed", time.Since(auditStart))
		r.checkConsistency(ctx, MVDriftViewRoutingAuditSummary7d)
	}

	// 2026-09-01 P1-B: Track consecutive failures and alert on threshold
	if hasError {
		r.failureCount++
		r.lastFailureTime = time.Now()

		// Alert on 2+ consecutive failures (交接文档建议)
		if r.failureCount >= 2 && r.alertCallback != nil {
			r.alertCallback(failedView, r.failureCount, lastErr)
		}
	} else {
		// Reset on success
		r.failureCount = 0
	}
}

// refreshSessionViews 刷新迁移 357 的会话分析物化视图族。
//
// 与 refreshAll 共用 refreshMu（本进程内串行），但走**独立的 Redis token 键**
// —— 两个节奏不同的周期共用一个 token 会互相抢票。
//
// 关于 Postgres advisory lock：refreshView 内部用的是全局的 mvRefreshLockKey，
// 这里沿用而没有另开一把。代价是两个周期撞上时其中一轮整体跳过；收益是不必给
// refreshView 再串一个锁键参数。session 轮约 60s、每 60min 一次，与 10min 的
// routing 轮相撞的概率约 1.7%，而 routing 的陈旧度预算（15min > 10min 间隔）
// 本就允许漏一轮。这个取舍是安全的：跳过是可见的（skipped_follower 指标 +
// Info 日志），而悄悄漏刷不是。
//
// 单视图失败不终止整轮：三个视图的用途彼此独立，第三个失败不该让前两个的
// 成果作废。失败计入各自视图的 failure 指标。
func (r *MaterializedViewRefresher) refreshSessionViews(parentCtx context.Context) {
	if r == nil || r.db == nil {
		return
	}
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()

	ctx, cancel := context.WithTimeout(parentCtx, SessionViewsTimeout)
	defer cancel()

	if handle := r.acquireDistLock(ctx, sessionViewsDistLockLogical); handle != nil {
		defer handle.Release(context.WithoutCancel(ctx))
		if !handle.IsLeader() {
			slog.Info("session analytics view refresh skipped, redis token held by another instance")
			recordMVCoordination("redis_follower")
			for _, v := range SessionAnalyticsViews {
				recordMVRefreshSkipped(v, "skipped_follower")
			}
			return
		}
		slog.Info("session analytics view refresh: redis leader token acquired")
		recordMVCoordination("redis_leader")
	} else {
		recordMVCoordination("advisory_only")
	}

	var failed int
	for _, view := range SessionAnalyticsViews {
		start := time.Now()
		skipped, err := r.refreshView(ctx, view, true)
		switch {
		case err != nil:
			// 不 return：三个视图用途独立，单个失败不该作废已刷好的。
			failed++
			recordMVRefreshFailure(view)
			slog.Error("failed to refresh session analytics view",
				"view", view, "error", err, "elapsed", time.Since(start))
		case !skipped:
			recordMVRefreshSuccess(view, time.Since(start).Seconds())
			slog.Info("refreshed session analytics view",
				"view", view, "elapsed", time.Since(start))
		}
	}

	// 整轮被 ctx 掐断是最难发现的一种失败：已刷的视图盖上了新时间戳，
	// 没刷的保持旧值，没有一条错误日志（refreshView 的错误由调用方 ctx 传播，
	// 这里必须自己判）。落到周期级指标上。
	if ctx.Err() != nil {
		slog.Error("session analytics refresh cycle cut off by deadline",
			"timeout", SessionViewsTimeout.String(),
			"views", len(SessionAnalyticsViews), "failed_before_cutoff", failed)
		recordMVRefreshFailure("session_analytics_cycle")
	}
}

// acquireDistLock attempts the Redis token-bucket leader election for one
// refresh cycle. Returns nil whenever Redis coordination was not usable —
// no manager wired, the manager reports Enabled()==false, or Acquire itself
// errored (e.g. Redis outage) — so the caller falls back to the Postgres
// advisory lock. Returns a non-nil handle (leader OR follower) whenever the
// Redis round-trip succeeded; the caller must Release() it either way and
// only proceeds with the refresh when handle.IsLeader() is true.
//
// Acquire is non-blocking for followers here: distlock's follower path
// only performs a quick pubsub subscribe handshake (bounded by
// handshakeTimeout, ~3s) and returns immediately — it does NOT wait for
// the leader to finish. That "return fast, let the caller decide" shape is
// exactly the token-bucket semantics: a follower redeems no token and
// skips the cycle instead of queueing behind the leader.
func (r *MaterializedViewRefresher) acquireDistLock(ctx context.Context, logical string) *distlock.Handle {
	if r.distLock == nil || !r.distLock.Enabled() {
		return nil
	}
	h, err := r.distLock.Acquire(ctx, distlock.AcquireOpts{
		Key:   distlock.BuildKey(mvRefreshDistLockNamespace, logical),
		TTL:   mvRefreshDistLockTTL,
		Mode:  distlock.ModeWaitFollower,
		Scope: "mv_refresh",
	})
	if err != nil {
		if !errors.Is(err, distlock.ErrNotEnabled) {
			slog.Warn("materialized view refresh: redis lock acquire failed, falling back to postgres advisory lock",
				"error", err)
		}
		return nil
	}
	return h
}

// checkConsistency records drift metrics after a successful refresh and emits
// a bounded operator alert when the same process observes material drift.
//
// 2026-09-24 252 SQL 日志审计轮：漂移核查重扫 7 天 routing_analytics_source，
// 在 252 当前负载下 med 21s / max 29.4s，被角色级 statement_timeout=30s
// 击杀（45min 窗口 ×14：mv_data effective_task_type ×8 + audit_summary ×6，
// 每次刷新后必跟一次）。核查跑在与刷新同等的会话级预算上（mvRefresh-
// StatementTimeout, 180s）：核查对 180s 仍然超时属于真异常，走既有的
// query_failed 指标路径暴露；被 30s rolconfig 误杀则只会留下"检查坏了"的
// 假信号并浪费 30s×N 的数据库时间。
func (r *MaterializedViewRefresher) checkConsistency(ctx context.Context, viewName string) {
	if r == nil || r.db == nil {
		return
	}
	// Pin one connection: the session-level statement_timeout must survive
	// both statements of the check (same pattern as refreshView).
	conn, err := r.db.Acquire(ctx)
	if err != nil {
		RecordMVConsistencyError(viewName, "query_failed")
		slog.Warn("materialized view consistency check: acquire conn failed", "view", viewName, "error", err)
		return
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx,
		"SET statement_timeout = '"+mvRefreshStatementTimeout+"'"); err != nil {
		RecordMVConsistencyError(viewName, "query_failed")
		slog.Warn("materialized view consistency check: timeout pin failed", "view", viewName, "error", err)
		return
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "RESET statement_timeout")
	}()
	result, err := CheckMVConsistency(ctx, conn, viewName)
	if err != nil {
		RecordMVConsistencyError(viewName, "query_failed")
		slog.Warn("materialized view consistency check failed", "view", viewName, "error", err)
		return
	}
	if !result.ViewExists {
		return
	}
	RecordMVConsistency(viewName, result)
	// The operator alert is intentionally stricter than the metric: one noisy
	// bucket should remain visible in Prometheus without paging the channel.
	if result.BreachCount < 3 || r.driftAlertCallback == nil || result.MaxAbs < 1000 {
		return
	}
	now := time.Now()
	if !r.lastDriftAlertTime.IsZero() && now.Sub(r.lastDriftAlertTime) < mvDriftAlertCooldown {
		return
	}
	r.lastDriftAlertTime = now
	r.driftAlertCallback(viewName, result.BreachCount, result.MaxPct, result.MaxAbs,
		fmt.Sprintf("视图 %s 检测到 %d 个漂移桶，最大百分比 %.2f%%，最大绝对差 %d", viewName, result.BreachCount, result.MaxPct, result.MaxAbs))
}

// refreshView refreshes a single materialized view using CONCURRENTLY.
// A missing view (e.g. migration 632 not applied on this database) is a
// skip, not an error — callers fall back to base-view queries anyway.
//
// The boolean return reports "skipped" (missing view / lock held by
// another instance) so the caller can distinguish it from success in
// metrics — 十七轮审计接线，skip 语义此前与 success 在观测上不可分。
//
// useAdvisoryLock controls the Postgres pg_try_advisory_lock guard
// (2026-09-01): the caller sets it to false when refreshAll already holds
// the Redis leader token for this cycle, since a second lock layer would
// only add latency. It stays true whenever Redis coordination is
// unavailable, preserving the original single-database dedup behaviour.
func (r *MaterializedViewRefresher) refreshView(ctx context.Context, viewName string, useAdvisoryLock bool) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_matviews
			WHERE schemaname = 'public'
			  AND matviewname = $1
		)
	`, viewName).Scan(&exists)
	if err != nil {
		return false, err
	}
	if !exists {
		recordMVRefreshSkipped(viewName, "skipped_missing_view")
		slog.Warn("materialized view does not exist, skipping refresh",
			"view", viewName)
		return true, nil
	}

	// Pin one connection for the whole timeout/refresh(/lock/unlock) sequence:
	// the session-level statement_timeout below and advisory locks are both
	// session-scoped, and pool.Exec may hop connections.
	conn, err := r.db.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Release()

	// Lift the role-level statement_timeout (252 生产 llm_gateway 角色=30s)
	// for this session only; see mvRefreshStatementTimeout. Reset via
	// WithoutCancel so a canceled/failed refresh still returns a clean
	// connection to the pool.
	if _, err := conn.Exec(ctx,
		"SET statement_timeout = '"+mvRefreshStatementTimeout+"'"); err != nil {
		return false, err
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "RESET statement_timeout")
	}()

	if !useAdvisoryLock {
		return false, r.execRefresh(ctx, conn, viewName)
	}

	var locked bool
	if err := conn.QueryRow(ctx,
		`SELECT pg_try_advisory_lock($1)`, mvRefreshLockKey).Scan(&locked); err != nil {
		return false, err
	}
	if !locked {
		recordMVRefreshSkipped(viewName, "skipped_no_lock")
		slog.Info("materialized view refresh skipped, another instance holds the lock",
			"view", viewName)
		return true, nil
	}
	defer func() {
		// Unlock even when ctx is done, or the lock sticks to the pooled
		// connection until it is recycled.
		_, _ = conn.Exec(context.WithoutCancel(ctx),
			`SELECT pg_advisory_unlock($1)`, mvRefreshLockKey)
	}()

	return false, r.execRefresh(ctx, conn, viewName)
}

// execRefresh runs one REFRESH ... CONCURRENTLY and, only on success, records
// the refresh time in routing_mv_refresh_state.
//
// Migration 837 moved that timestamp out of the materialized view's target
// list. The column used to be NOW() inside the view, which meant REFRESH had
// no way to tell which rows actually changed and rewrote 100% of them
// (measured 252 prod: 100.7% of rows per cycle, only 0.339% really different).
// The stamp now has to be written by whoever ran the refresh — that is the
// whole reason it lives in a table instead of the view.
//
// A failed stamp is returned as an error rather than swallowed: the view
// itself is fine, but every consumer's freshness gate (15-minute budget in
// admin/analytics_materialized.go) would silently read "stale" forever and
// fall back to the base-view queries that this refresh exists to avoid.
// That degradation is worth paging on.
//
// The statement runs on the caller's already-pinned connection, so the
// lifted statement_timeout still applies (see mvRefreshStatementTimeout).
func (r *MaterializedViewRefresher) execRefresh(ctx context.Context, conn *pgxpool.Conn, viewName string) error {
	if _, err := conn.Exec(ctx, "REFRESH MATERIALIZED VIEW CONCURRENTLY "+viewName); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, dbpkg.StampRoutingMVRefreshSQL, viewName); err != nil {
		return fmt.Errorf("refreshed %s but failed to record refresh time: %w", viewName, err)
	}
	return nil
}

// TriggerRefresh manually triggers an immediate refresh cycle (admin tools,
// tests). Blocking; returns after the cycle completes. A nil-db refresher
// is a no-op so tests can exercise wiring without a database.
func (r *MaterializedViewRefresher) TriggerRefresh(ctx context.Context) error {
	if r == nil || r.db == nil {
		return nil
	}
	slog.Info("manual refresh triggered")
	r.refreshAll(ctx)
	return nil
}
