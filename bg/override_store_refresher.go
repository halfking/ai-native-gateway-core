package bg

// override_store_refresher.go — P7.7: periodic reload of the
// OverrideStore so admin-approved overrides take effect within
// ~1 minute of the POST (faster than the 5-min cadence we use
// for tuning_signals views because override changes are
// operational, not analytical).
//
// Pattern follows bg/tuning_store_refresher.go (same sync.Once
// idempotent Stop, same stale-while-error tolerance).
//
// Cadence: 1 minute (faster than tuning signals 5 min because
// overrides are operational levers — operators expect near-real-time
// effect after POST).
//
// Failure handling: the snapshot from the last successful reload
// is retained on transient DB errors. The Decider keeps using the
// stale override set until a refresh succeeds.

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/autoroute"
)

// OverrideStoreRefresher periodically calls OverrideStore.Reload.
type OverrideStoreRefresher struct {
	pool     *pgxpool.Pool
	store    *autoroute.OverrideStore
	tick     time.Duration
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// NewOverrideStoreRefresher constructs the worker.
// Default tick = 1 minute (overrides are operational).
func NewOverrideStoreRefresher(pool *pgxpool.Pool, store *autoroute.OverrideStore) *OverrideStoreRefresher {
	return &OverrideStoreRefresher{
		pool:  pool,
		store: store,
		tick:  1 * time.Minute,
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
}

// Start spawns the background goroutine. Returns immediately.
// Performs an initial Reload so a fresh deploy has data
// immediately.
func (r *OverrideStoreRefresher) Start(ctx context.Context) {
	go r.run(ctx)
	slog.Info("override store refresher started", "interval", r.tick.String())
}

// Stop terminates the goroutine and waits for it to finish.
// Safe to call on a never-Started refresher (no-op).
func (r *OverrideStoreRefresher) Stop() {
	if r.stop == nil || r.done == nil {
		return
	}
	r.stopOnce.Do(func() {
		close(r.stop)
	})
	select {
	case <-r.done:
	default:
		// goroutine never started
	}
}

// RefreshOnce triggers an immediate reload (admin use).
func (r *OverrideStoreRefresher) RefreshOnce(ctx context.Context) error {
	if r.store == nil {
		return nil
	}
	start := time.Now()
	if err := r.store.Reload(ctx); err != nil {
		return err
	}
	slog.Debug("override store refreshed",
		"duration_ms", time.Since(start).Milliseconds(),
		"loaded_at", r.store.LoadedAt(),
	)
	return nil
}

func (r *OverrideStoreRefresher) run(ctx context.Context) {
	defer close(r.done)

	// Initial reload so a fresh deploy has data immediately
	// (operators don't want to wait 1 min for their first ban to
	// take effect after a restart).
	if err := r.RefreshOnce(ctx); err != nil {
		slog.Warn("override store initial reload failed", "error", err)
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
				slog.Warn("override store periodic reload failed", "error", err)
			}
		}
	}
}
