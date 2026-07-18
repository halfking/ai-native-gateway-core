package circuit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/pkg/logger"
)

var (
	// ErrOpen 表示熔断器处于 Open 状态
	ErrOpen = errors.New("circuit breaker is open")

	// ErrTooManyHalfOpenRequests 表示 Half-Open 探测请求过多
	ErrTooManyHalfOpenRequests = errors.New("too many half-open requests")
)

// State 是熔断器状态
type State int

const (
	StateClosed State = iota
	StateOpen
	StateHalfOpen
)

func (s State) String() string {
	return [...]string{"closed", "open", "half_open"}[s]
}

// Config 是熔断器配置
type Config struct {
	// 错误率阈值 (0.0-1.0)
	ErrorThreshold float64

	// 最小请求数 (防止样本太少误判)
	MinRequests int64

	// 统计窗口大小
	WindowSize time.Duration

	// Open 状态持续时间 (之后进入 Half-Open)
	OpenTimeout time.Duration

	// Half-Open 状态最大探测请求数
	HalfOpenMaxTest int

	// Half-Open 状态成功阈值 (成功请求数 / 总探测数)
	HalfOpenSuccessThreshold float64
}

// DefaultConfig 返回默认配置
func DefaultConfig() Config {
	return Config{
		ErrorThreshold:           0.5,
		MinRequests:              10,
		WindowSize:               10 * time.Second,
		OpenTimeout:              30 * time.Second,
		HalfOpenMaxTest:          3,
		HalfOpenSuccessThreshold: 0.67,
	}
}

// Metrics 是熔断器指标
type Metrics struct {
	State            State
	TotalRequests    int64
	TotalSuccesses   int64
	TotalFailures    int64
	ErrorRate        float64
	ConsecutiveFails int64
	LastStateChange  time.Time
}

// Breaker 是熔断器接口
type Breaker interface {
	// Call 包装一个可能失败的函数调用
	Call(ctx context.Context, fn func() error) error

	// State 返回当前状态
	State() State

	// Reset 手动重置为 Closed 状态
	Reset()

	// Metrics 返回当前指标
	Metrics() Metrics

	// Record 记录一次调用结果（用于外部调用场景）
	Record(success bool)
}

// breaker 是熔断器实现
type breaker struct {
	config Config

	// 状态 (使用原子操作)
	state atomic.Int32 // State

	// 滑动窗口
	window *SlidingWindow

	// Half-Open 探测计数器
	halfOpenSuccesses atomic.Int64
	halfOpenFailures  atomic.Int64
	halfOpenTotal     atomic.Int64

	// 上次状态变更时间
	lastStateChange atomic.Int64 // Unix timestamp

	// Open 状态的到期时间
	openUntil atomic.Int64 // Unix timestamp

	// 用于 Half-Open 状态的并发控制
	halfOpenMu sync.Mutex

	// 日志
	logger logger.Logger
}

// NewBreaker 创建一个新的熔断器
func NewBreaker(config Config) Breaker {
	b := &breaker{
		config: config,
		window: NewSlidingWindow(config.WindowSize),
		logger: logger.New("circuit"),
	}
	b.state.Store(int32(StateClosed))
	b.lastStateChange.Store(time.Now().Unix())

	b.logger.Info("circuit breaker created",
		"error_threshold", config.ErrorThreshold,
		"min_requests", config.MinRequests,
		"window_size", config.WindowSize,
		"open_timeout", config.OpenTimeout,
	)

	return b
}

// Call 包装一个可能失败的函数调用
func (b *breaker) Call(ctx context.Context, fn func() error) error {
	state := b.getState()

	switch state {
	case StateClosed:
		return b.callClosed(ctx, fn)
	case StateOpen:
		return b.callOpen(ctx, fn)
	case StateHalfOpen:
		return b.callHalfOpen(ctx, fn)
	default:
		return fmt.Errorf("unknown state: %v", state)
	}
}

// callClosed 在 Closed 状态下执行调用
func (b *breaker) callClosed(ctx context.Context, fn func() error) error {
	start := time.Now()
	err := fn()
	duration := time.Since(start)

	// 记录结果
	b.window.Record(err == nil)

	if err == nil {
		b.logger.Debug("request succeeded",
			"state", "closed",
			"duration_ms", duration.Milliseconds(),
		)
	} else {
		b.logger.Warn("request failed",
			"state", "closed",
			"error", err.Error(),
			"duration_ms", duration.Milliseconds(),
		)
	}

	// 检查是否需要熔断
	if b.shouldOpen() {
		b.transitionToOpen()
		return ErrOpen
	}

	return err
}

// callOpen 在 Open 状态下执行调用
func (b *breaker) callOpen(ctx context.Context, fn func() error) error {
	// 检查是否到期
	openUntil := time.Unix(b.openUntil.Load(), 0)
	if time.Now().After(openUntil) {
		b.logger.Info("circuit breaker timeout expired, transitioning to half-open",
			"open_duration", time.Since(time.Unix(b.lastStateChange.Load(), 0)),
		)
		b.transitionToHalfOpen()
		return b.callHalfOpen(ctx, fn)
	}

	// 仍在 Open 状态，快速失败
	b.logger.Debug("circuit breaker open, rejecting request",
		"retry_after", openUntil.Sub(time.Now()),
	)
	return ErrOpen
}

// callHalfOpen 在 Half-Open 状态下执行调用
func (b *breaker) callHalfOpen(ctx context.Context, fn func() error) error {
	// 限制并发探测数
	if b.halfOpenTotal.Load() >= int64(b.config.HalfOpenMaxTest) {
		return ErrTooManyHalfOpenRequests
	}

	b.halfOpenMu.Lock()
	defer b.halfOpenMu.Unlock()

	// 再次检查（防止并发竞争）
	if b.halfOpenTotal.Load() >= int64(b.config.HalfOpenMaxTest) {
		return ErrTooManyHalfOpenRequests
	}

	b.halfOpenTotal.Add(1)

	err := fn()

	if err == nil {
		b.halfOpenSuccesses.Add(1)
	} else {
		b.halfOpenFailures.Add(1)
	}

	// 检查是否达到探测数上限
	total := b.halfOpenTotal.Load()
	if total >= int64(b.config.HalfOpenMaxTest) {
		successes := b.halfOpenSuccesses.Load()
		successRate := float64(successes) / float64(total)

		if successRate >= b.config.HalfOpenSuccessThreshold {
			b.transitionToClosed()
		} else {
			b.transitionToOpen()
		}
	}

	return err
}

// shouldOpen 检查是否应该熔断
func (b *breaker) shouldOpen() bool {
	metrics := b.window.Metrics()

	// 请求数不足，不熔断
	if metrics.Total < b.config.MinRequests {
		return false
	}

	// 错误率超过阈值
	return metrics.ErrorRate >= b.config.ErrorThreshold
}

// transitionToOpen 转换到 Open 状态
func (b *breaker) transitionToOpen() {
	oldState := b.getState()
	metrics := b.window.Metrics()

	b.setState(StateOpen)
	b.openUntil.Store(time.Now().Add(b.config.OpenTimeout).Unix())
	b.lastStateChange.Store(time.Now().Unix())

	// 重置 Half-Open 计数器
	b.halfOpenSuccesses.Store(0)
	b.halfOpenFailures.Store(0)
	b.halfOpenTotal.Store(0)

	b.logger.Warn("circuit breaker opened",
		"from_state", oldState.String(),
		"to_state", "open",
		"error_rate", metrics.ErrorRate,
		"threshold", b.config.ErrorThreshold,
		"total_requests", metrics.Total,
		"failures", metrics.Failures,
		"open_timeout", b.config.OpenTimeout,
	)
}

// transitionToHalfOpen 转换到 Half-Open 状态
func (b *breaker) transitionToHalfOpen() {
	oldState := b.getState()

	b.setState(StateHalfOpen)
	b.lastStateChange.Store(time.Now().Unix())

	// 重置 Half-Open 计数器
	b.halfOpenSuccesses.Store(0)
	b.halfOpenFailures.Store(0)
	b.halfOpenTotal.Store(0)

	b.logger.Info("circuit breaker transitioned to half-open",
		"from_state", oldState.String(),
		"to_state", "half_open",
		"max_test_requests", b.config.HalfOpenMaxTest,
		"success_threshold", b.config.HalfOpenSuccessThreshold,
	)
}

// transitionToClosed 转换到 Closed 状态
func (b *breaker) transitionToClosed() {
	oldState := b.getState()
	successes := b.halfOpenSuccesses.Load()
	total := b.halfOpenTotal.Load()

	b.setState(StateClosed)
	b.lastStateChange.Store(time.Now().Unix())

	// 重置滑动窗口
	b.window.Reset()

	b.logger.Info("circuit breaker closed",
		"from_state", oldState.String(),
		"to_state", "closed",
		"half_open_successes", successes,
		"half_open_total", total,
	)
}

// State 返回当前状态
func (b *breaker) State() State {
	return b.getState()
}

// Reset 手动重置为 Closed 状态
func (b *breaker) Reset() {
	b.transitionToClosed()
}

// Metrics 返回当前指标
func (b *breaker) Metrics() Metrics {
	windowMetrics := b.window.Metrics()

	return Metrics{
		State:           b.getState(),
		TotalRequests:   windowMetrics.Total,
		TotalSuccesses:  windowMetrics.Successes,
		TotalFailures:   windowMetrics.Failures,
		ErrorRate:       windowMetrics.ErrorRate,
		LastStateChange: time.Unix(b.lastStateChange.Load(), 0),
	}
}

// Record 记录一次调用结果
func (b *breaker) Record(success bool) {
	b.window.Record(success)

	// 如果在 Closed 状态，检查是否需要熔断
	if b.getState() == StateClosed && b.shouldOpen() {
		b.transitionToOpen()
	}
}

// getState 获取当前状态
func (b *breaker) getState() State {
	return State(b.state.Load())
}

// setState 设置状态
func (b *breaker) setState(s State) {
	b.state.Store(int32(s))
}
