// Package cache 实现请求/响应缓存领域 (Hook)。
//
// 阶段定位：
//   - CacheLookupHook   → PhasePreRouting (PreRouting 阶段检查缓存命中)
//   - CacheSaveHook     → PhasePostResponse (PostResponse 阶段保存响应到缓存)
//
// 与旧 cache/ 包的关系：
//   - 本包是新抽象（与 Hook Pipeline 对齐），不依赖旧 cache/semantic、cache/prefix、sessions/session_cache.go
//   - 旧代码保持不变；未来如需彻底替换旧实现，由协调 Agent 决定迁移策略
//   - 本包只定义 CacheKey/Entry/Store 接口和一个进程内 InMemoryStore 实现
//
// 多租户隔离：
//   - CacheKey 必含 TenantID；跨租户命中在接口语义上不可能
package cache

import (
	"sync"
	"time"
)

// CacheKey 缓存键。
//
// 三元组 (TenantID, Model, Hash) 唯一标识一次"租户 + 模型 + 请求内容"的
// 缓存条目。Hash 由调用方从 TransformedRequest 字节计算（SHA-256 十六进制）。
type CacheKey struct {
	// TenantID 租户 ID（必填；空字符串视为非法 key）
	TenantID string
	// Model 模型名称（必填）
	Model string
	// Hash 请求体哈希（建议 SHA-256 十六进制）
	Hash string
}

// IsValid 报告 key 是否具有非空三元组。
func (k CacheKey) IsValid() bool {
	return k.TenantID != "" && k.Model != "" && k.Hash != ""
}

// String 返回可读的 key 表示（用于日志/调试）。
func (k CacheKey) String() string {
	return k.TenantID + "|" + k.Model + "|" + k.Hash
}

// CacheEntry 缓存条目。
type CacheEntry struct {
	// Key 缓存键（嵌入）
	Key CacheKey
	// Value 响应字节（约定为 JSON 或 SSE 字节流）
	Value []byte
	// CreatedAt 创建时间
	CreatedAt time.Time
	// TTL 过期时长；<=0 表示永不过期
	TTL time.Duration
}

// IsExpired 报告条目是否已过期。
func (e *CacheEntry) IsExpired() bool {
	if e == nil || e.TTL <= 0 {
		return false
	}
	return time.Since(e.CreatedAt) > e.TTL
}

// Store 缓存存储接口。
//
// 本接口刻意保持极简（Get/Set/Delete），让 InMemoryStore、Redis 后端、
// Memcached 后端都可以轻松实现。Lookup/Save Hook 只依赖此接口。
//
// 实现必须保证并发安全。
type Store interface {
	// Get 读取条目。
	// 返回 (entry, true, nil) 表示命中且未过期。
	// 返回 (nil, false, nil) 表示未命中或已过期。
	// 仅当底层存储故障时才返回 non-nil error。
	Get(key CacheKey) (entry *CacheEntry, hit bool, err error)

	// Set 写入条目；幂等（同 key 覆盖）。
	Set(entry *CacheEntry) error

	// Delete 删除条目；key 不存在视为成功（无 error）。
	Delete(key CacheKey) error
}

// InMemoryStore 进程内缓存存储。
//
// 用途：
//   - 单元测试（无需外部依赖）
//   - 单机部署（性能足够且零依赖）
//   - 默认 fallback（外部 Store 故障时降级到 InMemoryStore）
//
// 并发：使用 sync.RWMutex 保护 entries map。
type InMemoryStore struct {
	mu      sync.RWMutex
	entries map[CacheKey]*CacheEntry
}

// NewInMemoryStore 创建一个空 InMemoryStore。
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		entries: make(map[CacheKey]*CacheEntry),
	}
}

// Get 实现 Store.Get。
func (s *InMemoryStore) Get(key CacheKey) (*CacheEntry, bool, error) {
	s.mu.RLock()
	entry, ok := s.entries[key]
	s.mu.RUnlock()
	if !ok {
		return nil, false, nil
	}
	if entry.IsExpired() {
		// 惰性删除过期条目
		s.mu.Lock()
		// 双重检查（避免覆盖刚写入的）
		if cur, still := s.entries[key]; still && cur.IsExpired() {
			delete(s.entries, key)
		}
		s.mu.Unlock()
		return nil, false, nil
	}
	return entry, true, nil
}

// Set 实现 Store.Set。
func (s *InMemoryStore) Set(entry *CacheEntry) error {
	if entry == nil {
		return nil
	}
	s.mu.Lock()
	s.entries[entry.Key] = entry
	s.mu.Unlock()
	return nil
}

// Delete 实现 Store.Delete。
func (s *InMemoryStore) Delete(key CacheKey) error {
	s.mu.Lock()
	delete(s.entries, key)
	s.mu.Unlock()
	return nil
}

// Size 返回当前条目数（用于测试 / metrics）。
func (s *InMemoryStore) Size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// 编译期接口断言。
var _ Store = (*InMemoryStore)(nil)
