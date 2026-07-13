package admin

import (
	"context"
	"log/slog"

	"github.com/kaixuan/llm-gateway-go/maas"
)

// queryTotalCreditsCharged reads the dashboard "总积分消耗" KPI.
//
// Fast path: maas_credit_consumption_buckets (hourly counter, ≤ N rows).
// Fallback: SUM(credits) from request_logs, estimating from tokens when
// credits_charged was not persisted (pre wallet ::bigint fix).
//
// IMPORTANT: this must never be inlined into the main usageSummary SELECT.
// A missing optional bucket table used to poison the entire summary query
// and zero out total_requests / tokens / cost alongside credits.
func (h *Handler) queryTotalCreditsCharged(ctx context.Context, tenantID string, days int) int64 {
	if h.db == nil {
		return 0
	}
	credits, bucketRows, err := h.queryCreditsFromBuckets(ctx, tenantID, days)
	if err == nil && bucketRows > 0 {
		return credits
	}
	if err != nil && !IsMissingRelationError(err) {
		slog.Warn("usageSummary: credits bucket query failed, falling back to request_logs",
			"tenant_id", tenantID,
			"days", days,
			"error", err)
	}
	logsCredits, logsErr := h.queryCreditsFromRequestLogs(ctx, tenantID, days)
	if logsErr != nil {
		if IsMissingRelationError(logsErr) {
			slog.Debug("usageSummary: credits request_logs fallback unavailable",
				"relation", ExtractMissingRelationName(logsErr))
		} else {
			slog.Warn("usageSummary: credits request_logs fallback failed",
				"tenant_id", tenantID,
				"days", days,
				"error", logsErr)
		}
		return 0
	}
	return logsCredits
}

func (h *Handler) queryCreditsFromBuckets(ctx context.Context, tenantID string, days int) (credits int64, bucketRows int64, err error) {
	if tenantID != "" {
		err = h.db.QueryRow(ctx, `
			SELECT COALESCE(SUM(credits), 0)::bigint,
			       COUNT(*)::bigint
			  FROM maas_credit_consumption_buckets
			 WHERE tenant_id = $1
			   AND bucket_start >= now() - ($2::int * INTERVAL '1 day')
		`, tenantID, days).Scan(&credits, &bucketRows)
		return credits, bucketRows, err
	}
	err = h.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(credits), 0)::bigint,
		       COUNT(*)::bigint
		  FROM maas_credit_consumption_buckets
		 WHERE bucket_start >= now() - ($1::int * INTERVAL '1 day')
	`, days).Scan(&credits, &bucketRows)
	return credits, bucketRows, err
}

// requestLogsFromClause mirrors maas.requestLogsSource for admin usage queries.
func requestLogsFromClause(days int) (from string, alias string) {
	if days <= 7 {
		return "request_logs_hot AS r", "r"
	}
	return "request_logs_with_current_month AS r", "r"
}

func (h *Handler) queryCreditsFromRequestLogs(ctx context.Context, tenantID string, days int) (int64, error) {
	logsTable, alias := requestLogsFromClause(days)
	whereClause := alias + `.ts >= now() - ($1 * INTERVAL '1 day')
		AND ` + alias + `.tenant_id NOT IN ('', 'default')
		AND (` + alias + `.credits_charged IS NOT NULL
		  OR COALESCE(` + alias + `.prompt_tokens, 0)
		   + COALESCE(` + alias + `.completion_tokens, 0)
		   + COALESCE(` + alias + `.cache_read_tokens, 0)
		   + COALESCE(` + alias + `.cache_write_tokens, 0) > 0)`
	args := []any{days}
	if tenantID != "" {
		whereClause += " AND " + alias + ".tenant_id = $2"
		args = append(args, tenantID)
	}
	var credits int64
	err := h.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(`+maas.RequestLogCreditsSQL(alias)+`), 0)::bigint
		  FROM `+logsTable+`
		 WHERE `+whereClause, args...).Scan(&credits)
	return credits, err
}
