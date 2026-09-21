package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var successfulResponseBodyMissingTotal = promauto.NewCounter(prometheus.CounterOpts{
	Name: "llm_gateway_success_response_body_missing_total",
	Help: "Persisted successful request-log entries that have no response body.",
})

// RecordSuccessfulResponseBodyMissing records a data-quality gap after the
// successful request log transaction commits. It deliberately has no request,
// tenant, credential, or model labels to avoid unbounded metric cardinality.
func RecordSuccessfulResponseBodyMissing() {
	successfulResponseBodyMissingTotal.Inc()
}
