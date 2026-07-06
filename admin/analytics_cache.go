// Package admin — Analytics Results Cache
//
// 分析端点结果缓存（内存 map + TTL，5 分钟过期）
// 用于优化时间序列和分布查询性能，达到 P90 延迟目标
//
// 设计：
// - 使用简单的 map + mutex（无外部依赖）
// - 缓存键格式：<endpoint>:<tenant_id>:<filters_hash>
// - TTL 5 分钟（活动趋势场景足够，避免数据过期）
// - 最大容量 1000 条目（约 10MB 内存，单条 10KB）
// - 线程安全
// - 定期清理过期条目（后台 goroutine）
package admin

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// AnalyticsCache 分析结果缓存
type AnalyticsCache struct {
	items   map[string]*cacheEntry
	maxSize int
	ttl     time.Duration
	mu      sync.RWMutex

	// 统计
	hits   uint64
	misses uint64

	// 清理
	stopCleanup chan struct{}
}

// cacheEntry 缓存条目
type cacheEntry struct {
	value     interface{}
	expiresAt time.Time
}

// NewAnalyticsCache 创建缓存实例
//
// 参数：
//   - size: 最大条目数（推荐 1000）
//   - ttl: 过期时间（推荐 5 分钟）
func NewAnalyticsCache(size int, ttl time.Duration) (*AnalyticsCache, error) {
	c := &AnalyticsCache{
		items:       make(map[string]*cacheEntry),
		maxSize:     size,
		ttl:         ttl,
		stopCleanup: make(chan struct{}),
	}

	// 启动后台清理 goroutine（每分钟）
	go c.cleanupLoop()

	return c, nil
}

// cleanupLoop 定期清理过期条目
func (c *AnalyticsCache) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.cleanup()
		case <-c.stopCleanup:
			return
		}
	}
}

// cleanup 清理过期条目
func (c *AnalyticsCache) cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for key, entry := range c.items {
		if now.After(entry.expiresAt) {
			delete(c.items, key)
		}
	}
}

// Close 关闭缓存（停止清理 goroutine）
func (c *AnalyticsCache) Close() {
	close(c.stopCleanup)
}

// Get 获取缓存值
//
// 返回：
//   - value: 缓存的值（若存在且未过期）
//   - found: 是否命中缓存
func (c *AnalyticsCache) Get(key string) (interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, found := c.items[key]
	if !found {
		c.misses++
		return nil, false
	}

	// 检查过期
	if time.Now().After(entry.expiresAt) {
		c.misses++
		return nil, false
	}

	c.hits++
	return entry.value, true
}

// Set 设置缓存值
func (c *AnalyticsCache) Set(key string, value interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 容量限制（简单 LRU：满了就清理最老的）
	if len(c.items) >= c.maxSize {
		// 找到最早过期的条目删除
		var oldestKey string
		var oldestTime time.Time
		for k, v := range c.items {
			if oldestKey == "" || v.expiresAt.Before(oldestTime) {
				oldestKey = k
				oldestTime = v.expiresAt
			}
		}
		if oldestKey != "" {
			delete(c.items, oldestKey)
		}
	}

	entry := &cacheEntry{
		value:     value,
		expiresAt: time.Now().Add(c.ttl),
	}
	c.items[key] = entry
}

// Stats 缓存统计
func (c *AnalyticsCache) Stats() CacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	total := c.hits + c.misses
	hitRate := 0.0
	if total > 0 {
		hitRate = float64(c.hits) / float64(total)
	}

	return CacheStats{
		Hits:    c.hits,
		Misses:  c.misses,
		HitRate: hitRate,
		Size:    len(c.items),
	}
}

// CacheStats 缓存统计
type CacheStats struct {
	Hits    uint64  `json:"hits"`
	Misses  uint64  `json:"misses"`
	HitRate float64 `json:"hit_rate"`
	Size    int     `json:"size"`
}

// Clear 清空缓存
func (c *AnalyticsCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]*cacheEntry)
}

// BuildCacheKey 构建缓存键
//
// 格式：<endpoint>:<tenant_id>:<filters_hash>
//
// 示例：
//   - activity:tenant_abc:sha256(dateFrom=2026-06-01&dateTo=2026-07-06&model=gpt-4o)
//   - cost-trend:tenant_abc:sha256(dateFrom=2026-06-01&dateTo=2026-07-06&groupBy=model)
func BuildCacheKey(endpoint, tenantID string, filters interface{}) string {
	// 序列化过滤器为 JSON（确保字段顺序一致）
	filtersJSON, err := json.Marshal(filters)
	if err != nil {
		// 降级：使用 fmt.Sprintf
		return fmt.Sprintf("%s:%s:nohash", endpoint, tenantID)
	}

	// SHA256 哈希（避免键过长）
	hash := sha256.Sum256(filtersJSON)
	hashStr := fmt.Sprintf("%x", hash[:8]) // 取前 8 字节（16 字符）

	return fmt.Sprintf("%s:%s:%s", endpoint, tenantID, hashStr)
}

// InvalidateTenant 失效某租户的所有缓存
//
// 使用场景：租户数据发生变更（如手动触发聚合重算）
func (c *AnalyticsCache) InvalidateTenant(tenantID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 遍历删除包含租户 ID 的键
	prefix := fmt.Sprintf(":%s:", tenantID)
	for key := range c.items {
		// 键格式：<endpoint>:<tenant_id>:<hash>
		// 简单包含检查
		if len(key) > len(prefix) {
			for i := 0; i <= len(key)-len(prefix); i++ {
				if key[i:i+len(prefix)] == prefix {
					delete(c.items, key)
					break
				}
			}
		}
	}
}

// AnalyticsCacheFilters 通用过滤器结构（用于构建缓存键）
type AnalyticsCacheFilters struct {
	DateFrom  string   `json:"date_from"`
	DateTo    string   `json:"date_to"`
	Model     []string `json:"model,omitempty"`
	Provider  []string `json:"provider,omitempty"`
	Intent    string   `json:"intent,omitempty"`
	Compliance string  `json:"compliance,omitempty"`
	HealthGrade string `json:"health_grade,omitempty"`
	GroupBy   string   `json:"group_by,omitempty"`
	Granularity string `json:"granularity,omitempty"`
}
