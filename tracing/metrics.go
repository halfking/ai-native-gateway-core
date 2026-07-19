package tracing

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// SpansCreated 创建的 span 数
	SpansCreated = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "llm_gateway_spans_created_total",
			Help: "Total number of spans created",
		},
	)

	// SpanDuration span 持续时间
	SpanDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "llm_gateway_span_duration_seconds",
			Help:    "Duration of spans",
			Buckets: []float64{0.001, 0.01, 0.1, 0.5, 1, 5, 10},
		},
		[]string{"operation"},
	)

	// TraceExports 导出的 trace 数
	TraceExports = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_gateway_trace_exports_total",
			Help: "Total number of trace exports",
		},
		[]string{"status"}, // status: success|failure
	)
)

// RecordSpan 记录 span
func RecordSpan(operation string, duration float64) {
	SpansCreated.Inc()
	SpanDuration.WithLabelValues(operation).Observe(duration)
}

// RecordTraceExport 记录 trace 导出
func RecordTraceExport(success bool) {
	status := "success"
	if !success {
		status = "failure"
	}
	TraceExports.WithLabelValues(status).Inc()
}
