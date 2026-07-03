package ursm

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
)

// TestIsAvailable_FailOpen 测试fail-open策略
func TestIsAvailable_FailOpen(t *testing.T) {
	// Setup
	mockDB := &pgxpool.Pool{} // 空pool，触发缓存miss
	mockRedis := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	cfg := DefaultConfig()
	mgr := NewManager(mockDB, mockRedis, cfg)

	ctx := context.Background()

	// Test: 缓存miss时应返回true（fail-open）
	available, reason := mgr.IsAvailable(ctx, 999, "gpt-4")

	assert.True(t, available, "should be available on cache miss (fail-open)")
	assert.Empty(t, reason, "reason should be empty on fail-open")
}

// TestGetAvailableNodes_EmptyDB 测试空DB场景
func TestGetAvailableNodes_EmptyDB(t *testing.T) {
	t.Skip("requires real DB connection pool, cannot mock pgxpool.Pool")
}

// TestLoadNodesByModel 测试节点加载（需要真实DB）
// 注意：此测试需要集成测试环境，跳过单元测试
func TestLoadNodesByModel(t *testing.T) {
	t.Skip("requires integration test environment with real DB")
}
