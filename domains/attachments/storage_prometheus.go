// Package attachments - storage_prometheus.go
//
// Prometheus metrics 实现

package attachments

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// PrometheusMetrics Prometheus 实现的存储指标收集器
type PrometheusMetrics struct {
	// 操作计数器
	operationTotal *prometheus.CounterVec
	
	// 操作耗时直方图
	operationDuration *prometheus.HistogramVec
	
	// 传输字节数
	bytesTransferred *prometheus.CounterVec
	
	// 健康检查计数器
	healthCheckTotal *prometheus.CounterVec
	
	// 重试计数器
	retryTotal *prometheus.CounterVec
	
	// 重试次数直方图
	retryAttempts *prometheus.HistogramVec
}

// NewPrometheusMetrics 创建 Prometheus metrics 收集器
func NewPrometheusMetrics(namespace string) *PrometheusMetrics {
	if namespace == "" {
		namespace = "llm_gateway"
	}
	
	return &PrometheusMetrics{
		operationTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "storage",
				Name:      "operation_total",
				Help:      "Total number of storage operations",
			},
			[]string{"op", "backend", "success"},
		),
		
		operationDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: "storage",
				Name:      "operation_duration_seconds",
				Help:      "Duration of storage operations in seconds",
				Buckets:   prometheus.ExponentialBuckets(0.001, 2, 15), // 1ms ~ 16s
			},
			[]string{"op", "backend"},
		),
		
		bytesTransferred: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "storage",
				Name:      "bytes_transferred_total",
				Help:      "Total bytes transferred in storage operations",
			},
			[]string{"op", "backend"},
		),
		
		healthCheckTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "storage",
				Name:      "health_check_total",
				Help:      "Total number of health checks",
			},
			[]string{"backend", "success"},
		),
		
		retryTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "storage",
				Name:      "retry_total",
				Help:      "Total number of retried operations",
			},
			[]string{"op", "success"},
		),
		
		retryAttempts: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: "storage",
				Name:      "retry_attempts",
				Help:      "Number of retry attempts before success or failure",
				Buckets:   []float64{0, 1, 2, 3, 5, 10},
			},
			[]string{"op"},
		),
	}
}

// RecordOperation 实现 StorageMetrics 接口
func (m *PrometheusMetrics) RecordOperation(op, backend string, success bool, duration time.Duration, bytes int64) {
	successStr := boolToString(success)
	
	// 记录操作总数
	m.operationTotal.WithLabelValues(op, backend, successStr).Inc()
	
	// 记录操作耗时
	m.operationDuration.WithLabelValues(op, backend).Observe(duration.Seconds())
	
	// 记录传输字节数（仅 save/load 操作）
	if bytes > 0 && (op == "save" || op == "load") {
		m.bytesTransferred.WithLabelValues(op, backend).Add(float64(bytes))
	}
}

// RecordHealthCheck 实现 StorageMetrics 接口
func (m *PrometheusMetrics) RecordHealthCheck(backend string, success bool, duration time.Duration) {
	successStr := boolToString(success)
	m.healthCheckTotal.WithLabelValues(backend, successStr).Inc()
	
	// 健康检查也算一种操作
	m.operationDuration.WithLabelValues("health_check", backend).Observe(duration.Seconds())
}

// RecordRetry 记录重试操作（扩展方法）
func (m *PrometheusMetrics) RecordRetry(op string, attempts int, success bool) {
	successStr := boolToString(success)
	m.retryTotal.WithLabelValues(op, successStr).Inc()
	m.retryAttempts.WithLabelValues(op).Observe(float64(attempts))
}

// boolToString 转换布尔值为字符串
func boolToString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
