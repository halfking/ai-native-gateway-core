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
//     only trust the views when refreshed_at is within 15 minutes, so one
//     missed cycle is invisible while a dead refresher degrades callers
//     back to the base-view queries.
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
)

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
// Postgres advisory lock. Safe to call before or after Start().
func (r *MaterializedViewRefresher) SetDistLock(mgr distlock.Manager) {
	r.distLock = mgr
}

// Start begins the refresh loop in the background.
func (r *MaterializedViewRefresher) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel

	go r.refreshLoop(ctx)
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
// Safe to call before or after Start().
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

	ticker := time.NewTicker(RefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.refreshAll(ctx)
		}
	}
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
	// When Redis is nil/disabled/unreachable, useAdvisoryLock stays true
	// and refreshView falls back to its per-view Postgres advisory lock
	// exactly as before this change.
	useAdvisoryLock := true
	if handle := r.acquireDistLock(ctx); handle != nil {
		defer handle.Release(context.WithoutCancel(ctx))
		if !handle.IsLeader() {
			slog.Info("materialized view refresh skipped, redis token held by another instance")
			return
		}
		useAdvisoryLock = false
		slog.Info("materialized view refresh: redis leader token acquired")
	}

	var hasError bool
	var lastErr error
	var failedView string

	start := time.Now()
	if err := r.refreshView(ctx, "routing_analytics_7d", useAdvisoryLock); err != nil {
		hasError = true
		lastErr = err
		failedView = "routing_analytics_7d"
		slog.Error("failed to refresh routing_analytics_7d",
			"error", err,
			"elapsed", time.Since(start))
	} else {
		slog.Info("refreshed routing_analytics_7d",
			"elapsed", time.Since(start))
		r.checkConsistency(ctx, MVDriftViewRoutingAnalytics7d)
	}

	auditStart := time.Now()
	if err := r.refreshView(ctx, "routing_audit_summary_7d", useAdvisoryLock); err != nil {
		hasError = true
		lastErr = err
		failedView = "routing_audit_summary_7d"
		slog.Error("failed to refresh routing_audit_summary_7d",
			"error", err,
			"elapsed", time.Since(auditStart))
	} else {
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
func (r *MaterializedViewRefresher) acquireDistLock(ctx context.Context) *distlock.Handle {
	if r.distLock == nil || !r.distLock.Enabled() {
		return nil
	}
	h, err := r.distLock.Acquire(ctx, distlock.AcquireOpts{
		Key:   distlock.BuildKey(mvRefreshDistLockNamespace, mvRefreshDistLockLogical),
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
func (r *MaterializedViewRefresher) checkConsistency(ctx context.Context, viewName string) {
	if r == nil || r.db == nil {
		return
	}
	result, err := CheckMVConsistency(ctx, r.db, viewName)
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
// useAdvisoryLock controls the Postgres pg_try_advisory_lock guard
// (2026-09-01): the caller sets it to false when refreshAll already holds
// the Redis leader token for this cycle, since a second lock layer would
// only add latency. It stays true whenever Redis coordination is
// unavailable, preserving the original single-database dedup behaviour.
func (r *MaterializedViewRefresher) refreshView(ctx context.Context, viewName string, useAdvisoryLock bool) error {
	var exists bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_matviews
			WHERE schemaname = 'public'
			  AND matviewname = $1
		)
	`, viewName).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		slog.Warn("materialized view does not exist, skipping refresh",
			"view", viewName)
		return nil
	}

	if !useAdvisoryLock {
		_, err = r.db.Exec(ctx, "REFRESH MATERIALIZED VIEW CONCURRENTLY "+viewName)
		return err
	}

	// Pin one connection for the whole lock/refresh/unlock sequence:
	// advisory locks are session-scoped, and pool.Exec may hop connections.
	conn, err := r.db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	var locked bool
	if err := conn.QueryRow(ctx,
		`SELECT pg_try_advisory_lock($1)`, mvRefreshLockKey).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		slog.Info("materialized view refresh skipped, another instance holds the lock",
			"view", viewName)
		return nil
	}
	defer func() {
		// Unlock even when ctx is done, or the lock sticks to the pooled
		// connection until it is recycled.
		_, _ = conn.Exec(context.WithoutCancel(ctx),
			`SELECT pg_advisory_unlock($1)`, mvRefreshLockKey)
	}()

	_, err = conn.Exec(ctx, "REFRESH MATERIALIZED VIEW CONCURRENTLY "+viewName)
	return err
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
