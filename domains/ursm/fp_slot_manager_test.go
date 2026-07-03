package ursm

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupRedisForTest(t *testing.T) *redis.Client {
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   15, // 使用测试专用DB
	})

	// 测试连接
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Skip("Redis not available, skipping integration tests")
	}

	// 清空测试DB
	client.FlushDB(ctx)

	return client
}

func TestFingerprintSlotManager_PinReuse(t *testing.T) {
	rdb := setupRedisForTest(t)
	defer rdb.Close()

	cfg := FpSlotConfig{
		SlotTTL:    30 * time.Minute,
		PinTTL:     24 * time.Hour,
		ActiveGate: 5 * time.Minute,
	}
	mgr := NewFingerprintSlotManager(rdb, cfg)

	ctx := context.Background()
	credentialID := 100
	sessionID := "session-123"
	limit := 3

	// 第一次获取
	slot1, acquired1, reason1 := mgr.CheckAndAcquire(ctx, credentialID, sessionID, limit)
	require.True(t, acquired1, "first acquire should succeed")
	assert.GreaterOrEqual(t, slot1, 0)
	assert.LessOrEqual(t, slot1, limit-1)
	assert.Contains(t, reason1, "free_slot")

	// 第二次获取（同一会话）- 应该复用pin
	slot2, acquired2, reason2 := mgr.CheckAndAcquire(ctx, credentialID, sessionID, limit)
	require.True(t, acquired2, "pin reuse should succeed")
	assert.Equal(t, slot1, slot2, "should get the same slot")
	assert.Equal(t, "pin_reuse", reason2)
}

func TestFingerprintSlotManager_FreeSlotAcquisition(t *testing.T) {
	rdb := setupRedisForTest(t)
	defer rdb.Close()

	cfg := FpSlotConfig{
		SlotTTL:    30 * time.Minute,
		PinTTL:     24 * time.Hour,
		ActiveGate: 5 * time.Minute,
	}
	mgr := NewFingerprintSlotManager(rdb, cfg)

	ctx := context.Background()
	credentialID := 101
	limit := 3

	// 三个不同session分别获取槽位
	sessions := []string{"sess-1", "sess-2", "sess-3"}
	slots := make(map[int]bool)

	for _, sessionID := range sessions {
		slot, acquired, _ := mgr.CheckAndAcquire(ctx, credentialID, sessionID, limit)
		require.True(t, acquired)
		slots[slot] = true
	}

	// 应该获得3个不同的槽位
	assert.Equal(t, 3, len(slots), "should acquire 3 different slots")
}

func TestFingerprintSlotManager_LRUPreemption(t *testing.T) {
	rdb := setupRedisForTest(t)
	defer rdb.Close()

	cfg := FpSlotConfig{
		SlotTTL:    30 * time.Minute,
		PinTTL:     24 * time.Hour,
		ActiveGate: 2 * time.Second, // 缩短activeGate便于测试
	}
	mgr := NewFingerprintSlotManager(rdb, cfg)

	ctx := context.Background()
	credentialID := 102
	limit := 2

	// 占满所有槽位
	slot1, acquired1, _ := mgr.CheckAndAcquire(ctx, credentialID, "sess-1", limit)
	require.True(t, acquired1)

	slot2, acquired2, _ := mgr.CheckAndAcquire(ctx, credentialID, "sess-2", limit)
	require.True(t, acquired2)
	assert.NotEqual(t, slot1, slot2, "should get different slots")

	// 等待activeGate超时
	time.Sleep(3 * time.Second)

	// 新session应该能抢占LRU槽
	slot3, acquired3, reason3 := mgr.CheckAndAcquire(ctx, credentialID, "sess-3", limit)
	require.True(t, acquired3, "LRU preemption should succeed")
	assert.Contains(t, reason3, "lru_preempt")
	assert.True(t, slot3 == slot1 || slot3 == slot2, "should preempt one of the old slots")
}

func TestFingerprintSlotManager_AllSlotsActive(t *testing.T) {
	rdb := setupRedisForTest(t)
	defer rdb.Close()

	cfg := FpSlotConfig{
		SlotTTL:    30 * time.Minute,
		PinTTL:     24 * time.Hour,
		ActiveGate: 5 * time.Minute,
	}
	mgr := NewFingerprintSlotManager(rdb, cfg)

	ctx := context.Background()
	credentialID := 103
	limit := 2

	// 占满所有槽位
	_, acquired1, _ := mgr.CheckAndAcquire(ctx, credentialID, "sess-1", limit)
	require.True(t, acquired1)

	_, acquired2, _ := mgr.CheckAndAcquire(ctx, credentialID, "sess-2", limit)
	require.True(t, acquired2)

	// 第三个session应该被拒绝（所有槽位都在activeGate内）
	_, acquired3, reason3 := mgr.CheckAndAcquire(ctx, credentialID, "sess-3", limit)
	assert.False(t, acquired3, "should reject when all slots are active")
	assert.Equal(t, "all_slots_active", reason3)
}

func TestFingerprintSlotManager_SameHolderConcurrent(t *testing.T) {
	rdb := setupRedisForTest(t)
	defer rdb.Close()

	cfg := FpSlotConfig{
		SlotTTL:    30 * time.Minute,
		PinTTL:     24 * time.Hour,
		ActiveGate: 5 * time.Minute,
	}
	mgr := NewFingerprintSlotManager(rdb, cfg)

	ctx := context.Background()
	credentialID := 104
	sessionID := "sess-concurrent"
	limit := 3

	// 同一session并发获取多次
	slots := make(map[int]int) // slot -> count
	for i := 0; i < 5; i++ {
		slot, acquired, _ := mgr.CheckAndAcquire(ctx, credentialID, sessionID, limit)
		require.True(t, acquired)
		slots[slot]++
	}

	// 应该只占用1个槽位
	assert.Equal(t, 1, len(slots), "same session should only use 1 slot")
}

func TestFingerprintSlotManager_Release(t *testing.T) {
	rdb := setupRedisForTest(t)
	defer rdb.Close()

	cfg := FpSlotConfig{
		SlotTTL:    30 * time.Minute,
		PinTTL:     24 * time.Hour,
		ActiveGate: 5 * time.Minute,
	}
	mgr := NewFingerprintSlotManager(rdb, cfg)

	ctx := context.Background()
	credentialID := 105
	sessionID := "sess-release"
	limit := 2

	// 获取槽位
	slot, acquired, _ := mgr.CheckAndAcquire(ctx, credentialID, sessionID, limit)
	require.True(t, acquired)

	// 释放槽位
	err := mgr.Release(ctx, credentialID, slot, sessionID)
	assert.NoError(t, err)

	// 验证pin仍然存在（通过再次获取应该得到同一槽位）
	slot2, acquired2, reason2 := mgr.CheckAndAcquire(ctx, credentialID, sessionID, limit)
	require.True(t, acquired2)
	assert.Equal(t, slot, slot2)
	assert.Equal(t, "pin_reuse", reason2)
}

func TestFingerprintSlotManager_ForceUnpin(t *testing.T) {
	rdb := setupRedisForTest(t)
	defer rdb.Close()

	cfg := FpSlotConfig{
		SlotTTL:    30 * time.Minute,
		PinTTL:     24 * time.Hour,
		ActiveGate: 5 * time.Minute,
	}
	mgr := NewFingerprintSlotManager(rdb, cfg)

	ctx := context.Background()
	credentialID := 106
	sessionID := "sess-unpin"
	limit := 3

	// 获取槽位
	slot1, acquired1, _ := mgr.CheckAndAcquire(ctx, credentialID, sessionID, limit)
	require.True(t, acquired1)

	// 强制解绑pin
	err := mgr.ForceUnpin(ctx, credentialID, sessionID)
	assert.NoError(t, err)

	// 再次获取可能得到不同的槽位（因为pin已删除）
	slot2, acquired2, _ := mgr.CheckAndAcquire(ctx, credentialID, sessionID, limit)
	require.True(t, acquired2)
	// slot2可能等于slot1（如果slot1仍被占用），也可能不等（如果获取了其他空闲槽）
	t.Logf("After unpin: slot1=%d, slot2=%d", slot1, slot2)
}

func TestFingerprintSlotManager_UnlimitedSlots(t *testing.T) {
	rdb := setupRedisForTest(t)
	defer rdb.Close()

	cfg := FpSlotConfig{
		SlotTTL:    30 * time.Minute,
		PinTTL:     24 * time.Hour,
		ActiveGate: 5 * time.Minute,
	}
	mgr := NewFingerprintSlotManager(rdb, cfg)

	ctx := context.Background()
	credentialID := 107
	sessionID := "sess-unlimited"
	limit := 0 // 无限制

	// 应该直接返回槽位0
	slot, acquired, _ := mgr.CheckAndAcquire(ctx, credentialID, sessionID, limit)
	assert.True(t, acquired)
	assert.Equal(t, 0, slot)
}
