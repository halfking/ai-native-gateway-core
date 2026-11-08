package mcp

import (
	"sync"
	"time"
)

// limiterBucket 是一个令牌桶。
type limiterBucket struct {
	tokens   float64
	capacity float64
	rate     float64 // 每秒补充令牌数
	last     time.Time
}

// Limiter 是按 key 维度的令牌桶限流器（进程内）。
//
// 语义：limit 表示「每分钟 N 次」，桶容量 = N（允许突发 N 次），补充速率 =
// N/60 每秒。取 0 表示不限流。超限返回剩余等待时间，供 429 的 Retry-After 使用。
//
// 与既有 tools/mcp-gateway 的固定窗口计数（60 req/min per API key）相比，
// 令牌桶允许短时突发、长期速率严格受限，更贴近企业配额语义。
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*limiterBucket
	// now 可注入，便于测试。
	now func() time.Time
}

// NewLimiter 构造限流器。
func NewLimiter() *Limiter {
	return &Limiter{buckets: map[string]*limiterBucket{}, now: time.Now}
}

// Allow 判断是否放行；放行返回 true，否则返回需要等待的时长。
//
// limit<=0 视为不限流（恒放行）。
func (l *Limiter) Allow(key string, limit int) (bool, time.Duration) {
	if limit <= 0 {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		b = &limiterBucket{
			tokens:   float64(limit),
			capacity: float64(limit),
			rate:     float64(limit) / 60.0,
			last:     now,
		}
		l.buckets[key] = b
	}
	// 补充令牌。
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * b.rate
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	missing := 1 - b.tokens
	wait := time.Duration(missing / b.rate * float64(time.Second))
	if wait < time.Second {
		wait = time.Second
	}
	return false, wait
}

// Reset 清空某个 key 的桶（凭证吊销/轮换后调用）。
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, key)
}

// Size 返回当前桶数量（用于运维面板与测试断言）。
func (l *Limiter) Size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
