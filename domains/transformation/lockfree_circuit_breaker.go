package transformation

import (
	"sync/atomic"
	"time"
)

// LockFreeCircuitBreaker 无锁熔断器实现
// 使用原子操作替代互斥锁，提升高并发性能
type LockFreeCircuitBreaker struct {
	threshold int64         // 触发熔断的错误阈值
	window    time.Duration // 错误计数的时间窗口
	cooldown  time.Duration // Open → HalfOpen 的冷却时间

	// 原子状态字段
	state      atomic.Int32 // CircuitState (0=Closed, 1=Open, 2=HalfOpen)
	openedAt   atomic.Int64 // 进入 Open 态的时刻 (UnixNano)
	totalTrips atomic.Int64 // 累计熔断次数

	// 错误计数器 - 使用环形缓冲区 + 原子操作
	errorSlots  [64]atomic.Int64 // 环形缓冲区，存储错误时间戳
	errorHead   atomic.Uint64    // 写入位置（环形索引）
	errorCount  atomic.Int64     // 窗口内错误数量
	lastPruneAt atomic.Int64     // 上次清理时间戳
}

// NewLockFreeCircuitBreaker 构造无锁熔断器
func NewLockFreeCircuitBreaker(threshold int, window, cooldown time.Duration) *LockFreeCircuitBreaker {
	if threshold <= 0 {
		threshold = 3
	}
	if window <= 0 {
		window = time.Minute
	}
	if cooldown <= 0 {
		cooldown = time.Minute
	}

	cb := &LockFreeCircuitBreaker{
		threshold: int64(threshold),
		window:    window,
		cooldown:  cooldown,
	}
	cb.state.Store(int32(CircuitClosed))
	cb.lastPruneAt.Store(time.Now().UnixNano())
	return cb
}

// State 返回当前熔断器状态（无锁）
func (cb *LockFreeCircuitBreaker) State() CircuitState {
	return cb.getCurrentState()
}

// ShouldFallback 报告当前是否应该降级（无锁）
func (cb *LockFreeCircuitBreaker) ShouldFallback() bool {
	return cb.getCurrentState() == CircuitOpen
}

// RecordError 记录一次错误（无锁）
func (cb *LockFreeCircuitBreaker) RecordError() {
	now := time.Now()
	nowNano := now.UnixNano()

	// 定期清理过期错误（避免每次调用都清理）
	lastPrune := cb.lastPruneAt.Load()
	if nowNano-lastPrune > int64(cb.window)/10 { // 每 1/10 窗口清理一次
		if cb.lastPruneAt.CompareAndSwap(lastPrune, nowNano) {
			cb.pruneExpiredErrors(nowNano)
		}
	}

	state := cb.getCurrentState()

	if state == CircuitHalfOpen {
		// 半开态失败 → 重新熔断
		cb.trip(nowNano)
		return
	}

	// Closed 态：添加错误时间戳到环形缓冲区
	idx := cb.errorHead.Add(1) % 64
	cb.errorSlots[idx].Store(nowNano)

	// 增加错误计数
	count := cb.errorCount.Add(1)

	// 检查是否达到阈值
	if count >= cb.threshold {
		cb.trip(nowNano)
	}
}

// RecordSuccess 记录一次成功（无锁）
func (cb *LockFreeCircuitBreaker) RecordSuccess() {
	state := cb.getCurrentState()

	if state == CircuitHalfOpen {
		// 半开态成功 → 恢复到 Closed
		if cb.state.CompareAndSwap(int32(CircuitHalfOpen), int32(CircuitClosed)) {
			// 重置错误计数
			cb.errorCount.Store(0)
			cb.errorHead.Store(0)
			// 清空环形缓冲区
			for i := range cb.errorSlots {
				cb.errorSlots[i].Store(0)
			}
		}
	}
}

// TotalTrips 返回累计熔断次数
func (cb *LockFreeCircuitBreaker) TotalTrips() int64 {
	return cb.totalTrips.Load()
}

// getCurrentState 计算当前实际状态（处理 Open → HalfOpen 自动转换）
func (cb *LockFreeCircuitBreaker) getCurrentState() CircuitState {
	currentState := CircuitState(cb.state.Load())

	if currentState == CircuitOpen {
		openedAt := cb.openedAt.Load()
		if time.Since(time.Unix(0, openedAt)) >= cb.cooldown {
			// 尝试转换到 HalfOpen
			if cb.state.CompareAndSwap(int32(CircuitOpen), int32(CircuitHalfOpen)) {
				return CircuitHalfOpen
			}
		}
	}

	return currentState
}

// trip 进入熔断态（无锁）
func (cb *LockFreeCircuitBreaker) trip(nowNano int64) {
	// 尝试从 Closed/HalfOpen 转换到 Open
	for {
		current := cb.state.Load()
		if current == int32(CircuitOpen) {
			// 已经是 Open 态，无需重复熔断
			return
		}
		if cb.state.CompareAndSwap(current, int32(CircuitOpen)) {
			// 成功转换到 Open
			cb.openedAt.Store(nowNano)
			cb.totalTrips.Add(1)
			// 清空错误计数
			cb.errorCount.Store(0)
			cb.errorHead.Store(0)
			for i := range cb.errorSlots {
				cb.errorSlots[i].Store(0)
			}
			return
		}
		// CAS 失败，重试
	}
}

// pruneExpiredErrors 清理过期的错误记录（无锁）
func (cb *LockFreeCircuitBreaker) pruneExpiredErrors(nowNano int64) {
	cutoffNano := nowNano - int64(cb.window)
	validCount := int64(0)

	// 遍历环形缓冲区，统计有效错误数
	for i := range cb.errorSlots {
		ts := cb.errorSlots[i].Load()
		if ts > 0 && ts >= cutoffNano {
			validCount++
		} else if ts > 0 && ts < cutoffNano {
			// 清理过期的
			cb.errorSlots[i].Store(0)
		}
	}

	// 更新错误计数
	cb.errorCount.Store(validCount)
}

// Reset 重置熔断器（测试用）
func (cb *LockFreeCircuitBreaker) Reset() {
	cb.state.Store(int32(CircuitClosed))
	cb.errorCount.Store(0)
	cb.errorHead.Store(0)
	cb.openedAt.Store(0)
	cb.lastPruneAt.Store(time.Now().UnixNano())
	for i := range cb.errorSlots {
		cb.errorSlots[i].Store(0)
	}
}

// GetErrorCount 返回当前窗口内的错误数（监控用）
func (cb *LockFreeCircuitBreaker) GetErrorCount() int64 {
	return cb.errorCount.Load()
}
