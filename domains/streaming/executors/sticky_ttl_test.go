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
