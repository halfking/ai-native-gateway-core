package bg

// tuning_store_refresher.go — periodic reload worker for the TuningStore.
//
// TuningStore is read on the hot path via atomic.Pointer.Load(), so its
// snapshot only needs to be refreshed periodically. We align the cadence
// with auto_index_refresher (5 min) so any admin-approved proposal takes
// effect within 5 min without a restart.
//
// Failure handling: stale-while-error (the previous snapshot is kept on
// transient DB errors). The hot path is never blocked by a failed reload.

import (
	"context"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/autoroute"
)

// TuningStoreRefresher periodically calls TuningStore.Reload() to pick
// up admin-approved tuning_proposals writes.
type TuningStoreRefresher struct {
	store *autoroute.TuningStore
	tick  time.Duration
	stop  chan struct{}
	done  chan struct{}
}

// NewTuningStoreRefresher constructs the worker. Default tick = 5 min.
func NewTuningStoreRefresher(store *autoroute.TuningStore, _ interface{}) *TuningStoreRefresher {
	return &TuningStoreRefresher{
		store: store,
		tick:  5 * time.Minute,
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
}

// Start spawns the background goroutine. Returns immediately.
func (r *TuningStoreRefresher) Start(ctx context.Context) {
	go r.run(ctx)
	slog.Info("tuning store refresher started", "interval", r.tick.String())
}

// Stop terminates the goroutine and waits for it to finish.
func (r *TuningStoreRefresher) Stop() {
	close(r.stop)
	<-r.done
}

func (r *TuningStoreRefresher) run(ctx context.Context) {
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
				slog.Warn("tuning_store periodic reload failed", "error", err)
			}
		}
	}
}
