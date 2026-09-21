package promptoptimization

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// cacheEntry 缓存条目。
type cacheEntry struct {
	result    *OptimizeResult
	expiresAt time.Time
}

// OptimizationCache 基于 SHA256 的优化结果缓存。
//
// 目标：相同 prompt（+模型+模式）不重复调用优化服务，
// 命中率目标 >60%（见 docs/hooks/prompt-optimization.md）。
// 容量上限采用简单淘汰策略（先清过期，再删最早过期的条目），
// 避免长驻进程内存无界增长。
type OptimizationCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
	ttl     time.Duration
	maxSize int

	// 统计（与 Metrics 共享计数器）
	hits      *counter
	misses    *counter
	evictions *counter
}

// NewOptimizationCache 创建缓存。ttl<=0 或 maxSize<=0 时使用默认值。
func NewOptimizationCache(ttl time.Duration, maxSize int, m *Metrics) *OptimizationCache {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	if maxSize <= 0 {
		maxSize = 4096
	}
	if m == nil {
		m = NewMetrics()
	}
	return &OptimizationCache{
		entries:   make(map[string]cacheEntry, 64),
		ttl:       ttl,
		maxSize:   maxSize,
		hits:      m.CacheHits,
		misses:    m.CacheMisses,
		evictions: m.CacheEvictions,
	}
}

// CacheKey 计算缓存 key：SHA256(版本|model|mode|按序拼接的 role+content)。
//
// prompts 顺序参与 hash（system 在前 user 在后是自然顺序）：
// 写回结果时按数组下标对应原消息，顺序必须稳定，因此顺序不同视为不同 key。
func CacheKey(model string, mode Mode, prompts []PromptItem) string {
	h := sha256.New()
	h.Write([]byte("promptopt:v1:"))
	h.Write([]byte(strings.TrimSpace(model)))
	h.Write([]byte{0})
	h.Write([]byte(mode))
	for _, p := range prompts {
		h.Write([]byte(p.Role))
		h.Write([]byte{0})
		h.Write([]byte(p.Content))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Get 返回缓存结果；过期或不存在返回 false。
func (c *OptimizationCache) Get(key string) (*OptimizeResult, bool) {
	now := time.Now()
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok {
		c.misses.Inc()
		return nil, false
	}
	if now.After(entry.expiresAt) {
		c.mu.Lock()
		delete(c.entries, key)
		c.mu.Unlock()
		c.misses.Inc()
		return nil, false
	}
	c.hits.Inc()
	return entry.result, true
}

// Set 写入缓存；超容量时先淘汰过期条目，再删最早过期的条目。
func (c *OptimizationCache) Set(key string, result *OptimizeResult) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.entries) >= c.maxSize {
		c.evictLocked(now)
	}
	c.entries[key] = cacheEntry{
		result:    result,
		expiresAt: now.Add(c.ttl),
	}
}

// evictLocked 淘汰条目。调用方需持写锁。
func (c *OptimizationCache) evictLocked(now time.Time) {
	for k, e := range c.entries {
		if now.After(e.expiresAt) {
			delete(c.entries, k)
		}
	}
	if len(c.entries) < c.maxSize {
		return
	}
	var oldestKey string
	var oldest time.Time
	first := true
	for k, e := range c.entries {
		if first || e.expiresAt.Before(oldest) {
			oldestKey, oldest, first = k, e.expiresAt, false
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
		c.evictions.Inc()
	}
}

// Len 返回当前条目数（测试/诊断用）。
func (c *OptimizationCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}
