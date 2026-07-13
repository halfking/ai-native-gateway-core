package admin

import (
	"context"
	"log/slog"

	"github.com/kaixuan/llm-gateway-go/maas"
)

// isPlatformCreditsScope reports whether total_credits_charged should reflect
// the whole platform. Both "" (super_admin all-tenants) and "default" (platform
// tenant) map to platform-wide consumption.
func isPlatformCreditsScope(tenantID string) bool {
	return tenantID == "" || tenantID == "default"
}

// queryTotalCreditsCharged reads the dashboard "总积分消耗" KPI.
//
// Platform scope (default tenant / super_admin): sum all tenants, including
// estimated credits for default-tenant traffic that is not wallet-billed.
// Other tenants: only that tenant's own consumption.
//
// IMPORTANT: this must never be inlined into the main usageSummary SELECT.
func (h *Handler) queryTotalCreditsCharged(ctx context.Context, tenantID string, days int) int64 {
	if h.db == nil {
		return 0
	}
	platform := isPlatformCreditsScope(tenantID)
	bucketTenant := tenantID
	if platform {
		bucketTenant = ""
	}

	credits, bucketRows, err := h.queryCreditsFromBuckets(ctx, bucketTenant, days)
	if err == nil && bucketRows > 0 {
		if !platform {
			return credits
		}
		// Billed tenants are in buckets; default traffic is estimated separately.
		defaultCredits, estErr := h.queryCreditsFromRequestLogs(ctx, "default", days, false)
		if estErr != nil {
			slog.Warn("usageSummary: default-tenant credits estimate failed",
				"days", days, "error", estErr)
			return credits
		}
		return credits + defaultCredits
	}
	if err != nil && !IsMissingRelationError(err) {
		slog.Warn("usageSummary: credits bucket query failed, falling back to request_logs",
			"tenant_id", tenantID,
			"days", days,
			"error", err)
	}
	logsCredits, logsErr := h.queryCreditsFromRequestLogs(ctx, tenantID, days, platform)
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

func requestLogsBillableClause(alias string) string {
	return `(` + alias + `.credits_charged IS NOT NULL
		  OR COALESCE(` + alias + `.prompt_tokens, 0)
		   + COALESCE(` + alias + `.completion_tokens, 0)
		   + COALESCE(` + alias + `.cache_read_tokens, 0)
		   + COALESCE(` + alias + `.cache_write_tokens, 0) > 0)`
}

func (h *Handler) queryCreditsFromRequestLogs(ctx context.Context, tenantID string, days int, platformScope bool) (int64, error) {
	logsTable, alias := requestLogsFromClause(days)
	includeDefault := platformScope || tenantID == "default"
	whereClause := alias + `.ts >= now() - ($1 * INTERVAL '1 day')`
	args := []any{days}
	if platformScope {
		whereClause += " AND " + alias + `.tenant_id <> ''`
	} else {
		whereClause += " AND " + alias + `.tenant_id = $2`
		args = append(args, tenantID)
	}
	whereClause += " AND " + requestLogsBillableClause(alias)
	var credits int64
	err := h.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(`+maas.RequestLogCreditsSQL(alias, includeDefault)+`), 0)::bigint
		  FROM `+logsTable+`
		 WHERE `+whereClause, args...).Scan(&credits)
	return credits, err
}
