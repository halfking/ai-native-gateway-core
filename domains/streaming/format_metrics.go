package streaming

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// formatDetectionTotal counts format detection attempts by pattern and source.
	formatDetectionTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_format_detection_total",
			Help: "Total number of format detection attempts",
		},
		[]string{"pattern", "source"}, // source: "cache" or "detect"
	)

	// formatFixAppliedTotal counts format fixes applied by pattern and fix type.
	formatFixAppliedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_format_fix_applied_total",
			Help: "Total number of format fixes applied",
		},
		[]string{"pattern", "fix_type"},
	)

	// formatConfidence records the distribution of detection confidence scores.
	formatConfidence = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "llmgw_format_confidence",
			Help:    "Distribution of format detection confidence scores",
			Buckets: []float64{0.5, 0.6, 0.7, 0.8, 0.9, 0.95, 1.0},
		},
	)

	// formatCacheTotal counts cache hits and misses.
	formatCacheTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_format_cache_total",
			Help: "Format cache operations (hit or miss)",
		},
		[]string{"result"}, // "hit" or "miss"
	)

	// formatValidationFailureTotal counts validation failures by error type.
	formatValidationFailureTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_format_validation_failure_total",
			Help: "Total number of format validation failures",
		},
		[]string{"error_type"}, // "invalid_messages", "no_user_message", etc.
	)
)
