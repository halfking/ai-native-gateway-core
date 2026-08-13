package bg

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/autoroute"
)

// WorkTypeRouteStoreRefresher periodically reloads work-type route
// preferences so admin changes take effect on the V2 path within one minute.
type WorkTypeRouteStoreRefresher struct {
	store    *autoroute.WorkTypeRouteStore
	tick     time.Duration
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// NewWorkTypeRouteStoreRefresher constructs a one-minute refresher.
func NewWorkTypeRouteStoreRefresher(store *autoroute.WorkTypeRouteStore) *WorkTypeRouteStoreRefresher {
	return &WorkTypeRouteStoreRefresher{
		store: store,
		tick:  time.Minute,
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
}

// Start spawns the background worker and immediately reloads the store.
func (r *WorkTypeRouteStoreRefresher) Start(ctx context.Context) {
	go r.run(ctx)
	slog.Info("work type route store refresher started", "interval", r.tick.String())
}

// Stop terminates the worker. It is safe to call more than once.
func (r *WorkTypeRouteStoreRefresher) Stop() {
	if r.stop == nil || r.done == nil {
		return
	}
	r.stopOnce.Do(func() { close(r.stop) })
	select {
	case <-r.done:
	default:
	}
}

// RefreshOnce immediately reloads the store.
func (r *WorkTypeRouteStoreRefresher) RefreshOnce(ctx context.Context) error {
	if r.store == nil {
		return nil
	}
	return r.store.Reload(ctx)
}

func (r *WorkTypeRouteStoreRefresher) run(ctx context.Context) {
	defer close(r.done)
	if err := r.RefreshOnce(ctx); err != nil {
		slog.Warn("work type route store initial reload failed", "error", err)
	}

	ticker := time.NewTicker(r.tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stop:
			return
		case <-ticker.C:
			if err := r.RefreshOnce(ctx); err != nil {
				slog.Warn("work type route store periodic reload failed", "error", err)
			}
		}
	}
}
