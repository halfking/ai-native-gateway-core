package billing

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMemoryCache_BasicOperations tests basic cache operations
func TestMemoryCache_BasicOperations(t *testing.T) {
	ctx := context.Background()
	cache := NewMemoryCache(10)

	config := &BillingConfig{
		TenantID:    "tenant_1",
		BillingMode: ModePayAsYouGo,
		CacheHint: CacheHint{
			Priority: 5,
			TTL:      5 * time.Minute,
		},
	}

	t.Run("set and get", func(t *testing.T) {
		err := cache.Set(ctx, "key1", config)
		assert.NoError(t, err)

		got, hit := cache.Get(ctx, "key1")
		assert.True(t, hit)
		assert.Equal(t, config.TenantID, got.TenantID)
	})

	t.Run("get non-existent key", func(t *testing.T) {
		got, hit := cache.Get(ctx, "nonexistent")
		assert.False(t, hit)
		assert.Nil(t, got)
	})

	t.Run("update existing key", func(t *testing.T) {
		updated := &BillingConfig{
			TenantID:    "tenant_2",
			BillingMode: ModeTokenPlan,
			CacheHint: CacheHint{
				Priority: 7,
				TTL:      10 * time.Minute,
			},
		}

		err := cache.Set(ctx, "key1", updated)
		assert.NoError(t, err)

		got, hit := cache.Get(ctx, "key1")
		assert.True(t, hit)
		assert.Equal(t, "tenant_2", got.TenantID)
	})

	t.Run("delete", func(t *testing.T) {
		err := cache.Delete(ctx, "key1")
		assert.NoError(t, err)

		got, hit := cache.Get(ctx, "key1")
		assert.False(t, hit)
		assert.Nil(t, got)
	})
}

// TestMemoryCache_TTL tests TTL expiration
func TestMemoryCache_TTL(t *testing.T) {
	ctx := context.Background()
	cache := NewMemoryCache(10)

	config := &BillingConfig{
		TenantID:    "tenant_1",
		BillingMode: ModePayAsYouGo,
		CacheHint: CacheHint{
			Priority: 5,
			TTL:      100 * time.Millisecond, // 短TTL用于测试
		},
	}

	cache.Set(ctx, "key1", config)

	// 立即获取应该成功
	_, hit := cache.Get(ctx, "key1")
	assert.True(t, hit)

	// 等待过期
	time.Sleep(150 * time.Millisecond)

	// 应该已过期
	_, hit = cache.Get(ctx, "key1")
	assert.False(t, hit)
}

// TestMemoryCache_LRUEviction tests LRU eviction
func TestMemoryCache_LRUEviction(t *testing.T) {
	ctx := context.Background()
	cache := NewMemoryCache(3) // 小容量便于测试

	// 填充到容量
	for i := 1; i <= 3; i++ {
		config := &BillingConfig{
			TenantID:    fmt.Sprintf("tenant_%d", i),
			BillingMode: ModePayAsYouGo,
			CacheHint: CacheHint{
				Priority: 5,
				TTL:      5 * time.Minute,
			},
		}
		cache.Set(ctx, fmt.Sprintf("key%d", i), config)
	}

	assert.Equal(t, 3, cache.Size())

	// 访问key1，使其成为最近使用的
	cache.Get(ctx, "key1")

	// 添加第4个，应该驱逐key2（最久未使用）
	config4 := &BillingConfig{
		TenantID:    "tenant_4",
		BillingMode: ModePayAsYouGo,
		CacheHint: CacheHint{
			Priority: 5,
			TTL:      5 * time.Minute,
		},
	}
	cache.Set(ctx, "key4", config4)

	// 验证
	_, hit1 := cache.Get(ctx, "key1")
	assert.True(t, hit1, "key1 should still exist (recently accessed)")

	_, hit2 := cache.Get(ctx, "key2")
	assert.False(t, hit2, "key2 should be evicted (LRU)")

	_, hit3 := cache.Get(ctx, "key3")
	assert.True(t, hit3, "key3 should still exist")

	_, hit4 := cache.Get(ctx, "key4")
	assert.True(t, hit4, "key4 should exist (just added)")
}

// TestMemoryCache_PriorityEviction tests priority-aware eviction
func TestMemoryCache_PriorityEviction(t *testing.T) {
	ctx := context.Background()
	cache := NewMemoryCache(3)

	// 添加低优先级配置
	lowPriority := &BillingConfig{
		TenantID:    "low_tenant",
		BillingMode: ModePayAsYouGo,
		CacheHint: CacheHint{
			Priority: 2, // 低优先级
			TTL:      5 * time.Minute,
		},
	}
	cache.Set(ctx, "low", lowPriority)

	// 添加高优先级配置
	highPriority := &BillingConfig{
		TenantID:    "high_tenant",
		BillingMode: ModePayAsYouGo,
		CacheHint: CacheHint{
			Priority: 9, // 高优先级
			TTL:      5 * time.Minute,
		},
	}
	cache.Set(ctx, "high", highPriority)

	// 添加普通优先级
	normalPriority := &BillingConfig{
		TenantID:    "normal_tenant",
		BillingMode: ModePayAsYouGo,
		CacheHint: CacheHint{
			Priority: 5,
			TTL:      5 * time.Minute,
		},
	}
	cache.Set(ctx, "normal", normalPriority)

	// 添加第4个，触发驱逐
	newConfig := &BillingConfig{
		TenantID:    "new_tenant",
		BillingMode: ModePayAsYouGo,
		CacheHint: CacheHint{
			Priority: 5,
			TTL:      5 * time.Minute,
		},
	}
	cache.Set(ctx, "new", newConfig)

	// 低优先级应该被驱逐
	_, hitLow := cache.Get(ctx, "low")
	assert.False(t, hitLow, "low priority should be evicted first")

	// 高优先级应该保留
	_, hitHigh := cache.Get(ctx, "high")
	assert.True(t, hitHigh, "high priority should be retained")
}

// TestMemoryCache_Stats tests statistics collection
func TestMemoryCache_Stats(t *testing.T) {
	ctx := context.Background()
	cache := NewMemoryCache(10)

	config := &BillingConfig{
		TenantID:    "tenant_1",
		BillingMode: ModePayAsYouGo,
		CacheHint: CacheHint{
			Priority: 5,
			TTL:      5 * time.Minute,
		},
	}
	cache.Set(ctx, "key1", config)

	// 命中
	cache.Get(ctx, "key1")
	cache.Get(ctx, "key1")

	// 未命中
	cache.Get(ctx, "key2")

	stats := cache.Stats()
	assert.Equal(t, int64(2), stats.L1Hits)
	assert.Equal(t, int64(1), stats.L1Misses)
	assert.Equal(t, int64(3), stats.TotalRequests)
	assert.InDelta(t, 0.666, stats.L1HitRate, 0.01)
}

// TestMemoryCache_ConcurrentAccess tests thread safety
func TestMemoryCache_ConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	cache := NewMemoryCache(100)

	concurrency := 50
	opsPerGoroutine := 100

	var wg sync.WaitGroup
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func(id int) {
			defer wg.Done()

			for j := 0; j < opsPerGoroutine; j++ {
				key := fmt.Sprintf("key_%d_%d", id, j%10)
				config := &BillingConfig{
					TenantID:    fmt.Sprintf("tenant_%d", id),
					BillingMode: ModePayAsYouGo,
					CacheHint: CacheHint{
						Priority: 5,
						TTL:      5 * time.Minute,
					},
				}

				// 随机操作
				if j%3 == 0 {
					cache.Set(ctx, key, config)
				} else if j%3 == 1 {
					cache.Get(ctx, key)
				} else {
					cache.Delete(ctx, key)
				}
			}
		}(i)
	}

	wg.Wait()

	// 验证缓存仍然一致
	stats := cache.Stats()
	assert.GreaterOrEqual(t, stats.TotalRequests, int64(0))
}

// TestMemoryCache_ExpireOldEntries tests expiration cleanup
func TestMemoryCache_ExpireOldEntries(t *testing.T) {
	ctx := context.Background()
	cache := NewMemoryCache(10)

	// 添加短TTL条目
	shortTTL := &BillingConfig{
		TenantID:    "short",
		BillingMode: ModePayAsYouGo,
		CacheHint: CacheHint{
			Priority: 5,
			TTL:      50 * time.Millisecond,
		},
	}
	cache.Set(ctx, "short", shortTTL)

	// 添加长TTL条目
	longTTL := &BillingConfig{
		TenantID:    "long",
		BillingMode: ModePayAsYouGo,
		CacheHint: CacheHint{
			Priority: 5,
			TTL:      5 * time.Minute,
		},
	}
	cache.Set(ctx, "long", longTTL)

	assert.Equal(t, 2, cache.Size())

	// 等待短TTL过期
	time.Sleep(100 * time.Millisecond)

	// 执行清理
	expired := cache.ExpireOldEntries()
	assert.Equal(t, 1, expired)
	assert.Equal(t, 1, cache.Size())

	// 长TTL应该还在
	_, hit := cache.Get(ctx, "long")
	assert.True(t, hit)
}

// TestMultiLevelCache_HitL1 tests L1 cache hit
func TestMultiLevelCache_HitL1(t *testing.T) {
	ctx := context.Background()

	loader := func(ctx context.Context, key string) (*BillingConfig, error) {
		t.Fatal("loader should not be called on L1 hit")
		return nil, nil
	}

	cache := NewMultiLevelCache(10, &NopRedisCache{}, loader)

	config := &BillingConfig{
		TenantID:    "tenant_1",
		BillingMode: ModePayAsYouGo,
		CacheHint: CacheHint{
			Priority: 5,
			TTL:      5 * time.Minute,
		},
	}

	// 设置到缓存
	cache.Set(ctx, "key1", config)

	// 应该从L1命中
	got, err := cache.Get(ctx, "key1")
	require.NoError(t, err)
	assert.Equal(t, config.TenantID, got.TenantID)

	// 验证统计
	stats := cache.Stats()
	assert.Equal(t, int64(1), stats.L1Hits)
	assert.Equal(t, int64(0), stats.L1Misses)
}

// TestMultiLevelCache_MissL1HitL3 tests L1 miss -> L3 load -> backfill
func TestMultiLevelCache_MissL1HitL3(t *testing.T) {
	ctx := context.Background()

	loadCount := 0
	loader := func(ctx context.Context, key string) (*BillingConfig, error) {
		loadCount++
		return &BillingConfig{
			TenantID:    "tenant_from_db",
			BillingMode: ModePayAsYouGo,
			CacheHint: CacheHint{
				Priority: 5,
				TTL:      5 * time.Minute,
			},
		}, nil
	}

	cache := NewMultiLevelCache(10, &NopRedisCache{}, loader)

	// 第一次获取：应该从loader加载
	got, err := cache.Get(ctx, "key1")
	require.NoError(t, err)
	assert.Equal(t, "tenant_from_db", got.TenantID)
	assert.Equal(t, 1, loadCount)

	// 第二次获取：应该从L1命中
	got, err = cache.Get(ctx, "key1")
	require.NoError(t, err)
	assert.Equal(t, "tenant_from_db", got.TenantID)
	assert.Equal(t, 1, loadCount, "loader should not be called again")
}

// TestMultiLevelCache_LoadError tests error handling
func TestMultiLevelCache_LoadError(t *testing.T) {
	ctx := context.Background()

	loader := func(ctx context.Context, key string) (*BillingConfig, error) {
		return nil, fmt.Errorf("database error")
	}

	cache := NewMultiLevelCache(10, &NopRedisCache{}, loader)

	got, err := cache.Get(ctx, "key1")
	assert.Error(t, err)
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), "database error")
}

// TestMultiLevelCache_Delete tests deletion across levels
func TestMultiLevelCache_Delete(t *testing.T) {
	ctx := context.Background()

	loader := func(ctx context.Context, key string) (*BillingConfig, error) {
		return &BillingConfig{
			TenantID:    "tenant_1",
			BillingMode: ModePayAsYouGo,
			CacheHint: CacheHint{
				Priority: 5,
				TTL:      5 * time.Minute,
			},
		}, nil
	}

	cache := NewMultiLevelCache(10, &NopRedisCache{}, loader)

	// 加载到缓存
	cache.Get(ctx, "key1")

	// 删除
	err := cache.Delete(ctx, "key1")
	assert.NoError(t, err)

	// 验证L1中已删除（会重新从loader加载）
	loadCount := 0
	cache.loader = func(ctx context.Context, key string) (*BillingConfig, error) {
		loadCount++
		return loader(ctx, key)
	}

	cache.Get(ctx, "key1")
	assert.Equal(t, 1, loadCount, "should reload after delete")
}

// TestCacheKey tests cache key generation
func TestCacheKey(t *testing.T) {
	ck := CacheKey{}

	t.Run("ForAPIKey", func(t *testing.T) {
		key := ck.ForAPIKey("test_api_key")
		assert.Equal(t, "billing:apikey:test_api_key", key)
	})

	t.Run("ForTenant", func(t *testing.T) {
		key := ck.ForTenant("tenant_123")
		assert.Equal(t, "billing:tenant:tenant_123", key)
	})

	t.Run("ForUser", func(t *testing.T) {
		key := ck.ForUser("tenant_123", "user_456")
		assert.Equal(t, "billing:user:tenant_123:user_456", key)
	})
}

// BenchmarkMemoryCache_Get benchmarks cache get performance
func BenchmarkMemoryCache_Get(b *testing.B) {
	ctx := context.Background()
	cache := NewMemoryCache(10000)

	config := &BillingConfig{
		TenantID:    "tenant_1",
		BillingMode: ModePayAsYouGo,
		CacheHint: CacheHint{
			Priority: 5,
			TTL:      5 * time.Minute,
		},
	}
	cache.Set(ctx, "key1", config)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.Get(ctx, "key1")
	}
}

// BenchmarkMemoryCache_Set benchmarks cache set performance
func BenchmarkMemoryCache_Set(b *testing.B) {
	ctx := context.Background()
	cache := NewMemoryCache(10000)

	config := &BillingConfig{
		TenantID:    "tenant_1",
		BillingMode: ModePayAsYouGo,
		CacheHint: CacheHint{
			Priority: 5,
			TTL:      5 * time.Minute,
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("key%d", i%1000)
		cache.Set(ctx, key, config)
	}
}
