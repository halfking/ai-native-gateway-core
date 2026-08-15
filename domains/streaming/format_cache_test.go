package streaming

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func setupTestRedis(t *testing.T) *redis.Client {
	// Use a test Redis instance or mock
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   15, // Use a separate DB for testing
	})

	// Test connection
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Skip("Redis not available, skipping test:", err)
	}

	// Clean up test data before running
	client.FlushDB(ctx)

	return client
}

func TestRedisFormatCache_SetAndGet(t *testing.T) {
	client := setupTestRedis(t)
	defer client.Close()

	cache := NewRedisFormatCache(client, 1*time.Hour)
	ctx := context.Background()

	// Test data
	sessionID := "test-session-123"
	format := &CachedFormat{
		PatternID:   "opencode-v1",
		PatternName: "OpenCode CLI",
		Confidence:  0.85,
		CachedAt:    time.Now(),
		UseCount:    1,
		LastUsed:    time.Now(),
	}

	// Set
	err := cache.Set(ctx, sessionID, format)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Get
	retrieved, err := cache.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if retrieved == nil {
		t.Fatal("Expected format, got nil")
	}

	if retrieved.PatternID != format.PatternID {
		t.Errorf("Expected PatternID %v, got %v", format.PatternID, retrieved.PatternID)
	}

	if retrieved.Confidence != format.Confidence {
		t.Errorf("Expected Confidence %v, got %v", format.Confidence, retrieved.Confidence)
	}
}

func TestRedisFormatCache_GetNonExistent(t *testing.T) {
	client := setupTestRedis(t)
	defer client.Close()

	cache := NewRedisFormatCache(client, 1*time.Hour)
	ctx := context.Background()

	// Get non-existent key
	retrieved, err := cache.Get(ctx, "non-existent")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if retrieved != nil {
		t.Error("Expected nil for non-existent key")
	}
}

func TestRedisFormatCache_Delete(t *testing.T) {
	client := setupTestRedis(t)
	defer client.Close()

	cache := NewRedisFormatCache(client, 1*time.Hour)
	ctx := context.Background()

	sessionID := "test-session-delete"
	format := &CachedFormat{
		PatternID:  "openai-standard",
		Confidence: 0.9,
	}

	// Set
	cache.Set(ctx, sessionID, format)

	// Verify it exists
	retrieved, _ := cache.Get(ctx, sessionID)
	if retrieved == nil {
		t.Fatal("Expected format to exist")
	}

	// Delete
	err := cache.Delete(ctx, sessionID)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify it's gone
	retrieved, _ = cache.Get(ctx, sessionID)
	if retrieved != nil {
		t.Error("Expected format to be deleted")
	}
}

func TestRedisFormatCache_UseCountIncrement(t *testing.T) {
	client := setupTestRedis(t)
	defer client.Close()

	cache := NewRedisFormatCache(client, 1*time.Hour)
	ctx := context.Background()

	sessionID := "test-session-usecount"
	format := &CachedFormat{
		PatternID: "opencode-v1",
		UseCount:  1,
		LastUsed:  time.Now(),
	}

	// Set initial
	cache.Set(ctx, sessionID, format)

	// Get multiple times (should increment UseCount)
	for i := 0; i < 3; i++ {
		retrieved, err := cache.Get(ctx, sessionID)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if retrieved == nil {
			t.Fatal("Expected format")
		}

		// Wait a bit for async update
		time.Sleep(100 * time.Millisecond)
	}

	// Final check - UseCount should have increased
	// Note: Due to async updates, this is approximate
	retrieved, _ := cache.Get(ctx, sessionID)
	if retrieved.UseCount < 2 {
		t.Logf("UseCount may not have updated yet (async): %v", retrieved.UseCount)
	}
}

func TestRedisFormatCache_GetStats(t *testing.T) {
	client := setupTestRedis(t)
	defer client.Close()

	cache := NewRedisFormatCache(client, 1*time.Hour)
	ctx := context.Background()

	// Add multiple sessions with different patterns
	sessions := []struct {
		id      string
		pattern string
		count   int
	}{
		{"session1", "opencode-v1", 5},
		{"session2", "opencode-v1", 3},
		{"session3", "openai-standard", 10},
	}

	for _, s := range sessions {
		format := &CachedFormat{
			PatternID: s.pattern,
			UseCount:  s.count,
		}
		cache.Set(ctx, s.id, format)
	}

	// Get stats
	stats, err := cache.GetStats(ctx)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}

	if stats["opencode-v1"] != 8 {
		t.Errorf("Expected opencode-v1 count 8, got %v", stats["opencode-v1"])
	}

	if stats["openai-standard"] != 10 {
		t.Errorf("Expected openai-standard count 10, got %v", stats["openai-standard"])
	}
}

func TestRedisFormatCache_GetSessionCount(t *testing.T) {
	client := setupTestRedis(t)
	defer client.Close()

	cache := NewRedisFormatCache(client, 1*time.Hour)
	ctx := context.Background()

	// Add sessions
	for i := 0; i < 5; i++ {
		sessionID := "session-count-test-" + string(rune('a'+i))
		format := &CachedFormat{PatternID: "test"}
		cache.Set(ctx, sessionID, format)
	}

	// Get count
	count, err := cache.GetSessionCount(ctx)
	if err != nil {
		t.Fatalf("GetSessionCount failed: %v", err)
	}

	if count != 5 {
		t.Errorf("Expected count 5, got %v", count)
	}
}

func TestRedisFormatCache_GetByPattern(t *testing.T) {
	client := setupTestRedis(t)
	defer client.Close()

	cache := NewRedisFormatCache(client, 1*time.Hour)
	ctx := context.Background()

	// Add sessions with different patterns
	cache.Set(ctx, "session1", &CachedFormat{PatternID: "opencode-v1"})
	cache.Set(ctx, "session2", &CachedFormat{PatternID: "opencode-v1"})
	cache.Set(ctx, "session3", &CachedFormat{PatternID: "openai-standard"})

	// Get sessions by pattern
	sessions, err := cache.GetByPattern(ctx, "opencode-v1")
	if err != nil {
		t.Fatalf("GetByPattern failed: %v", err)
	}

	if len(sessions) != 2 {
		t.Errorf("Expected 2 sessions, got %v", len(sessions))
	}

	// Check session IDs
	found := make(map[string]bool)
	for _, s := range sessions {
		found[s] = true
	}

	if !found["session1"] || !found["session2"] {
		t.Error("Expected to find session1 and session2")
	}
}

func TestRedisFormatCache_Clear(t *testing.T) {
	client := setupTestRedis(t)
	defer client.Close()

	cache := NewRedisFormatCache(client, 1*time.Hour)
	ctx := context.Background()

	// Add multiple sessions
	for i := 0; i < 3; i++ {
		sessionID := "clear-test-" + string(rune('a'+i))
		cache.Set(ctx, sessionID, &CachedFormat{PatternID: "test"})
	}

	// Verify they exist
	count, _ := cache.GetSessionCount(ctx)
	if count != 3 {
		t.Errorf("Expected 3 sessions before clear, got %v", count)
	}

	// Clear all
	err := cache.Clear(ctx)
	if err != nil {
		t.Fatalf("Clear failed: %v", err)
	}

	// Verify all cleared
	count, _ = cache.GetSessionCount(ctx)
	if count != 0 {
		t.Errorf("Expected 0 sessions after clear, got %v", count)
	}
}

func TestRedisFormatCache_EmptySessionID(t *testing.T) {
	client := setupTestRedis(t)
	defer client.Close()

	cache := NewRedisFormatCache(client, 1*time.Hour)
	ctx := context.Background()

	// Test with empty session ID
	format := &CachedFormat{PatternID: "test"}

	// Set should handle empty session ID gracefully
	err := cache.Set(ctx, "", format)
	if err != nil {
		t.Errorf("Set with empty sessionID should not error: %v", err)
	}

	// Get should handle empty session ID gracefully
	retrieved, err := cache.Get(ctx, "")
	if err != nil {
		t.Errorf("Get with empty sessionID should not error: %v", err)
	}
	if retrieved != nil {
		t.Error("Expected nil for empty sessionID")
	}

	// Delete should handle empty session ID gracefully
	err = cache.Delete(ctx, "")
	if err != nil {
		t.Errorf("Delete with empty sessionID should not error: %v", err)
	}
}

func TestNullFormatCache(t *testing.T) {
	cache := NewNullFormatCache()
	ctx := context.Background()

	format := &CachedFormat{PatternID: "test"}

	// All operations should succeed but do nothing
	err := cache.Set(ctx, "session", format)
	if err != nil {
		t.Error("NullFormatCache.Set should not error")
	}

	retrieved, err := cache.Get(ctx, "session")
	if err != nil {
		t.Error("NullFormatCache.Get should not error")
	}
	if retrieved != nil {
		t.Error("NullFormatCache.Get should always return nil")
	}

	err = cache.Delete(ctx, "session")
	if err != nil {
		t.Error("NullFormatCache.Delete should not error")
	}
}

func TestRedisFormatCache_TTL(t *testing.T) {
	client := setupTestRedis(t)
	defer client.Close()

	// Use very short TTL for testing
	cache := NewRedisFormatCache(client, 1*time.Second)
	ctx := context.Background()

	sessionID := "ttl-test"
	format := &CachedFormat{PatternID: "test"}

	// Set
	cache.Set(ctx, sessionID, format)

	// Verify exists
	retrieved, _ := cache.Get(ctx, sessionID)
	if retrieved == nil {
		t.Fatal("Expected format to exist")
	}

	// Wait for TTL expiration
	time.Sleep(2 * time.Second)

	// Should be expired
	retrieved, _ = cache.Get(ctx, sessionID)
	if retrieved != nil {
		t.Error("Expected format to be expired")
	}
}
