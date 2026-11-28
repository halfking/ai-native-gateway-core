package billing

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 注意：这些测试需要运行中的Redis实例
// 可以通过设置环境变量 SKIP_REDIS_TESTS=1 跳过这些测试

func skipIfNoRedis(t *testing.T) *redis.Client {
	// 检查是否跳过Redis测试
	// if os.Getenv("SKIP_REDIS_TESTS") == "1" {
	// 	t.Skip("Skipping Redis tests (SKIP_REDIS_TESTS=1)")
	// }

	// 创建测试用Redis客户端
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   15, // 使用测试专用数据库
	})

	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("Redis not available: %v", err)
	}

	// 清空测试数据库
	client.FlushDB(ctx)

	return client
}

// TestRedisBillingCache_BasicOperations tests basic Redis cache operations
func TestRedisBillingCache_BasicOperations(t *testing.T) {
	client := skipIfNoRedis(t)
	defer client.Close()

	ctx := context.Background()
	cache := NewRedisBillingCache(client, "test:")

	config := &BillingConfig{
		TenantID:    "tenant_1",
		BillingMode: ModePayAsYouGo,
		EngineConfig: map[string]interface{}{
			"cents_per_credit": 2.5,
		},
		CacheHint: CacheHint{
			Priority: 5,
			TTL:      5 * time.Minute,
		},
	}

	t.Run("set and get", func(t *testing.T) {
		err := cache.Set(ctx, "key1", config, 5*time.Minute)
		require.NoError(t, err)

		got, err := cache.Get(ctx, "key1")
		require.NoError(t, err)
		assert.Equal(t, config.TenantID, got.TenantID)
		assert.Equal(t, config.BillingMode, got.BillingMode)
	})

	t.Run("get non-existent key", func(t *testing.T) {
		_, err := cache.Get(ctx, "nonexistent")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("delete", func(t *testing.T) {
		cache.Set(ctx, "key2", config, 5*time.Minute)

		err := cache.Delete(ctx, "key2")
		assert.NoError(t, err)

		_, err = cache.Get(ctx, "key2")
		assert.Error(t, err)
	})
}

// TestRedisBillingCache_TTL tests TTL expiration
func TestRedisBillingCache_TTL(t *testing.T) {
	client := skipIfNoRedis(t)
	defer client.Close()

	ctx := context.Background()
	cache := NewRedisBillingCache(client, "test:")

	config := &BillingConfig{
		TenantID:    "tenant_1",
		BillingMode: ModePayAsYouGo,
		EngineConfig: map[string]interface{}{
			"cents_per_credit": 2.5,
		},
		CacheHint: CacheHint{
			Priority: 5,
			TTL:      1 * time.Second, // 短TTL用于测试
		},
	}

	cache.Set(ctx, "key1", config, 1*time.Second)

	// 立即获取应该成功
	_, err := cache.Get(ctx, "key1")
	assert.NoError(t, err)

	// 等待过期
	time.Sleep(2 * time.Second)

	// 应该已过期
	_, err = cache.Get(ctx, "key1")
	assert.Error(t, err)
}

// TestRedisBillingCache_ComplexConfig tests complex config with all fields
func TestRedisBillingCache_ComplexConfig(t *testing.T) {
	client := skipIfNoRedis(t)
	defer client.Close()

	ctx := context.Background()
	cache := NewRedisBillingCache(client, "test:")

	planID := int64(123)
	dailyMax := int64(10000)
	monthlyMax := int64(1000000)

	config := &BillingConfig{
		ID:          1,
		TenantID:    "tenant_complex",
		UserID:      "user_123",
		BillingMode: ModeTokenPlan,
		PlanID:      &planID,
		EngineConfig: map[string]interface{}{
			"monthly_quota": 1000000,
			"overage_rate":  0.015,
		},
		DiscountRules: []DiscountRule{
			{
				ID:          1,
				Type:        "percentage",
				Value:       0.1,
				Condition:   "volume > 10000000",
				Priority:    1,
				Description: "10% off for high volume",
			},
		},
		QuotaLimits: &QuotaLimits{
			DailyMax:   &dailyMax,
			MonthlyMax: &monthlyMax,
		},
		CacheHint: CacheHint{
			Priority:    8,
			TTL:         1 * time.Hour,
			PreloadHint: true,
		},
		CreatedAt: time.Now().Add(-24 * time.Hour),
		UpdatedAt: time.Now(),
	}

	// 存储复杂配置
	err := cache.Set(ctx, "complex", config, 5*time.Minute)
	require.NoError(t, err)

	// 读取并验证
	got, err := cache.Get(ctx, "complex")
	require.NoError(t, err)

	assert.Equal(t, config.TenantID, got.TenantID)
	assert.Equal(t, config.UserID, got.UserID)
	assert.Equal(t, config.BillingMode, got.BillingMode)
	assert.Equal(t, *config.PlanID, *got.PlanID)
	assert.Len(t, got.DiscountRules, 1)
	assert.Equal(t, *config.QuotaLimits.DailyMax, *got.QuotaLimits.DailyMax)
}

// TestRedisBillingCache_BatchOperations tests batch get/set
func TestRedisBillingCache_BatchOperations(t *testing.T) {
	client := skipIfNoRedis(t)
	defer client.Close()

	ctx := context.Background()
	cache := NewRedisBillingCache(client, "test:")

	// 准备批量配置
	configs := make(map[string]*BillingConfig)
	for i := 1; i <= 10; i++ {
		configs[string(rune('a'+i-1))] = &BillingConfig{
			TenantID:    string(rune('a' + i - 1)),
			BillingMode: ModePayAsYouGo,
			EngineConfig: map[string]interface{}{
				"cents_per_credit": float64(i),
			},
			CacheHint: CacheHint{
				Priority: 5,
				TTL:      5 * time.Minute,
			},
		}
	}

	t.Run("batch set", func(t *testing.T) {
		err := cache.BatchSetConfigs(ctx, configs, 5*time.Minute)
		assert.NoError(t, err)
	})

	t.Run("batch get", func(t *testing.T) {
		keys := []string{"a", "b", "c", "d", "e"}
		results, err := cache.BatchGetConfigs(ctx, keys)
		require.NoError(t, err)

		assert.Len(t, results, 5)
		for _, key := range keys {
			assert.Contains(t, results, key)
			assert.Equal(t, key, results[key].TenantID)
		}
	})
}

// TestRedisBillingCache_DeleteByPattern tests pattern-based deletion
func TestRedisBillingCache_DeleteByPattern(t *testing.T) {
	client := skipIfNoRedis(t)
	defer client.Close()

	ctx := context.Background()
	cache := NewRedisBillingCache(client, "test:")

	config := &BillingConfig{
		TenantID:    "tenant_1",
		BillingMode: ModePayAsYouGo,
		EngineConfig: map[string]interface{}{
			"cents_per_credit": 2.5,
		},
		CacheHint: CacheHint{
			Priority: 5,
			TTL:      5 * time.Minute,
		},
	}

	// 创建多个带有相同前缀的键
	cache.Set(ctx, "tenant:1:user:a", config, 5*time.Minute)
	cache.Set(ctx, "tenant:1:user:b", config, 5*time.Minute)
	cache.Set(ctx, "tenant:2:user:a", config, 5*time.Minute)

	// 删除tenant:1的所有键
	err := cache.DeleteByPattern(ctx, "tenant:1:*")
	assert.NoError(t, err)

	// 验证
	_, err = cache.Get(ctx, "tenant:1:user:a")
	assert.Error(t, err, "should be deleted")

	_, err = cache.Get(ctx, "tenant:1:user:b")
	assert.Error(t, err, "should be deleted")

	_, err = cache.Get(ctx, "tenant:2:user:a")
	assert.NoError(t, err, "should still exist")
}

// TestResilientRedisCache_ErrorHandling tests error resilience
func TestResilientRedisCache_ErrorHandling(t *testing.T) {
	// 创建一个指向不存在Redis的客户端
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:16379", // 错误的端口
		DB:   15,
	})
	defer client.Close()

	errorCount := 0
	errorCallback := func(operation string, err error) {
		errorCount++
	}

	ctx := context.Background()
	cache := NewResilientRedisCache(client, "test:", errorCallback)

	config := &BillingConfig{
		TenantID:    "tenant_1",
		BillingMode: ModePayAsYouGo,
		EngineConfig: map[string]interface{}{
			"cents_per_credit": 2.5,
		},
		CacheHint: CacheHint{
			Priority: 5,
			TTL:      5 * time.Minute,
		},
	}

	t.Run("get with error", func(t *testing.T) {
		_, err := cache.Get(ctx, "key1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
		assert.Equal(t, 1, errorCount)
	})

	t.Run("set with error", func(t *testing.T) {
		err := cache.Set(ctx, "key1", config, 5*time.Minute)
		assert.NoError(t, err, "should not return error (resilient)")
		assert.Equal(t, 2, errorCount)
	})

	t.Run("delete with error", func(t *testing.T) {
		err := cache.Delete(ctx, "key1")
		assert.NoError(t, err, "should not return error (resilient)")
		assert.Equal(t, 3, errorCount)
	})
}

// TestRedisBillingCache_Stats tests stats collection
func TestRedisBillingCache_Stats(t *testing.T) {
	client := skipIfNoRedis(t)
	defer client.Close()

	ctx := context.Background()
	cache := NewRedisBillingCache(client, "test:")

	stats, err := cache.Stats(ctx)
	require.NoError(t, err)

	assert.Contains(t, stats, "db_size")
	assert.Contains(t, stats, "connected")
	assert.True(t, stats["connected"].(bool))
}

// TestRedisBillingCache_Ping tests connection check
func TestRedisBillingCache_Ping(t *testing.T) {
	client := skipIfNoRedis(t)
	defer client.Close()

	ctx := context.Background()
	cache := NewRedisBillingCache(client, "test:")

	err := cache.Ping(ctx)
	assert.NoError(t, err)
}

// TestNewRedisClientFromOptions tests client creation from options
func TestNewRedisClientFromOptions(t *testing.T) {
	opts := RedisConfigOptions{
		Addr:     "localhost:6379",
		DB:       15,
		PoolSize: 20,
	}

	client := NewRedisClientFromOptions(opts)
	assert.NotNil(t, client)

	ctx := context.Background()
	err := client.Ping(ctx).Err()
	if err == nil {
		client.Close()
	}
}

// BenchmarkRedisBillingCache_Get benchmarks Redis get performance
func BenchmarkRedisBillingCache_Get(b *testing.B) {
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   15,
	})
	defer client.Close()

	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		b.Skip("Redis not available")
	}

	cache := NewRedisBillingCache(client, "bench:")
	config := &BillingConfig{
		TenantID:    "tenant_1",
		BillingMode: ModePayAsYouGo,
		EngineConfig: map[string]interface{}{
			"cents_per_credit": 2.5,
		},
		CacheHint: CacheHint{
			Priority: 5,
			TTL:      5 * time.Minute,
		},
	}

	cache.Set(ctx, "key1", config, 5*time.Minute)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.Get(ctx, "key1")
	}
}

// BenchmarkRedisBillingCache_BatchGet benchmarks batch get performance
func BenchmarkRedisBillingCache_BatchGet(b *testing.B) {
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   15,
	})
	defer client.Close()

	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		b.Skip("Redis not available")
	}

	cache := NewRedisBillingCache(client, "bench:")
	
	// 准备测试数据
	configs := make(map[string]*BillingConfig)
	keys := make([]string, 100)
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("key%d", i)
		keys[i] = key
		configs[key] = &BillingConfig{
			TenantID:    fmt.Sprintf("tenant_%d", i),
			BillingMode: ModePayAsYouGo,
			EngineConfig: map[string]interface{}{
				"cents_per_credit": 2.5,
			},
			CacheHint: CacheHint{
				Priority: 5,
				TTL:      5 * time.Minute,
			},
		}
	}

	cache.BatchSetConfigs(ctx, configs, 5*time.Minute)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.BatchGetConfigs(ctx, keys)
	}
}
