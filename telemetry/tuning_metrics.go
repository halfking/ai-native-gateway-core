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

import (
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
