package provider

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	candidateDiagnosticMetricsOnce sync.Once
	candidateDiagnosticMetrics     *prometheus.CounterVec
	candidateDiagnosticEvents      = []string{
		"db_empty",
		"db_empty_fallback",
		"cache_empty",
		"db_unavailable",
		"stale_cache_empty",
		"enrich_empty",
		"db_query_retry",
		"other",
	}
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
		initializeCandidateDiagnosticMetrics()
	})
}

func initializeCandidateDiagnosticMetrics() {
	for _, event := range candidateDiagnosticEvents {
		candidateDiagnosticMetrics.WithLabelValues(event).Add(0)
	}
}

func init() { registerCandidateDiagnosticMetrics() }

// recordCandidateDiagnostic records one of the fixed, low-cardinality event
// types. Unknown values are deliberately collapsed into "other".
func recordCandidateDiagnostic(event string) {
	allowed := false
	for _, candidateEvent := range candidateDiagnosticEvents {
		if event == candidateEvent {
			allowed = true
			break
		}
	}
	if !allowed {
		event = "other"
	}
	candidateDiagnosticMetrics.WithLabelValues(event).Inc()
}
