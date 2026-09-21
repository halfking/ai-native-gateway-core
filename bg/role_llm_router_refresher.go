package bg

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/autoroute"
)

// RoleLLMRouterRefresher periodically reloads the R48 role × kind LLM
// preference router so admin edits to role_task_llm_mapping take effect
// within one minute（与 WorkTypeRouteStoreRefresher 同款生命周期模式）。
type RoleLLMRouterRefresher struct {
	router    *autoroute.RoleLLMRouter
	tick      time.Duration
	stop      chan struct{}
	done      chan struct{}
	stopOnce  sync.Once
	started   bool
	stopped   bool
	lifecycle sync.Mutex
	cancel    context.CancelFunc
}

// NewRoleLLMRouterRefresher constructs a one-minute refresher.
func NewRoleLLMRouterRefresher(router *autoroute.RoleLLMRouter) *RoleLLMRouterRefresher {
	return &RoleLLMRouterRefresher{
		router: router,
		tick:   time.Minute,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
}

// Start spawns the background worker and immediately reloads the router.
func (r *RoleLLMRouterRefresher) Start(ctx context.Context) {
	if r == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.lifecycle.Lock()
	if r.started || r.stopped {
		r.lifecycle.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.started = true
	r.lifecycle.Unlock()

	go r.run(runCtx)
	slog.Info("role llm router refresher started", "interval", r.tick.String())
}

// Stop terminates the worker and waits for it to finish. Safe before Start
// and safe to call more than once.
func (r *RoleLLMRouterRefresher) Stop() {
	if r == nil {
		return
	}
	r.lifecycle.Lock()
	if r.stopped {
		started := r.started
		r.lifecycle.Unlock()
		if started {
			<-r.done
		}
		return
	}
	r.stopped = true
	cancel := r.cancel
	started := r.started
	r.lifecycle.Unlock()

	r.stopOnce.Do(func() { close(r.stop) })
	if cancel != nil {
		cancel()
	}
	if started {
		<-r.done
	}
}

func (r *RoleLLMRouterRefresher) run(ctx context.Context) {
	defer close(r.done)
	if ctx.Err() != nil {
		return
	}
	r.reloadRecovered(ctx, "initial")
	ticker := time.NewTicker(r.tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stop:
			return
		case <-ticker.C:
			r.reloadRecovered(ctx, "periodic")
		}
	}
}

// reloadRecovered 守护单次 Reload（R50 审计 P3：goroutine 原先无任何
// recover，单次 panic 即整进程崩溃）——panic 记日志后 tick 循环继续。
func (r *RoleLLMRouterRefresher) reloadRecovered(ctx context.Context, stage string) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("role llm router refresher reload panic", "stage", stage, "recover", rec)
		}
	}()
	if err := r.router.Reload(ctx); err != nil {
		slog.Warn("role llm router "+stage+" reload failed", "error", err)
	}
}
