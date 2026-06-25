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

	val, found := sp.Get(ctx, "session-123")
	assert.False(t, found)
	assert.Nil(t, val)

	err := sp.Set(ctx, "session-123", 42, "gpt-4")
	require.NoError(t, err)

	val, found = sp.Get(ctx, "session-123")
	require.True(t, found)
	assert.Equal(t, 42, val.CredentialID)
	assert.Equal(t, "gpt-4", val.Model)
}

func TestSessionPreference_GetModel(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	sp := NewSessionPreference(client)
	ctx := context.Background()

	model := sp.GetModel(ctx, "session-nonexistent")
	assert.Equal(t, "", model)

	sp.Set(ctx, "session-model", 42, "claude-3-opus")
	model = sp.GetModel(ctx, "session-model")
	assert.Equal(t, "claude-3-opus", model)
}

func TestSessionPreference_GetModel_Empty(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	sp := NewSessionPreference(client)
	ctx := context.Background()

	sp.Set(ctx, "session-no-model", 42, "")
	model := sp.GetModel(ctx, "session-no-model")
	assert.Equal(t, "", model)
}

func TestSessionPreference_LegacyCompat(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	sp := NewSessionPreference(client)
	ctx := context.Background()

	// Simulate old plain-string value (written by older code)
	key := sp.redisKey("session-legacy")
	err := client.Set(ctx, key, "99", SessionPreferenceTTL).Err()
	require.NoError(t, err)

	// Should still be readable
	val, found := sp.Get(ctx, "session-legacy")
	require.True(t, found)
	assert.Equal(t, 99, val.CredentialID)
	assert.Equal(t, "", val.Model) // no model in legacy format
}

func TestSessionPreference_Delete(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	sp := NewSessionPreference(client)
	ctx := context.Background()

	err := sp.Set(ctx, "session-456", 99, "claude-3")
	require.NoError(t, err)

	val, found := sp.Get(ctx, "session-456")
	require.True(t, found)
	assert.Equal(t, 99, val.CredentialID)

	err = sp.Delete(ctx, "session-456")
	require.NoError(t, err)

	val, found = sp.Get(ctx, "session-456")
	assert.False(t, found)
	assert.Nil(t, val)
}

func TestSessionPreference_ClearOnModelSwitch(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	sp := NewSessionPreference(client)
	ctx := context.Background()

	err := sp.Set(ctx, "session-789", 100, "gpt-4")
	require.NoError(t, err)

	err = sp.ClearOnModelSwitch(ctx, "session-789", "gpt-4", "claude-3")
	require.NoError(t, err)

	val, found := sp.Get(ctx, "session-789")
	assert.False(t, found)
	assert.Nil(t, val)
}

func TestSessionPreference_RedisTTL(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	sp := NewSessionPreference(client)
	ctx := context.Background()

	err := sp.Set(ctx, "session-ttl-test", 200, "gpt-4")
	require.NoError(t, err)

	key := sp.redisKey("session-ttl-test")
	ttl := client.TTL(ctx, key).Val()

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

	err := sp.Set(ctx, "session-A", 10, "gpt-4")
	require.NoError(t, err)
	err = sp.Set(ctx, "session-B", 20, "claude-3")
	require.NoError(t, err)
	err = sp.Set(ctx, "session-C", 30, "gemini-pro")
	require.NoError(t, err)

	val, found := sp.Get(ctx, "session-A")
	require.True(t, found)
	assert.Equal(t, 10, val.CredentialID)
	assert.Equal(t, "gpt-4", val.Model)

	val, found = sp.Get(ctx, "session-B")
	require.True(t, found)
	assert.Equal(t, 20, val.CredentialID)
	assert.Equal(t, "claude-3", val.Model)

	val, found = sp.Get(ctx, "session-C")
	require.True(t, found)
	assert.Equal(t, 30, val.CredentialID)
	assert.Equal(t, "gemini-pro", val.Model)

	err = sp.Delete(ctx, "session-B")
	require.NoError(t, err)

	val, found = sp.Get(ctx, "session-A")
	require.True(t, found)
	assert.Equal(t, 10, val.CredentialID)

	val, found = sp.Get(ctx, "session-B")
	assert.False(t, found)
	assert.Nil(t, val)

	val, found = sp.Get(ctx, "session-C")
	require.True(t, found)
	assert.Equal(t, 30, val.CredentialID)
}
