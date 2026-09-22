// Package bg — base_worker.go
//
// 2026-08-31: 引入 BaseWorker 嵌入结构，统一封装后台 worker 的 lifecycle 字段
// （started/stopped/cancel/done/wg）的并发安全访问。审计报告 (2026-08-31-24h-correction-followup
// §模式 C) 指出 VacuumWorker/ProviderErrorAggregator/StorageRetentionWorker 都重复实现了
// 这套样板且容易在加锁粒度上写错。BaseWorker 把正确做法固化下来：
//
//   - Start 多次调用幂等（仅第一次启动 goroutine）
//   - Stop 多次调用幂等；nil receiver 安全
//   - Start/Stop 并发调用不会拿到 stale cancel/done
//   - done 由监督循环在最终退出时恰好在 BaseWorker 侧 close 一次
//
// 2026-09-22 (R53, R51-F13 遗留): Start 的 goroutine 升级为监督循环——
// runFn panic 不再让 worker 静默停摆到进程重启，而是 recover + 指数退避
// 后在同一 ctx 上重启（自愈），每次重启计数进 Restarts() 与
// llm_gateway_bg_worker_restarts_total。runFn 正常 return 视为有意退出，
// 不重启。done 的 close 收敛到监督循环退出点；runFn 里的 NotifyStopped
// 保留为兼容 no-op（见其文档），不再是 Stop 解阻塞的依赖。
//
// 用法（嵌入）：
//
//	type MyWorker struct {
//		*bg.BaseWorker
//		interval time.Duration
//	}
//
//	func NewMyWorker(...) *MyWorker {
//		return &MyWorker{BaseWorker: bg.NewBaseWorker("my-worker")}
//	}
//
//	func (w *MyWorker) Start(ctx context.Context) {
//		w.BaseWorker.Start(ctx, w.run)
//	}
//
//	func (w *MyWorker) Stop() { w.BaseWorker.Stop() }
//
//	func (w *MyWorker) run(ctx context.Context) {
//		t := time.NewTicker(w.interval)
//		defer t.Stop()
//		for { select { case <-ctx.Done(): return; case <-t.C: ... } }
//	}
package bg

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"
)

// 自愈重启退避参数。包级变量便于测试收缩；生产默认 1s 起步、60s 封顶。
var (
	workerRestartBackoff    = time.Second
	workerRestartMaxBackoff = time.Minute
)

// BaseWorker 统一封装后台 goroutine 的 lifecycle 字段。
//
// Embedding 用户负责：调用 Start(ctx, runFn) / Stop()。BaseWorker 通过
// sync.Mutex 保证 started/stopped/cancel/done 的原子可见性，避免跨
// goroutine 看到 stale 字段。
type BaseWorker struct {
	name string

	mu         sync.Mutex
	cancel     context.CancelFunc
	done       chan struct{}
	started    bool
	stopped    bool
	doneClosed bool
	restarts   int
}

// NewBaseWorker 构造一个命名 worker（name 仅用于日志/指标，便于定位）。
func NewBaseWorker(name string) *BaseWorker {
	return &BaseWorker{name: name}
}

// Start 启动 runFn 的监督 goroutine。如果已启动或已停止，返回 false；否则复制
// cancel/done 后释放锁再启动（避免持锁启动）。ctx 为 nil 时退化为
// context.Background()。
func (b *BaseWorker) Start(parent context.Context, runFn func(context.Context)) bool {
	if b == nil || runFn == nil {
		return false
	}
	if parent == nil {
		parent = context.Background()
	}
	b.mu.Lock()
	if b.started || b.stopped {
		b.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(parent)
	b.cancel = cancel
	b.done = make(chan struct{})
	b.started = true
	b.mu.Unlock()
	go b.supervise(ctx, runFn)
	return true
}

// supervise 在自愈循环中运行 runFn：panic 被 recover 并在指数退避后于同一
// ctx 上重启；runFn 正常 return 视为有意退出、监督循环随之终止。done 恰在
// 本循环退出时 close 一次（R51-F13：单次 panic 不得让 worker 静默停摆）。
func (b *BaseWorker) supervise(ctx context.Context, runFn func(context.Context)) {
	defer b.closeDone()
	for {
		if !b.runOnce(ctx, runFn) {
			return
		}
		b.mu.Lock()
		b.restarts++
		n := b.restarts
		b.mu.Unlock()
		metricWorkerRestarts.WithLabelValues(b.name).Inc()
		// 1s, 2s, 4s, ... 封顶；<< 溢出为负时同样取封顶值。
		delay := workerRestartBackoff << (n - 1)
		if delay <= 0 || delay > workerRestartMaxBackoff {
			delay = workerRestartMaxBackoff
		}
		slog.Warn("bg worker restarting after panic",
			"worker", b.name, "restart", n, "backoff", delay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// runOnce 运行 runFn 一次，报告它是否以 panic 告终（正常返回/ctx 退出均为 false）。
func (b *BaseWorker) runOnce(ctx context.Context, runFn func(context.Context)) (panicked bool) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("bg worker panicked", "worker", b.name, "panic", rec, "stack", string(debug.Stack()))
			panicked = true
		}
	}()
	runFn(ctx)
	return false
}

// Stop 取消并等待监督 goroutine 退出。多次调用、未启动、nil receiver 都安全。
// 已停止时直接返回（done 只会 close 一次）。
func (b *BaseWorker) Stop() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if !b.started || b.stopped {
		b.mu.Unlock()
		return
	}
	cancel := b.cancel
	done := b.done
	b.stopped = true
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// NotifyStopped 是历史契约的兼容入口：R53 起 done 的 close 收敛到监督循环
// 退出点，runFn 退出（含 panic）不再需要（也不会）通知 Stop。保留为 no-op
// 是为了让既有 runFn 的 `defer BaseWorker.NotifyStopped()` 继续编译并通过
// 语义审查；新代码不需要调用它。
func (b *BaseWorker) NotifyStopped() {
	_ = b // 兼容 no-op；nil receiver 安全
}

// closeDone 由监督循环 defer 调用，保证 done 恰好 close 一次。
func (b *BaseWorker) closeDone() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done != nil && !b.doneClosed {
		b.doneClosed = true
		close(b.done)
	}
}

// Started 返回是否已成功启动（不区分是否已停止）。仅用于日志/metrics。
func (b *BaseWorker) Started() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.started
}

// Stopped 返回是否已调用 Stop。仅用于日志/metrics。
func (b *BaseWorker) Stopped() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stopped
}

// Restarts 返回自愈重启次数（runFn panic 后的重启计数）。仅用于日志/metrics。
func (b *BaseWorker) Restarts() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.restarts
}

// Name 返回构造时传入的 worker 名，便于 slog/指标引用。
func (b *BaseWorker) Name() string {
	if b == nil {
		return ""
	}
	return b.name
}
