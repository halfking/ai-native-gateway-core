package admin

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// useMaterializedView decides whether to use the materialized view or fall back
// to the base view. Returns true if the materialized view is fresh enough.
func useMaterializedView(ctx context.Context, db *pgxpool.Pool, windowLabel string) bool {
	// Only support 7d window for now (24h uses base view for freshness)
	if windowLabel != "7d" {
		return false
	}

	// Check if materialized view exists and is recent
	var refreshedAt *time.Time
	err := db.QueryRow(ctx, `
		SELECT MAX(refreshed_at)
		FROM routing_analytics_7d
		LIMIT 1
	`).Scan(&refreshedAt)

	if err != nil || refreshedAt == nil {
		// View doesn't exist or is empty — fall back
		return false
	}

	// Use materialized view if refreshed within last 15 minutes
	age := time.Since(*refreshedAt)
	return age < 15*time.Minute
}

// buildMatrixQueryMaterialized builds a query against routing_analytics_7d.
// This is significantly faster than buildMatrixQuery for large datasets.
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
		// Approximate: use weighted average of p95 from each bucket
		// Not perfect but close enough for materialized view
		metricExpr = "SUM(p95_latency_ms * request_count) / NULLIF(SUM(request_count), 0)"
	case "cost_usd":
		metricExpr = "COALESCE(SUM(total_cost_usd), 0)"
	default:
		return "", fmt.Errorf("metric must be one of: count, success_rate, p95_ms, cost_usd")
	}

	// Row = model (same as base query)
	rowExpr := "effective_model"
	rowNullFilter := "effective_model IS NOT NULL"

	// Col = task or work_type
	colExpr := "effective_task_type"
	if rowDim == "work_type" {
		colExpr = "effective_work_type"
	}

	return fmt.Sprintf(`
		SELECT %s AS row_key,
		       %s AS col_key,
		       %s AS val
		FROM routing_analytics_7d
		WHERE %s
		  AND %s IS NOT NULL
		GROUP BY %s, %s
	`, rowExpr, colExpr, metricExpr, rowNullFilter, colExpr, rowExpr, colExpr), nil
}

// buildFlowL12QueryMaterialized builds L1→L2 flow query from materialized view.
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

// buildFlowL23QueryMaterialized builds L2→L3 flow query from materialized view.
func buildFlowL23QueryMaterialized() string {
	return `
		SELECT mv.effective_task_type AS task_type,
		       mv.effective_model AS src,
		       COALESCE(p.display_name, 'unknown') AS dst,
		       SUM(mv.request_count)::float8 AS val
		FROM routing_analytics_7d mv
		LEFT JOIN providers p ON p.id = mv.provider_id
		WHERE mv.effective_task_type IS NOT NULL
		  AND mv.effective_model IS NOT NULL
		GROUP BY mv.effective_task_type, mv.effective_model, p.display_name
	`
}

// getAuditSummaryMaterialized retrieves high-level counts from the audit summary view.
// Returns total, successes, totalAuto, totalSpecified, found.
func getAuditSummaryMaterialized(ctx context.Context, db *pgxpool.Pool, tenantID *int) (int64, int64, int64, int64, bool) {
	var total, successes, totalAuto, totalSpecified int64

	var err error
	if tenantID != nil {
		err = db.QueryRow(ctx, `
			SELECT total_requests, success_count, auto_request_count, specified_request_count
			FROM routing_audit_summary_7d
			WHERE tenant_id = $1
		`, *tenantID).Scan(&total, &successes, &totalAuto, &totalSpecified)
	} else {
		// Super-admin: sum across all tenants
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
