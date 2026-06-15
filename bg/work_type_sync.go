// Package bg — optional ACC work-type sync worker (Phase 3 placeholder).
//
// Enable with LLM_GATEWAY_ACC_WORK_TYPE_SYNC_INTERVAL (e.g. "6h").
// Manual sync remains available via POST /api/admin/work-types/sync-from-acc.
package bg

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// StartWorkTypeACCSync launches a periodic ACC → gateway work_type sync when configured.
func StartWorkTypeACCSync(ctx context.Context, db *pgxpool.Pool, syncFn func(context.Context) error) {
	raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_ACC_WORK_TYPE_SYNC_INTERVAL"))
	if raw == "" || syncFn == nil {
		slog.Info("work_type ACC sync worker disabled (set LLM_GATEWAY_ACC_WORK_TYPE_SYNC_INTERVAL to enable)")
		return
	}
	interval, err := time.ParseDuration(raw)
	if err != nil || interval < time.Minute {
		slog.Warn("invalid LLM_GATEWAY_ACC_WORK_TYPE_SYNC_INTERVAL", "value", raw, "error", err)
		return
	}
	slog.Info("work_type ACC sync worker started", "interval", interval.String())
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				if err := syncFn(runCtx); err != nil {
					slog.Warn("work_type ACC sync failed", "error", err)
				}
				cancel()
			}
		}
	}()
}
