// Package externalhost 的可观测性面：熔断器 + Prometheus 指标。
//
// 2026-11-27 修订（W3 上线前必修项③「熔断与观测」）：
//
// 熔断器语义（W3.2 fail-open 的延伸）：
//   - 连续 N 次失败（默认 5 次）后打开熔断，窗口内**跳过钩子调用**，直接原样直通。
//   - 打开窗口（默认 30s）结束后进入半开态：放一次探测调用，成功则闭合，
//     失败则重新打开并重置窗口。
//   - 熔断期间**不计入失败计数**（本来就没调），单独计入 skipped 指标。
//
// 观测维度（低基数，遵循 domains/hooks/handoff/metrics.go 的基数纪律）：
//   - 钩子点（on_request|on_response）
//   - 结果（continue|rewrite|block|timeout|crash|protocol|protected_field|hook_error|
//     skipped|circuit_open）
//   - 不含 request_id/session_id/tenant 等高基数标识。
package externalhost

import (
	"errors"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// externalhostMetrics 是外部钩子的 Prometheus 采集器集合。
type externalhostMetrics struct {
	calls    *prometheus.CounterVec   // labels: hook_point, result
	duration *prometheus.HistogramVec // labels: hook_point, result
	circuit  *prometheus.GaugeVec     // labels: hook_point（1=熔断打开）
}

var defaultExternalHostMetrics = newExternalHostMetrics()

func newExternalHostMetrics() *externalhostMetrics {
	return &externalhostMetrics{
		calls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "external_hook_calls_total",
			Help: "External hook invocations by hook point and result (continue|rewrite|block|timeout|crash|protocol|protected_field|hook_error|skipped|circuit_open).",
		}, []string{"hook_point", "result"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "external_hook_duration_seconds",
			Help:    "External hook call duration by hook point and result.",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 14), // 1ms → 16s
		}, []string{"hook_point", "result"}),
		circuit: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "external_hook_circuit_open",
			Help: "External hook circuit breaker state by hook point (1=open, 0=closed).",
		}, []string{"hook_point"}),
	}
}

func init() {
	collectors := []prometheus.Collector{
		defaultExternalHostMetrics.calls,
		defaultExternalHostMetrics.duration,
		defaultExternalHostMetrics.circuit,
	}
	for _, c := range collectors {
		if err := prometheus.Register(c); err != nil {
			var are prometheus.AlreadyRegisteredError
			if !errors.As(err, &are) {
				panic(err)
			}
		}
	}
}

// recordCall 记录一次钩子调用的结果与耗时。
func recordCall(hookPoint string, result string, d time.Duration) {
	defaultExternalHostMetrics.calls.WithLabelValues(hookPoint, result).Inc()
	defaultExternalHostMetrics.duration.WithLabelValues(hookPoint, result).Observe(d.Seconds())
}

// recordCircuitState 记录熔断器状态变化。
func recordCircuitState(hookPoint string, open bool) {
	v := 0.0
	if open {
		v = 1.0
	}
	defaultExternalHostMetrics.circuit.WithLabelValues(hookPoint).Set(v)
}

// CircuitBreaker 是外部钩子调用的熔断器。
//
// 零值不可用——请用 NewCircuitBreaker 构造。
type CircuitBreaker struct {
	mu sync.Mutex

	// threshold 连续失败多少次后打开熔断。
	threshold int
	// openWindow 熔断打开的持续时长；到期后进入半开态。
	openWindow time.Duration

	// consecutiveFailures 当前连续失败次数。
	consecutiveFailures int
	// openedAt 熔断打开的时刻；零值表示未打开。
	openedAt time.Time
	// halfOpenProbeInFlight 半开态是否已有探测在途，防止并发探测击穿。
	halfOpenProbeInFlight bool
}

// NewCircuitBreaker 构造熔断器。
//
// threshold <= 0 时用默认 5 次；openWindow <= 0 时用默认 30s。
func NewCircuitBreaker(threshold int, openWindow time.Duration) *CircuitBreaker {
	if threshold <= 0 {
		threshold = DefaultCircuitThreshold
	}
	if openWindow <= 0 {
		openWindow = DefaultCircuitOpenWindow
	}
	cb := &CircuitBreaker{threshold: threshold, openWindow: openWindow}
	recordCircuitState("all", false)
	return cb
}

// 熔断器默认参数。
const (
	// DefaultCircuitThreshold 默认连续失败阈值。
	DefaultCircuitThreshold = 5
	// DefaultCircuitOpenWindow 默认熔断打开时长。
	DefaultCircuitOpenWindow = 30 * time.Second
)

// Allow 判断是否允许发起一次钩子调用。
//
// 返回 true 表示允许调用；返回 false 表示熔断打开，本次应跳过（原样直通）。
// 半开态允许一次探测调用在途（并发下只放一个）。
func (cb *CircuitBreaker) Allow(hookPoint string) bool {
	if cb == nil {
		return true
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.openedAt.IsZero() {
		return true // 闭合态
	}

	// 熔断打开中：窗口未到期则拒绝；到期则进入半开态放一次探测。
	if time.Since(cb.openedAt) < cb.openWindow {
		return false
	}

	// 半开态：只放一个探测调用在途。
	if cb.halfOpenProbeInFlight {
		return false
	}
	cb.halfOpenProbeInFlight = true
	return true
}

// RecordSuccess 记录一次成功调用，重置连续失败计数并闭合熔断。
func (cb *CircuitBreaker) RecordSuccess(hookPoint string) {
	if cb == nil {
		return
	}
	cb.mu.Lock()
	wasOpen := !cb.openedAt.IsZero()
	cb.consecutiveFailures = 0
	cb.openedAt = time.Time{}
	cb.halfOpenProbeInFlight = false
	cb.mu.Unlock()

	if wasOpen {
		recordCircuitState(hookPoint, false)
	}
}

// RecordFailure 记录一次失败调用；达到阈值则打开熔断。
//
// 注意：被跳过（熔断打开）的调用不应计入本方法——它们没有真正发出。
func (cb *CircuitBreaker) RecordFailure(hookPoint string) {
	if cb == nil {
		return
	}
	cb.mu.Lock()
	cb.consecutiveFailures++
	cb.halfOpenProbeInFlight = false
	opened := false
	if cb.consecutiveFailures >= cb.threshold && cb.openedAt.IsZero() {
		cb.openedAt = time.Now()
		opened = true
	}
	cb.mu.Unlock()

	if opened {
		recordCircuitState(hookPoint, true)
	}
}

// State 返回熔断器当前状态（供测试与诊断）。
func (cb *CircuitBreaker) State() (consecutiveFailures int, open bool) {
	if cb == nil {
		return 0, false
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.consecutiveFailures, !cb.openedAt.IsZero()
}
