package core

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ManagerConfig 管理器配置
type ManagerConfig struct {
	// 缓存配置
	CacheTTL     time.Duration // 缓存有效期（默认 5 分钟）
	CacheMaxSize int           // 缓存最大条目数（默认 10000）

	// 存储配置
	EnableStorage   bool          // 是否启用持久化存储
	StorageTimeout  time.Duration // 存储操作超时（默认 5 秒）
	StorageBatchSize int          // 批量操作大小（默认 100）

	// 监控配置
	EnableMetrics bool // 是否启用指标收集
	EnableAlerts  bool // 是否启用告警

	// 性能配置
	MaxConcurrency int // 最大并发数（默认 100）
}

// DefaultManagerConfig 返回默认配置
func DefaultManagerConfig() ManagerConfig {
	return ManagerConfig{
		CacheTTL:         5 * time.Minute,
		CacheMaxSize:     10000,
		EnableStorage:    true,
		StorageTimeout:   5 * time.Second,
		StorageBatchSize: 100,
		EnableMetrics:    true,
		EnableAlerts:     true,
		MaxConcurrency:   100,
	}
}

// BucketManager 桶管理器
// 统一管理所有类型的桶，提供缓存、监控、告警等功能
type BucketManager struct {
	config     ManagerConfig
	providers  map[BucketType]BucketProvider
	cache      BucketCache
	storage    BucketStorage
	metrics    MetricsCollector
	alerts     AlertManager
	mu         sync.RWMutex
}

// NewBucketManager 创建新的桶管理器
func NewBucketManager(config ManagerConfig) *BucketManager {
	return &BucketManager{
		config:    config,
		providers: make(map[BucketType]BucketProvider),
	}
}

// RegisterProvider 注册桶提供者
func (m *BucketManager) RegisterProvider(provider BucketProvider) error {
	if provider == nil {
		return &BucketError{
			Type:    ErrTypeInvalidInput,
			Message: "provider cannot be nil",
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	bucketType := provider.Type()
	if _, exists := m.providers[bucketType]; exists {
		return &BucketError{
			Type:    ErrTypeInvalidInput,
			Message: fmt.Sprintf("provider for type %s already registered", bucketType),
		}
	}

	m.providers[bucketType] = provider
	return nil
}

// UnregisterProvider 注销桶提供者
func (m *BucketManager) UnregisterProvider(bucketType BucketType) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.providers[bucketType]; !exists {
		return &BucketError{
			Type:    ErrTypeNotFound,
			Message: fmt.Sprintf("provider for type %s not found", bucketType),
		}
	}

	delete(m.providers, bucketType)
	return nil
}

// GetProvider 获取指定类型的桶提供者
func (m *BucketManager) GetProvider(bucketType BucketType) (BucketProvider, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	provider, exists := m.providers[bucketType]
	if !exists {
		return nil, &BucketError{
			Type:    ErrTypeNotFound,
			Message: fmt.Sprintf("provider for type %s not found", bucketType),
		}
	}

	return provider, nil
}

// SetCache 设置缓存实现
func (m *BucketManager) SetCache(cache BucketCache) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cache = cache
}

// SetStorage 设置存储实现
func (m *BucketManager) SetStorage(storage BucketStorage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.storage = storage
}

// SetMetrics 设置指标收集器
func (m *BucketManager) SetMetrics(metrics MetricsCollector) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.metrics = metrics
}

// SetAlerts 设置告警管理器
func (m *BucketManager) SetAlerts(alerts AlertManager) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.alerts = alerts
}

// GetState 获取桶状态（带缓存）
func (m *BucketManager) GetState(ctx context.Context, key BucketKey) (*BucketState, error) {
	startTime := time.Now()
	defer func() {
		if m.metrics != nil {
			m.metrics.RecordRequest(key.Type, true, time.Since(startTime))
		}
	}()

	// 1. 尝试从缓存获取
	if m.cache != nil {
		if state, ok := m.cache.Get(key); ok {
			if m.metrics != nil {
				m.metrics.RecordCacheHit(key.Type, true)
			}
			return state, nil
		}
		if m.metrics != nil {
			m.metrics.RecordCacheHit(key.Type, false)
		}
	}

	// 2. 从 provider 获取
	provider, err := m.GetProvider(key.Type)
	if err != nil {
		return nil, err
	}

	state, err := provider.GetState(ctx, key)
	if err != nil {
		if m.metrics != nil {
			m.metrics.RecordRequest(key.Type, false, time.Since(startTime))
		}
		return nil, err
	}

	// 3. 更新缓存
	if m.cache != nil && state != nil {
		_ = m.cache.Set(key, state, m.config.CacheTTL)
	}

	return state, nil
}

// UpdateState 更新桶状态
func (m *BucketManager) UpdateState(ctx context.Context, key BucketKey, delta BucketDelta) (*BucketState, error) {
	startTime := time.Now()
	defer func() {
		if m.metrics != nil {
			m.metrics.RecordRequest(key.Type, true, time.Since(startTime))
		}
	}()

	// 1. 获取 provider
	provider, err := m.GetProvider(key.Type)
	if err != nil {
		return nil, err
	}

	// 2. 更新状态
	state, err := provider.UpdateState(ctx, key, delta)
	if err != nil {
		if m.metrics != nil {
			m.metrics.RecordRequest(key.Type, false, time.Since(startTime))
		}
		return nil, err
	}

	// 3. 更新缓存
	if m.cache != nil && state != nil {
		_ = m.cache.Set(key, state, m.config.CacheTTL)
	}

	// 4. 持久化（异步）
	if m.storage != nil && m.config.EnableStorage {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), m.config.StorageTimeout)
			defer cancel()
			_ = m.storage.Save(ctx, key, state)
		}()
	}

	// 5. 记录指标
	if m.metrics != nil && state != nil {
		m.metrics.RecordQuotaUsage(key.Type, key.TenantID, state.Used, state.Capacity)
	}

	return state, nil
}

// Query 执行桶查询
func (m *BucketManager) Query(ctx context.Context, query BucketQuery) (*BucketResult, error) {
	provider, err := m.GetProvider(query.Type)
	if err != nil {
		return nil, err
	}

	return provider.Query(ctx, query)
}

// Reset 重置桶
func (m *BucketManager) Reset(ctx context.Context, key BucketKey) error {
	provider, err := m.GetProvider(key.Type)
	if err != nil {
		return err
	}

	// 清除缓存
	if m.cache != nil {
		_ = m.cache.Delete(key)
	}

	return provider.Reset(ctx, key)
}

// GetMetrics 获取指标
func (m *BucketManager) GetMetrics(bucketType BucketType) (*BucketMetrics, error) {
	if m.metrics == nil {
		return nil, &BucketError{
			Type:    ErrTypeNotFound,
			Message: "metrics collector not configured",
		}
	}

	return m.metrics.GetMetrics(bucketType)
}

// ClearCache 清空缓存
func (m *BucketManager) ClearCache() error {
	if m.cache == nil {
		return nil
	}
	return m.cache.Clear()
}

// GetCacheStats 获取缓存统计
func (m *BucketManager) GetCacheStats() CacheStats {
	if m.cache == nil {
		return CacheStats{}
	}
	return m.cache.GetStats()
}

// ListProviders 列出所有已注册的提供者
func (m *BucketManager) ListProviders() []BucketType {
	m.mu.RLock()
	defer m.mu.RUnlock()

	types := make([]BucketType, 0, len(m.providers))
	for t := range m.providers {
		types = append(types, t)
	}
	return types
}

// Shutdown 关闭管理器，清理资源
func (m *BucketManager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 清空缓存
	if m.cache != nil {
		_ = m.cache.Clear()
	}

	// TODO: 关闭 providers（如果需要）

	return nil
}
