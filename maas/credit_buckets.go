package maas

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// BackfillCreditConsumptionBuckets re-derives the hourly credit-consumption
// counter table from request_logs.credits_charged over the lookback window
// (typically 90 days, the longest window the dashboard supports).
//
// Idempotency: rows are overwritten (ON CONFLICT … DO UPDATE SET credits =
// EXCLUDED.credits), so repeated runs in the same hour bucket converge to
// the same total that request_logs reports. ChargeRequest's atomic upsert
// has already been writing the same hour bucket live; the only data that
// could differ between the live counter and the backfill at run-time is
// the request just committed between SELECT and INSERT — which lands in
// the same hour bucket and is reconciled on the next backfill or the
// natural row's eventual re-derivation.
//
// Safe to call from cmd/gateway startup. Designed to be fast (<5s on
// hot 7-day table + monthly partitions). Errors are logged but not
// fatal — dashboard degrades to a zero hint when the counter is empty.
func (s *Service) BackfillCreditConsumptionBuckets(ctx context.Context, lookbackDays int) (int64, error) {
	if !s.Enabled() {
		return 0, nil
	}
	if lookbackDays < 1 {
		lookbackDays = 1
	}
	if lookbackDays > 90 {
		lookbackDays = 90
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	logsTable, alias := requestLogsSource(lookbackDays)
	res, err := s.pool.Exec(ctx, `
		INSERT INTO maas_credit_consumption_buckets
			(tenant_id, bucket_start, credits, request_count, updated_at)
		SELECT
			`+alias+`.tenant_id,
			date_trunc('hour', `+alias+`.ts) AS bucket_start,
			COALESCE(SUM(`+RequestLogCreditsSQL(alias)+`), 0)::bigint AS credits,
			COUNT(*)::int AS request_count,
			now() AS updated_at
		FROM `+logsTable+`
		WHERE `+alias+`.ts >= now() - ($1::int * INTERVAL '1 day')
		  AND `+alias+`.tenant_id NOT IN ('', 'default')
		  AND (`+alias+`.credits_charged IS NOT NULL
		    OR COALESCE(`+alias+`.prompt_tokens, 0)
		     + COALESCE(`+alias+`.completion_tokens, 0)
		     + COALESCE(`+alias+`.cache_read_tokens, 0)
		     + COALESCE(`+alias+`.cache_write_tokens, 0) > 0)
		GROUP BY `+alias+`.tenant_id, date_trunc('hour', `+alias+`.ts)
		ON CONFLICT (tenant_id, bucket_start) DO UPDATE
			SET credits       = EXCLUDED.credits,
			    request_count = EXCLUDED.request_count,
			    updated_at    = EXCLUDED.updated_at
	`, lookbackDays)
	if err != nil {
		slog.Warn("maas: BackfillCreditConsumptionBuckets failed",
			"lookback_days", lookbackDays, "error", err)
		return 0, fmt.Errorf("backfill credit consumption buckets: %w", err)
	}
	rows := res.RowsAffected()
	slog.Info("maas: BackfillCreditConsumptionBuckets completed",
		"lookback_days", lookbackDays, "rows_affected", rows)
	return rows, nil
}

// QueryConsumedCreditsWindow sums the hourly bucket counter for a tenant
// over the last `days` days. Returns 0 when the bucket table is missing or
// empty so callers can degrade gracefully.
//
// Used by admin /api/usage/summary as the fast path for
// total_credits_charged (replaces SELECT SUM(credits_charged) FROM
// usage_ledger … which scans all rows in the window).
func (s *Service) QueryConsumedCreditsWindow(ctx context.Context, tenantID string, days int) (int64, error) {
	if !s.Enabled() {
		return 0, nil
	}
	if tenantID == "" {
		return 0, fmt.Errorf("tenant_id required")
	}
	if days < 1 {
		days = 1
	}
	if days > 90 {
		days = 90
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var credits int64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(credits), 0)::bigint
		  FROM maas_credit_consumption_buckets
		 WHERE tenant_id = $1
		   AND bucket_start >= now() - ($2::int * INTERVAL '1 day')
	`, tenantID, days).Scan(&credits)
	if err != nil {
		return 0, fmt.Errorf("query consumed credits window: %w", err)
	}
	return credits, nil
}
