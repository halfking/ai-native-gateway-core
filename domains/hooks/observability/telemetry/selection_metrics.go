package telemetry

// selection_metrics.go — Prometheus metrics for the auto-route selection log.
//
// Exposed metrics (collected at /metrics):
//
//	llm_gateway_auto_selections_total{task_type,affinity_applied,explore}
//	  Counter — auto_route_selections rows persisted. The affinity_applied and
//	  explore labels are what let you confirm the explore ratio is actually
//	  being honoured in production, and compare reward between the applied and
//	  explore arms during the shadow/rollout period.
//
//	llm_gateway_auto_selections_dropped_total
//	  Counter — rows lost to a full queue or a failed insert. Non-zero here
//	  means the learned ranking is being trained on a biased sample, so this is
//	  worth alerting on rather than merely graphing.

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	autoSelectionsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_gateway_auto_selections_total",
			Help: "Total auto-route selection rows persisted",
		},
		[]string{"task_type", "affinity_applied", "explore"},
	)

	autoSelectionsDroppedTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "llm_gateway_auto_selections_dropped_total",
			Help: "Total auto-route selection rows dropped (queue full or insert failure)",
		},
	)
)

// RecordAutoSelectionWritten increments the persisted-row counter.
func RecordAutoSelectionWritten(taskType string, affinityApplied, explore bool) {
	if taskType == "" {
		taskType = "unknown"
	}
	autoSelectionsTotal.WithLabelValues(
		taskType,
		strconv.FormatBool(affinityApplied),
		strconv.FormatBool(explore),
	).Inc()
}

// RecordAutoSelectionDropped increments the dropped-row counter.
func RecordAutoSelectionDropped() {
	autoSelectionsDroppedTotal.Inc()
}
