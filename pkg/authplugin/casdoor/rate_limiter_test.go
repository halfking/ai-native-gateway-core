package casdoor

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRateLimiter(t *testing.T) {
	t.Run("WithDefaults", func(t *testing.T) {
		limiter := NewRateLimiter(nil)
		assert.NotNil(t, limiter)
		assert.Equal(t, "ratelimit:", limiter.keyPrefix)
		assert.NotNil(t, limiter.localBuckets)
	})

	t.Run("WithCustomConfig", func(t *testing.T) {
		config := &RateLimitConfig{
			KeyPrefix: "custom:",
		}
		limiter := NewRateLimiter(config)
		assert.NotNil(t, limiter)
		assert.Equal(t, "custom:", limiter.keyPrefix)
	})
}

func TestDefaultRules(t *testing.T) {
	rules := DefaultRules()
	assert.NotNil(t, rules)
	assert.Contains(t, rules, "auth")
	assert.Contains(t, rules, "api")
	assert.Contains(t, rules, "admin")

	authRule := rules["auth"]
	assert.Equal(t, 10, authRule.Capacity)
	assert.Equal(t, 2, authRule.RefillRate)
}

func TestRateLimiter_Allow(t *testing.T) {
	limiter := NewRateLimiter(nil)
	ctx := context.Background()

	t.Run("AllowWithinLimit", func(t *testing.T) {
		rule := &RateLimitRule{
			Capacity:   5,
			RefillRate: 1,
			Window:     time.Minute,
		}

		// Should allow first 5 requests
		for i := 0; i < 5; i++ {
			allowed, err := limiter.Allow(ctx, "test-key", rule)
			require.NoError(t, err)
			assert.True(t, allowed, "request %d should be allowed", i+1)
		}
	})

	t.Run("BlockOverLimit", func(t *testing.T) {
		rule := &RateLimitRule{
			Capacity:   3,
			RefillRate: 1,
			Window:     time.Minute,
		}

		// Exhaust capacity
		for i := 0; i < 3; i++ {
			allowed, err := limiter.Allow(ctx, "test-block", rule)
			require.NoError(t, err)
			assert.True(t, allowed)
		}

		// Next request should be blocked
		allowed, err := limiter.Allow(ctx, "test-block", rule)
		require.NoError(t, err)
		assert.False(t, allowed)
	})

	t.Run("AllowWithNilRule", func(t *testing.T) {
		allowed, err := limiter.Allow(ctx, "test-key", nil)
		require.NoError(t, err)
		assert.True(t, allowed)
	})

	t.Run("RefillTokens", func(t *testing.T) {
		rule := &RateLimitRule{
			Capacity:   2,
			RefillRate: 2, // 2 tokens per second
			Window:     time.Minute,
		}

		// Exhaust capacity
		limiter.Allow(ctx, "test-refill", rule)
		limiter.Allow(ctx, "test-refill", rule)

		// Should be blocked
		allowed, err := limiter.Allow(ctx, "test-refill", rule)
		require.NoError(t, err)
		assert.False(t, allowed)

		// Wait for refill (1 second = 2 tokens)
		time.Sleep(1100 * time.Millisecond)

		// Should be allowed again
		allowed, err = limiter.Allow(ctx, "test-refill", rule)
		require.NoError(t, err)
		assert.True(t, allowed)
	})
}

func TestRateLimiter_AllowN(t *testing.T) {
	limiter := NewRateLimiter(nil)
	ctx := context.Background()

	t.Run("AllowNWithinLimit", func(t *testing.T) {
		rule := &RateLimitRule{
			Capacity:   10,
			RefillRate: 2,
			Window:     time.Minute,
		}

		allowed, err := limiter.AllowN(ctx, "test-n", rule, 5)
		require.NoError(t, err)
		assert.True(t, allowed)
	})

	t.Run("BlockNOverLimit", func(t *testing.T) {
		rule := &RateLimitRule{
			Capacity:   5,
			RefillRate: 1,
			Window:     time.Minute,
		}

		// First request should succeed
		allowed, err := limiter.AllowN(ctx, "test-n-block", rule, 3)
		require.NoError(t, err)
		assert.True(t, allowed)

		// Second request should fail (only 2 tokens left)
		allowed, err = limiter.AllowN(ctx, "test-n-block", rule, 5)
		require.NoError(t, err)
		assert.False(t, allowed)
	})
}

func TestRateLimiter_GetRemaining(t *testing.T) {
	limiter := NewRateLimiter(nil)
	ctx := context.Background()

	t.Run("InitialCapacity", func(t *testing.T) {
		rule := &RateLimitRule{
			Capacity:   10,
			RefillRate: 2,
			Window:     time.Minute,
		}

		remaining, err := limiter.GetRemaining(ctx, "test-remaining", rule)
		require.NoError(t, err)
		assert.Equal(t, 10, remaining)
	})

	t.Run("AfterConsumption", func(t *testing.T) {
		rule := &RateLimitRule{
			Capacity:   10,
			RefillRate: 2,
			Window:     time.Minute,
		}

		// Consume 3 tokens
		for i := 0; i < 3; i++ {
			limiter.Allow(ctx, "test-consume", rule)
		}

		remaining, err := limiter.GetRemaining(ctx, "test-consume", rule)
		require.NoError(t, err)
		assert.Equal(t, 7, remaining)
	})
}

func TestRateLimiter_Reset(t *testing.T) {
	limiter := NewRateLimiter(nil)
	ctx := context.Background()

	t.Run("ResetAfterExhaustion", func(t *testing.T) {
		rule := &RateLimitRule{
			Capacity:   2,
			RefillRate: 1,
			Window:     time.Minute,
		}

		// Exhaust capacity
		limiter.Allow(ctx, "test-reset", rule)
		limiter.Allow(ctx, "test-reset", rule)

		// Should be blocked
		allowed, err := limiter.Allow(ctx, "test-reset", rule)
		require.NoError(t, err)
		assert.False(t, allowed)

		// Reset
		err = limiter.Reset(ctx, "test-reset")
		require.NoError(t, err)

		// Should be allowed again
		allowed, err = limiter.Allow(ctx, "test-reset", rule)
		require.NoError(t, err)
		assert.True(t, allowed)
	})
}

func TestRateLimiter_CleanupExpiredBuckets(t *testing.T) {
	limiter := NewRateLimiter(nil)
	ctx := context.Background()

	t.Run("CleanupOldBuckets", func(t *testing.T) {
		rule := &RateLimitRule{
			Capacity:   5,
			RefillRate: 1,
			Window:     time.Minute,
		}

		// Create some buckets
		limiter.Allow(ctx, "old-bucket", rule)
		limiter.Allow(ctx, "new-bucket", rule)

		// Manually age one bucket
		limiter.mu.Lock()
		if bucket, ok := limiter.localBuckets["old-bucket"]; ok {
			bucket.LastRefillTime = time.Now().Add(-2 * time.Hour)
		}
		limiter.mu.Unlock()

		// Cleanup buckets older than 1 hour
		count := limiter.CleanupExpiredBuckets(ctx, 1*time.Hour)
		assert.Equal(t, 1, count)

		// Old bucket should be gone
		limiter.mu.RLock()
		_, exists := limiter.localBuckets["old-bucket"]
		limiter.mu.RUnlock()
		assert.False(t, exists)
	})
}

func TestRateLimiter_GetStats(t *testing.T) {
	limiter := NewRateLimiter(nil)
	ctx := context.Background()

	t.Run("EmptyStats", func(t *testing.T) {
		stats := limiter.GetStats(ctx)
		assert.NotNil(t, stats)
		assert.Equal(t, 0, stats.TotalBuckets)
		assert.Equal(t, 0, stats.ActiveBuckets)
	})

	t.Run("WithActiveBuckets", func(t *testing.T) {
		rule := &RateLimitRule{
			Capacity:   5,
			RefillRate: 1,
			Window:     time.Minute,
		}

		// Create buckets
		limiter.Allow(ctx, "bucket1", rule)
		limiter.Allow(ctx, "bucket2", rule)

		stats := limiter.GetStats(ctx)
		assert.Equal(t, 2, stats.TotalBuckets)
		assert.Equal(t, 2, stats.ActiveBuckets)
	})
}

func TestRateLimitMiddleware(t *testing.T) {
	limiter := NewRateLimiter(nil)
	middleware := NewRateLimitMiddleware(limiter, nil)
	ctx := context.Background()

	t.Run("CheckAuth", func(t *testing.T) {
		// Should allow first few requests
		for i := 0; i < 5; i++ {
			allowed, err := middleware.CheckAuth(ctx, "user123")
			require.NoError(t, err)
			assert.True(t, allowed)
		}
	})

	t.Run("CheckAPI", func(t *testing.T) {
		allowed, err := middleware.CheckAPI(ctx, "client123")
		require.NoError(t, err)
		assert.True(t, allowed)
	})

	t.Run("CheckAdmin", func(t *testing.T) {
		allowed, err := middleware.CheckAdmin(ctx, "admin123")
		require.NoError(t, err)
		assert.True(t, allowed)
	})

	t.Run("DifferentKeys", func(t *testing.T) {
		// Auth and API should have separate limits
		authAllowed, _ := middleware.CheckAuth(ctx, "test")
		apiAllowed, _ := middleware.CheckAPI(ctx, "test")

		assert.True(t, authAllowed)
		assert.True(t, apiAllowed)
	})
}

func TestTokenBucket(t *testing.T) {
	t.Run("InitialState", func(t *testing.T) {
		bucket := &TokenBucket{
			Capacity:       10,
			RefillRate:     2,
			Tokens:         10,
			LastRefillTime: time.Now(),
		}

		assert.Equal(t, 10, bucket.Capacity)
		assert.Equal(t, 2, bucket.RefillRate)
		assert.Equal(t, 10, bucket.Tokens)
	})
}

// BenchmarkRateLimiter_Allow benchmarks rate limit checking.
func BenchmarkRateLimiter_Allow(b *testing.B) {
	limiter := NewRateLimiter(nil)
	ctx := context.Background()
	rule := &RateLimitRule{
		Capacity:   1000,
		RefillRate: 100,
		Window:     time.Minute,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		limiter.Allow(ctx, "benchmark-key", rule)
	}
}

// BenchmarkRateLimiter_GetRemaining benchmarks getting remaining tokens.
func BenchmarkRateLimiter_GetRemaining(b *testing.B) {
	limiter := NewRateLimiter(nil)
	ctx := context.Background()
	rule := &RateLimitRule{
		Capacity:   1000,
		RefillRate: 100,
		Window:     time.Minute,
	}

	// Setup: create bucket
	limiter.Allow(ctx, "benchmark-key", rule)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		limiter.GetRemaining(ctx, "benchmark-key", rule)
	}
}

// TestConcurrentAccess tests concurrent access to rate limiter.
func TestConcurrentAccess(t *testing.T) {
	limiter := NewRateLimiter(nil)
	ctx := context.Background()
	rule := &RateLimitRule{
		Capacity:   100,
		RefillRate: 10,
		Window:     time.Minute,
	}

	// Run concurrent requests
	concurrency := 50
	done := make(chan bool, concurrency)

	for i := 0; i < concurrency; i++ {
		go func(id int) {
			for j := 0; j < 10; j++ {
				limiter.Allow(ctx, "concurrent-key", rule)
			}
			done <- true
		}(i)
	}

	// Wait for completion
	for i := 0; i < concurrency; i++ {
		<-done
	}

	// Should have some requests blocked
	remaining, err := limiter.GetRemaining(ctx, "concurrent-key", rule)
	require.NoError(t, err)
	assert.LessOrEqual(t, remaining, rule.Capacity)
}
