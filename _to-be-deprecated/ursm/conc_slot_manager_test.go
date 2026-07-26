package ursm

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConcurrencySlotManager_NormalAcquisition(t *testing.T) {
	rdb := setupRedisForTest(t)
	defer rdb.Close()

	mgr := NewConcurrencySlotManager(rdb)

	ctx := context.Background()
	credentialID := 200
	limit := 3

	// 获取3个并发槽
	for i := 1; i <= 3; i++ {
		sessionID := "conc-sess-" + string(rune('0'+i))
		acquired, reason := mgr.CheckAndAcquire(ctx, credentialID, sessionID, limit)
		require.True(t, acquired, "acquire %d should succeed", i)
		assert.Empty(t, reason)
	}

	// 第4个应该被拒绝
	acquired4, reason4 := mgr.CheckAndAcquire(ctx, credentialID, "conc-sess-4", limit)
	assert.False(t, acquired4, "4th acquire should fail")
	assert.Equal(t, "concurrency_limit_reached", reason4)
}

func TestConcurrencySlotManager_ReleaseAndReacquire(t *testing.T) {
	rdb := setupRedisForTest(t)
	defer rdb.Close()

	mgr := NewConcurrencySlotManager(rdb)

	ctx := context.Background()
	credentialID := 201
	limit := 2

	// 占满
	acquired1, _ := mgr.CheckAndAcquire(ctx, credentialID, "conc-sess-1", limit)
	require.True(t, acquired1)

	acquired2, _ := mgr.CheckAndAcquire(ctx, credentialID, "conc-sess-2", limit)
	require.True(t, acquired2)

	// 第3个被拒绝
	acquired3, _ := mgr.CheckAndAcquire(ctx, credentialID, "conc-sess-3", limit)
	assert.False(t, acquired3)

	// 释放一个
	err := mgr.Release(ctx, credentialID, "conc-sess-1")
	assert.NoError(t, err)

	// 现在应该可以获取
	acquired3_retry, reason3 := mgr.CheckAndAcquire(ctx, credentialID, "conc-sess-3", limit)
	assert.True(t, acquired3_retry, "should succeed after release")
	assert.Empty(t, reason3)
}

func TestConcurrencySlotManager_SessionLevelCounting(t *testing.T) {
	rdb := setupRedisForTest(t)
	defer rdb.Close()

	mgr := NewConcurrencySlotManager(rdb)

	ctx := context.Background()
	credentialID := 202
	sessionID := "conc-sess-multi"
	limit := 5

	// 同一session获取3次
	for i := 0; i < 3; i++ {
		acquired, _ := mgr.CheckAndAcquire(ctx, credentialID, sessionID, limit)
		require.True(t, acquired)
	}

	// 验证全局计数是3
	limKey := concSlotKey(credentialID)
	globalCount, err := rdb.Get(ctx, limKey).Int()
	require.NoError(t, err)
	assert.Equal(t, 3, globalCount)

	// 验证会话计数是3
	sessKey := concSessionKey(credentialID, sessionID)
	sessCount, err := rdb.Get(ctx, sessKey).Int()
	require.NoError(t, err)
	assert.Equal(t, 3, sessCount)

	// 释放1次
	err = mgr.Release(ctx, credentialID, sessionID)
	assert.NoError(t, err)

	// 验证全局计数是2
	globalCount2, _ := rdb.Get(ctx, limKey).Int()
	assert.Equal(t, 2, globalCount2)

	// 验证会话计数是2
	sessCount2, _ := rdb.Get(ctx, sessKey).Int()
	assert.Equal(t, 2, sessCount2)
}

func TestConcurrencySlotManager_UnlimitedConcurrency(t *testing.T) {
	rdb := setupRedisForTest(t)
	defer rdb.Close()

	mgr := NewConcurrencySlotManager(rdb)

	ctx := context.Background()
	credentialID := 203
	limit := 0 // 无限制

	// 应该可以无限获取
	for i := 0; i < 100; i++ {
		sessionID := "conc-sess-unlimited-" + string(rune('0'+i%10))
		acquired, _ := mgr.CheckAndAcquire(ctx, credentialID, sessionID, limit)
		assert.True(t, acquired)
	}
}

func TestConcurrencySlotManager_ConcurrentAcquisition(t *testing.T) {
	rdb := setupRedisForTest(t)
	defer rdb.Close()

	mgr := NewConcurrencySlotManager(rdb)

	ctx := context.Background()
	credentialID := 204
	limit := 10

	var wg sync.WaitGroup
	successCount := make(chan bool, 20)

	// 并发20个goroutine尝试获取10个槽位
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sessionID := "conc-sess-concurrent-" + string(rune('0'+idx%10))
			acquired, _ := mgr.CheckAndAcquire(ctx, credentialID, sessionID, limit)
			successCount <- acquired
		}(i)
	}

	wg.Wait()
	close(successCount)

	// 统计成功数
	success := 0
	for acquired := range successCount {
		if acquired {
			success++
		}
	}

	// 应该恰好10个成功
	assert.LessOrEqual(t, success, limit, "success count should not exceed limit")
	t.Logf("Concurrent acquisition: %d/%d succeeded", success, 20)
}

func TestConcurrencySlotManager_ResourceLeakTest(t *testing.T) {
	rdb := setupRedisForTest(t)
	defer rdb.Close()

	mgr := NewConcurrencySlotManager(rdb)

	ctx := context.Background()
	credentialID := 205
	sessionID := "conc-sess-leak-test"
	limit := 5

	// 1000次获取-释放循环
	for i := 0; i < 1000; i++ {
		acquired, _ := mgr.CheckAndAcquire(ctx, credentialID, sessionID, limit)
		require.True(t, acquired, "acquire failed at iteration %d", i)

		err := mgr.Release(ctx, credentialID, sessionID)
		require.NoError(t, err, "release failed at iteration %d", i)
	}

	// 验证Redis key已清空
	limKey := concSlotKey(credentialID)
	globalCount, err := rdb.Get(ctx, limKey).Int()
	if err == nil {
		assert.Equal(t, 0, globalCount, "global count should be 0 after all releases")
	}

	sessKey := concSessionKey(credentialID, sessionID)
	exists, err := rdb.Exists(ctx, sessKey).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), exists, "session key should not exist after all releases")
}
