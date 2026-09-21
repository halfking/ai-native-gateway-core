package executors

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCalculateSessionStickyTTL(t *testing.T) {
	tests := []struct {
		model       string
		expectedTTL time.Duration
	}{
		{"text-embedding-3-large", 30 * time.Second},
		{"text-embedding-ada-002", 30 * time.Second},
		{"gpt-4-turbo", 10 * time.Minute},
		{"gpt-3.5-turbo", 10 * time.Minute},
		{"claude-3-sonnet", 10 * time.Minute},
		{"claude-3-opus", 10 * time.Minute},
		{"gemini-pro", 10 * time.Minute},
		{"gpt-3.5-turbo-instruct", 30 * time.Minute},
		{"text-davinci-003", 30 * time.Minute},
		{"unknown-model-xyz", 15 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			ttl := calculateSessionStickyTTL(tt.model)
			assert.Equal(t, tt.expectedTTL, ttl, "TTL mismatch for model %s", tt.model)
		})
	}
}

func TestCalculateSessionStickyTTL_CaseInsensitive(t *testing.T) {
	tests := []string{
		"GPT-4-TURBO",
		"gpt-4-turbo",
		"Gpt-4-Turbo",
	}

	for _, model := range tests {
		t.Run(model, func(t *testing.T) {
			ttl := calculateSessionStickyTTL(model)
			assert.Equal(t, 10*time.Minute, ttl, "Should be case insensitive")
		})
	}
}

func TestStickyCache_TTLExpiry(t *testing.T) {
	cache := NewStickyCache()

	// 设置短TTL
	cache.Set("test-key", 123, 100*time.Millisecond)

	// 立即查询应该成功
	credID, found := cache.Get("test-key")
	assert.True(t, found)
	assert.Equal(t, 123, credID)

	// 等待过期
	time.Sleep(150 * time.Millisecond)

	// 查询应该失败
	_, found = cache.Get("test-key")
	assert.False(t, found)
}

func TestStickyCache_RecordFailure_ReroutesAfterTwoConsecutiveFailures(t *testing.T) {
	cache := NewStickyCache()
	cache.Set("route", 123, time.Minute)

	if reroute := cache.RecordFailure("route", 2); reroute {
		t.Fatal("first failure must retain the sticky route")
	}
	if reroute := cache.RecordFailure("route", 2); !reroute {
		t.Fatal("second consecutive failure must remove the sticky route")
	}
	if _, found := cache.Get("route"); found {
		t.Fatal("sticky route must be absent after the reroute threshold")
	}
}

func TestStickyCache_RecordFailure_ResetsAfterTenSecondWindow(t *testing.T) {
	cache := NewStickyCache()
	cache.Set("route", 123, time.Minute)

	if reroute := cache.RecordFailure("route", 2); reroute {
		t.Fatal("first failure must retain the sticky route")
	}

	cache.mu.Lock()
	entry := cache.items["route"]
	entry.lastFailureAt = time.Now().Add(-11 * time.Second)
	cache.items["route"] = entry
	cache.mu.Unlock()

	if reroute := cache.RecordFailure("route", 2); reroute {
		t.Fatal("failure after the ten-second window must start a new streak")
	}
	if _, found := cache.Get("route"); !found {
		t.Fatal("route must remain after a reset first failure")
	}
}

func TestStickyCache_RecordFailureMultiLevel_RemovesAllMatchingLevels(t *testing.T) {
	cache := NewStickyCache()
	appID, apiKeyID := 1, 2
	cache.RecordSuccessMultiLevel("tenant", &appID, &apiKeyID, "profile", "session", "gpt-4", 123)

	if reroute := cache.RecordFailureMultiLevel("tenant", &appID, &apiKeyID, "profile", "session", "gpt-4", 123, 2); reroute {
		t.Fatal("first multi-level failure must retain sticky entries")
	}
	if reroute := cache.RecordFailureMultiLevel("tenant", &appID, &apiKeyID, "profile", "session", "gpt-4", 123, 2); !reroute {
		t.Fatal("second multi-level failure must trigger reroute")
	}

	lookup := cache.GetMultiLevel("tenant", &appID, &apiKeyID, "profile", "session", "gpt-4")
	if lookup.Found {
		t.Fatal("all matching sticky levels must be removed after reroute")
	}
}

func TestStickyCache_RecordSuccessMultiLevel_DynamicTTL(t *testing.T) {
	cache := NewStickyCache()

	// 记录 chat 模型的成功
	cache.RecordSuccessMultiLevel(
		"tenant1",
		intPtr(1),
		intPtr(1),
		"profile1",
		"session1",
		"gpt-4-turbo",
		123,
	)

	// L1 应该使用 10 分钟 TTL（chat 模型）
	l1Key := "tenant1:1:1:profile1:session1:gpt-4-turbo"
	credID, found := cache.Get(l1Key)
	assert.True(t, found)
	assert.Equal(t, 123, credID)

	// 验证 TTL 是否正确（通过检查 expiresAt）
	cache.mu.RLock()
	entry, exists := cache.items[l1Key]
	cache.mu.RUnlock()

	assert.True(t, exists)
	expectedExpiry := time.Now().Add(10 * time.Minute)
	// 允许 1 秒的误差
	assert.WithinDuration(t, expectedExpiry, entry.expiresAt, 1*time.Second)
}

func TestStickyCache_NewSessionDoesNotInheritClientModelOrClientBinding(t *testing.T) {
	cache := NewStickyCache()
	appID, apiKeyID := 1, 2

	cache.RecordSuccessMultiLevel("tenant", &appID, &apiKeyID, "profile", "session-a", "minimax-m3", 42)

	first := cache.GetMultiLevel("tenant", &appID, &apiKeyID, "profile", "session-a", "minimax-m3")
	if !first.Found || first.Level != StickyLevelSession || first.CredentialID != 42 {
		t.Fatalf("same session must reuse L1 binding, got %+v", first)
	}

	second := cache.GetMultiLevel("tenant", &appID, &apiKeyID, "profile", "session-b", "minimax-m3")
	if second.Found {
		t.Fatalf("new session must not inherit prior client/model binding, got %+v", second)
	}

	_, l2, l3 := buildStickyKeys("tenant", &appID, &apiKeyID, "profile", "session-a", "minimax-m3")
	cache.mu.RLock()
	_, l2Exists := cache.items[l2]
	_, l3Exists := cache.items[l3]
	cache.mu.RUnlock()
	if l2Exists || l3Exists {
		t.Fatalf("complete session/model success must persist only L1; l2=%t l3=%t", l2Exists, l3Exists)
	}
}

func TestStickyCache_IncompleteIdentityUsesL3Fallback(t *testing.T) {
	cache := NewStickyCache()
	appID, apiKeyID := 1, 2

	cache.RecordSuccessMultiLevel("tenant", &appID, &apiKeyID, "profile", "", "", 42)

	lookup := cache.GetMultiLevel("tenant", &appID, &apiKeyID, "profile", "", "")
	if !lookup.Found || lookup.Level != StickyLevelClient || lookup.CredentialID != 42 {
		t.Fatalf("incomplete identity must retain L3 compatibility fallback, got %+v", lookup)
	}
}
