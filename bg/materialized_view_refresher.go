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
//   - A session-level advisory lock guards each refresh: canary and prod
//     gateway instances share one database, and stacked REFRESHes would
//     serialize on the view lock and waste cycles. The instance that
//     fails pg_try_advisory_lock simply skips that cycle.
//   - Freshness contract with admin/analytics_materialized.go: consumers
//     only trust the views when refreshed_at is within 15 minutes, so one
//     missed cycle is invisible while a dead refresher degrades callers
//     back to the base-view queries.
package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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
)

// MaterializedViewRefresher manages periodic refresh of routing analytics
// materialized views.
type MaterializedViewRefresher struct {
	db     *pgxpool.Pool
	cancel context.CancelFunc
	done   chan struct{}
}

// NewMaterializedViewRefresher creates a new refresher instance.
func NewMaterializedViewRefresher(db *pgxpool.Pool) *MaterializedViewRefresher {
	return &MaterializedViewRefresher{
		db:   db,
		done: make(chan struct{}),
	}
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
	ctx, cancel := context.WithTimeout(parentCtx, RefreshTimeout)
	defer cancel()

	start := time.Now()
	if err := r.refreshView(ctx, "routing_analytics_7d"); err != nil {
		slog.Error("failed to refresh routing_analytics_7d",
			"error", err,
			"elapsed", time.Since(start))
	} else {
		slog.Info("refreshed routing_analytics_7d",
			"elapsed", time.Since(start))
	}

	auditStart := time.Now()
	if err := r.refreshView(ctx, "routing_audit_summary_7d"); err != nil {
		slog.Error("failed to refresh routing_audit_summary_7d",
			"error", err,
			"elapsed", time.Since(auditStart))
	} else {
		slog.Info("refreshed routing_audit_summary_7d",
			"elapsed", time.Since(auditStart))
	}
}

// refreshView refreshes a single materialized view using CONCURRENTLY
// under a cross-instance advisory lock. A missing view (e.g. migration
// 632 not applied on this database) is a skip, not an error — callers
// fall back to base-view queries anyway.
func (r *MaterializedViewRefresher) refreshView(ctx context.Context, viewName string) error {
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
