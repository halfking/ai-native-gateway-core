package telemetry

// tuning_metrics.go — Prometheus metrics for the auto-route feedback loop.
//
// Exposed metrics (collected at /metrics):
//
//   llm_gateway_tuning_signals_total{task_type,classifier}
//     Counter — number of tuning_signals rows written
//
//   llm_gateway_tuning_signal_quality_score{task_type}
//     Gauge — running average of quality_score (1-min window, reset on each sample)
//
//   llm_gateway_tuning_proposals_total{category,status}
//     Counter — number of tuning_proposals by lifecycle status
//
//   llm_gateway_tuning_signal_dropped_total
//     Counter — number of signals dropped due to queue full (hot path overflow)
//
//   llm_gateway_tuning_signal_batch_size
//     Histogram — distribution of batch sizes seen by the writer
//
//   llm_gateway_tuning_params_loaded_total{source}
//     Counter — number of params loaded from each source
//       (default/feedback/manual) on each Reload
//
//   llm_gateway_llm_classifier_total{outcome}
//     Counter — LLM fallback classifier calls by outcome
//       (success/failure/timeout/breaker_open/disabled)
//
//   llm_gateway_llm_classifier_latency_seconds
//     Histogram — LLM fallback call latency
//
//   llm_gateway_llm_circuit_breaker_state
//     Gauge — current breaker state (0=closed, 1=half-open, 2=open)
//
//   llm_gateway_llm_circuit_breaker_consecutive_failures
//     Gauge — current consecutive failure count

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// tuningSignalsTotal counts tuning_signals rows written.
	tuningSignalsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_gateway_tuning_signals_total",
		Help: "Total number of tuning_signals rows written for the auto-route feedback loop.",
	}, []string{"task_type", "classifier"})

	// tuningSignalQuality is a per-task-type running average of quality_score.
	// Reset on each scrape to maintain a moving 1-min average.
	tuningSignalQuality = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_gateway_tuning_signal_quality_score",
		Help: "Average quality_score of tuning signals in the current scrape window, by task type.",
	}, []string{"task_type"})

	// tuningProposalsTotal counts proposal lifecycle transitions.
	tuningProposalsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_gateway_tuning_proposals_total",
		Help: "Number of tuning_proposals rows, by category and status.",
	}, []string{"category", "status"})

	// tuningSignalDroppedTotal counts signals dropped due to queue overflow.
	tuningSignalDroppedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "llm_gateway_tuning_signal_dropped_total",
		Help: "Number of tuning signals dropped because the writer queue was full.",
	})

	// tuningSignalBatchSize is a histogram of batch sizes flushed to DB.
	tuningSignalBatchSize = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "llm_gateway_tuning_signal_batch_size",
		Help:    "Distribution of tuning_signals batch sizes flushed to the DB.",
		Buckets: []float64{1, 5, 10, 25, 50, 100, 250, 500},
	})

	// tuningParamsLoaded counts parameter loads per source.
	tuningParamsLoaded = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_gateway_tuning_params_loaded_total",
		Help: "Number of tuning_params loaded, grouped by source (default/manual/feedback).",
	}, []string{"source"})

	// llmClassifierTotal counts LLM fallback classifier calls.
	llmClassifierTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_gateway_llm_classifier_total",
		Help: "LLM fallback classifier calls, by outcome (success/failure/timeout/breaker_open/disabled).",
	}, []string{"outcome"})

	// llmClassifierLatency is the distribution of LLM fallback latency.
	llmClassifierLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "llm_gateway_llm_classifier_latency_seconds",
		Help:    "LLM fallback classifier call latency in seconds.",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 3, 5, 10},
	})

	// llmCircuitBreakerState tracks the current breaker state.
	// 0 = closed (normal), 1 = half-open (cooldown passed), 2 = open.
	llmCircuitBreakerState = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "llm_gateway_llm_circuit_breaker_state",
		Help: "Current LLM circuit breaker state (0=closed, 1=half-open, 2=open).",
	})

	// llmCircuitBreakerConsecutiveFailures is the current failure count.
	llmCircuitBreakerConsecutiveFailures = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "llm_gateway_llm_circuit_breaker_consecutive_failures",
		Help: "Current consecutive failure count in the LLM circuit breaker.",
	})
)

// RecordTuningSignalWritten increments the per-task-type counter and
// updates the rolling quality gauge. Safe for concurrent use.
func RecordTuningSignalWritten(taskType, classifier string, quality float64) {
	tuningSignalsTotal.WithLabelValues(taskType, classifier).Inc()
	tuningSignalQuality.WithLabelValues(taskType).Set(quality)
}

// RecordTuningProposalTransition increments the proposal lifecycle counter.
func RecordTuningProposalTransition(category, status string) {
	tuningProposalsTotal.WithLabelValues(category, status).Inc()
}

// RecordTuningSignalDropped increments the dropped-signal counter.
func RecordTuningSignalDropped() {
	tuningSignalDroppedTotal.Inc()
}

// RecordTuningSignalBatch observes a batch size.
func RecordTuningSignalBatch(size int) {
	tuningSignalBatchSize.Observe(float64(size))
}

// RecordTuningParamLoad increments the per-source param counter.
func RecordTuningParamLoad(source string) {
	tuningParamsLoaded.WithLabelValues(source).Inc()
}

// RecordLLMClassifierCall increments the LLM classifier outcome counter
// and observes the call latency. Safe for concurrent use.
//
// outcome is one of: "success", "failure", "timeout", "breaker_open",
// "disabled". A latency of 0 skips the histogram observation.
func RecordLLMClassifierCall(outcome string, latency time.Duration) {
	llmClassifierTotal.WithLabelValues(outcome).Inc()
	if latency > 0 {
		llmClassifierLatency.Observe(latency.Seconds())
	}
}

// RecordLLMCircuitBreakerState updates the breaker gauges.
// consecutive is the current failure count; open indicates the breaker
// is currently rejecting calls.
func RecordLLMCircuitBreakerState(consecutive int, open bool) {
	var state float64
	switch {
	case open:
		state = 2
	case consecutive > 0:
		state = 1
	default:
		state = 0
	}
	llmCircuitBreakerState.Set(state)
	llmCircuitBreakerConsecutiveFailures.Set(float64(consecutive))
}
