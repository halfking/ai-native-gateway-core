package casdoor

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupMockRedis creates a mock Redis client for testing.
// In real tests, you'd use miniredis or a real Redis instance.
func setupMockRedis(t *testing.T) *redis.Client {
	// For unit tests, we'll use a Redis mock or skip if Redis not available
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   15, // Use a separate DB for tests
	})

	// Test connection
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Skip("Redis not available, skipping cache tests")
	}

	// Clear test database
	client.FlushDB(ctx)

	return client
}

func TestNewCachedIDMapper(t *testing.T) {
	mapper := &IDMapper{} // Mock mapper
	redisClient := setupMockRedis(t)
	defer redisClient.Close()

	t.Run("WithDefaultTTL", func(t *testing.T) {
		cached := NewCachedIDMapper(mapper, redisClient, 0)
		assert.NotNil(t, cached)
		assert.Equal(t, 1*time.Hour, cached.ttl)
	})

	t.Run("WithCustomTTL", func(t *testing.T) {
		ttl := 30 * time.Minute
		cached := NewCachedIDMapper(mapper, redisClient, ttl)
		assert.NotNil(t, cached)
		assert.Equal(t, ttl, cached.ttl)
	})
}

func TestCachedIDMapper_CacheInvalidation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cache test in short mode")
	}

	redisClient := setupMockRedis(t)
	defer redisClient.Close()

	ctx := context.Background()

	// Test data
	provider := "casdoor"
	externalID := "ext-123"
	tenantID := "tenant-1"
	username := "testuser"
	internalID := 42

	t.Run("InvalidateMapping", func(t *testing.T) {
		// Create a cached mapper with mock
		mapper := &IDMapper{} // Mock mapper
		cached := NewCachedIDMapper(mapper, redisClient, 5*time.Minute)

		// Manually set cache entries
		cached.cache.HMSet(ctx, "id_map:internal:42", map[string]interface{}{
			"provider":    provider,
			"external_id": externalID,
			"tenant_id":   tenantID,
		})
		cached.cache.Set(ctx, "id_map:external:casdoor:ext-123:tenant-1", 42, 5*time.Minute)
		cached.cache.HMSet(ctx, "id_map:username:casdoor:testuser:tenant-1", map[string]interface{}{
			"internal_id": 42,
			"external_id": externalID,
		})

		// Verify cache entries exist
		exists, _ := cached.cache.Exists(ctx, "id_map:internal:42").Result()
		assert.Greater(t, exists, int64(0))

		// Invalidate
		cached.invalidateMapping(ctx, provider, externalID, tenantID, username, internalID)

		// Verify all entries are gone
		exists, _ = cached.cache.Exists(ctx,
			"id_map:internal:42",
			"id_map:external:casdoor:ext-123:tenant-1",
			"id_map:username:casdoor:testuser:tenant-1",
		).Result()
		assert.Equal(t, int64(0), exists)
	})
}

func TestCachedIDMapper_ClearCache(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cache test in short mode")
	}

	redisClient := setupMockRedis(t)
	defer redisClient.Close()

	ctx := context.Background()
	mapper := &IDMapper{} // Mock mapper
	cached := NewCachedIDMapper(mapper, redisClient, 5*time.Minute)

	t.Run("ClearAllCacheEntries", func(t *testing.T) {
		// Add some cache entries
		cached.cache.Set(ctx, "id_map:internal:1", "data1", 5*time.Minute)
		cached.cache.Set(ctx, "id_map:external:casdoor:ext-1:tenant-1", 1, 5*time.Minute)
		cached.cache.Set(ctx, "id_map:username:casdoor:user1:tenant-1", "data3", 5*time.Minute)
		cached.cache.Set(ctx, "other_key", "should_not_be_deleted", 5*time.Minute)

		// Clear cache
		err := cached.ClearCache(ctx)
		require.NoError(t, err)

		// Verify id_map:* keys are gone
		exists, _ := cached.cache.Exists(ctx,
			"id_map:internal:1",
			"id_map:external:casdoor:ext-1:tenant-1",
			"id_map:username:casdoor:user1:tenant-1",
		).Result()
		assert.Equal(t, int64(0), exists)

		// Verify other keys remain
		exists, _ = cached.cache.Exists(ctx, "other_key").Result()
		assert.Greater(t, exists, int64(0))
	})
}

func TestCachedIDMapper_GetCacheStats(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cache test in short mode")
	}

	redisClient := setupMockRedis(t)
	defer redisClient.Close()

	ctx := context.Background()
	mapper := &IDMapper{} // Mock mapper
	cached := NewCachedIDMapper(mapper, redisClient, 5*time.Minute)

	t.Run("GetStats", func(t *testing.T) {
		// Add some cache entries
		cached.cache.Set(ctx, "id_map:internal:1", "data1", 5*time.Minute)
		cached.cache.Set(ctx, "id_map:external:casdoor:ext-1:tenant-1", 1, 5*time.Minute)

		stats, err := cached.GetCacheStats(ctx)
		require.NoError(t, err)
		assert.NotNil(t, stats)
		assert.GreaterOrEqual(t, stats["cached_mappings"].(int), 2)
		assert.Equal(t, 300, stats["ttl_seconds"].(int)) // 5 minutes
	})
}

func TestCachedIDMapper_CacheHitMiss(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cache test in short mode")
	}

	redisClient := setupMockRedis(t)
	defer redisClient.Close()

	ctx := context.Background()

	t.Run("CacheHit_GetByExternalID", func(t *testing.T) {
		mapper := &IDMapper{} // Mock mapper - won't be called on cache hit
		cached := NewCachedIDMapper(mapper, redisClient, 5*time.Minute)

		// Pre-populate cache
		cacheKey := "id_map:external:casdoor:ext-456:tenant-1"
		cached.cache.Set(ctx, cacheKey, 123, 5*time.Minute)

		// This should hit cache and not call the underlying mapper
		internalID, err := cached.GetByExternalID(ctx, "casdoor", "ext-456", "tenant-1")
		require.NoError(t, err)
		assert.Equal(t, 123, internalID)
	})

	t.Run("CacheHit_GetByInternalID", func(t *testing.T) {
		mapper := &IDMapper{} // Mock mapper
		cached := NewCachedIDMapper(mapper, redisClient, 5*time.Minute)

		// Pre-populate cache
		cacheKey := "id_map:internal:999"
		cached.cache.HMSet(ctx, cacheKey, map[string]interface{}{
			"provider":    "casdoor",
			"external_id": "ext-999",
			"tenant_id":   "tenant-1",
		})
		cached.cache.Expire(ctx, cacheKey, 5*time.Minute)

		// This should hit cache
		provider, externalID, tenantID, err := cached.GetByInternalID(ctx, 999)
		require.NoError(t, err)
		assert.Equal(t, "casdoor", provider)
		assert.Equal(t, "ext-999", externalID)
		assert.Equal(t, "tenant-1", tenantID)
	})

	t.Run("CacheHit_GetByUsername", func(t *testing.T) {
		mapper := &IDMapper{} // Mock mapper
		cached := NewCachedIDMapper(mapper, redisClient, 5*time.Minute)

		// Pre-populate cache
		cacheKey := "id_map:username:casdoor:john:tenant-1"
		cached.cache.HMSet(ctx, cacheKey, map[string]interface{}{
			"internal_id": "777",
			"external_id": "ext-777",
		})
		cached.cache.Expire(ctx, cacheKey, 5*time.Minute)

		// This should hit cache
		internalID, externalID, err := cached.GetByUsername(ctx, "casdoor", "john", "tenant-1")
		require.NoError(t, err)
		assert.Equal(t, 777, internalID)
		assert.Equal(t, "ext-777", externalID)
	})
}

func TestCachedIDMapper_CacheMiss(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// This test requires a real IDMapper with database connection
	// For now, we test that cache miss returns the expected error
	redisClient := setupMockRedis(t)
	defer redisClient.Close()

	ctx := context.Background()
	mapper := &IDMapper{} // Mock mapper - will return errors since no DB
	cached := NewCachedIDMapper(mapper, redisClient, 5*time.Minute)

	t.Run("CacheMiss_GetByExternalID", func(t *testing.T) {
		// Cache miss - should try to fetch from mapper (which will fail with mock)
		_, err := cached.GetByExternalID(ctx, "casdoor", "nonexistent", "tenant-1")
		assert.Error(t, err) // Expected since mapper is mock
	})
}

func TestCachedIDMapper_TTL(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cache test in short mode")
	}

	redisClient := setupMockRedis(t)
	defer redisClient.Close()

	ctx := context.Background()
	mapper := &IDMapper{} // Mock mapper
	ttl := 2 * time.Second
	cached := NewCachedIDMapper(mapper, redisClient, ttl)

	t.Run("CacheExpiration", func(t *testing.T) {
		// Set a cache entry
		cacheKey := "id_map:external:casdoor:exp-test:tenant-1"
		cached.cache.Set(ctx, cacheKey, 999, ttl)

		// Verify it exists
		exists, _ := cached.cache.Exists(ctx, cacheKey).Result()
		assert.Greater(t, exists, int64(0))

		// Wait for expiration
		time.Sleep(ttl + 500*time.Millisecond)

		// Verify it's gone
		exists, _ = cached.cache.Exists(ctx, cacheKey).Result()
		assert.Equal(t, int64(0), exists)
	})
}

// BenchmarkCachedIDMapper_GetByExternalID benchmarks cache performance.
func BenchmarkCachedIDMapper_GetByExternalID(b *testing.B) {
	redisClient := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   15,
	})
	defer redisClient.Close()

	ctx := context.Background()
	if err := redisClient.Ping(ctx).Err(); err != nil {
		b.Skip("Redis not available")
	}

	mapper := &IDMapper{} // Mock mapper
	cached := NewCachedIDMapper(mapper, redisClient, 5*time.Minute)

	// Pre-populate cache
	cacheKey := "id_map:external:casdoor:bench-test:tenant-1"
	cached.cache.Set(ctx, cacheKey, 123, 5*time.Minute)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cached.GetByExternalID(ctx, "casdoor", "bench-test", "tenant-1")
	}
}

// Note: Full integration tests with real IDMapper and database
// are in integration_test.go
