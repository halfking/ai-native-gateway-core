package v2

import (
	"context"
	"testing"
	"time"
)

// TestRedisGovernanceCache_DisabledCache 测试禁用缓存（fail-open）
func TestRedisGovernanceCache_DisabledCache(t *testing.T) {
	// Empty address = disabled cache
	cache := NewRedisGovernanceCache("", 0)

	if cache.enabled {
		t.Error("expected disabled cache")
	}

	ctx := context.Background()

	// Get should return nil (not error)
	meta, err := cache.Get(ctx, "tenant1", "session1")
	if err != nil {
		t.Errorf("expected no error for disabled cache, got: %v", err)
	}
	if meta != nil {
		t.Error("expected nil meta for disabled cache")
	}

	// Set should be no-op
	err = cache.Set(ctx, "tenant1", "session1", &GovernanceMeta{
		LastInjectionVerdict: "pass",
	})
	if err != nil {
		t.Errorf("expected no error for disabled cache, got: %v", err)
	}

	// Delete should be no-op
	err = cache.Delete(ctx, "tenant1", "session1")
	if err != nil {
		t.Errorf("expected no error for disabled cache, got: %v", err)
	}
}

// TestRedisGovernanceCache_RedisKeyFormat 测试 Redis key 格式
func TestRedisGovernanceCache_RedisKeyFormat(t *testing.T) {
	tests := []struct {
		name      string
		tenantID  string
		sessionID string
		expected  string
	}{
		{
			name:      "default tenant",
			tenantID:  "default",
			sessionID: "gw_abc123",
			expected:  "session:v2:default:gw_abc123",
		},
		{
			name:      "custom tenant",
			tenantID:  "tenant-prod",
			sessionID: "gw_xyz789",
			expected:  "session:v2:tenant-prod:gw_xyz789",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := redisKeyV2(tt.tenantID, tt.sessionID)
			if key != tt.expected {
				t.Errorf("expected key %s, got %s", tt.expected, key)
			}
		})
	}
}

// TestRedisGovernanceCache_SetGet 测试存取流程（需要 Redis）
func TestRedisGovernanceCache_SetGet(t *testing.T) {
	// Skip if Redis not available
	t.Skip("Skipping test that requires Redis connection")

	cache := NewRedisGovernanceCache("localhost:6379", 5*time.Minute)
	if !cache.enabled {
		t.Skip("Redis not available, skipping test")
	}
	defer cache.Close()

	ctx := context.Background()
	tenantID := "test-tenant"
	sessionID := "test-session-" + time.Now().Format("20060102150405")

	// Clean up before test
	defer cache.Delete(ctx, tenantID, sessionID)

	// Test Set
	meta := &GovernanceMeta{
		LastInjectionVerdict: "pass",
		LastOutputVerdict:    "warn",
		AuditedAt:            time.Now().Truncate(time.Second),
		SensitiveDetected:    true,
	}

	err := cache.Set(ctx, tenantID, sessionID, meta)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Test Get
	retrieved, err := cache.Get(ctx, tenantID, sessionID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if retrieved == nil {
		t.Fatal("expected meta, got nil")
	}

	if retrieved.LastInjectionVerdict != meta.LastInjectionVerdict {
		t.Errorf("injection_verdict: expected %s, got %s",
			meta.LastInjectionVerdict, retrieved.LastInjectionVerdict)
	}

	if retrieved.LastOutputVerdict != meta.LastOutputVerdict {
		t.Errorf("output_verdict: expected %s, got %s",
			meta.LastOutputVerdict, retrieved.LastOutputVerdict)
	}

	if retrieved.SensitiveDetected != meta.SensitiveDetected {
		t.Errorf("sensitive: expected %v, got %v",
			meta.SensitiveDetected, retrieved.SensitiveDetected)
	}

	// Test Delete
	err = cache.Delete(ctx, tenantID, sessionID)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify deleted
	retrieved, err = cache.Get(ctx, tenantID, sessionID)
	if err != nil {
		t.Fatalf("Get after delete failed: %v", err)
	}
	if retrieved != nil {
		t.Error("expected nil after delete")
	}
}

// TestRedisGovernanceCache_CacheMiss 测试缓存未命中
func TestRedisGovernanceCache_CacheMiss(t *testing.T) {
	t.Skip("Skipping test that requires Redis connection")

	cache := NewRedisGovernanceCache("localhost:6379", 5*time.Minute)
	if !cache.enabled {
		t.Skip("Redis not available, skipping test")
	}
	defer cache.Close()

	ctx := context.Background()

	// Get non-existent key
	meta, err := cache.Get(ctx, "nonexistent", "session123")
	if err != nil {
		t.Errorf("expected no error for cache miss, got: %v", err)
	}
	if meta != nil {
		t.Error("expected nil meta for cache miss")
	}
}

// TestRedisGovernanceCache_NilMeta 测试 nil meta 处理
func TestRedisGovernanceCache_NilMeta(t *testing.T) {
	cache := NewRedisGovernanceCache("", 0) // disabled cache

	ctx := context.Background()

	// Set with nil meta should be no-op
	err := cache.Set(ctx, "tenant1", "session1", nil)
	if err != nil {
		t.Errorf("expected no error for nil meta, got: %v", err)
	}
}

// TestRedisGovernanceCache_TTL 测试 TTL 设置
func TestRedisGovernanceCache_TTL(t *testing.T) {
	tests := []struct {
		name        string
		inputTTL    time.Duration
		expectedTTL time.Duration
	}{
		{
			name:        "default TTL",
			inputTTL:    0,
			expectedTTL: 30 * time.Minute,
		},
		{
			name:        "custom TTL",
			inputTTL:    10 * time.Minute,
			expectedTTL: 10 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := NewRedisGovernanceCache("", tt.inputTTL)
			if cache.ttl != tt.expectedTTL {
				t.Errorf("expected TTL %v, got %v", tt.expectedTTL, cache.ttl)
			}
		})
	}
}

// TestRedisGovernanceCache_Close 测试关闭连接
func TestRedisGovernanceCache_Close(t *testing.T) {
	// Disabled cache
	cache := NewRedisGovernanceCache("", 0)
	err := cache.Close()
	if err != nil {
		t.Errorf("expected no error closing disabled cache, got: %v", err)
	}
}
