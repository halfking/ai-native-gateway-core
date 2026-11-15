package approval

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// ApprovalRoutingTotal tracks routing decisions by provider and sensitivity level.
	ApprovalRoutingTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_approval_routing_total",
			Help: "Approval routing decisions by provider and sensitivity level.",
		},
		[]string{"provider", "sensitivity"},
	)

	// ApprovalDegradationTotal tracks Casdoor → native degradations.
	ApprovalDegradationTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_approval_degradation_total",
			Help: "Approval system degradations (Casdoor unavailable → native fallback).",
		},
		[]string{"reason"},
	)

	// ApprovalCreationTotal tracks approval creations by provider and status.
	ApprovalCreationTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_approval_creation_total",
			Help: "Approval task creations by provider and result.",
		},
		[]string{"provider", "status"},
	)

	// ApprovalErrorTotal tracks errors by provider and operation.
	ApprovalErrorTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_approval_error_total",
			Help: "Approval operation errors by provider and operation.",
		},
		[]string{"provider", "operation"},
	)

	// ApprovalDecisionDurationSeconds tracks approval decision latency.
	ApprovalDecisionDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "llmgw_approval_decision_duration_seconds",
			Help:    "Approval decision duration from creation to resolution.",
			Buckets: []float64{1, 5, 10, 30, 60, 300, 600, 1800, 3600},
		},
		[]string{"provider", "status"},
	)
)

// RecordApprovalRouting records a routing decision.
func RecordApprovalRouting(provider, sensitivity string) {
	ApprovalRoutingTotal.WithLabelValues(provider, sensitivity).Inc()
}

// RecordApprovalDegradation records a degradation event.
func RecordApprovalDegradation(reason string) {
	ApprovalDegradationTotal.WithLabelValues(reason).Inc()
}

// RecordApprovalCreation records an approval creation result.
func RecordApprovalCreation(provider, status string) {
	ApprovalCreationTotal.WithLabelValues(provider, status).Inc()
}

// RecordApprovalError records an operation error.
func RecordApprovalError(provider, operation string, err error) {
	if err != nil {
		ApprovalErrorTotal.WithLabelValues(provider, operation).Inc()
	}
}

// RecordApprovalDecision records a decision outcome with latency.
func RecordApprovalDecision(provider, status string, durationSeconds float64) {
	ApprovalDecisionDurationSeconds.WithLabelValues(provider, status).Observe(durationSeconds)
}
