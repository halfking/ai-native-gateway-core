// Package reconciler implements backoff policies for plugin restart retry.
//
// D10 修复（2026-11-15）：reconciler 无限重试无退避无禁用终态
// - 原问题：StateFailed 插件每 30s reconcile 循环必重启，直到 MaxCrashCount
// - 修复：引入独立退避状态机，支持三种策略（线性/指数/Fibonacci）
// - 设计：与 crashTracker 协同（crashTracker 记录用于终态，backoffTracker 控制时间）
package reconciler

import (
	"sync"
	"time"
)

// BackoffPolicy 定义重试退避策略
type BackoffPolicy string

const (
	// BackoffLinear 线性退避：InitialWait * (retryCount + 1)
	// 例：30s, 60s, 90s, 120s, ...
	BackoffLinear BackoffPolicy = "linear"

	// BackoffExponential 指数退避：InitialWait * 2^retryCount
	// 例：30s, 60s, 120s, 240s, 480s, 600s (capped), ...
	BackoffExponential BackoffPolicy = "exponential"

	// BackoffFibonacci 斐波那契退避：InitialWait * fib(retryCount+1)
	// 例：30s, 30s, 60s, 90s, 150s, 240s, ...
	BackoffFibonacci BackoffPolicy = "fibonacci"
)

// BackoffConfig 配置退避行为
type BackoffConfig struct {
	// Policy 退避策略类型（默认 exponential）
	Policy BackoffPolicy

	// InitialWait 首次失败后的等待时间（默认 30s）
	InitialWait time.Duration

	// MaxWait 单次退避的最大等待上限（默认 10min）
	MaxWait time.Duration

	// MaxRetries 最大重试次数，达到后永久禁用（默认 10，与 MaxCrashCount 同步）
	MaxRetries int
}

// PLACEHOLDER_FOR_BACKOFFSTATE_AND_TRACKER

// BackoffState 跟踪单个插件的退避状态
type BackoffState struct {
	PluginID      string
	RetryCount    int       // 当前重试计数（0 开始，每次失败递增）
	NextRetryTime time.Time // 下次允许重试的时间
	LastFailTime  time.Time // 最近一次失败时间
}

// BackoffTracker 跟踪所有插件的退避状态
type BackoffTracker struct {
	mu     sync.RWMutex
	states map[string]*BackoffState
	config BackoffConfig
}

// NewBackoffTracker 创建新的退避跟踪器
func NewBackoffTracker(config BackoffConfig) *BackoffTracker {
	// 设置默认值
	if config.Policy == "" {
		config.Policy = BackoffExponential
	}
	if config.InitialWait == 0 {
		config.InitialWait = 30 * time.Second
	}
	if config.MaxWait == 0 {
		config.MaxWait = 10 * time.Minute
	}
	if config.MaxRetries == 0 {
		config.MaxRetries = 10
	}

	return &BackoffTracker{
		states: make(map[string]*BackoffState),
		config: config,
	}
}

// PLACEHOLDER_FOR_METHODS

// CanRetry 检查插件是否可以重试（不在退避期内）
func (bt *BackoffTracker) CanRetry(pluginID string) bool {
	bt.mu.RLock()
	defer bt.mu.RUnlock()

	state, exists := bt.states[pluginID]
	if !exists {
		// 首次失败，允许立即重试
		return true
	}

	// 达到最大重试次数，永久禁用
	if state.RetryCount >= bt.config.MaxRetries {
		return false
	}

	// 检查是否仍在退避期
	return time.Now().After(state.NextRetryTime) || time.Now().Equal(state.NextRetryTime)
}

// RecordRetry 记录一次重试尝试，更新退避状态
func (bt *BackoffTracker) RecordRetry(pluginID string) {
	bt.mu.Lock()
	defer bt.mu.Unlock()

	now := time.Now()
	state, exists := bt.states[pluginID]

	if !exists {
		// 首次失败，创建状态
		state = &BackoffState{
			PluginID:     pluginID,
			RetryCount:   1,
			LastFailTime: now,
		}
		bt.states[pluginID] = state
	} else {
		// 递增重试计数
		state.RetryCount++
		state.LastFailTime = now
	}

	// 计算下次允许重试的时间
	waitDuration := bt.calculateWait(state.RetryCount)
	state.NextRetryTime = now.Add(waitDuration)
}

// Reset 重置插件的退避状态（成功启动后调用）
func (bt *BackoffTracker) Reset(pluginID string) {
	bt.mu.Lock()
	defer bt.mu.Unlock()
	delete(bt.states, pluginID)
}

// NextRetryTime 返回插件下次允许重试的时间（用于日志）
func (bt *BackoffTracker) NextRetryTime(pluginID string) time.Time {
	bt.mu.RLock()
	defer bt.mu.RUnlock()

	state, exists := bt.states[pluginID]
	if !exists {
		return time.Now()
	}
	return state.NextRetryTime
}

// GetState 返回插件的退避状态（用于测试和调试）
func (bt *BackoffTracker) GetState(pluginID string) *BackoffState {
	bt.mu.RLock()
	defer bt.mu.RUnlock()

	state, exists := bt.states[pluginID]
	if !exists {
		return nil
	}
	// 返回副本，避免外部修改
	stateCopy := *state
	return &stateCopy
}

// PLACEHOLDER_FOR_CALCULATEWAIT

// calculateWait 根据策略计算等待时间
func (bt *BackoffTracker) calculateWait(retryCount int) time.Duration {
	switch bt.config.Policy {
	case BackoffExponential:
		// 指数退避：InitialWait * 2^(retryCount-1)
		// retryCount=1: 30s, 2: 60s, 3: 120s, 4: 240s, 5: 480s, 6+: 600s (capped)
		if retryCount <= 0 {
			return bt.config.InitialWait
		}
		exponent := retryCount - 1
		if exponent > 10 { // 防止溢出，2^10 = 1024
			exponent = 10
		}
		wait := bt.config.InitialWait * time.Duration(1<<uint(exponent))
		if wait > bt.config.MaxWait {
			wait = bt.config.MaxWait
		}
		return wait

	case BackoffLinear:
		// 线性退避：InitialWait * retryCount
		// retryCount=1: 30s, 2: 60s, 3: 90s, 4: 120s, ...
		wait := bt.config.InitialWait * time.Duration(retryCount)
		if wait > bt.config.MaxWait {
			wait = bt.config.MaxWait
		}
		return wait

	case BackoffFibonacci:
		// 斐波那契退避：InitialWait * fib(retryCount)
		// retryCount=1: 30s (fib(1)=1), 2: 30s (fib(2)=1), 3: 60s (fib(3)=2),
		// 4: 90s (fib(4)=3), 5: 150s (fib(5)=5), 6: 240s (fib(6)=8), ...
		fib := fibonacci(retryCount)
		wait := bt.config.InitialWait * time.Duration(fib)
		if wait > bt.config.MaxWait {
			wait = bt.config.MaxWait
		}
		return wait

	default:
		return bt.config.InitialWait
	}
}

// fibonacci 计算第 n 个斐波那契数（迭代法，避免递归栈溢出）
func fibonacci(n int) int {
	if n <= 1 {
		return 1 // fib(0)=1, fib(1)=1（起始值调整，避免 0 等待）
	}
	if n == 2 {
		return 1
	}
	a, b := 1, 1
	for i := 3; i <= n; i++ {
		a, b = b, a+b
	}
	return b
}

// defaultBackoffConfig 返回默认退避配置
func defaultBackoffConfig() BackoffConfig {
	return BackoffConfig{
		Policy:      BackoffExponential,
		InitialWait: 30 * time.Second,
		MaxWait:     10 * time.Minute,
		MaxRetries:  10,
	}
}
