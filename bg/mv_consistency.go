// Package bg — mv_consistency.go
//
// Materialized view consistency checker (2026-09-01, P2-D) for the routing
// analytics views created by migration 632. Compares the pre-aggregated
// materialized view against the live base view to detect drift (stale refresh,
// broken WHERE filters, schema mismatch, etc.).
//
// Runs inline after every successful REFRESH ... CONCURRENTLY so the same
// process that refreshes also validates. Results surface via
// metrics.RoutingAnalyticsMVDrift* (Prometheus /metrics) and are also emitted
// by the offline cron script (scripts/monitoring/verify-mv-consistency.sh →
// node_exporter textfile collector), keeping both paths symmetric.
//
// Design:
//   - FULL OUTER JOIN between the materialized view aggregates and a re-run
//     of the base query (same WHERE + GROUP BY as the MV definition).
//   - Per-(task_type, model) bucket: compute abs_diff and diff_pct.
//   - Return max_pct, max_abs, and breach_count (buckets exceeding both the
//     percentage and absolute thresholds).
//   - Alert threshold defaults: 5% AND abs > 100 (small buckets are noisy by
//     nature; a 100% drift on a 4-row bucket is only 4 rows, vs 5% on 200k is
//     10k rows — both conditions guard against false positives).
//
// SQL mirrors the shell script's query (scripts/monitoring/verify-mv-consistency.sh
// L95-138) so Grafana panels stay comparable whether data comes from the
// runtime path or the cron path.
package bg

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kaixuan/llm-gateway-go/metrics"
)

// mvConsistencyDB is the minimal pgx query surface needed by the checker.
// Keeping this as an interface allows deterministic pgxmock tests while the
// production caller continues to pass *pgxpool.Pool.
type mvConsistencyDB interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

const (
	// MVDriftAlertPct is the default percentage drift threshold (5%).
	// A bucket with diff_pct > this AND abs_diff > MVDriftAlertAbs counts
	// as a breach. Small buckets (< 100 rows) easily hit 5% from single
	// late-arriving rows, so the absolute floor filters noise.
	MVDriftAlertPct = 5.0

	// MVDriftAlertAbs is the default absolute request-count drift floor (100).
	// Paired with MVDriftAlertPct: both must be exceeded to count as a breach.
	MVDriftAlertAbs = 100

	// MVDriftViewRoutingAnalytics7d is the view name constant for
	// routing_analytics_7d (used as the "view" label value).
	MVDriftViewRoutingAnalytics7d = "routing_analytics_7d"

	// MVDriftViewRoutingAuditSummary7d is the view name constant for
	// routing_audit_summary_7d (used as the "view" label value).
	MVDriftViewRoutingAuditSummary7d = "routing_audit_summary_7d"
)

// MVConsistencyResult holds the drift summary for one materialized view.
type MVConsistencyResult struct {
	// ViewExists distinguishes a successful zero-drift check from a skipped
	// check because migration 632 has not created the materialized view.
	ViewExists bool

	// MaxPct is the largest percentage drift across all (task_type, model)
	// buckets. Range [0, 100+]; 0 means perfect consistency.
	MaxPct float64

	// MaxAbs is the largest absolute request-count delta in any bucket.
	// Useful to distinguish "tiny bucket noise" from "real big-bucket drift".
	MaxAbs int64

	// BreachCount is how many buckets exceeded both MVDriftAlertPct and
	// MVDriftAlertAbs. Alert on BreachCount > 0 sustained for >1 cycle.
	BreachCount int

	// DiffRowCount is the total number of (task_type, model) buckets that
	// had any non-zero difference (diagnostic; not used for alerting).
	DiffRowCount int
}

// CheckMVConsistency compares routing_analytics_7d (or the named view) against
// the base view request_logs_with_current_month_without_customer_id and returns
// drift metrics. The query mirrors migration 632's MV definition and the shell
// script's FULL OUTER JOIN so all three paths (MV creation, runtime check,
// offline cron) stay aligned.
//
// A missing view (migration 632 not applied) returns a zero result and no
// error — callers log a warning but do not treat it as a database failure,
// since consumers already fall back to the base query when the MV is absent.
//
// SQL errors (connection lost, query timeout, broken schema) return an error
// so the caller can increment metrics.RoutingAnalyticsMVConsistencyErrorsTotal
// and avoid updating the consistency_last_unix timestamp (stale timestamp = signal).
func CheckMVConsistency(ctx context.Context, pool mvConsistencyDB, viewName string) (MVConsistencyResult, error) {
	var result MVConsistencyResult

	// Sanity: does the view exist? Migration 632 is a startup migration, but
	// not all deployments enable dbConn (traffic-only roles in some clusters),
	// and a fresh install may not have run migrations yet.
	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_matviews
			WHERE schemaname = 'public' AND matviewname = $1
		)
	`, viewName).Scan(&exists)
	if err != nil {
		return result, fmt.Errorf("pg_matviews existence check: %w", err)
	}
	if !exists {
		// View not present — not an error, just a skip. Consumers (admin
		// analytics endpoints) already fall back to base-view queries, so
		// there's no user-visible outage. Keep ViewExists=false so callers
		// do not publish a successful freshness timestamp for a skipped check.
		slog.Warn("materialized view does not exist, skipping consistency check",
			"view", viewName)
		return result, nil
	}
	result.ViewExists = true

	// Build the comparison SQL. The primary analytics view compares
	// (task_type, model) buckets; the audit summary compares tenant buckets.
	// Both queries use the same seven-day filter as migration 632.
	var sql string
	switch viewName {
	case MVDriftViewRoutingAnalytics7d:
		sql = routingAnalyticsConsistencySQL
	case MVDriftViewRoutingAuditSummary7d:
		sql = routingAuditSummaryConsistencySQL
	default:
		slog.Warn("consistency check not implemented for this view, skipping",
			"view", viewName)
		return result, nil
	}

	rows, err := pool.Query(ctx, sql)
	if err != nil {
		return result, fmt.Errorf("consistency check query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var taskType, model string
		var mvCount, baseCount, absDiff int64
		var diffPct float64

		if err := rows.Scan(&taskType, &model, &mvCount, &baseCount, &absDiff, &diffPct); err != nil {
			return result, fmt.Errorf("scan consistency row: %w", err)
		}

		result.DiffRowCount++

		if diffPct > result.MaxPct {
			result.MaxPct = diffPct
		}
		if absDiff > result.MaxAbs {
			result.MaxAbs = absDiff
		}

		// Breach = both thresholds exceeded (percentage AND absolute).
		// Single-row buckets easily hit 100% drift from one late arrival;
		// the absolute floor (default 100) filters those out.
		if diffPct > MVDriftAlertPct && absDiff > MVDriftAlertAbs {
			result.BreachCount++
		}
	}

	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("consistency check iteration: %w", err)
	}

	return result, nil
}

// RecordMVConsistency updates Prometheus metrics with the consistency check
// result for the given view. Should be called immediately after CheckMVConsistency
// when it returns without error.
func RecordMVConsistency(viewName string, res MVConsistencyResult) {
	if !res.ViewExists {
		return
	}
	metrics.RoutingAnalyticsMVDriftPct.WithLabelValues(viewName).Set(res.MaxPct)
	metrics.RoutingAnalyticsMVDriftAbs.WithLabelValues(viewName).Set(float64(res.MaxAbs))
	metrics.RoutingAnalyticsMVBreachCount.WithLabelValues(viewName).Set(float64(res.BreachCount))
	metrics.RoutingAnalyticsMVConsistencyLastUnix.WithLabelValues(viewName).Set(float64(time.Now().Unix()))
}

// RecordMVConsistencyError increments the consistency check error counter with
// a reason label. Called when CheckMVConsistency returns an error (SQL failure,
// connection lost, etc.). Does NOT update the _last_unix timestamp so the
// staleness alert can fire when checks are broken.
func RecordMVConsistencyError(viewName, reason string) {
	metrics.RoutingAnalyticsMVConsistencyErrorsTotal.WithLabelValues(viewName, reason).Inc()
}

// routingAnalyticsConsistencySQL compares routing_analytics_7d against the
// base view by (task_type, model) bucket. FULL OUTER JOIN keeps both sides'
// rows; the query returns only buckets with non-zero drift, ordered by
// abs_diff DESC. Mirrors migration 632's WHERE + GROUP BY logic.
const routingAnalyticsConsistencySQL = `
WITH mv_data AS (
  SELECT
    effective_task_type,
    effective_model,
    SUM(request_count)::bigint AS mv_count
  FROM routing_analytics_7d
  WHERE effective_model IS NOT NULL
  GROUP BY effective_task_type, effective_model
),
base_data AS (
  SELECT
    COALESCE(
      NULLIF(task_type, ''),
      CASE WHEN COALESCE(is_auto_request, FALSE) THEN 'unknown' ELSE '__specified__' END
    ) AS task_type,
    COALESCE(NULLIF(outbound_model, ''), client_model) AS model,
    COUNT(*)::bigint AS base_count
  FROM request_logs_with_current_month_without_customer_id
  WHERE ts >= NOW() - INTERVAL '7 days'
    AND COALESCE(origin_stage, '') NOT IN ('self_check', 'node_probe', 'system_health', 'probe_direct', 'probe_v2', 'model_probe', 'passive_probe', 'manual')
    AND COALESCE(task_type, '') <> 'probe_triggered'
    AND COALESCE(request_id, '') NOT LIKE 'probe-%'
    AND (
      COALESCE(is_auto_request, FALSE) = TRUE
      OR (COALESCE(is_auto_request, FALSE) = FALSE AND client_model IS NOT NULL AND client_model <> '')
    )
    AND COALESCE(NULLIF(outbound_model, ''), client_model) IS NOT NULL
	GROUP BY
	  COALESCE(
	    NULLIF(task_type, ''),
	    CASE WHEN COALESCE(is_auto_request, FALSE) THEN 'unknown' ELSE '__specified__' END
	  ),
	  COALESCE(NULLIF(outbound_model, ''), client_model)
)
SELECT
  COALESCE(mv.effective_task_type, base.task_type) AS dim1,
  COALESCE(mv.effective_model, base.model) AS dim2,
  COALESCE(mv.mv_count, 0) AS mv_count,
  COALESCE(base.base_count, 0) AS base_count,
  ABS(COALESCE(mv.mv_count, 0) - COALESCE(base.base_count, 0)) AS abs_diff,
  CASE
    WHEN COALESCE(base.base_count, 0) > 0
      THEN ROUND((ABS(COALESCE(mv.mv_count, 0) - COALESCE(base.base_count, 0))::numeric
                  / base.base_count::numeric) * 100, 2)
    ELSE 0
  END AS diff_pct
FROM mv_data mv
FULL OUTER JOIN base_data base
  ON mv.effective_task_type = base.task_type
 AND mv.effective_model = base.model
WHERE ABS(COALESCE(mv.mv_count, 0) - COALESCE(base.base_count, 0)) > 0
ORDER BY abs_diff DESC
LIMIT 200;
`

// routingAuditSummaryConsistencySQL compares routing_audit_summary_7d against
// the base view by tenant_id bucket. Same FULL OUTER JOIN shape as
// routingAnalyticsConsistencySQL, but grouped by tenant instead of (task,model).
// tenant_id is COALESCEd to ” (matching migration 632's unique-index text
// sentinel) so NULL-tenant rows on both sides join correctly instead of
// producing a spurious "missing" diff.
const routingAuditSummaryConsistencySQL = `
WITH mv_data AS (
  SELECT
    COALESCE(tenant_id, '') AS tenant_key,
    total_requests::bigint AS mv_count
  FROM routing_audit_summary_7d
),
base_data AS (
  SELECT
    COALESCE(tenant_id, '') AS tenant_key,
    COUNT(*)::bigint AS base_count
  FROM request_logs_with_current_month_without_customer_id
  WHERE ts >= NOW() - INTERVAL '7 days'
    AND COALESCE(origin_stage, '') NOT IN ('self_check', 'node_probe', 'system_health', 'probe_direct', 'probe_v2', 'model_probe', 'passive_probe', 'manual')
    AND COALESCE(task_type, '') <> 'probe_triggered'
    AND COALESCE(request_id, '') NOT LIKE 'probe-%'
    AND (
      COALESCE(is_auto_request, FALSE) = TRUE
      OR (COALESCE(is_auto_request, FALSE) = FALSE AND client_model IS NOT NULL AND client_model <> '')
    )
  GROUP BY COALESCE(tenant_id, '')
)
SELECT
  COALESCE(mv.tenant_key, base.tenant_key) AS dim1,
  '' AS dim2,
  COALESCE(mv.mv_count, 0) AS mv_count,
  COALESCE(base.base_count, 0) AS base_count,
  ABS(COALESCE(mv.mv_count, 0) - COALESCE(base.base_count, 0)) AS abs_diff,
  CASE
    WHEN COALESCE(base.base_count, 0) > 0
      THEN ROUND((ABS(COALESCE(mv.mv_count, 0) - COALESCE(base.base_count, 0))::numeric
                  / base.base_count::numeric) * 100, 2)
    ELSE 0
  END AS diff_pct
FROM mv_data mv
FULL OUTER JOIN base_data base
  ON mv.tenant_key = base.tenant_key
WHERE ABS(COALESCE(mv.mv_count, 0) - COALESCE(base.base_count, 0)) > 0
ORDER BY abs_diff DESC
LIMIT 200;
`
