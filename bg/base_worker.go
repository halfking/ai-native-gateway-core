// Package bg — base_worker.go
//
// 2026-08-31: 引入 BaseWorker 嵌入结构，统一封装后台 worker 的 lifecycle 字段
//（started/stopped/cancel/done/wg）的并发安全访问。审计报告 (2026-08-31-24h-correction-followup
// §模式 C) 指出 VacuumWorker/ProviderErrorAggregator/StorageRetentionWorker 都重复实现了
// 这套样板且容易在加锁粒度上写错。BaseWorker 把正确做法固化下来：
//
//   - Start 多次调用幂等（仅第一次启动 goroutine）
//   - Stop 多次调用幂等；nil receiver 安全
//   - Start/Stop 并发调用不会拿到 stale cancel/done
//   - run() 必须 defer close(done) 通知 Stop
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
//		defer w.BaseWorker.NotifyStopped()
//		t := time.NewTicker(w.interval)
//		defer t.Stop()
//		for { select { case <-ctx.Done(): return; case <-t.C: ... } }
//	}
package bg

import (
	"context"
	"sync"
)

// BaseWorker 统一封装后台 goroutine 的 lifecycle 字段。
//
// Embedding 用户负责：调用 Start(ctx, runFn) / Stop()，并在 runFn 入口 defer
// BaseWorker.NotifyStopped()。BaseWorker 内部通过 sync.Mutex 保证 started/stopped/
// cancel/done 的原子可见性，避免跨 goroutine 看到 stale 字段。
type BaseWorker struct {
	name string

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	started bool
	stopped bool
}

// NewBaseWorker 构造一个命名 worker（name 仅用于日志，便于定位）。
func NewBaseWorker(name string) *BaseWorker {
	return &BaseWorker{name: name}
}

// Start 启动 runFn goroutine。如果已启动或已停止，返回 false；否则复制 cancel/done
// 后释放锁再启动 goroutine（避免持锁启动）。ctx 为 nil 时退化为 context.Background()。
//
// 典型 runFn 模板：
//
//	func (w *X) run(ctx context.Context) {
//		defer w.BaseWorker.NotifyStopped()
//		for {
//			select {
//			case <-ctx.Done():
//				return
//			case <-ticker.C:
//				w.tick(ctx)
//			}
//		}
//	}
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
	go func() {
		runFn(ctx)
	}()
	return true
}

// Stop 取消并等待 goroutine 退出。多次调用、未启动、nil receiver 都安全。
// 已停止时直接返回（不会重复 close done）。
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

// NotifyStopped 由 runFn 在退出前调用，确保 Stop() 的 <-done 立即解除阻塞。
// 必须在 runFn 入口处 defer 调用，否则 Stop 会无限等待。
func (b *BaseWorker) NotifyStopped() {
	if b == nil {
		return
	}
	b.mu.Lock()
	done := b.done
	b.mu.Unlock()
	if done != nil {
		close(done)
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

// Name 返回构造时传入的 worker 名，便于 slog/指标引用。
func (b *BaseWorker) Name() string {
	if b == nil {
		return ""
	}
	return b.name
}