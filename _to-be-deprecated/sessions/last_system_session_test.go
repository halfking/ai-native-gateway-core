
package sessions

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestRedis(t *testing.T) (*redis.Client, func()) {
	mr, err := miniredis.Run()
	require.NoError(t, err)

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	cleanup := func() {
		client.Close()
		mr.Close()
	}

	return client, cleanup
}

func TestLastSystemSessionIndex_GetSet(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	idx := NewLastSystemSessionIndex(client)
	ctx := context.Background()

	// Get non-existent entry
	entry, found := idx.Get(ctx, 123)
	assert.False(t, found)
	assert.Nil(t, entry)

	// Set entry
	newEntry := &LastSystemSessionEntry{
		SessionID:  "gw_test_session",
		DeviceSeed: "device_123",
		TaskID:     "task_456",
	}
	err := idx.Set(ctx, 123, newEntry)
	require.NoError(t, err)

	// Get entry back
	retrieved, found := idx.Get(ctx, 123)
	require.True(t, found)
	require.NotNil(t, retrieved)
	assert.Equal(t, "gw_test_session", retrieved.SessionID)
	assert.Equal(t, "device_123", retrieved.DeviceSeed)
	assert.Equal(t, "task_456", retrieved.TaskID)
	assert.WithinDuration(t, time.Now(), retrieved.LastAssignedAt, 1*time.Second)
}

func TestLastSystemSessionIndex_Expiration(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	idx := NewLastSystemSessionIndex(client)
	ctx := context.Background()

	// Set entry with old timestamp
	oldEntry := &LastSystemSessionEntry{
		SessionID:      "gw_old_session",
		LastAssignedAt: time.Now().Add(-10 * time.Minute), // 10 minutes ago
	}

	// Manually set with old timestamp (bypass Set which sets current time)
	key := idx.redisKey(123)
	data, _ := json.Marshal(oldEntry)
	client.Set(ctx, key, data, LastSystemSessionTTL)

	// Get should return false because entry is expired (>5 minutes old)
	entry, found := idx.Get(ctx, 123)
	assert.False(t, found)
	assert.Nil(t, entry)
}

func TestLastSystemSessionIndex_Delete(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	idx := NewLastSystemSessionIndex(client)
	ctx := context.Background()

	// Set entry
	entry := &LastSystemSessionEntry{
		SessionID: "gw_delete_test",
	}
	err := idx.Set(ctx, 123, entry)
	require.NoError(t, err)

	// Verify it exists
	retrieved, found := idx.Get(ctx, 123)
	require.True(t, found)
	assert.Equal(t, "gw_delete_test", retrieved.SessionID)

	// Delete entry
	err = idx.Delete(ctx, 123)
	require.NoError(t, err)

	// Verify it's gone
	retrieved, found = idx.Get(ctx, 123)
	assert.False(t, found)
	assert.Nil(t, retrieved)
}

func TestLastSystemSessionIndex_Touch(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	idx := NewLastSystemSessionIndex(client)
	ctx := context.Background()

	// Set initial entry
	entry := &LastSystemSessionEntry{
		SessionID: "gw_touch_test",
	}
	err := idx.Set(ctx, 123, entry)
	require.NoError(t, err)

	// Get initial timestamp
	retrieved1, found := idx.Get(ctx, 123)
	require.True(t, found)
	timestamp1 := retrieved1.LastAssignedAt

	// Wait a bit
	time.Sleep(100 * time.Millisecond)

	// Touch to update timestamp
	err = idx.Touch(ctx, 123)
	require.NoError(t, err)

	// Get updated timestamp
	retrieved2, found := idx.Get(ctx, 123)
	require.True(t, found)
	timestamp2 := retrieved2.LastAssignedAt

	// Timestamp should be updated
	assert.True(t, timestamp2.After(timestamp1))
	assert.Equal(t, "gw_touch_test", retrieved2.SessionID) // SessionID unchanged
}

func TestLastSystemSessionIndex_TouchNonExistent(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	idx := NewLastSystemSessionIndex(client)
	ctx := context.Background()

	// Touch non-existent entry should return error
	err := idx.Touch(ctx, 999)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no entry to touch")
}

func TestLastSystemSessionIndex_RedisTTL(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	idx := NewLastSystemSessionIndex(client)
	ctx := context.Background()

	// Set entry
	entry := &LastSystemSessionEntry{
		SessionID: "gw_ttl_test",
	}
	err := idx.Set(ctx, 123, entry)
	require.NoError(t, err)

	// Check Redis TTL
	key := idx.redisKey(123)
	ttl := client.TTL(ctx, key).Val()
	assert.True(t, ttl > 4*time.Minute && ttl <= 5*time.Minute,
		"TTL should be close to 5 minutes, got %v", ttl)
}

func TestLastSystemSessionIndex_Within5MinuteWindow(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	idx := NewLastSystemSessionIndex(client)
	ctx := context.Background()

	// Set entry
	entry := &LastSystemSessionEntry{
		SessionID: "gw_window_test",
	}
	err := idx.Set(ctx, 123, entry)
	require.NoError(t, err)

	// Immediately get should work
	retrieved, found := idx.Get(ctx, 123)
	require.True(t, found)
	assert.Equal(t, "gw_window_test", retrieved.SessionID)

	// Simulate 4 minutes passing (still within window)
	oldEntry := &LastSystemSessionEntry{
		SessionID:      "gw_window_test",
		LastAssignedAt: time.Now().Add(-4 * time.Minute),
	}
	key := idx.redisKey(123)
	data, _ := json.Marshal(oldEntry)
	client.Set(ctx, key, data, LastSystemSessionTTL)

	// Should still be found (4 min < 5 min)
	retrieved, found = idx.Get(ctx, 123)
	require.True(t, found)
	assert.Equal(t, "gw_window_test", retrieved.SessionID)

	// Simulate 6 minutes passing (outside window)
	veryOldEntry := &LastSystemSessionEntry{
		SessionID:      "gw_window_test",
		LastAssignedAt: time.Now().Add(-6 * time.Minute),
	}
	data, _ = json.Marshal(veryOldEntry)
	client.Set(ctx, key, data, LastSystemSessionTTL)

	// Should not be found (6 min > 5 min)
	retrieved, found = idx.Get(ctx, 123)
	assert.False(t, found)
	assert.Nil(t, retrieved)
}
