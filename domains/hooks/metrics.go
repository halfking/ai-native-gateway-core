package hooks

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// PrometheusMetricsCollector 实现基于Prometheus的MetricsCollector
type PrometheusMetricsCollector struct {
	executionsTotal *prometheus.CounterVec
	duration        *prometheus.HistogramVec
	failuresTotal   *prometheus.CounterVec
	skippedTotal    *prometheus.CounterVec
	timeoutTotal    *prometheus.CounterVec
}

// NewMetricsCollector 创建新的Prometheus指标收集器
func NewMetricsCollector() *PrometheusMetricsCollector {
	return &PrometheusMetricsCollector{
		executionsTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llmgw_hook_executions_total",
				Help: "Total number of hook executions",
			},
			[]string{"hook", "phase", "status"},
		),
		duration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "llmgw_hook_duration_seconds",
				Help:    "Hook execution duration in seconds",
				Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5},
			},
			[]string{"hook", "phase"},
		),
		failuresTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llmgw_hook_failures_total",
				Help: "Total number of hook failures",
			},
			[]string{"hook", "phase", "error_type"},
		),
		skippedTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llmgw_hook_skipped_total",
				Help: "Total number of hooks skipped",
			},
			[]string{"hook", "phase"},
		),
		timeoutTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llmgw_hook_timeout_total",
				Help: "Total number of hook timeouts",
			},
			[]string{"hook", "phase"},
		),
	}
}

// RecordHookExecution 记录Hook执行
func (mc *PrometheusMetricsCollector) RecordHookExecution(hookName string, phase Phase, duration time.Duration, success bool) {
	status := "success"
	if !success {
		status = "failure"
	}

	mc.executionsTotal.WithLabelValues(hookName, string(phase), status).Inc()
	mc.duration.WithLabelValues(hookName, string(phase)).Observe(duration.Seconds())
}

// RecordHookFailure 记录Hook失败
func (mc *PrometheusMetricsCollector) RecordHookFailure(hookName string, phase Phase, errorType string) {
	mc.failuresTotal.WithLabelValues(hookName, string(phase), errorType).Inc()
}

// RecordHookSkipped 记录Hook被跳过
func (mc *PrometheusMetricsCollector) RecordHookSkipped(hookName string, phase Phase) {
	mc.skippedTotal.WithLabelValues(hookName, string(phase)).Inc()
}

// RecordHookTimeout 记录Hook超时
func (mc *PrometheusMetricsCollector) RecordHookTimeout(hookName string, phase Phase) {
	mc.timeoutTotal.WithLabelValues(hookName, string(phase)).Inc()
}

// NoOpMetricsCollector 实现无操作的MetricsCollector
type NoOpMetricsCollector struct{}

// NewNoOpMetricsCollector 创建无操作的指标收集器
func NewNoOpMetricsCollector() *NoOpMetricsCollector {
	return &NoOpMetricsCollector{}
}

func (mc *NoOpMetricsCollector) RecordHookExecution(hookName string, phase Phase, duration time.Duration, success bool) {
}

func (mc *NoOpMetricsCollector) RecordHookFailure(hookName string, phase Phase, errorType string) {
}

func (mc *NoOpMetricsCollector) RecordHookSkipped(hookName string, phase Phase) {
}

func (mc *NoOpMetricsCollector) RecordHookTimeout(hookName string, phase Phase) {
}
