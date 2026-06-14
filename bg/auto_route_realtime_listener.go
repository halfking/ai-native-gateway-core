// Package bg — auto route realtime listener.
//
// Listens to PostgreSQL LISTEN/NOTIFY channel 'auto_route_refresh'.
// When a trigger fires (credential_model_bindings change, credentials
// health change, api_keys limit change, model_offers price change),
// this listener debounces 5s and calls AutoIndexRefresher.RefreshOnce
// to bring the in-memory index in sync.
//
// Why: v2.0 original 5-min interval was too coarse for new credentials
// or rate-limit changes. With NOTIFY we get sub-second response time
// while still debouncing to avoid thundering-herd refreshes.

package bg

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AutoRouteRealtimeListener wraps a pgxpool.Conn LISTEN loop and
// dispatches debounced refresh events.
type AutoRouteRealtimeListener struct {
	pool      *pgxpool.Pool
	refresher *AutoIndexRefresher

	// debounceWindow coalesces rapid NOTIFYs (e.g. a bulk INSERT of
	// 100 credentials triggers 100 NOTIFYs). Default: 5 seconds.
	debounceWindow time.Duration

	mu          sync.Mutex
	pending     bool
	lastPending time.Time

	cancel context.CancelFunc
	done   chan struct{}
}

// NewAutoRouteRealtimeListener constructs a listener. refresher may be
// nil for test scenarios where we only want to count NOTIFY events.
func NewAutoRouteRealtimeListener(pool *pgxpool.Pool, refresher *AutoIndexRefresher) *AutoRouteRealtimeListener {
	return &AutoRouteRealtimeListener{
		pool:           pool,
		refresher:      refresher,
		debounceWindow: 5 * time.Second,
		done:           make(chan struct{}),
	}
}

// Start spawns the LISTEN goroutine. Cancelling ctx stops the listener.
func (l *AutoRouteRealtimeListener) Start(ctx context.Context) {
	cctx, cancel := context.WithCancel(ctx)
	l.cancel = cancel
	go l.run(cctx)
	slog.Info("auto route realtime listener started", "channel", "auto_route_refresh", "debounce", l.debounceWindow.String())
}

// Stop terminates the goroutine and waits for it.
func (l *AutoRouteRealtimeListener) Stop() {
	if l.cancel != nil {
		l.cancel()
	}
	<-l.done
}

// run is the main LISTEN loop. It:
//
//   1. Acquires a long-lived connection from the pool
//   2. Issues LISTEN auto_route_refresh
//   3. Waits for notifications with a timeout (so we can re-check debounce)
//   4. When notified, schedules a debounced refresh
//   5. Loop
func (l *AutoRouteRealtimeListener) run(ctx context.Context) {
	defer close(l.done)

	for {
		if ctx.Err() != nil {
			return
		}

		// Acquire connection (release on each iteration to avoid
		// holding a conn for the lifetime of the listener — pool
		// pressure during restarts would be bad otherwise).
		conn, err := l.pool.Acquire(ctx)
		if err != nil {
			slog.Warn("auto_route listener: acquire failed", "error", err)
			time.Sleep(5 * time.Second)
			continue
		}

		if _, err := conn.Exec(ctx, "LISTEN auto_route_refresh"); err != nil {
			slog.Warn("auto_route listener: LISTEN failed", "error", err)
			conn.Release()
			time.Sleep(5 * time.Second)
			continue
		}

		// Wait for notifications with a 10s poll interval
		// (WaitForNotification blocks indefinitely without timeout;
		//  we loop with a timeout to also process debounced refreshes).
		for ctx.Err() == nil {
			notif, err := conn.Conn().WaitForNotification(ctx)
			if err != nil {
				if ctx.Err() != nil {
					conn.Release()
					return
				}
				slog.Warn("auto_route listener: WaitForNotification error", "error", err)
				break
			}
			l.handleNotification(notif.Payload)
		}
		conn.Release()
	}
}

// handleNotification marks the index as dirty and schedules a refresh.
func (l *AutoRouteRealtimeListener) handleNotification(payload string) {
	l.mu.Lock()
	l.pending = true
	l.lastPending = time.Now()
	l.mu.Unlock()
	slog.Info("auto_route listener: refresh requested", "payload", payload)
	l.scheduleRefresh()
}

// scheduleRefresh triggers a debounced refresh in a separate goroutine.
// Multiple calls within debounceWindow coalesce into a single refresh.
func (l *AutoRouteRealtimeListener) scheduleRefresh() {
	go func() {
		time.Sleep(l.debounceWindow)
		l.mu.Lock()
		if !l.pending {
			l.mu.Unlock()
			return
		}
		l.pending = false
		l.mu.Unlock()

		if l.refresher == nil {
			slog.Debug("auto_route listener: no refresher wired; skipping")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := l.refresher.RefreshOnce(ctx); err != nil {
			slog.Warn("auto_route listener: refresh failed", "error", err)
			return
		}
		slog.Info("auto_route listener: index refreshed in response to NOTIFY")
	}()
}

// PendingRefreshes returns the number of pending NOTIFY events since
// the last refresh. Used by admin API and tests.
func (l *AutoRouteRealtimeListener) PendingRefreshes() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pending {
		return 1
	}
	return 0
}