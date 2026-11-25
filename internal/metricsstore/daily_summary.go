package metricsstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/metrics"
	"github.com/kaixuan/llm-gateway-go/domains/reportrollup"
)

const claimDailySummarySQL = `
	SELECT day::text FROM public.request_metrics_daily_state
	WHERE dirty AND day < (CURRENT_TIMESTAMP AT TIME ZONE 'Asia/Shanghai')::date
	  AND (SELECT backfill_complete FROM public.request_metrics_repair_state WHERE singleton)
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

func (p *ProjectionProcessor) RefreshDailyBoard(ctx context.Context) (bool, error) {
	const boardDays = "public.request_metrics_board_days"
	var hoursAvailable, distributionsAvailable bool
	if err := p.db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL, to_regclass($2) IS NOT NULL`,
		metrics.RequestBoardHourDaysTable, metrics.RequestBoardHourDistributionDaysTable).Scan(&hoursAvailable, &distributionsAvailable); err != nil {
		return false, err
	}
	publishDistributions := hoursAvailable && distributionsAvailable
	missing := `NOT EXISTS (SELECT FROM ` + boardDays + ` b WHERE b.day=s.day)`
	if hoursAvailable {
		missing += ` OR NOT EXISTS (SELECT FROM ` + metrics.RequestBoardHourDaysTable + ` h WHERE h.day=s.day)`
	}
	if publishDistributions {
		missing += ` OR NOT EXISTS (SELECT FROM ` + metrics.RequestBoardHourDistributionDaysTable + ` h WHERE h.day=s.day)`
	}
	claim := `SELECT s.day::text FROM public.request_metrics_daily_state s
		WHERE NOT s.dirty
		  AND (SELECT backfill_complete FROM public.request_metrics_repair_state WHERE singleton)
		  AND (` + missing + `)
		ORDER BY s.day DESC LIMIT 1 FOR UPDATE OF s SKIP LOCKED`
	return p.refreshDay(ctx, claim, func(ctx context.Context, tx *sql.Tx, day string) error {
		if err := publishMissingDailyReport(ctx, tx, day, boardDays, metrics.RebuildRequestDailyBoard); err != nil {
			return err
		}
		if hoursAvailable {
			if err := publishMissingDailyReport(ctx, tx, day, metrics.RequestBoardHourDaysTable, metrics.RebuildRequestBoardHours); err != nil {
				return err
			}
		}
		if publishDistributions {
			return publishMissingDailyReport(ctx, tx, day, metrics.RequestBoardHourDistributionDaysTable, metrics.RebuildRequestBoardHourDistributions)
		}
		return nil
	})
}

func (p *ProjectionProcessor) RefreshDailyHistory(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	const historyDays = "public.request_metrics_history_days"
	const tenantDays = "public.request_metrics_tenant_history_days"
	const detailDays = "public.request_metrics_history_detail_days"
	const pageDays = "public.request_metrics_history_page_days"
	var tenantsAvailable, detailsAvailable, pagesAvailable bool
	if err := p.db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL, to_regclass($2) IS NOT NULL, to_regclass($3) IS NOT NULL`, tenantDays, detailDays, pageDays).
		Scan(&tenantsAvailable, &detailsAvailable, &pagesAvailable); err != nil {
		return false, err
	}
	type publication struct {
		days    string
		rebuild func(context.Context, *sql.Tx, string) error
	}
	publications := []publication{{historyDays, reportrollup.RebuildDailyHistory}}
	if tenantsAvailable {
		publications = append(publications, publication{tenantDays, reportrollup.RebuildDailyTenantHistory})
	}
	if detailsAvailable {
		publications = append(publications, publication{detailDays, reportrollup.RebuildDailyHistoryDetails})
	}
	if pagesAvailable {
		publications = append(publications, publication{pageDays, reportrollup.RebuildDailyPageHistory})
	}
	missing := make([]string, 0, len(publications))
	for _, pub := range publications {
		missing = append(missing, `NOT EXISTS (SELECT 1 FROM `+pub.days+` h WHERE h.day=s.day)`)
	}
	claim := `
	SELECT s.day::text FROM public.request_metrics_daily_state s
	WHERE NOT s.dirty
	  AND (SELECT backfill_complete FROM public.request_metrics_repair_state WHERE singleton)
	  AND (` + strings.Join(missing, " OR ") + `)
	ORDER BY s.day DESC LIMIT 1 FOR UPDATE OF s SKIP LOCKED`
	return p.refreshDay(ctx, claim, func(ctx context.Context, tx *sql.Tx, day string) error {
		// Use one lock acquisition for the publications. Separate polling
		// workers can otherwise lose a tick each while competing for this row.
		for _, pub := range publications {
			if err := publishMissingDailyReport(ctx, tx, day, pub.days, pub.rebuild); err != nil {
				return err
			}
		}
		return nil
	})
}

func publishMissingDailyReport(ctx context.Context, tx *sql.Tx, day, days string, rebuild func(context.Context, *sql.Tx, string) error) error {
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
	if _, err := tx.ExecContext(ctx, "LOCK TABLE public.request_metrics IN ACCESS SHARE MODE"); err != nil {
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
