package bg

// default_routing_refresher.go — M2: 周期性 Reload autoroute.DefaultRoutingStore。
//
// 与 override_store_refresher.go 同构：1 分钟 cadence，stale-on-error 容忍，
// sync.Once 幂等 Stop。运营通过 admin API 改 task_default_routing 后，热路径
// 在 1 分钟内生效。详见 docs/拆分/22-Auto智能路由与任务识别.md §22.6。

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/autoroute"
)

// reloadableStore 是 OverrideStore / DefaultRoutingStore 共有的最小接口。
// 用 interface 而非具体类型，便于复用。
type reloadableStore interface {
	Reload(context.Context) error
}

// DefaultRoutingStoreRefresher periodically calls DefaultRoutingStore.Reload.
type DefaultRoutingStoreRefresher struct {
	pool     *pgxpool.Pool
	store    *autoroute.DefaultRoutingStore
	tick     time.Duration
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// NewDefaultRoutingStoreRefresher constructs the worker. Default tick = 1 min.
func NewDefaultRoutingStoreRefresher(pool *pgxpool.Pool, store *autoroute.DefaultRoutingStore) *DefaultRoutingStoreRefresher {
	return &DefaultRoutingStoreRefresher{
		pool:  pool,
		store: store,
		tick:  1 * time.Minute,
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
}

// Start spawns the background goroutine. Performs an initial Reload.
func (r *DefaultRoutingStoreRefresher) Start(ctx context.Context) {
	go r.run(ctx)
	slog.Info("default routing store refresher started", "interval", r.tick.String())
}

// Stop terminates the goroutine and waits. No-op if never started.
func (r *DefaultRoutingStoreRefresher) Stop() {
	if r.stop == nil || r.done == nil {
		return
	}
	r.stopOnce.Do(func() { close(r.stop) })
	select {
	case <-r.done:
	default:
	}
}

// RefreshOnce triggers an immediate reload (admin use / tests).
func (r *DefaultRoutingStoreRefresher) RefreshOnce(ctx context.Context) error {
	if r.store == nil {
		return nil
	}
	return r.store.Reload(ctx)
}

func (r *DefaultRoutingStoreRefresher) run(ctx context.Context) {
	defer close(r.done)
	if err := r.RefreshOnce(ctx); err != nil {
		slog.Warn("default routing store initial reload failed", "error", err)
	}
	t := time.NewTicker(r.tick)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			if err := r.store.Reload(ctx); err != nil {
				slog.Warn("default routing store reload failed", "error", err)
			}
		}
	}
}
