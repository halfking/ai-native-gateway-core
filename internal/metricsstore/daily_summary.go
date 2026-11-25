package metricsstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/metrics"
)

const claimDailySummarySQL = `
	SELECT day::text FROM public.request_metrics_daily_state
	WHERE dirty AND day < (CURRENT_TIMESTAMP AT TIME ZONE 'Asia/Shanghai')::date
	  AND (SELECT backfill_complete FROM public.request_metrics_projection_state WHERE singleton)
	ORDER BY day DESC LIMIT 1 FOR UPDATE SKIP LOCKED`

// RefreshDailySummary rebuilds and publishes at most one closed day. Projection
// writes invalidate the day in their own transaction and serialize with this
// row lock. Readers therefore see either a clean summary or fresh detail.
func (p *ProjectionProcessor) RefreshDailySummary(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	// Match TRUNCATE's lock order: projection table, then day state.
	if _, err := tx.ExecContext(ctx, "LOCK TABLE public.request_metrics_projection IN ACCESS SHARE MODE"); err != nil {
		return false, err
	}
	var day string
	if err := tx.QueryRowContext(ctx, claimDailySummarySQL).Scan(&day); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if err := metrics.RebuildRequestDailySummary(ctx, tx, day); err != nil {
		return false, fmt.Errorf("rebuild request metrics day %s: %w", day, err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE public.request_metrics_daily_state SET dirty=false, refreshed_at=clock_timestamp()
		WHERE day=$1::date`, day); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (p *ProjectionProcessor) RunDailySummary(ctx context.Context, cfg Config) {
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		for range cfg.MaxRoundsPerTick {
			built, err := p.RefreshDailySummary(ctx)
			if err != nil && ctx.Err() == nil {
				slog.Error("request metrics daily summary refresh failed", "error", err)
			}
			if err != nil || !built {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
