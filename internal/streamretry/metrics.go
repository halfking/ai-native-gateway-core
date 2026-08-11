package streamretry

import "github.com/prometheus/client_golang/prometheus"

var (
	streamRetryAttemptsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "llm_gateway",
		Subsystem: "stream_retry",
		Name:      "attempts_total",
		Help:      "Total streaming request attempts executed by the retry wrapper.",
	})
	streamRetryRetriesTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "llm_gateway",
		Subsystem: "stream_retry",
		Name:      "retries_total",
		Help:      "Total retry attempts scheduled by the streaming retry wrapper.",
	})
	streamRetrySuccessTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "llm_gateway",
		Subsystem: "stream_retry",
		Name:      "success_total",
		Help:      "Total streaming executions that completed successfully.",
	})
	streamRetryExhaustedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "llm_gateway",
		Subsystem: "stream_retry",
		Name:      "exhausted_total",
		Help:      "Total streaming executions that ended after retry exhaustion.",
	})
)

func init() {
	prometheus.MustRegister(
		streamRetryAttemptsTotal,
		streamRetryRetriesTotal,
		streamRetrySuccessTotal,
		streamRetryExhaustedTotal,
	)
}

func recordExecutionMetrics(metrics WrapperMetrics, err error) {
	streamRetryAttemptsTotal.Add(float64(metrics.TotalAttempts))
	streamRetryRetriesTotal.Add(float64(metrics.TotalRetries))
	if err == nil {
		streamRetrySuccessTotal.Inc()
		return
	}
	if metrics.TotalRetries > 0 && metrics.SuccessAttempt < 0 {
		streamRetryExhaustedTotal.Inc()
	}
}
