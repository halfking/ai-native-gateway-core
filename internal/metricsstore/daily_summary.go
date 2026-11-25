package metricsstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/metrics"
	"github.com/kaixuan/llm-gateway-go/domains/reportrollup"
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
	return p.refreshDay(ctx, claimDailySummarySQL, func(ctx context.Context, tx *sql.Tx, day string) error {
		if err := metrics.RebuildRequestDailySummary(ctx, tx, day); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
		UPDATE public.request_metrics_daily_state SET dirty=false, refreshed_at=clock_timestamp()
		WHERE day=$1::date`, day)
		return err
	})
}

const claimDailyBoardSQL = `
	SELECT s.day::text FROM public.request_metrics_daily_state s
	WHERE NOT s.dirty
	  AND (SELECT backfill_complete FROM public.request_metrics_projection_state WHERE singleton)
	  AND NOT EXISTS (SELECT 1 FROM public.request_metrics_board_days b WHERE b.day=s.day)
	ORDER BY s.day DESC LIMIT 1 FOR UPDATE OF s SKIP LOCKED`

func (p *ProjectionProcessor) RefreshDailyBoard(ctx context.Context) (bool, error) {
	return p.refreshDay(ctx, claimDailyBoardSQL, func(ctx context.Context, tx *sql.Tx, day string) error {
		// A concurrent worker can commit the marker after the claim statement
		// starts and before it acquires the unchanged summary day row.
		var published bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM public.request_metrics_board_days WHERE day=$1::date)", day).Scan(&published); err != nil || published {
			return err
		}
		return metrics.RebuildRequestDailyBoard(ctx, tx, day)
	})
}

func (p *ProjectionProcessor) RefreshDailyHistory(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	const historyDays = "public.request_metrics_history_days"
	const tenantDays = "public.request_metrics_tenant_history_days"
	var tenantsAvailable bool
	if err := p.db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, tenantDays).Scan(&tenantsAvailable); err != nil {
		return false, err
	}
	missing := `NOT EXISTS (SELECT 1 FROM ` + historyDays + ` h WHERE h.day=s.day)`
	if tenantsAvailable {
		missing += ` OR NOT EXISTS (SELECT 1 FROM ` + tenantDays + ` t WHERE t.day=s.day)`
	}
	claim := `
	SELECT s.day::text FROM public.request_metrics_daily_state s
	WHERE NOT s.dirty
	  AND (SELECT backfill_complete FROM public.request_metrics_projection_state WHERE singleton)
	  AND (` + missing + `)
	ORDER BY s.day DESC LIMIT 1 FOR UPDATE OF s SKIP LOCKED`
	return p.refreshDay(ctx, claim, func(ctx context.Context, tx *sql.Tx, day string) error {
		// Use one lock acquisition for both publications. Separate polling
		// workers can otherwise lose a tick each while competing for this row.
		if err := publishMissingHistory(ctx, tx, day, historyDays, reportrollup.RebuildDailyHistory); err != nil {
			return err
		}
		if tenantsAvailable {
			return publishMissingHistory(ctx, tx, day, tenantDays, reportrollup.RebuildDailyTenantHistory)
		}
		return nil
	})
}

func publishMissingHistory(ctx context.Context, tx *sql.Tx, day, days string, rebuild func(context.Context, *sql.Tx, string) error) error {
	// Another worker can publish the day between the claim snapshot and lock.
	var published bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM "+days+" WHERE day=$1::date)", day).Scan(&published); err != nil || published {
		return err
	}
	return rebuild(ctx, tx, day)
}

func (p *ProjectionProcessor) refreshDay(ctx context.Context, claim string, rebuild func(context.Context, *sql.Tx, string) error) (bool, error) {
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
	if err := tx.QueryRowContext(ctx, claim).Scan(&day); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if err := rebuild(ctx, tx, day); err != nil {
		return false, fmt.Errorf("rebuild request metrics day %s: %w", day, err)
	}
	return true, tx.Commit()
}

func (p *ProjectionProcessor) RunDailySummary(ctx context.Context, cfg Config) {
	runDaily(ctx, cfg, "summary", p.RefreshDailySummary)
}

func (p *ProjectionProcessor) RunDailyBoard(ctx context.Context, cfg Config) {
	runDaily(ctx, cfg, "board", p.RefreshDailyBoard)
}

func (p *ProjectionProcessor) RunDailyHistory(ctx context.Context, cfg Config) {
	runDaily(ctx, cfg, "history", p.RefreshDailyHistory)
}

func runDaily(ctx context.Context, cfg Config, report string, refresh func(context.Context) (bool, error)) {
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		for range cfg.MaxRoundsPerTick {
			built, err := refresh(ctx)
			if err != nil && ctx.Err() == nil {
				slog.Error("request metrics daily refresh failed", "report", report, "error", err)
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
