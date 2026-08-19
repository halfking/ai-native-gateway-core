// Package metrics - stats_reconciliation_metrics.go
// Prometheus metrics for the stats reconciliation pipeline.
//
// Metrics exposed:
//   - llm_gateway_stats_reconciliation_runs_total       (CounterVec) reconciliation run terminal status (logical, post-Go decision)
//   - llm_gateway_stats_reconciliation_diffs_total      (CounterVec) reconciliation diffs by terminal resolution
//   - llm_gateway_stats_adjustments_total               (CounterVec) admin approval adjustments by action/result
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	dto "github.com/prometheus/client_model/go"
)

var (
	// statsReconciliationRuns counts reconciliation runs by terminal status.
	//
	// status is fixed: completed | failed. The counter reflects the Go-level
	// outcome (i.e. after ReconcilePeriod decided which branch to take);
	// it does NOT distinguish a logical "completed" from a persisted
	// "completed" because finishRun swallows UPDATE errors. Operators
	// should pair this metric with stats_reconciliation_runs.status from
	// the database when investigating drift.
	statsReconciliationRuns = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_gateway_stats_reconciliation_runs_total",
			Help: "Stats reconciliation runs by terminal status (logical outcome).",
		},
		[]string{"status"}, // completed | failed
	)

	// statsReconciliationDiffs counts reconciliation diffs by terminal
	// resolution. The counter only increments after the persistence
	// boundary (RowsAffected for auto-repaired, diff count for unresolved),
	// so it can be safely used as a rate of persisted diffs.
	statsReconciliationDiffs = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_gateway_stats_reconciliation_diffs_total",
			Help: "Stats reconciliation diffs by terminal resolution.",
		},
		[]string{"resolution"}, // open | auto_repaired
	)

	// statsAdjustments counts admin approval/rejection outcomes. The
	// committed label is only incremented after tx.Commit succeeds;
	// any error path or rollback increments failed instead.
	statsAdjustments = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_gateway_stats_adjustments_total",
			Help: "Stats adjustments processed via admin approval by action and result.",
		},
		[]string{"action", "result"}, // action: approve|reject, result: committed|failed
	)
)

// RecordStatsReconciliationRun records the terminal status of a single
// ReconcilePeriod call. n lets callers pass a pre-aggregated count; the
// metric is only incremented when n > 0 to avoid spawning empty label
// combinations on the registry.
func RecordStatsReconciliationRun(status string, n int64) {
	if n <= 0 {
		return
	}
	statsReconciliationRuns.WithLabelValues(status).Add(float64(n))
}

// ObserveStatsReconciliationDiffs records the number of reconciliation
// diffs that landed in a given terminal resolution bucket. n must be
// the count of persisted rows (e.g. RowsAffected for auto_repaired, or
// the diff count for unresolved/open diffs).
func ObserveStatsReconciliationDiffs(resolution string, n int64) {
	if n <= 0 {
		return
	}
	statsReconciliationDiffs.WithLabelValues(resolution).Add(float64(n))
}

// RecordStatsAdjustment records the outcome of an admin approval batch.
// result must be committed after tx.Commit succeeds; any pre-commit
// failure (loop error, commit error, schema mismatch) must use failed
// so that the committed vs failed ratio reflects real durability.
func RecordStatsAdjustment(action, result string, n int64) {
	if n <= 0 {
		return
	}
	statsAdjustments.WithLabelValues(action, result).Add(float64(n))
}

// StatsReconciliationRunsVec exposes the runs CounterVec so tests can
// assert on persisted counts without leaking the internal symbol.
func StatsReconciliationRunsVec(status string) interface{ Write(*dto.Metric) error } {
	return statsReconciliationRuns.WithLabelValues(status)
}

// StatsReconciliationDiffsVec exposes the diffs CounterVec for tests.
func StatsReconciliationDiffsVec(resolution string) interface{ Write(*dto.Metric) error } {
	return statsReconciliationDiffs.WithLabelValues(resolution)
}

// StatsAdjustmentsVec exposes the adjustments CounterVec for tests.
func StatsAdjustmentsVec(action, result string) interface{ Write(*dto.Metric) error } {
	return statsAdjustments.WithLabelValues(action, result)
}