// Package metrics — routing_analytics_mv_drift.go
//
// Prometheus metrics for the routing analytics materialized view
// consistency check (P2-D, 2026-09-01). Surfaced by
// bg.MaterializedViewRefresher after every refresh cycle so the same drift
// signal is visible to both:
//   - runtime callers (Prometheus scrape via /metrics)
//   - offline cron (scripts/monitoring/verify-mv-consistency.sh writes the
//     same metric to a textfile collector)
//
// The two paths use the same label shape and metric names so dashboards
// stay symmetric: view name is the only dimension (no per-bucket fan-out,
// since the breach-count gauge already conveys "how many buckets drifted").
//
// Cardinality is bounded: `view` is one of {routing_analytics_7d,
// routing_audit_summary_7d} — closed enum, see bg.MVDriftView* constants.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// RoutingAnalyticsMVDriftPct is the max percentage drift between the
	// materialized view aggregation and the base view aggregation across
	// all (task_type, model) buckets. Range [0, 100]; a fresh, healthy
	// view stays at 0 — anything above ~1% on a large bucket is a smell,
	// and >5% is the alert threshold (see
	// scripts/monitoring/verify-mv-consistency.sh).
	RoutingAnalyticsMVDriftPct = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "routing_analytics_mv_drift_pct",
		Help: "Max percentage drift between routing analytics materialized view and base view aggregations.",
	}, []string{"view"})

	// RoutingAnalyticsMVDriftAbs is the largest absolute request-count
	// delta in any single (task_type, model) bucket. Useful to
	// distinguish "tiny buckets are noisy" from "real big-bucket drift":
	// 100% drift on a 4-row bucket is 4 rows, while 5% drift on a 200k
	// bucket is 10k rows.
	RoutingAnalyticsMVDriftAbs = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "routing_analytics_mv_drift_abs",
		Help: "Max absolute request-count drift in any bucket between materialized and base view.",
	}, []string{"view"})

	// RoutingAnalyticsMVBreachCount is how many (task_type, model)
	// buckets exceeded the configured alert thresholds (default: pct > 5
	// AND abs > 100). Bounded by the row count of the base view, but in
	// practice <20 in healthy operation. Alert on >0 sustained.
	RoutingAnalyticsMVBreachCount = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "routing_analytics_mv_breach_count",
		Help: "Number of (task_type, model) buckets exceeding the drift alert threshold.",
	}, []string{"view"})

	// RoutingAnalyticsMVConsistencyLastUnix is the unix timestamp of the
	// last successful consistency check. Pair with
	// gateway_mv_refresh_last_success to spot "refresh is fine but check
	// is broken" outages — both should be recent; a stale timestamp means
	// the checker (or its goroutine) is wedged.
	RoutingAnalyticsMVConsistencyLastUnix = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "routing_analytics_mv_consistency_last_unix",
		Help: "Unix timestamp of the last completed routing analytics MV consistency check.",
	}, []string{"view"})

	// RoutingAnalyticsMVConsistencyErrorsTotal counts consistency check
	// failures (SQL error, missing view, pool error). Distinct from
	// routing_analytics_mv_refresh_failures_total: a refresh can succeed
	// while a subsequent check fails (e.g. base view dropped).
	RoutingAnalyticsMVConsistencyErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "routing_analytics_mv_consistency_errors_total",
		Help: "Total routing analytics MV consistency check failures.",
	}, []string{"view", "reason"})
)

func init() {
	// Pre-initialize time series for the two fixed views (closed enum) so
	// they appear in /metrics even before the first refresh cycle. Mirrors
	// the pattern in metrics/prometheus.go for ursmv2ShadowResult et al.
	for _, view := range []string{"routing_analytics_7d", "routing_audit_summary_7d"} {
		RoutingAnalyticsMVDriftPct.WithLabelValues(view).Set(0)
		RoutingAnalyticsMVDriftAbs.WithLabelValues(view).Set(0)
		RoutingAnalyticsMVBreachCount.WithLabelValues(view).Set(0)
		RoutingAnalyticsMVConsistencyLastUnix.WithLabelValues(view).Set(0)
	}
}
