package streaming

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// goalRetryAttemptsTotal counts retry attempts by tenant and outcome.
	// Labels: tenant_id, cost_mode, outcome (success|exhausted|cancelled|error)
	goalRetryAttemptsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_goal_retry_attempts_total",
			Help: "Total number of Goal retry attempts by outcome",
		},
		[]string{"tenant_id", "cost_mode", "outcome"},
	)

	// goalRetryDuration measures total time spent in retry loops.
	// Labels: tenant_id, cost_mode
	goalRetryDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "llmgw_goal_retry_duration_seconds",
			Help:    "Total duration of Goal retry loops in seconds",
			Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 120},
		},
		[]string{"tenant_id", "cost_mode"},
	)

	// goalRetryCountDistribution tracks how many retries were performed per request.
	// Labels: tenant_id, cost_mode
	goalRetryCountDistribution = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "llmgw_goal_retry_count",
			Help:    "Distribution of retry counts per request",
			Buckets: []float64{0, 1, 2, 3, 5, 10},
		},
		[]string{"tenant_id", "cost_mode"},
	)

	// goalRetryPolicyResolutionTotal counts policy resolution calls.
	// Labels: tenant_id, cost_mode, source (resolver|fallback)
	goalRetryPolicyResolutionTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_goal_retry_policy_resolution_total",
			Help: "Total number of retry policy resolutions",
		},
		[]string{"tenant_id", "cost_mode", "source"},
	)

	// goalRetryCountPersistenceTotal counts retry count persistence operations.
	// Labels: tenant_id, status (success|failure|skipped)
	goalRetryCountPersistenceTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_goal_retry_count_persistence_total",
			Help: "Total number of retry count persistence attempts",
		},
		[]string{"tenant_id", "status"},
	)

	// goalActiveRetries tracks currently executing retry loops.
	// Labels: tenant_id
	goalActiveRetries = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "llmgw_goal_active_retries",
			Help: "Number of currently active retry loops",
		},
		[]string{"tenant_id"},
	)
)

// recordGoalRetryOutcome records the final outcome of a retry loop.
func recordGoalRetryOutcome(tenantID, costMode, outcome string, retriesPerformed int, duration time.Duration) {
	goalRetryAttemptsTotal.WithLabelValues(tenantID, costMode, outcome).Inc()
	goalRetryDuration.WithLabelValues(tenantID, costMode).Observe(duration.Seconds())
	goalRetryCountDistribution.WithLabelValues(tenantID, costMode).Observe(float64(retriesPerformed))
}

// recordGoalRetryPolicyResolution records a policy resolution event.
func recordGoalRetryPolicyResolution(tenantID, costMode, source string) {
	goalRetryPolicyResolutionTotal.WithLabelValues(tenantID, costMode, source).Inc()
}

// recordGoalRetryCountPersistence records a persistence attempt.
func recordGoalRetryCountPersistence(tenantID, status string) {
	goalRetryCountPersistenceTotal.WithLabelValues(tenantID, status).Inc()
}

// trackGoalActiveRetry increments/decrements the active retry gauge.
func trackGoalActiveRetry(tenantID string, delta float64) {
	goalActiveRetries.WithLabelValues(tenantID).Add(delta)
}
