package circuit

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// CircuitBreakerState 熔断器状态 (0=closed, 1=open, 2=half_open)
	CircuitBreakerState = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "llm_gateway_circuit_breaker_state",
			Help: "Circuit breaker state (0=closed, 1=open, 2=half_open)",
		},
		[]string{"provider"},
	)

	// CircuitBreakerTriggered 熔断触发总数
	CircuitBreakerTriggered = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_gateway_circuit_breaker_triggered_total",
			Help: "Total number of circuit breaker triggers",
		},
		[]string{"provider", "reason"},
	)

	// CircuitBreakerRejected 被熔断拒绝的请求数
	CircuitBreakerRejected = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_gateway_circuit_breaker_rejected_total",
			Help: "Total number of requests rejected by circuit breaker",
		},
		[]string{"provider"},
	)

	// CircuitBreakerRequestsTotal 熔断器总请求数
	CircuitBreakerRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_gateway_circuit_breaker_requests_total",
			Help: "Total requests through circuit breaker",
		},
		[]string{"provider", "result"}, // result: success | error | rejected
	)

	// CircuitBreakerErrorRate 当前错误率
	CircuitBreakerErrorRate = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "llm_gateway_circuit_breaker_error_rate",
			Help: "Current error rate in the circuit breaker sliding window",
		},
		[]string{"provider"},
	)
)

// MetricsBreaker 是带 Prometheus metrics 的熔断器包装
type MetricsBreaker struct {
	breaker  Breaker
	provider string
}

// NewMetricsBreaker 创建一个带 metrics 的熔断器
func NewMetricsBreaker(config Config, provider string) Breaker {
	return &MetricsBreaker{
		breaker:  NewBreaker(config),
		provider: provider,
	}
}

// Call 包装调用并记录 metrics
func (mb *MetricsBreaker) Call(ctx context.Context, fn func() error) error {
	state := mb.breaker.State()

	// 记录当前状态
	CircuitBreakerState.WithLabelValues(mb.provider).Set(float64(state))

	// 如果是 Open 状态，记录拒绝
	if state == StateOpen {
		CircuitBreakerRejected.WithLabelValues(mb.provider).Inc()
		CircuitBreakerRequestsTotal.WithLabelValues(mb.provider, "rejected").Inc()
	}

	err := mb.breaker.Call(ctx, fn)

	// 记录结果
	if err == nil {
		CircuitBreakerRequestsTotal.WithLabelValues(mb.provider, "success").Inc()
	} else if err == ErrOpen {
		// 已在上面记录
	} else {
		CircuitBreakerRequestsTotal.WithLabelValues(mb.provider, "error").Inc()
	}

	// 更新错误率
	metrics := mb.breaker.Metrics()
	CircuitBreakerErrorRate.WithLabelValues(mb.provider).Set(metrics.ErrorRate)

	return err
}

// State 返回当前状态
func (mb *MetricsBreaker) State() State {
	return mb.breaker.State()
}

// Reset 重置熔断器
func (mb *MetricsBreaker) Reset() {
	mb.breaker.Reset()
	CircuitBreakerState.WithLabelValues(mb.provider).Set(float64(StateClosed))
}

// Metrics 返回指标
func (mb *MetricsBreaker) Metrics() Metrics {
	return mb.breaker.Metrics()
}

// Record 记录一次调用结果
func (mb *MetricsBreaker) Record(success bool) {
	mb.breaker.Record(success)

	// 更新 metrics
	if success {
		CircuitBreakerRequestsTotal.WithLabelValues(mb.provider, "success").Inc()
	} else {
		CircuitBreakerRequestsTotal.WithLabelValues(mb.provider, "error").Inc()
	}

	metrics := mb.breaker.Metrics()
	CircuitBreakerErrorRate.WithLabelValues(mb.provider).Set(metrics.ErrorRate)
	CircuitBreakerState.WithLabelValues(mb.provider).Set(float64(mb.breaker.State()))
}

// updateStateMetrics 更新状态相关 metrics (在状态转换时调用)
func (mb *MetricsBreaker) updateStateMetrics(oldState, newState State, reason string) {
	CircuitBreakerState.WithLabelValues(mb.provider).Set(float64(newState))

	if newState == StateOpen && oldState != StateOpen {
		CircuitBreakerTriggered.WithLabelValues(mb.provider, reason).Inc()
	}
}
