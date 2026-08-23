// Package bg — auto route realtime listener.
//
// Listens to PostgreSQL LISTEN/NOTIFY channel 'auto_route_refresh'.
// When a trigger fires (credential_model_bindings change, credentials
// health change, api_keys limit change), this listener debounces 5s and
// calls AutoIndexRefresher.RefreshOnce to bring the in-memory index
// in sync.
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
type indexRefresher interface {
	RefreshOnce(ctx context.Context) error
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
		notifyCh:       make(chan string, 64),
	}
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
	go func() { defer l.wg.Done(); l.run(cctx) }()
	go func() { defer l.wg.Done(); l.debounceLoop(cctx) }()
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

// run is the main LISTEN loop. It holds one long-lived connection for as
// long as the subscription is active and reacquires (with a cancellable
// backoff) after transport errors.
func (l *AutoRouteRealtimeListener) run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}

		conn, err := l.pool.Acquire(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("auto route listener: acquire failed", "error", err)
			if !sleepCtx(ctx, 5*time.Second) {
				return
			}
			continue
		}

		if _, err := conn.Exec(ctx, "LISTEN auto_route_refresh"); err != nil {
			conn.Release()
			if ctx.Err() != nil {
				return
			}
			slog.Warn("auto route listener: LISTEN failed", "error", err)
			if !sleepCtx(ctx, 5*time.Second) {
				return
			}
			continue
		}

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
		conn.Release()
	}
}

// debounceLoop owns the single trailing-edge debounce timer. Every
// notification resets the timer; the refresh runs debounceWindow after
// the last event of a burst. A failed refresh keeps the pending flag set
// and rearms the timer so the burst retries once per window instead of
// being silently dropped.
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

// refresh performs one bounded RefreshOnce on the listener lifecycle
// context. It reports whether the refresh completed successfully; a
// context cancellation reports false without a warning.
func (l *AutoRouteRealtimeListener) refresh(ctx context.Context) bool {
	if l.refresher == nil {
		slog.Debug("auto route listener: no refresher wired; skipping")
		return true
	}
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := l.refresher.RefreshOnce(rctx); err != nil {
		if rctx.Err() == nil {
			slog.Warn("auto route listener: refresh failed", "error", err)
		}
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
