package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	statsShadowComparisons = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_gateway_stats_shadow_comparisons_total",
			Help: "Statistics shadow comparisons by endpoint and outcome.",
		},
		[]string{"endpoint", "result"},
	)
	statsShadowDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "llm_gateway_stats_shadow_duration_seconds",
			Help: "Duration of statistics shadow comparisons.",
		},
		[]string{"endpoint"},
	)
	statsShadowDropped = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_gateway_stats_shadow_dropped_total",
			Help: "Statistics shadow comparisons dropped because the bounded queue was full.",
		},
		[]string{"endpoint"},
	)
)

// RecordStatsShadowComparison records a low-cardinality comparison outcome.
func RecordStatsShadowComparison(endpoint, result string) {
	statsShadowComparisons.WithLabelValues(endpoint, result).Inc()
}

// ObserveStatsShadowDuration records a shadow query duration.
func ObserveStatsShadowDuration(endpoint string, duration time.Duration) {
	statsShadowDuration.WithLabelValues(endpoint).Observe(duration.Seconds())
}

// RecordStatsShadowDropped records a bounded-queue drop.
func RecordStatsShadowDropped(endpoint string) {
	statsShadowDropped.WithLabelValues(endpoint).Inc()
}
