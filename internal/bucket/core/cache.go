package core

import (
	"sync"
	"time"
)

// MemoryCache 内存缓存实现
type MemoryCache struct {
	data     map[string]*cacheEntry
	mu       sync.RWMutex
	maxSize  int
	stats    CacheStats
}

type cacheEntry struct {
	state     *BucketState
	expiresAt time.Time
	hits      int64
}

// NewMemoryCache 创建新的内存缓存
func NewMemoryCache(maxSize int) *MemoryCache {
	if maxSize <= 0 {
		maxSize = 10000
	}
	return &MemoryCache{
		data:    make(map[string]*cacheEntry),
		maxSize: maxSize,
		stats:   CacheStats{MaxSize: maxSize},
	}
}

// Get 从缓存获取桶状态
func (c *MemoryCache) Get(key BucketKey) (*BucketState, bool) {
	c.mu.RLock()
	entry, ok := c.data[key.String()]
	c.mu.RUnlock()

	if !ok {
		c.mu.Lock()
		c.stats.Misses++
		c.mu.Unlock()
		return nil, false
	}

	// 检查是否过期
	if time.Now().After(entry.expiresAt) {
		c.mu.Lock()
		delete(c.data, key.String())
		c.stats.Evictions++
		c.stats.Misses++
		c.stats.Size = len(c.data)
		c.mu.Unlock()
		return nil, false
	}

	// 更新命中统计
	c.mu.Lock()
	entry.hits++
	c.stats.Hits++
	c.mu.Unlock()

	return entry.state, true
}

// Set 设置桶状态到缓存
func (c *MemoryCache) Set(key BucketKey, state *BucketState, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 检查是否需要淘汰
	if len(c.data) >= c.maxSize {
		c.evictLRU()
	}

	c.data[key.String()] = &cacheEntry{
		state:     state,
		expiresAt: time.Now().Add(ttl),
		hits:      0,
	}

	c.stats.Size = len(c.data)
	return nil
}

// Delete 删除缓存
func (c *MemoryCache) Delete(key BucketKey) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.data[key.String()]; ok {
		delete(c.data, key.String())
		c.stats.Size = len(c.data)
	}

	return nil
}

// Clear 清空所有缓存
func (c *MemoryCache) Clear() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.data = make(map[string]*cacheEntry)
	c.stats.Size = 0

	return nil
}

// GetStats 获取缓存统计信息
func (c *MemoryCache) GetStats() CacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.stats
}

// evictLRU 淘汰最少使用的缓存项
// 注意：调用前必须已持有锁
func (c *MemoryCache) evictLRU() {
	if len(c.data) == 0 {
		return
	}

	// 找到命中次数最少且最早过期的项
	var evictKey string
	var minHits int64 = -1
	var earliestExpire time.Time

	for key, entry := range c.data {
		if minHits == -1 || entry.hits < minHits ||
			(entry.hits == minHits && entry.expiresAt.Before(earliestExpire)) {
			evictKey = key
			minHits = entry.hits
			earliestExpire = entry.expiresAt
		}
	}

	if evictKey != "" {
		delete(c.data, evictKey)
		c.stats.Evictions++
	}
}

// CleanExpired 清理过期的缓存项
func (c *MemoryCache) CleanExpired() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	count := 0

	for key, entry := range c.data {
		if now.After(entry.expiresAt) {
			delete(c.data, key)
			count++
		}
	}

	c.stats.Evictions += int64(count)
	c.stats.Size = len(c.data)

	return count
}

// StartCleanup 启动后台清理 goroutine
func (c *MemoryCache) StartCleanup(interval time.Duration) chan<- struct{} {
	stop := make(chan struct{})

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				c.CleanExpired()
			case <-stop:
				return
			}
		}
	}()

	return stop
}

// GetHitRate 获取缓存命中率
func (c *MemoryCache) GetHitRate() float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()

	total := c.stats.Hits + c.stats.Misses
	if total == 0 {
		return 0
	}

	return float64(c.stats.Hits) / float64(total)
}
