package mcp

import (
	"sync"
	"time"
)

// 熔断器状态。
const (
	circuitClosed   = "closed"
	circuitOpen     = "open"
	circuitHalfOpen = "half_open"
)

// CircuitBreaker 是按适配器维度的熔断器。
//
// 语义：连续失败达到阈值后打开（open），打开期间直接拒绝（不再打上游），
// 经过冷却窗口后进入半开（half_open）允许一次探测，成功则闭合、失败则重新打开。
type CircuitBreaker struct {
	mu     sync.Mutex
	states map[string]*circuitState
	// threshold 打开前的连续失败阈值。
	threshold int
	// openDuration 打开后的冷却时长。
	openDuration time.Duration
	now          func() time.Time
}

type circuitState struct {
	state       string
	failures    int
	openedAt    time.Time
	lastFailure time.Time
}

// NewCircuitBreaker 构造熔断器；threshold<=0 时默认 5 次。
func NewCircuitBreaker(threshold int, openDuration time.Duration) *CircuitBreaker {
	if threshold <= 0 {
		threshold = 5
	}
	if openDuration <= 0 {
		openDuration = 30 * time.Second
	}
	return &CircuitBreaker{
		states:       map[string]*circuitState{},
		threshold:    threshold,
		openDuration: openDuration,
		now:          time.Now,
	}
}

// Allow 判断是否允许调用上游；返回 false 时应拒绝（熔断打开）。
func (c *CircuitBreaker) Allow(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.states[name]
	if !ok {
		return true
	}
	if st.state == circuitOpen {
		if c.now().Sub(st.openedAt) >= c.openDuration {
			st.state = circuitHalfOpen
			return true
		}
		return false
	}
	return true
}

// Success 记录一次成功（闭合熔断器）。
func (c *CircuitBreaker) Success(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.states[name]
	if !ok {
		st = &circuitState{state: circuitClosed}
		c.states[name] = st
	}
	st.state = circuitClosed
	st.failures = 0
}

// Failure 记录一次失败；达到阈值则打开熔断。
func (c *CircuitBreaker) Failure(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.states[name]
	if !ok {
		st = &circuitState{state: circuitClosed}
		c.states[name] = st
	}
	st.failures++
	st.lastFailure = c.now()
	if st.state == circuitHalfOpen || st.failures >= c.threshold {
		st.state = circuitOpen
		st.openedAt = c.now()
	}
}

// State 返回某适配器的熔断状态（未记录时为 closed）。
func (c *CircuitBreaker) State(name string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if st, ok := c.states[name]; ok {
		return st.state
	}
	return circuitClosed
}

// Snapshot 返回全部适配器的熔断状态（运维面板用）。
func (c *CircuitBreaker) Snapshot() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]string, len(c.states))
	for k, v := range c.states {
		out[k] = v.state
	}
	return out
}
