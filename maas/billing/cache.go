package billing

import (
	"container/list"
	"context"
	"fmt"
	"sync"
	"time"
)

// MemoryCache 实现L1内存缓存（LRU策略）
//
// 特性：
// - LRU驱逐策略
// - 线程安全
// - TTL支持
// - 统计信息收集
// - 优先级感知（高优先级条目不易被驱逐）
type MemoryCache struct {
	capacity int
	items    map[string]*cacheEntry
	lruList  *list.List
	mu       sync.RWMutex

	// 统计信息
	hits      int64
	misses    int64
	evictions int64

	// 性能统计
	totalGetLatency time.Duration
	getCount        int64
}

type cacheEntry struct {
	key        string
	value      *BillingConfig
	element    *list.Element
	expireAt   time.Time
	priority   int // 缓存优先级（1-10）
	accessTime time.Time
}

// NewMemoryCache 创建一个新的内存缓存
//
// capacity: 最大条目数
func NewMemoryCache(capacity int) *MemoryCache {
	if capacity <= 0 {
		capacity = 10000 // 默认容量
	}

	return &MemoryCache{
		capacity: capacity,
		items:    make(map[string]*cacheEntry, capacity),
		lruList:  list.New(),
	}
}

// Get 获取缓存条目
func (c *MemoryCache) Get(ctx context.Context, key string) (*BillingConfig, bool) {
	start := time.Now()
	defer func() {
		c.mu.Lock()
		c.totalGetLatency += time.Since(start)
		c.getCount++
		c.mu.Unlock()
	}()

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, exists := c.items[key]
	if !exists {
		c.misses++
		return nil, false
	}

	// 检查是否过期
	if time.Now().After(entry.expireAt) {
		c.remove(entry)
		c.misses++
		return nil, false
	}

	// 命中：更新LRU位置和访问时间
	c.lruList.MoveToFront(entry.element)
	entry.accessTime = time.Now()
	c.hits++

	return entry.value, true
}

// Set 设置缓存条目
func (c *MemoryCache) Set(ctx context.Context, key string, config *BillingConfig) error {
	if config == nil {
		return fmt.Errorf("cannot cache nil config")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// 如果已存在，更新
	if entry, exists := c.items[key]; exists {
		entry.value = config
		entry.expireAt = time.Now().Add(config.CacheHint.TTL)
		entry.priority = config.CacheHint.Priority
		c.lruList.MoveToFront(entry.element)
		return nil
	}

	// 如果达到容量，驱逐条目
	if len(c.items) >= c.capacity {
		c.evictOne()
	}

	// 添加新条目
	entry := &cacheEntry{
		key:        key,
		value:      config,
		expireAt:   time.Now().Add(config.CacheHint.TTL),
		priority:   config.CacheHint.Priority,
		accessTime: time.Now(),
	}

	entry.element = c.lruList.PushFront(entry)
	c.items[key] = entry

	return nil
}

// Delete 删除缓存条目
func (c *MemoryCache) Delete(ctx context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if entry, exists := c.items[key]; exists {
		c.remove(entry)
	}

	return nil
}

// evictOne 驱逐一个条目（LRU + 优先级感知）
//
// 驱逐策略：
// - 优先驱逐低优先级条目
// - 相同优先级下，驱逐最久未使用的
// - 高优先级（>= 8）的条目只在必要时驱逐
func (c *MemoryCache) evictOne() {
	if c.lruList.Len() == 0 {
		return
	}

	// 从后向前查找可驱逐的条目
	var victimElement *list.Element
	var victimPriority = 11 // 比最高优先级还高

	for e := c.lruList.Back(); e != nil; e = e.Prev() {
		entry := e.Value.(*cacheEntry)

		// 跳过高优先级条目（除非没有其他选择）
		if entry.priority >= 8 && victimElement != nil {
			continue
		}

		// 选择优先级最低的
		if entry.priority < victimPriority {
			victimElement = e
			victimPriority = entry.priority
		}

		// 如果找到低优先级的就不再继续
		if victimPriority <= 3 {
			break
		}
	}

	// 如果没找到（全是高优先级），驱逐最旧的
	if victimElement == nil {
		victimElement = c.lruList.Back()
	}

	if victimElement != nil {
		entry := victimElement.Value.(*cacheEntry)
		c.remove(entry)
		c.evictions++
	}
}

// remove 移除条目（内部方法，调用者需持有锁）
func (c *MemoryCache) remove(entry *cacheEntry) {
	c.lruList.Remove(entry.element)
	delete(c.items, entry.key)
}

// Stats 获取统计信息
func (c *MemoryCache) Stats() CacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	total := c.hits + c.misses
	hitRate := 0.0
	if total > 0 {
		hitRate = float64(c.hits) / float64(total)
	}

	avgLatency := time.Duration(0)
	if c.getCount > 0 {
		avgLatency = c.totalGetLatency / time.Duration(c.getCount)
	}

	return CacheStats{
		L1Size:        len(c.items),
		L1Capacity:    c.capacity,
		L1HitRate:     hitRate,
		L1Hits:        c.hits,
		L1Misses:      c.misses,
		L1Evictions:   c.evictions,
		TotalRequests: total,
		TotalHitRate:  hitRate,
		AvgGetLatency: avgLatency,
	}
}

// Clear 清空缓存
func (c *MemoryCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = make(map[string]*cacheEntry, c.capacity)
	c.lruList = list.New()
}

// Size 返回当前缓存条目数
func (c *MemoryCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.items)
}

// ExpireOldEntries 清理过期条目
//
// 应定期调用此方法清理过期条目，释放内存。
// 建议每5-10分钟调用一次。
func (c *MemoryCache) ExpireOldEntries() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	expired := 0

	// 遍历所有条目，移除过期的
	for _, entry := range c.items {
		if now.After(entry.expireAt) {
			c.remove(entry)
			expired++
		}
	}

	return expired
}

// MultiLevelCache 实现多级缓存（L1内存 + L2 Redis）
//
// 缓存层次：
// L1 (内存) - 快速访问，容量有限
// L2 (Redis) - 较慢，容量大
// L3 (数据库) - 最慢，权威数据源
//
// 查询流程：
// Get -> L1 -> L2 -> Loader(L3) -> 回填L2和L1
type MultiLevelCache struct {
	l1     *MemoryCache
	l2     RedisCache // 接口，可替换实现
	loader DataLoader // 从数据库加载的函数

	// 统计
	l2Hits   int64
	l2Misses int64
	mu       sync.RWMutex
}

// RedisCache L2缓存接口（Redis或其他KV存储）
type RedisCache interface {
	Get(ctx context.Context, key string) (*BillingConfig, error)
	Set(ctx context.Context, key string, config *BillingConfig, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	Close() error
}

// DataLoader 从权威数据源加载数据
type DataLoader func(ctx context.Context, key string) (*BillingConfig, error)

// NewMultiLevelCache 创建多级缓存
func NewMultiLevelCache(l1Capacity int, l2 RedisCache, loader DataLoader) *MultiLevelCache {
	return &MultiLevelCache{
		l1:     NewMemoryCache(l1Capacity),
		l2:     l2,
		loader: loader,
	}
}

// Get 获取配置（穿透多级缓存）
func (c *MultiLevelCache) Get(ctx context.Context, key string) (*BillingConfig, error) {
	// L1: 内存缓存
	if config, hit := c.l1.Get(ctx, key); hit {
		return config, nil
	}

	// L2: Redis缓存
	if c.l2 != nil {
		config, err := c.l2.Get(ctx, key)
		if err == nil && config != nil {
			c.recordL2Hit()

			// 回填L1
			c.l1.Set(ctx, key, config)
			return config, nil
		}
		c.recordL2Miss()
	}

	// L3: 从数据库加载
	if c.loader == nil {
		return nil, fmt.Errorf("no data loader configured")
	}

	config, err := c.loader(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("load from database: %w", err)
	}

	if config == nil {
		return nil, fmt.Errorf("config not found: %s", key)
	}

	// 回填L2和L1
	if c.l2 != nil {
		c.l2.Set(ctx, key, config, config.CacheHint.TTL)
	}
	c.l1.Set(ctx, key, config)

	return config, nil
}

// Set 设置配置到缓存
func (c *MultiLevelCache) Set(ctx context.Context, key string, config *BillingConfig) error {
	// 写入L1
	if err := c.l1.Set(ctx, key, config); err != nil {
		return fmt.Errorf("set L1: %w", err)
	}

	// 写入L2
	if c.l2 != nil {
		if err := c.l2.Set(ctx, key, config, config.CacheHint.TTL); err != nil {
			// L2失败不影响L1
			// TODO: 记录错误日志
		}
	}

	return nil
}

// Delete 删除缓存
func (c *MultiLevelCache) Delete(ctx context.Context, key string) error {
	c.l1.Delete(ctx, key)

	if c.l2 != nil {
		c.l2.Delete(ctx, key)
	}

	return nil
}

// InvalidateTenant 失效租户的所有缓存
func (c *MultiLevelCache) InvalidateTenant(ctx context.Context, tenantID string) error {
	// 生成所有可能的键
	// 注意：这里需要维护一个租户->keys的映射，或者使用通配符删除
	// 简化实现：只删除已知的键格式
	tenantKey := CacheKey{}.ForTenant(tenantID)
	return c.Delete(ctx, tenantKey)
}

// PreloadBatch 批量预加载
func (c *MultiLevelCache) PreloadBatch(ctx context.Context, keys []string, loader func(key string) (*BillingConfig, error)) error {
	for _, key := range keys {
		config, err := loader(key)
		if err != nil {
			continue // 跳过失败的，继续下一个
		}

		if config != nil {
			c.Set(ctx, key, config)
		}
	}

	return nil
}

// Stats 获取统计信息
func (c *MultiLevelCache) Stats() CacheStats {
	stats := c.l1.Stats()

	// 添加L2统计
	c.mu.RLock()
	l2Total := c.l2Hits + c.l2Misses
	stats.L2Hits = c.l2Hits
	stats.L2Misses = c.l2Misses
	if l2Total > 0 {
		stats.L2HitRate = float64(c.l2Hits) / float64(l2Total)
	}
	c.mu.RUnlock()

	// 重新计算总命中率
	totalHits := stats.L1Hits + stats.L2Hits
	totalReqs := stats.TotalRequests
	if totalReqs > 0 {
		stats.TotalHitRate = float64(totalHits) / float64(totalReqs)
	}

	return stats
}

// Close 关闭缓存
func (c *MultiLevelCache) Close() error {
	if c.l2 != nil {
		return c.l2.Close()
	}
	return nil
}

// 内部辅助方法

func (c *MultiLevelCache) recordL2Hit() {
	c.mu.Lock()
	c.l2Hits++
	c.mu.Unlock()
}

func (c *MultiLevelCache) recordL2Miss() {
	c.mu.Lock()
	c.l2Misses++
	c.mu.Unlock()
}

// NopRedisCache 空实现（用于测试或无Redis环境）
type NopRedisCache struct{}

func (n *NopRedisCache) Get(ctx context.Context, key string) (*BillingConfig, error) {
	return nil, fmt.Errorf("not found")
}

func (n *NopRedisCache) Set(ctx context.Context, key string, config *BillingConfig, ttl time.Duration) error {
	return nil
}

func (n *NopRedisCache) Delete(ctx context.Context, key string) error {
	return nil
}

func (n *NopRedisCache) Close() error {
	return nil
}
