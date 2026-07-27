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
		db:       db,
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

// Start begins the recovery loop.
func (w *HealthAutoRecover) Start(ctx context.Context) {
	slog.Info("health_auto_recover started", "interval", w.interval)

	go func() {
		ticker := time.NewTicker(w.interval)
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
