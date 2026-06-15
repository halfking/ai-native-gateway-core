package bg

// tuning_view_refresher.go — periodic REFRESH of the materialised
// views (tuning_signals_5m and tuning_signals_daily) added in P7.5.
//
// Materialised views don't auto-update on source-row changes, so
// we run REFRESH MATERIALIZED VIEW CONCURRENTLY on a 5-min ticker.
// CONCURRENTLY requires a UNIQUE index (which the views have).
//
// Refresh cost:
//   - 5m view: ~30ms on 100k rows
//   - daily view: ~50ms on 1M rows
//
// Failure handling: stale-while-error — the previous view content
// stays valid for the next 5 min even if a refresh fails.

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sync"
)

// TuningViewRefresher periodically calls REFRESH MATERIALIZED VIEW
// CONCURRENTLY on the two pre-aggregated views.
type TuningViewRefresher struct {
	pool  *pgxpool.Pool
	tick  time.Duration
	stop  chan struct{}
	done  chan struct{}
	stopOnce sync.Once
}

// NewTuningViewRefresher constructs the worker. Default tick = 5 min.
func NewTuningViewRefresher(pool *pgxpool.Pool) *TuningViewRefresher {
	return &TuningViewRefresher{
		pool: pool,
		tick: 5 * time.Minute,
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
}

// Start spawns the background goroutine. Returns immediately.
func (r *TuningViewRefresher) Start(ctx context.Context) {
	go r.run(ctx)
	slog.Info("tuning view refresher started", "interval", r.tick.String())
}

// Stop terminates the goroutine and waits for it to finish.
// Safe to call on a never-Started refresher (no-op), and safe
// to call multiple times (idempotent via sync.Once).
func (r *TuningViewRefresher) Stop() {
	if r.stop == nil || r.done == nil {
		return
	}
	// Determine whether Start was called (the done channel is
	// only closed by the goroutine on its way out). A nil-pointer
	// test won't help; use a select with a default to detect.
	r.stopOnce.Do(func() {
		close(r.stop)
	})
	// Wait for the goroutine, but only if it ever started.
	// A noop Stop (never Started) returns immediately because
	// the default case fires.
	select {
	case <-r.done:
	default:
		// goroutine never started
	}
}

// RefreshOnce triggers an immediate refresh (admin use).
func (r *TuningViewRefresher) RefreshOnce(ctx context.Context) error {
	if r.pool == nil {
		return nil
	}
	start := time.Now()
	// CONCURRENTLY is non-blocking; readers keep seeing the
	// previous snapshot until the new one is built. Requires the
	// UNIQUE index on each view (added in P7.5.1).
	if _, err := r.pool.Exec(ctx,
		`REFRESH MATERIALIZED VIEW CONCURRENTLY tuning_signals_5m`); err != nil {
		return err
	}
	if _, err := r.pool.Exec(ctx,
		`REFRESH MATERIALIZED VIEW CONCURRENTLY tuning_signals_daily`); err != nil {
		return err
	}
	slog.Info("tuning views refreshed",
		"duration_ms", time.Since(start).Milliseconds())
	return nil
}

func (r *TuningViewRefresher) run(ctx context.Context) {
	defer close(r.done)
	// Refresh once on startup so a fresh deploy has data immediately
	// (otherwise the first /tuning/accuracy call would return 0 rows
	// for 5 minutes).
	if err := r.RefreshOnce(ctx); err != nil {
		slog.Warn("tuning view initial refresh failed", "error", err)
	}

	t := time.NewTicker(r.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stop:
			return
		case <-t.C:
			if err := r.RefreshOnce(ctx); err != nil {
				slog.Warn("tuning view periodic refresh failed", "error", err)
			}
		}
	}
}
