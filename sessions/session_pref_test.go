package sessions

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionPreference_GetSet(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	sp := NewSessionPreference(client)
	ctx := context.Background()

	// Get non-existent preference
	credID, found := sp.Get(ctx, "session-123")
	assert.False(t, found)
	assert.Equal(t, 0, credID)

	// Set preference
	err := sp.Set(ctx, "session-123", 42)
	require.NoError(t, err)

	// Get preference back
	credID, found = sp.Get(ctx, "session-123")
	require.True(t, found)
	assert.Equal(t, 42, credID)
}

func TestSessionPreference_Delete(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	sp := NewSessionPreference(client)
	ctx := context.Background()

	// Set preference
	err := sp.Set(ctx, "session-456", 99)
	require.NoError(t, err)

	// Verify it exists
	credID, found := sp.Get(ctx, "session-456")
	require.True(t, found)
	assert.Equal(t, 99, credID)

	// Delete preference
	err = sp.Delete(ctx, "session-456")
	require.NoError(t, err)

	// Verify it's gone
	credID, found = sp.Get(ctx, "session-456")
	assert.False(t, found)
	assert.Equal(t, 0, credID)
}

func TestSessionPreference_ClearOnModelSwitch(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	sp := NewSessionPreference(client)
	ctx := context.Background()

	// Set preference
	err := sp.Set(ctx, "session-789", 100)
	require.NoError(t, err)

	// Clear on model switch
	err = sp.ClearOnModelSwitch(ctx, "session-789", "gpt-4", "gpt-3.5")
	require.NoError(t, err)

	// Verify it's cleared
	credID, found := sp.Get(ctx, "session-789")
	assert.False(t, found)
	assert.Equal(t, 0, credID)
}

func TestSessionPreference_RedisTTL(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	sp := NewSessionPreference(client)
	ctx := context.Background()

	// Set preference
	err := sp.Set(ctx, "session-ttl-test", 200)
	require.NoError(t, err)

	// Check Redis TTL
	key := sp.redisKey("session-ttl-test")
	ttl := client.TTL(ctx, key).Val()

	// TTL should be close to 7 days
	expectedTTL := 7 * 24 * time.Hour
	assert.True(t, ttl > expectedTTL-time.Minute && ttl <= expectedTTL,
		"TTL should be close to 7 days, got %v", ttl)
}

func TestSessionPreference_RedisKey(t *testing.T) {
	sp := NewSessionPreference(nil)
	key := sp.redisKey("test-session-id")
	assert.Equal(t, "session_pref:test-session-id", key)
}

func TestSessionPreference_MultipleSessions(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	sp := NewSessionPreference(client)
	ctx := context.Background()

	// Set preferences for multiple sessions
	err := sp.Set(ctx, "session-A", 10)
	require.NoError(t, err)

	err = sp.Set(ctx, "session-B", 20)
	require.NoError(t, err)

	err = sp.Set(ctx, "session-C", 30)
	require.NoError(t, err)

	// Verify each session has its own preference
	credID, found := sp.Get(ctx, "session-A")
	require.True(t, found)
	assert.Equal(t, 10, credID)

	credID, found = sp.Get(ctx, "session-B")
	require.True(t, found)
	assert.Equal(t, 20, credID)

	credID, found = sp.Get(ctx, "session-C")
	require.True(t, found)
	assert.Equal(t, 30, credID)

	// Delete one session's preference
	err = sp.Delete(ctx, "session-B")
	require.NoError(t, err)

	// Verify only session-B is deleted
	credID, found = sp.Get(ctx, "session-A")
	require.True(t, found)
	assert.Equal(t, 10, credID)

	_, found = sp.Get(ctx, "session-B")
	assert.False(t, found)

	credID, found = sp.Get(ctx, "session-C")
	require.True(t, found)
	assert.Equal(t, 30, credID)
}
