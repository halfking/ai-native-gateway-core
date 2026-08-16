package provider

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	candidateDiagnosticMetricsOnce sync.Once
	candidateDiagnosticMetrics     *prometheus.CounterVec
)

func registerCandidateDiagnosticMetrics() {
	candidateDiagnosticMetricsOnce.Do(func() {
		candidateDiagnosticMetrics = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llmgw_routing_candidate_diagnostics_total",
				Help: "Total routing candidate diagnostic events.",
			},
			[]string{"event"},
		)
		prometheus.MustRegister(candidateDiagnosticMetrics)
	})
}

func init() { registerCandidateDiagnosticMetrics() }

// recordCandidateDiagnostic records one of the fixed, low-cardinality event
// types. Unknown values are deliberately collapsed into "other".
func recordCandidateDiagnostic(event string) {
	switch event {
	case "db_empty", "db_empty_fallback", "cache_empty", "db_unavailable", "stale_cache_empty", "enrich_empty", "db_query_retry", "other":
	default:
		event = "other"
	}
	candidateDiagnosticMetrics.WithLabelValues(event).Inc()
}
