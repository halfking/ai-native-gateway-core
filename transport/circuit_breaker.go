package transport

import (
	"sync"
	"time"
)

// CircuitState 熔断器状态。
type CircuitState int

const (
	// CircuitClosed 闭合（正常）：请求通过，记录错误。
	CircuitClosed CircuitState = iota
	// CircuitOpen 断开（熔断）：请求被拒绝/降级，等待冷却时间。
	CircuitOpen
	// CircuitHalfOpen 半开：允许试探性请求，成功则恢复，失败则继续熔断。
	CircuitHalfOpen
)

// String 返回状态的可读名称。
func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// StreamCircuitBreaker 流式转换熔断器。
//
// 用于 Phase 2 降级开关：当 IR 流式路径在时间窗口内错误次数达到阈值，
// 熔断器进入 Open 态，后续流式请求由 TransportFactory 降级到 Legacy。
//
// 状态机：
//   - Closed → Open：窗口内错误数 >= threshold
//   - Open → HalfOpen：冷却时间 cooldown 后
//   - HalfOpen → Closed：一次成功
//   - HalfOpen → Open：一次失败
//
// 线程安全。
type StreamCircuitBreaker struct {
	mu sync.Mutex

	threshold int           // 触发熔断的错误阈值（窗口内）
	window    time.Duration // 错误计数的时间窗口
	cooldown  time.Duration // Open → HalfOpen 的冷却时间

	state      CircuitState // 当前状态
	errorTimes []time.Time  // 滑动窗口内的错误时间戳
	openedAt   time.Time    // 进入 Open 态的时刻
	totalTrips int64        // 累计熔断次数（监控用）
}

// NewStreamCircuitBreaker 构造一个流式熔断器。
//
// 默认参数：threshold=3, window=1m, cooldown=1m（对齐 Phase 2 "3 次错误/分钟"）。
func NewStreamCircuitBreaker() *StreamCircuitBreaker {
	return &StreamCircuitBreaker{
		threshold: 3,
		window:    time.Minute,
		cooldown:  time.Minute,
		state:     CircuitClosed,
	}
}

// State 返回当前熔断器状态（线程安全）。
func (cb *StreamCircuitBreaker) State() CircuitState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.currentStateLocked()
}

// ShouldFallback 报告当前是否应该降级到 Legacy。
//
// 当熔断器处于 Open 态时返回 true。调用此方法不会改变状态。
// 由 TransportFactory.Pick 在选择实现前调用。
func (cb *StreamCircuitBreaker) ShouldFallback() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.currentStateLocked() == CircuitOpen
}

// RecordError 记录一次流式转换错误。
//
// 在时间窗口内累计达阈值时熔断。半开态下错误会重新熔断。
func (cb *StreamCircuitBreaker) RecordError() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := time.Now()
	state := cb.currentStateLocked()

	if state == CircuitHalfOpen {
		// 半开态失败 → 重新熔断
		cb.tripLocked(now)
		return
	}

	// Closed 态：滑动窗口计数
	cb.errorTimes = append(cb.errorTimes, now)
	cb.pruneLocked(now)

	if len(cb.errorTimes) >= cb.threshold {
		cb.tripLocked(now)
	}
}

// RecordSuccess 记录一次流式转换成功。
//
// 半开态下成功 → 恢复到 Closed。
func (cb *StreamCircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.currentStateLocked() == CircuitHalfOpen {
		cb.state = CircuitClosed
		cb.errorTimes = nil
	}
}

// TotalTrips 返回累计熔断次数（监控用）。
func (cb *StreamCircuitBreaker) TotalTrips() int64 {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.totalTrips
}

// currentStateLocked 计算当前实际状态（处理 Open → HalfOpen 的自动转换）。
// 调用者必须持有 cb.mu。
func (cb *StreamCircuitBreaker) currentStateLocked() CircuitState {
	if cb.state == CircuitOpen && time.Since(cb.openedAt) >= cb.cooldown {
		cb.state = CircuitHalfOpen
	}
	return cb.state
}

// tripLocked 进入熔断态。调用者必须持有 cb.mu。
func (cb *StreamCircuitBreaker) tripLocked(now time.Time) {
	cb.state = CircuitOpen
	cb.openedAt = now
	cb.errorTimes = nil
	cb.totalTrips++
}

// pruneLocked 清理滑动窗口外的过期错误记录。调用者必须持有 cb.mu。
func (cb *StreamCircuitBreaker) pruneLocked(now time.Time) {
	cutoff := now.Add(-cb.window)
	kept := cb.errorTimes[:0]
	for _, t := range cb.errorTimes {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	cb.errorTimes = kept
}

// Reset 重置熔断器到 Closed 态（测试用）。
func (cb *StreamCircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.state = CircuitClosed
	cb.errorTimes = nil
	cb.openedAt = time.Time{}
}
