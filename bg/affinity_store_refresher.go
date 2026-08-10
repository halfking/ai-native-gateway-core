package bg

// affinity_store_refresher.go — periodic reload worker for AffinityStore.
//
// Mirrors TuningStoreRefresher: AffinityStore is read on the hot path via an
// atomic pointer, so its snapshot only needs refreshing periodically. Cadence
// aligns with the affinity worker (15 min learning) — no point reloading more
// often than new rankings are produced.
//
// Stale-while-error: a failed Reload keeps the previous snapshot. The hot path
// is never blocked.

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/autoroute"
)

// AffinityStoreRefresher periodically reloads the AffinityStore snapshot.
type AffinityStoreRefresher struct {
	store *autoroute.AffinityStore
	tick  time.Duration
	stop  chan struct{}
	done  chan struct{}

	stopOnce sync.Once
	started  atomic.Bool
}

// NewAffinityStoreRefresher constructs the worker.
func NewAffinityStoreRefresher(store *autoroute.AffinityStore, interval time.Duration) *AffinityStoreRefresher {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &AffinityStoreRefresher{
		store: store,
		tick:  interval,
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
}

// Start spawns the background goroutine. Returns immediately.
func (r *AffinityStoreRefresher) Start(ctx context.Context) {
	if !r.started.CompareAndSwap(false, true) {
		return
	}
	go r.run(ctx)
	slog.Info("affinity store refresher started", "interval", r.tick.String())
}

// Stop terminates the goroutine and waits. Safe on never-Started and twice.
func (r *AffinityStoreRefresher) Stop() {
	r.stopOnce.Do(func() {
		close(r.stop)
	})
	if !r.started.Load() {
		return
	}
	<-r.done
}

func (r *AffinityStoreRefresher) run(ctx context.Context) {
	defer close(r.done)
	t := time.NewTicker(r.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stop:
			return
		case <-t.C:
			if err := r.store.Reload(ctx); err != nil {
				slog.Warn("affinity store periodic reload failed", "error", err)
			}
		}
	}
}
