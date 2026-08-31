// Package bg — materialized_view_refresher.go
//
// MaterializedViewRefresher periodically refreshes routing analytics
// materialized views to keep them up-to-date for fast query performance.
//
// Refresh strategy:
//   - routing_analytics_7d: REFRESH MATERIALIZED VIEW CONCURRENTLY every 10 minutes
//   - routing_audit_summary_7d: REFRESH MATERIALIZED VIEW CONCURRENTLY every 10 minutes
//
// CONCURRENTLY refresh allows queries to continue during refresh (requires unique index).
//
// Performance characteristics:
//   - Refresh time: ~5-10s for 314K+ base rows → ~1K-10K aggregated rows
//   - Query latency: 15s (base view) → <500ms (materialized view)
//   - Staleness: up to 10 minutes (acceptable for analytics)
//
// Related: migration 632, admin/analytics.go, admin/analytics_materialized.go
package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// RefreshInterval is how often we refresh the materialized views.
	// 10 minutes provides a good balance between freshness and database load.
	RefreshInterval = 10 * time.Minute

	// RefreshTimeout is the maximum time allowed for a single refresh operation.
	// Should be less than RefreshInterval to avoid overlapping refreshes.
	RefreshTimeout = 5 * time.Minute
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

// Stop gracefully shuts down the refresh loop.
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

	ticker := time.NewTicker(RefreshInterval)
	defer ticker.Stop()

	// Perform initial refresh on startup (with a short delay to let migrations complete)
	time.Sleep(30 * time.Second)
	r.refreshAll(ctx)

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
	
	// Refresh routing_analytics_7d
	if err := r.refreshView(ctx, "routing_analytics_7d"); err != nil {
		slog.Error("failed to refresh routing_analytics_7d",
			"error", err,
			"elapsed", time.Since(start))
	} else {
		slog.Info("refreshed routing_analytics_7d",
			"elapsed", time.Since(start))
	}

	// Refresh routing_audit_summary_7d
	auditStart := time.Now()
	if err := r.refreshView(ctx, "routing_audit_summary_7d"); err != nil {
		slog.Error("failed to refresh routing_audit_summary_7d",
			"error", err,
			"elapsed", time.Since(auditStart))
	} else {
		slog.Info("refreshed routing_audit_summary_7d",
			"elapsed", time.Since(auditStart))
	}

	slog.Info("materialized view refresh cycle completed",
		"total_elapsed", time.Since(start))
}

// refreshView refreshes a single materialized view using CONCURRENTLY.
func (r *MaterializedViewRefresher) refreshView(ctx context.Context, viewName string) error {
	// Check if view exists first
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

	// REFRESH MATERIALIZED VIEW CONCURRENTLY allows queries during refresh
	// (requires unique index, which migration 632 created)
	_, err = r.db.Exec(ctx, "REFRESH MATERIALIZED VIEW CONCURRENTLY "+viewName)
	return err
}

// TriggerRefresh manually triggers an immediate refresh (useful for testing or admin tools).
// This is a blocking operation that returns after refresh completes.
func (r *MaterializedViewRefresher) TriggerRefresh(ctx context.Context) error {
	slog.Info("manual refresh triggered")
	r.refreshAll(ctx)
	return nil
}
