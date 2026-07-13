package admin

import (
	"context"
	"log/slog"
)

// queryTotalCreditsCharged reads the dashboard "总积分消耗" KPI.
//
// Fast path: maas_credit_consumption_buckets (hourly counter, ≤ N rows).
// Fallback: SUM(credits_charged) from usage_ledger_with_current_month when
// the bucket table is not migrated yet.
//
// IMPORTANT: this must never be inlined into the main usageSummary SELECT.
// A missing optional bucket table used to poison the entire summary query
// and zero out total_requests / tokens / cost alongside credits.
func (h *Handler) queryTotalCreditsCharged(ctx context.Context, tenantID string, days int) int64 {
	if h.db == nil {
		return 0
	}
	credits, err := h.queryCreditsFromBuckets(ctx, tenantID, days)
	if err == nil {
		return credits
	}
	if !IsMissingRelationError(err) {
		slog.Warn("usageSummary: credits bucket query failed, falling back to ledger",
			"tenant_id", tenantID,
			"days", days,
			"error", err)
	}
	ledgerCredits, ledgerErr := h.queryCreditsFromLedger(ctx, tenantID, days)
	if ledgerErr != nil {
		if IsMissingRelationError(ledgerErr) {
			slog.Debug("usageSummary: credits ledger fallback unavailable",
				"relation", ExtractMissingRelationName(ledgerErr))
		} else {
			slog.Warn("usageSummary: credits ledger fallback failed",
				"tenant_id", tenantID,
				"days", days,
				"error", ledgerErr)
		}
		return 0
	}
	return ledgerCredits
}

func (h *Handler) queryCreditsFromBuckets(ctx context.Context, tenantID string, days int) (int64, error) {
	var credits int64
	if tenantID != "" {
		err := h.db.QueryRow(ctx, `
			SELECT COALESCE(SUM(credits), 0)::bigint
			  FROM maas_credit_consumption_buckets
			 WHERE tenant_id = $1
			   AND bucket_start >= now() - ($2::int * INTERVAL '1 day')
		`, tenantID, days).Scan(&credits)
		return credits, err
	}
	err := h.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(credits), 0)::bigint
		  FROM maas_credit_consumption_buckets
		 WHERE bucket_start >= now() - ($1::int * INTERVAL '1 day')
	`, days).Scan(&credits)
	return credits, err
}

func (h *Handler) queryCreditsFromLedger(ctx context.Context, tenantID string, days int) (int64, error) {
	whereClause := "ts >= now() - ($1 * INTERVAL '1 day') AND credits_charged IS NOT NULL"
	args := []any{days}
	if tenantID != "" {
		whereClause += " AND tenant_id = $2"
		args = append(args, tenantID)
	}
	var credits int64
	err := h.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(credits_charged), 0)::bigint
		  FROM usage_ledger_with_current_month
		 WHERE `+whereClause, args...).Scan(&credits)
	return credits, err
}
