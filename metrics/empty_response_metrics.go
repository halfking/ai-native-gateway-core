package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	emptyResponseAttempts = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_gateway_empty_response_attempts_total",
		Help: "Empty-response attempts by normalized stream detection reason.",
	}, []string{"reason"})
	emptyResponsePenaltyApplied = promauto.NewCounter(prometheus.CounterOpts{
		Name: "llm_gateway_ursm_empty_response_penalty_applied_total",
		Help: "URSM routing scores that applied a binding-scoped empty-response penalty.",
	})
)

func RecordEmptyResponseAttempt(reason string) {
	switch reason {
	case "empty_stream_no_content":
		reason = "done_no_content"
	case "early_empty_detection":
		reason = "early_empty"
	default:
		reason = "other"
	}
	emptyResponseAttempts.WithLabelValues(reason).Inc()
}

func RecordURSMSoftEmptyResponsePenalty() {
	emptyResponsePenaltyApplied.Inc()
}
