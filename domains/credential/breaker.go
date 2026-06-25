package credential

import (
	"errors"
	"sync"
	"time"
)

// CircuitState 熔断器状态
type CircuitState string

const (
	StateClosed   CircuitState = "closed"    // 正常
	StateOpen     CircuitState = "open"      // 熔断
	StateHalfOpen CircuitState = "half_open" // 半开（探测）
)

// CircuitBreaker 熔断器
type CircuitBreaker struct {
	mu sync.Mutex

	// state 当前状态
	state CircuitState
	// consecutiveFails 连续失败次数
	consecutiveFails int
	// openUntil 熔断到期时间
	openUntil time.Time
	// failThreshold 触发熔断的失败次数
	failThreshold int
	// openTimeout 熔断持续时间
	openTimeout time.Duration
}

// NewCircuitBreaker 创建熔断器
func NewCircuitBreaker(failThreshold int, openTimeout time.Duration) *CircuitBreaker {
	if failThreshold <= 0 {
		failThreshold = 5
	}
	if openTimeout <= 0 {
		openTimeout = 30 * time.Second
	}
	return &CircuitBreaker{
		state:         StateClosed,
		failThreshold: failThreshold,
		openTimeout:   openTimeout,
	}
}

// Allow 检查是否允许请求通过
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	switch cb.state {
	case StateClosed:
		return true
	case StateOpen:
		if time.Now().After(cb.openUntil) {
			// 进入半开状态
			cb.state = StateHalfOpen
			return true
		}
		return false
	case StateHalfOpen:
		return true
	}
	return false
}

// RecordSuccess 记录成功
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.consecutiveFails = 0
	if cb.state == StateHalfOpen {
		cb.state = StateClosed
	}
}

// RecordFailure 记录失败
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.consecutiveFails++
	if cb.consecutiveFails >= cb.failThreshold {
		cb.state = StateOpen
		cb.openUntil = time.Now().Add(cb.openTimeout)
	}
}

// State 返回当前状态
func (cb *CircuitBreaker) State() CircuitState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

// ErrCircuitOpen 熔断器打开错误
var ErrCircuitOpen = errors.New("credential: circuit breaker is open")
