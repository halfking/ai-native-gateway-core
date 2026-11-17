package securitypattern

// 本地模式快照：请求路径 O(1) 查表，把"全网视角"挂到本机事件上。
//
// # 为什么是快照而不是实时查询
//
// 安全检测跑在请求热路径上。每条事件都去 memora 查一次 ⇒ 外部系统的
// 抖动直接变成用户请求的延迟。⇒ 本包用后台循环定期取回一份快照，请求
// 路径只读内存。
//
// # 新鲜度是显式的
//
// 快照带 fetched_at。超过 maxAge 的快照**不再**用于挂提示 —— 陈旧的
// "别的实例也见过"比没有更危险（它会让人以为这个模式很活跃）。
// 陈旧的份数会被计数，不是静默丢弃。

import (
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/securityevent"
)

// DefaultMaxAge 是快照的最大可用年龄（默认 15 分钟）。
//
// 超过它就不再挂提示。取值要大于同步间隔（否则快照几乎总是过期）。
const DefaultMaxAge = 15 * time.Minute

// Cache 是本地模式快照。
type Cache struct {
	mu        sync.RWMutex
	patterns  map[string]Pattern
	fetchedAt time.Time
	maxAge    time.Duration

	// annotated 是成功挂上提示的事件数；staleSkipped 是因为快照过期
	// 而没挂的次数。两个都要能被看见。
	annotated    uint64
	staleSkipped uint64
	lookups      uint64
}

// NewCache 构造缓存。maxAge <= 0 ⇒ DefaultMaxAge。
func NewCache(maxAge time.Duration) *Cache {
	if maxAge <= 0 {
		maxAge = DefaultMaxAge
	}
	return &Cache{
		patterns: map[string]Pattern{},
		maxAge:   maxAge,
	}
}

// Replace 用一批新模式替换整份快照。
//
// 整份替换（而不是逐条合并）⇒ 共享域里被删掉的模式也会随之消失，
// 不会在本地留下"幽灵模式"。
func (c *Cache) Replace(patterns map[string]Pattern, fetchedAt time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if patterns == nil {
		patterns = map[string]Pattern{}
	}
	c.patterns = patterns
	c.fetchedAt = fetchedAt.UTC()
}

// Lookup 按签名查一个模式。
func (c *Cache) Lookup(signature string) (Pattern, bool) {
	if c == nil {
		return Pattern{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	pattern, ok := c.patterns[signature]
	return pattern, ok
}

// Stale 报告当前快照是否已过期。
//
// 零值快照（一次都没成功取回）算**过期** —— 没有数据不是"没有模式"。
func (c *Cache) Stale() bool {
	if c == nil {
		return true
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.staleLocked()
}

func (c *Cache) staleLocked() bool {
	if c.fetchedAt.IsZero() {
		return true
	}
	return time.Since(c.fetchedAt) > c.maxAge
}

// Annotate 把"全网视角"挂到一条本机检测到的事件上。
//
// 命中时设置 event.FleetHint，返回 true。
//
// ★ 它**不改变**事件的严重度、动作或任何处置结论 —— 共享域的内容可能被
// 污染，让它影响拦截等于把外部系统的可信度接到请求路径上。
//
// 快照过期时不挂（返回 false），并计数。
func (c *Cache) Annotate(event *securityevent.Event) bool {
	if c == nil || event == nil {
		return false
	}
	signature := Signature(event.EventType, event.Code)
	if signature == "" {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.lookups++
	pattern, ok := c.patterns[signature]
	if !ok {
		return false
	}
	if c.staleLocked() {
		c.staleSkipped++
		return false
	}
	event.FleetHint = &securityevent.FleetHint{
		Signature:     pattern.Signature,
		Observations:  pattern.Observations,
		Instances:     pattern.Instances,
		InstanceCount: pattern.InstanceCount,
		FetchedAt:     c.fetchedAt,
	}
	c.annotated++
	return true
}

// CacheStats 是缓存的运行计数。
type CacheStats struct {
	// Patterns 是当前快照里的模式数。
	Patterns int `json:"patterns"`
	// FetchedAt 是快照取回时间（零值 = 从未成功取回）。
	FetchedAt time.Time `json:"fetched_at"`
	// Stale 报告快照是否已过期。
	Stale bool `json:"stale"`
	// Annotated 是成功挂上提示的事件数。
	Annotated uint64 `json:"annotated"`
	// StaleSkipped 是因快照过期而没挂提示的事件数。
	StaleSkipped uint64 `json:"stale_skipped"`
	// Lookups 是查表总次数（含未命中）。
	Lookups uint64 `json:"lookups"`
}

// Stats 返回当前计数。
func (c *Cache) Stats() CacheStats {
	if c == nil {
		return CacheStats{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return CacheStats{
		Patterns:     len(c.patterns),
		FetchedAt:    c.fetchedAt,
		Stale:        c.staleLocked(),
		Annotated:    c.annotated,
		StaleSkipped: c.staleSkipped,
		Lookups:      c.lookups,
	}
}
