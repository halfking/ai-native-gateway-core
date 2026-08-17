package bg

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/credentialhealth"
)

// HealthAutoRecover checks for credentials with expired availability_recover_at
// and restores them to 'ready' state.
type HealthAutoRecover struct {
	db       credentialhealth.DBQuerier
	interval time.Duration // default 1 minute
	stopCh   chan struct{}

	// 2026-07-27 concurrency fix: Stop() did an unguarded close(stopCh),
	// so a second Stop() panicked with "close of closed channel".
	// Same guard as bg/routing_health_checker.go.
	stopOnce sync.Once

	// 2026-08-17 P0 fix: tickInterval is the live, possibly-updated interval.
	// Reads/writes are guarded by tickMu so cmd/gateway/main.go can install an
	// env-driven override (LLM_GATEWAY_AUTO_RECOVER_INTERVAL_SECONDS) at any
	// point in the worker lifecycle.
	tickMu       sync.RWMutex
	tickInterval  time.Duration
}

// NewHealthAutoRecover creates a recovery worker.
func NewHealthAutoRecover(
	db credentialhealth.DBQuerier,
	interval time.Duration,
) *HealthAutoRecover {
	if interval == 0 {
		interval = 1 * time.Minute
	}

	return &HealthAutoRecover{
		db:          db,
		interval:    interval,
		tickInterval: interval,
		stopCh:      make(chan struct{}),
	}
}

// SetTickInterval changes the recovery loop period at runtime. Set to 0 to
// disable the loop (the next tick simply won't fire). The change is picked
// up after the current tick completes. Useful for ops who want to silence
// the worker temporarily during a maintenance window without recompiling.
func (w *HealthAutoRecover) SetTickInterval(d time.Duration) {
	if d < 0 {
		return
	}
	if d > 0 && d < time.Second {
		d = time.Second
	}
	w.tickMu.Lock()
	w.tickInterval = d
	w.tickMu.Unlock()
}

func (w *HealthAutoRecover) currentInterval() time.Duration {
	w.tickMu.RLock()
	defer w.tickMu.RUnlock()
	if w.tickInterval <= 0 {
		return w.interval // legacy field used as the boot-time default
	}
	return w.tickInterval
}

// Start begins the recovery loop.
func (w *HealthAutoRecover) Start(ctx context.Context) {
	slog.Info("health_auto_recover started", "interval", w.currentInterval().String())

	go func() {
		interval := w.currentInterval()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				slog.Info("health_auto_recover stopping")
				return
			case <-w.stopCh:
				slog.Info("health_auto_recover stopped")
				return
			case <-ticker.C:
				if err := w.recover(ctx); err != nil {
					slog.Error("health_auto_recover failed", "error", err)
				}
				if newInterval := w.currentInterval(); newInterval != interval && newInterval > 0 {
					interval = newInterval
					ticker.Reset(interval)
					slog.Info("health_auto_recover: tick interval updated",
						"new_interval", interval.String())
				}
			}
		}
	}()
}

// Stop gracefully stops the worker. Idempotent; safe on a
// never-Started worker.
func (w *HealthAutoRecover) Stop() {
	w.stopOnce.Do(func() { close(w.stopCh) })
}

// recover restores expired credentials to 'ready' state.
func (w *HealthAutoRecover) recover(ctx context.Context) error {
	count, err := credentialhealth.RecoverExpired(ctx, w.db)
	if err != nil {
		return err
	}

	if count > 0 {
		slog.Info("health_auto_recover: recovered credentials", "count", count)
	}

	return nil
}
