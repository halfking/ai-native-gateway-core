// Package admin — analytics_materialized.go
//
// Materialized-view fast path for /api/admin/auto-route/analytics/* and
// /api/admin/auto-route/audit endpoints.
//
// Background (2026-08-31): every aggregation over
// request_logs_with_current_month_without_customer_id Seq-Scans 314K+ rows
// in the current columnar partition and blew past the 15s handler timeout.
// Migration 632 pre-aggregates those rows into routing_analytics_7d
// (hourly buckets × task × model × provider × tenant) and
// routing_audit_summary_7d (per-tenant totals); bg.MaterializedViewRefresher
// keeps them current every 10 minutes.
//
// Fallback contract: when the materialized view is missing, empty, or stale
// (refresher down / disabled), every helper here reports "not usable" and
// callers fall back to the original base-view queries — the endpoints
// degrade to pre-632 behavior instead of serving wrong numbers.
package admin

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// mvFreshnessBudget is the maximum staleness we tolerate before falling
// back to the base view. The refresher runs every 10 minutes, so 15 minutes
// gives one missed cycle of headroom; anything older means the refresher is
// down and stale numbers must not be served.
const mvFreshnessBudget = 15 * time.Minute

// mvFreshWithin reports whether the named materialized view exists and was
// refreshed within mvFreshnessBudget. A missing view, a query error, or an
// empty view (MAX(refreshed_at) = NULL) all report false so callers take
// the base-view fallback.
func mvFreshWithin(ctx context.Context, db *pgxpool.Pool, view string) bool {
	if db == nil {
		return false
	}
	var refreshedAt *time.Time
	// view is a package-internal constant, never user input.
	if err := db.QueryRow(ctx, fmt.Sprintf(`SELECT MAX(refreshed_at) FROM %s`, view)).Scan(&refreshedAt); err != nil {
		return false
	}
	return refreshedAt != nil && time.Since(*refreshedAt) < mvFreshnessBudget
}

// useMaterializedView decides whether analytics endpoints (matrix / flow)
// can serve windowLabel from routing_analytics_7d. Only the 7d window is
// pre-aggregated; 24h always uses the base view for freshness.
func useMaterializedView(ctx context.Context, db *pgxpool.Pool, windowLabel string) bool {
	if windowLabel != "7d" {
		return false
	}
	return mvFreshWithin(ctx, db, "routing_analytics_7d")
}

// buildMatrixQueryMaterialized builds the heatmap query against
// routing_analytics_7d. Semantically it mirrors buildMatrixQuery:
//
//   - row axis  = effective model (outbound_model, falling back to
//     client_model for explicit-model requests); Go-side canonicalization
//     in handleMatrix applies to both paths identically.
//   - col axis  = effective task (__specified__ for explicit-model
//     requests) or work_type.
//   - p95_ms is the one approximation: a request-count-weighted average of
//     hourly bucket p95s instead of an exact percentile over raw rows.
func buildMatrixQueryMaterialized(rowDim, metric string) (string, error) {
	if rowDim != "task_type" && rowDim != "work_type" {
		return "", fmt.Errorf("row must be task_type or work_type")
	}

	var metricExpr string
	switch metric {
	case "count":
		metricExpr = "SUM(request_count)::float8"
	case "success_rate":
		metricExpr = "CASE WHEN SUM(request_count) > 0 THEN SUM(success_count)::float8 / SUM(request_count)::float8 ELSE 0 END"
	case "p95_ms":
		metricExpr = "SUM(p95_latency_ms * request_count) / NULLIF(SUM(request_count), 0)"
	case "cost_usd":
		metricExpr = "COALESCE(SUM(total_cost_usd), 0)"
	default:
		return "", fmt.Errorf("metric must be one of: count, success_rate, p95_ms, cost_usd")
	}

	rowExpr := "effective_model"
	colExpr := "effective_task_type"
	if rowDim == "work_type" {
		colExpr = "effective_work_type"
	}

	return fmt.Sprintf(`
		SELECT %s AS row_key,
		       %s AS col_key,
		       %s AS val
		FROM routing_analytics_7d
			WHERE effective_model IS NOT NULL
			  AND %s IS NOT NULL
			GROUP BY %s, %s
		`, rowExpr, colExpr, metricExpr, colExpr, rowExpr, colExpr), nil

}

// buildFlowL12QueryMaterialized builds the L1→L2 (task → model) Sankey
// query against routing_analytics_7d. Mirrors buildFlowL12Query.
func buildFlowL12QueryMaterialized() string {
	return `
		SELECT effective_task_type AS src,
		       effective_model AS dst,
		       SUM(request_count)::float8 AS val
		FROM routing_analytics_7d
		WHERE effective_task_type IS NOT NULL
		  AND effective_model IS NOT NULL
		GROUP BY effective_task_type, effective_model
	`
}

// buildFlowL23QueryMaterialized builds the L2→L3 (model × task → provider)
// Sankey query. The base query (buildFlowL23Query) resolves a provider via
// `COALESCE(provider_id, credential lookup)`; migration 632 bakes the same
// fallback into the view's effective_provider_id column so both paths agree
// on how much traffic lands in the 'unknown' provider bucket.
func buildFlowL23QueryMaterialized() string {
	return `
		SELECT mv.effective_task_type AS task_type,
		       mv.effective_model AS src,
		       COALESCE(p.display_name, 'unknown') AS dst,
		       SUM(mv.request_count)::float8 AS val
		FROM routing_analytics_7d mv
		LEFT JOIN providers p ON p.id = mv.effective_provider_id
		WHERE mv.effective_task_type IS NOT NULL
		  AND mv.effective_model IS NOT NULL
		GROUP BY mv.effective_task_type, mv.effective_model, p.display_name
	`
}

// getAuditSummaryMaterialized retrieves the headline 7-day counters for
// handleAudit from routing_audit_summary_7d. tenantID follows the string
// form returned by tenantLogsClause (admin/session_tenant.go); nil means
// super-admin, which sums every tenant group — the same rows the base
// query counts. The freshness gate keeps a dead refresher from freezing
// the audit numbers forever.
//
// The final return value reports whether the materialized path produced
// usable numbers; on false the caller must run the base-view query.
func getAuditSummaryMaterialized(ctx context.Context, db *pgxpool.Pool, tenantID *string) (total, successes, totalAuto, totalSpecified int64, ok bool) {
	if db == nil || !mvFreshWithin(ctx, db, "routing_audit_summary_7d") {
		return 0, 0, 0, 0, false
	}

	var err error
	if tenantID != nil {
		err = db.QueryRow(ctx, `
			SELECT total_requests, success_count, auto_request_count, specified_request_count
			FROM routing_audit_summary_7d
			WHERE tenant_id = $1
		`, *tenantID).Scan(&total, &successes, &totalAuto, &totalSpecified)
	} else {
		err = db.QueryRow(ctx, `
			SELECT
				COALESCE(SUM(total_requests), 0),
				COALESCE(SUM(success_count), 0),
				COALESCE(SUM(auto_request_count), 0),
				COALESCE(SUM(specified_request_count), 0)
			FROM routing_audit_summary_7d
		`).Scan(&total, &successes, &totalAuto, &totalSpecified)
	}
	if err != nil {
		return 0, 0, 0, 0, false
	}
	return total, successes, totalAuto, totalSpecified, true
}
