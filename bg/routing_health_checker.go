package bg

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type RoutingHealthChecker struct {
	db       *pgxpool.Pool
	interval time.Duration
	stopCh   chan struct{}
	stopOnce sync.Once
}

func NewRoutingHealthChecker(db *pgxpool.Pool) *RoutingHealthChecker {
	interval := 15 * time.Minute
	if v := os.Getenv("LLM_GATEWAY_HEALTH_CHECK_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			interval = d
		}
	}
	return &RoutingHealthChecker{db: db, interval: interval, stopCh: make(chan struct{})}
}

func (w *RoutingHealthChecker) Start(ctx context.Context) {
	slog.Info("routing_health_checker started", "interval", w.interval)
	go func() {
		w.runOnce(ctx)
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				slog.Info("routing_health_checker stopping")
				return
			case <-w.stopCh:
				slog.Info("routing_health_checker stopped")
				return
			case <-ticker.C:
				w.runOnce(ctx)
			}
		}
	}()
}

func (w *RoutingHealthChecker) Stop() {
	w.stopOnce.Do(func() { close(w.stopCh) })
}

func (w *RoutingHealthChecker) runOnce(ctx context.Context) {
	crit, warn, err := RunChecks(ctx, w.db)
	if err != nil {
		slog.Error("routing_health_checker: run failed", "error", err)
		return
	}
	slog.Info("routing_health_checker: run completed", "new_critical", crit, "new_warning", warn)
}
