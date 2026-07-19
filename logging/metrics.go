package logging

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// LogsWritten 写入的日志数
	LogsWritten = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_gateway_logs_written_total",
			Help: "Total number of logs written",
		},
		[]string{"level"},
	)

	// LogErrors 日志错误数
	LogErrors = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "llm_gateway_log_errors_total",
			Help: "Total number of log errors",
		},
	)
)

// RecordLog 记录日志
func RecordLog(level string) {
	LogsWritten.WithLabelValues(level).Inc()
}

// RecordLogError 记录日志错误
func RecordLogError() {
	LogErrors.Inc()
}
