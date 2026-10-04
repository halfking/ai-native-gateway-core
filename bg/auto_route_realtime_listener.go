// Package bg — auto route realtime listener.
//
// Listens to PostgreSQL LISTEN/NOTIFY channel 'auto_route_refresh'.
// When a trigger fires (credential_model_bindings change, credentials
// health change, api_keys limit change), this listener debounces 5s and
// calls AutoIndexRefresher.RefreshOnceStatus to bring the in-memory index
// in sync. A skipped refresh (concurrent run in flight / coalesced into
// the current bucket) is NOT treated as processed: pending is kept and
// the refresh retries once per debounce window.
//
// Why: v2.0 original 5-min interval was too coarse for new credentials
// or rate-limit changes. With NOTIFY we get sub-second response time
// while still debouncing to avoid thundering-herd refreshes.
//
// Lifecycle contract (2026-08-23 Stage E audit fix):
//   - Start is idempotent; a second call is a no-op.
//   - Stop is safe before Start, after Start, and on repeated calls.
//   - Retry sleeps and the debounce window honor context cancellation,
//     so Stop returns promptly instead of blocking up to 5s.
//   - Debounce is trailing-edge: every notification resets the timer;
//     one refresh runs debounceWindow after the LAST event of a burst.
//   - RefreshOnce runs on a context derived from the listener lifecycle,
//     so pending refreshes are cancelled at shutdown and never outlive
//     the database pool.

package bg

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// indexRefresher is the consumer seam for the debounced refresh. It exists
// so listener lifecycle/debounce tests can inject a fake without a real
// PostgreSQL-backed AutoIndexRefresher.
//
// 2026-10-05 P2（R44 移交 §R44/移交.2）：seam 从 RefreshOnce(ctx) error 升级
// 为 RefreshOnceStatus —— 旧形态只看 error，而 singleflight / 同 bucket 合并
// 「跳过」时返回 nil error，监听器把「没执行」当成「已处理」清掉 pending，
// 该 NOTIFY 永不再重试；跑着的那次 rollup 的 SELECT 早于该 NOTIFY 对应的
// 提交 ⇒ 内存索引丢一次路由配置直到下个 ticker。skipped 语义必须上浮到本层。
type indexRefresher interface {
	// RefreshOnceStatus 执行一次刷新。skipped=true 表示本次触发**没有执行**
	// （已有刷新在跑 / 同 bucket 触发被合并），此时 err 必为 nil —— 跳过不是
	// 错误，但也绝不是「已处理」，调用方必须保持 pending 重试。
	RefreshOnceStatus(ctx context.Context) (skipped bool, err error)
}

// AutoRouteRealtimeListener wraps a pgxpool.Conn LISTEN loop and
// dispatches debounced refresh events.
type AutoRouteRealtimeListener struct {
	pool           *pgxpool.Pool
	refresher      indexRefresher
	debounceWindow time.Duration

	mu      sync.Mutex
	pending bool

	started  atomic.Bool
	stopOnce sync.Once
	cancelMu sync.Mutex
	cancel   context.CancelFunc
	wg       sync.WaitGroup

	// ready 在 LISTEN 首次注册成功后关闭。Start 返回只代表 goroutine 已派生，
	// 注册是异步的——注册之前发出的 NOTIFY 会静默丢失（生产里是收流早于
	// 武装的小窗口丢刷新事件，由周期性全量对账兜底；测试里是 33% flaky 的
	// 根因，24h 审计第二十八轮实测收口）。
	readyOnce sync.Once
	ready     chan struct{}

	// notifyCh feeds the single debounce goroutine. It is buffered; a full
	// channel means a debounce cycle is already scheduled and the event
	// coalesces into it.
	notifyCh chan string
}

// NewAutoRouteRealtimeListener constructs a listener. refresher may be
// nil for test scenarios where we only want to count NOTIFY events.
func NewAutoRouteRealtimeListener(pool *pgxpool.Pool, refresher indexRefresher) *AutoRouteRealtimeListener {
	// A typed nil *AutoIndexRefresher would make the interface non-nil;
	// normalize it so the nil check below keeps working.
	if r, ok := refresher.(*AutoIndexRefresher); ok && r == nil {
		refresher = nil
	}
	return &AutoRouteRealtimeListener{
		pool:           pool,
		refresher:      refresher,
		debounceWindow: 5 * time.Second,
		ready:          make(chan struct{}),
		notifyCh:       make(chan string, 64),
	}
}

// Ready returns a channel that is closed once LISTEN has been registered on
// the live connection for the first time. Start returns as soon as the
// goroutines are spawned; a change committed before that registration lands
// is silently lost (no listener => pg_notify goes nowhere). Callers that
// need change-observation guarantees (integration tests, post-deploy
// reconciliation triggers) should wait on Ready.
func (l *AutoRouteRealtimeListener) Ready() <-chan struct{} {
	if l == nil {
		// nil receiver 返回 nil channel 会让调用方 select 永久阻塞；返回
		// 已关闭 channel 表达"无可等待的就绪信号"（R29 审计）。
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return l.ready
}

// Start spawns the LISTEN and debounce goroutines. Cancelling ctx stops
// the listener. Calling Start more than once is a no-op.
func (l *AutoRouteRealtimeListener) Start(ctx context.Context) {
	if l == nil || l.pool == nil {
		return
	}
	if !l.started.CompareAndSwap(false, true) {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := context.WithCancel(ctx)
	l.cancelMu.Lock()
	l.cancel = cancel
	l.cancelMu.Unlock()
	l.wg.Add(2)
	Go("auto_route_realtime_listener.run", func() { defer l.wg.Done(); l.run(cctx) })
	Go("auto_route_realtime_listener.debounceLoop", func() { defer l.wg.Done(); l.debounceLoop(cctx) })
	slog.Info("auto route realtime listener started", "channel", "auto_route_refresh", "debounce", l.debounceWindow.String())
}

// Stop terminates the goroutines and waits for them. Safe to call before
// Start and on repeated calls.
func (l *AutoRouteRealtimeListener) Stop() {
	if l == nil {
		return
	}
	l.stopOnce.Do(func() {
		l.cancelMu.Lock()
		if l.cancel != nil {
			l.cancel()
		}
		l.cancelMu.Unlock()
	})
	if l.started.Load() {
		l.wg.Wait()
	}
}

// connOutcome 描述一次连接 episode 结束后外层 run 循环的下一步动作。
type connOutcome int

const (
	// connStop：ctx 已取消 / 监听器停机，外层循环退出。
	connStop connOutcome = iota
	// connRetryBackoff：先释放连接再退避 5s 重试（Acquire 或 LISTEN 失败）。
	connRetryBackoff
	// connReconnect：立即重取连接（通知循环因传输错误 break，非停机）。
	connReconnect
)

// run is the main LISTEN loop. It holds one long-lived connection for as
// long as the subscription is active and reacquires (with a cancellable
// backoff) after transport errors.
//
// 2026-10-01 审计：连接的释放收敛到 serveOne 的 defer——此前 Acquire 成功后
// Release 只在 LISTEN 失败与通知循环正常 break 两处手动执行，
// handleNotification 一旦 panic（Go 收口后 goroutine 退出）Release 永不执行，
// 池化连接永久泄漏（ acquire计数只增不减，最终耗尽连接池）。
func (l *AutoRouteRealtimeListener) run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		switch l.serveOne(ctx) {
		case connStop:
			return
		case connRetryBackoff:
			if !sleepCtx(ctx, 5*time.Second) {
				return
			}
		case connReconnect:
			// 立即重取连接。
		}
	}
}

// serveOne 执行一个连接 episode：Acquire 一条池化连接、LISTEN、循环消费通知
// 直到传输错误或 ctx 取消。返回外层循环的下一步动作。
//
// Release 走 defer（Acquire 成功后立刻登记）：任何退出路径——LISTEN 失败、
// 通知循环 break、handleNotification panic——都恰好释放一次。本仓 pgxpool
// v5.9.2 的 Conn.Release 自身幂等（res==nil 直返），但此处不依赖重复调用：
// defer 形态保证 Acquire/Release 严格一一配对。释放发生在返回之前，因此
// connRetryBackoff 的 5s 退避 sleep 期间不占住连接（与修复前手动 Release
// 的时序一致）。
func (l *AutoRouteRealtimeListener) serveOne(ctx context.Context) connOutcome {
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return connStop
		}
		slog.Warn("auto route listener: acquire failed", "error", err)
		return connRetryBackoff
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "LISTEN auto_route_refresh"); err != nil {
		if ctx.Err() != nil {
			return connStop
		}
		slog.Warn("auto route listener: LISTEN failed", "error", err)
		return connRetryBackoff
	}
	l.readyOnce.Do(func() { close(l.ready) })

	for ctx.Err() == nil {
		notif, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("auto route listener: WaitForNotification error", "error", err)
			}
			break
		}
		l.handleNotification(notif.Payload)
	}
	if ctx.Err() != nil {
		return connStop
	}
	return connReconnect
}

// debounceLoop owns the single trailing-edge debounce timer. Every
// notification resets the timer; the refresh runs debounceWindow after
// the last event of a burst. A failed OR SKIPPED refresh keeps the pending
// flag set and rearms the timer so the burst retries once per window
// instead of being silently dropped (2026-10-05 P2：跳过的 NOTIFY 若被记成
// 已处理，内存索引会丢一次路由配置直到下个 ticker —— 缺陷 1).
func (l *AutoRouteRealtimeListener) debounceLoop(ctx context.Context) {
	timer := time.NewTimer(l.debounceWindow)
	// Start stopped: only an actual notification arms the first cycle.
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-l.notifyCh:
			// Coalesce any queued burst into this cycle.
		drain:
			for {
				select {
				case <-l.notifyCh:
				default:
					break drain
				}
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(l.debounceWindow)
		case <-timer.C:
			l.mu.Lock()
			if !l.pending {
				l.mu.Unlock()
				continue
			}
			l.mu.Unlock()

			if l.refresh(ctx) {
				l.mu.Lock()
				l.pending = false
				l.mu.Unlock()
				continue
			}
			// Refresh failed (or was cancelled): keep pending=true and
			// retry after another debounce window.
			timer.Reset(l.debounceWindow)
		}
	}
}

// refresh performs one bounded RefreshOnceStatus on the listener lifecycle
// context. It reports whether the pending NOTIFY has been **covered by a
// completed execution**:
//   - executed（skipped=false, err=nil）→ true，调用方可清 pending；
//   - skipped（singleflight 忙 / 同 bucket 合并）→ false：本次触发尚未落地，
//     pending 保持，下一轮 debounce 窗口重试 —— 跳过 ≠ 处理完成（缺陷 1 的
//     修法）。重试要么执行、要么继续等；最终一致性由 refresher 的 bucket
//     滚动放行 + 5 分钟 ticker 兜底；
//   - error（含 ctx 取消）→ false；取消不打警告。
func (l *AutoRouteRealtimeListener) refresh(ctx context.Context) bool {
	if l.refresher == nil {
		slog.Debug("auto route listener: no refresher wired; skipping")
		return true
	}
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	skipped, err := l.refresher.RefreshOnceStatus(rctx)
	if err != nil {
		if rctx.Err() == nil {
			slog.Warn("auto route listener: refresh failed", "error", err)
		}
		return false
	}
	if skipped {
		slog.Debug("auto route listener: refresh skipped (concurrent run in flight / coalesced into current bucket); pending kept for retry")
		return false
	}
	slog.Info("auto route listener: index refreshed in response to NOTIFY")
	return true
}

// handleNotification marks the index as dirty and wakes the debounce
// loop. Payload is observability metadata only.
func (l *AutoRouteRealtimeListener) handleNotification(payload string) {
	l.mu.Lock()
	l.pending = true
	l.mu.Unlock()
	slog.Info("auto route listener: refresh requested", "payload", payload)
	select {
	case l.notifyCh <- payload:
	default:
		// Debounce cycle already armed; the burst coalesces.
	}
}

// PendingRefreshes returns the number of pending NOTIFY events since the
// last refresh. Used by admin API and tests.
func (l *AutoRouteRealtimeListener) PendingRefreshes() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pending {
		return 1
	}
	return 0
}

// sleepCtx waits for d, returning false when ctx was cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
