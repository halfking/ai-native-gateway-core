package middleware

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedisNonceStore_Check(t *testing.T) {
	// Setup miniredis
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	store := newRedisNonceStore(client)
	ctx := context.Background()

	t.Run("first use of nonce succeeds", func(t *testing.T) {
		nonce := "test-nonce-1"
		ok, err := store.check(ctx, nonce)
		require.NoError(t, err)
		assert.True(t, ok, "first use of nonce should succeed")
	})

	t.Run("second use of same nonce fails", func(t *testing.T) {
		nonce := "test-nonce-2"
		ok1, err := store.check(ctx, nonce)
		require.NoError(t, err)
		assert.True(t, ok1, "first use should succeed")

		// Second use should fail (replay attack)
		ok2, err := store.check(ctx, nonce)
		require.NoError(t, err)
		assert.False(t, ok2, "second use of same nonce should fail")
	})

	t.Run("nonce expires after TTL", func(t *testing.T) {
		nonce := "test-nonce-expiry"
		ok, err := store.check(ctx, nonce)
		require.NoError(t, err)
		assert.True(t, ok)

		// Fast-forward time in miniredis
		mr.FastForward(6 * time.Minute)

		// Should be able to use again after expiry
		ok2, err := store.check(ctx, nonce)
		require.NoError(t, err)
		assert.True(t, ok2, "nonce should be reusable after TTL expiry")
	})

	t.Run("multiple different nonces succeed", func(t *testing.T) {
		nonces := []string{"nonce-a", "nonce-b", "nonce-c"}
		for _, nonce := range nonces {
			ok, err := store.check(ctx, nonce)
			require.NoError(t, err)
			assert.True(t, ok, "each unique nonce should succeed")
		}
	})
}

func TestRedisNonceStore_KeyFormat(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	store := newRedisNonceStore(client)
	ctx := context.Background()

	nonce := "test-nonce-key"
	_, err = store.check(ctx, nonce)
	require.NoError(t, err)

	// Verify key format in Redis
	expectedKey := "nonce:test-nonce-key"
	exists := mr.Exists(expectedKey)
	assert.True(t, exists, "nonce should be stored with 'nonce:' prefix")

	// Verify TTL is set
	ttl := mr.TTL(expectedKey)
	assert.True(t, ttl > 0, "nonce should have TTL set")
	assert.LessOrEqual(t, ttl, 5*time.Minute, "TTL should be <= 5 minutes")
}

func TestRedisNonceStore_RedisUnavailable(t *testing.T) {
	// Create client pointing to non-existent Redis
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:9999",
	})
	defer client.Close()

	store := newRedisNonceStore(client)
	ctx := context.Background()

	// Should return error when Redis is unavailable
	_, err := store.check(ctx, "some-nonce")
	assert.Error(t, err, "should fail when Redis is unavailable")
}
