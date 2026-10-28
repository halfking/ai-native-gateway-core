package taskbus

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// 每 TaskType 暴露 pending / failed / dlq（沿用 outbox 指标文化）。
var (
	pendingGauge = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "taskbus_pending_count",
			Help: "Pending taskbus tasks by task_type",
		},
		[]string{"task_type"},
	)
	failedGauge = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "taskbus_failed_count",
			Help: "Failed (retryable) taskbus tasks by task_type",
		},
		[]string{"task_type"},
	)
	dlqGauge = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "taskbus_dlq_count",
			Help: "DLQ taskbus tasks by task_type",
		},
		[]string{"task_type"},
	)
	handledTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "taskbus_handled_total",
			Help: "Tasks handled by taskbus dispatcher",
		},
		[]string{"task_type", "result"},
	)
)

// SetStatusGauges 刷新某一 TaskType 的三态计数。
func SetStatusGauges(taskType string, pending, failed, dlq int) {
	pendingGauge.WithLabelValues(taskType).Set(float64(pending))
	failedGauge.WithLabelValues(taskType).Set(float64(failed))
	dlqGauge.WithLabelValues(taskType).Set(float64(dlq))
}

// RecordHandled 记录一次消费结果（ok|error|dlq）。
func RecordHandled(taskType, result string) {
	handledTotal.WithLabelValues(taskType, result).Inc()
}
