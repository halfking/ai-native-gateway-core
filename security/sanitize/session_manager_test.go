package sanitize

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestRedis 启动一个内嵌的 miniredis 实例，返回连接到它的 redis.Client。
// 测试结束后自动清理（t.Cleanup）。
func setupTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	return client
}

func TestSessionSanitizeManager_MergeSanitizeMap(t *testing.T) {
	client := setupTestRedis(t)
	mgr := NewSessionSanitizeManager(client, 5*time.Minute)
	ctx := context.Background()
	tenantID := "tenant-001"
	sessionID := "session-001"

	// 第一次合并
	map1 := SanitizeMap{
		"{SENSITIVE:phone:1}": "13800138000",
		"{SENSITIVE:email:1}": "test@example.com",
	}

	err := mgr.MergeSanitizeMap(ctx, tenantID, sessionID, map1)
	require.NoError(t, err)

	// 读取验证
	result, err := mgr.GetSanitizeMap(ctx, tenantID, sessionID)
	require.NoError(t, err)
	assert.Equal(t, map1, result)

	// 第二次合并（新增）
	map2 := SanitizeMap{
		"{SENSITIVE:phone:2}":  "13900139000",
		"{SENSITIVE:id_card:1}": "110101199001011234",
	}

	err = mgr.MergeSanitizeMap(ctx, tenantID, sessionID, map2)
	require.NoError(t, err)

	// 应该包含所有4个键
	result, err = mgr.GetSanitizeMap(ctx, tenantID, sessionID)
	require.NoError(t, err)
	assert.Len(t, result, 4)
	assert.Equal(t, "13800138000", result["{SENSITIVE:phone:1}"])
	assert.Equal(t, "13900139000", result["{SENSITIVE:phone:2}"])
}

func TestSessionSanitizeManager_MergeDoesNotOverwrite(t *testing.T) {
	client := setupTestRedis(t)
	mgr := NewSessionSanitizeManager(client, 5*time.Minute)
	ctx := context.Background()

	// 第一次写入
	map1 := SanitizeMap{"{SENSITIVE:phone:1}": "13800138000"}
	err := mgr.MergeSanitizeMap(ctx, "t1", "s1", map1)
	require.NoError(t, err)

	// 第二次写入相同占位符但不同值——不应覆盖
	map2 := SanitizeMap{"{SENSITIVE:phone:1}": "99999999999"}
	err = mgr.MergeSanitizeMap(ctx, "t1", "s1", map2)
	require.NoError(t, err)

	result, err := mgr.GetSanitizeMap(ctx, "t1", "s1")
	require.NoError(t, err)
	assert.Equal(t, "13800138000", result["{SENSITIVE:phone:1}"], "已有键不应被覆盖")
}

func TestSessionSanitizeManager_GetSanitizeMap_NotExists(t *testing.T) {
	client := setupTestRedis(t)
	mgr := NewSessionSanitizeManager(client, 5*time.Minute)
	ctx := context.Background()

	// 不存在的会话，应返回空map而不是error
	result, err := mgr.GetSanitizeMap(ctx, "tenant-999", "session-999")
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestSessionSanitizeManager_ExtendTTL(t *testing.T) {
	client := setupTestRedis(t)
	mgr := NewSessionSanitizeManager(client, 1*time.Minute)
	ctx := context.Background()
	tenantID := "tenant-001"
	sessionID := "session-001"

	// 创建映射表
	map1 := SanitizeMap{
		"{SENSITIVE:phone:1}": "13800138000",
	}
	err := mgr.MergeSanitizeMap(ctx, tenantID, sessionID, map1)
	require.NoError(t, err)

	// 延长TTL
	err = mgr.ExtendTTL(ctx, tenantID, sessionID)
	require.NoError(t, err)

	// 验证TTL已刷新（应该接近60秒）
	stats, err := mgr.GetStats(ctx, tenantID, sessionID)
	require.NoError(t, err)
	ttl := stats["ttl_seconds"].(int)
	assert.Greater(t, ttl, 50, "TTL应被刷新到接近60秒")
}

func TestSessionSanitizeManager_ExtendTTL_KeyNotExists(t *testing.T) {
	client := setupTestRedis(t)
	mgr := NewSessionSanitizeManager(client, 1*time.Minute)
	ctx := context.Background()

	// key不存在时ExtendTTL应返回nil（不报错）
	err := mgr.ExtendTTL(ctx, "t-missing", "s-missing")
	require.NoError(t, err)
}

func TestSessionSanitizeManager_RestoreSanitizeMap(t *testing.T) {
	client := setupTestRedis(t)
	mgr := NewSessionSanitizeManager(client, 5*time.Minute)
	ctx := context.Background()
	tenantID := "tenant-001"
	sessionID := "session-001"

	// 从快照恢复
	snapshotMap := SanitizeMap{
		"{SENSITIVE:phone:1}":  "13800138000",
		"{SENSITIVE:email:1}":  "test@example.com",
		"{SENSITIVE:id_card:1}": "110101199001011234",
	}

	err := mgr.RestoreSanitizeMap(ctx, tenantID, sessionID, snapshotMap)
	require.NoError(t, err)

	// 验证恢复成功
	result, err := mgr.GetSanitizeMap(ctx, tenantID, sessionID)
	require.NoError(t, err)
	assert.Equal(t, snapshotMap, result)
}

func TestSessionSanitizeManager_DeleteSanitizeMap(t *testing.T) {
	client := setupTestRedis(t)
	mgr := NewSessionSanitizeManager(client, 5*time.Minute)
	ctx := context.Background()
	tenantID := "tenant-001"
	sessionID := "session-001"

	// 创建映射表
	map1 := SanitizeMap{
		"{SENSITIVE:phone:1}": "13800138000",
	}
	err := mgr.MergeSanitizeMap(ctx, tenantID, sessionID, map1)
	require.NoError(t, err)

	// 删除
	err = mgr.DeleteSanitizeMap(ctx, tenantID, sessionID)
	require.NoError(t, err)

	// 验证已删除
	result, err := mgr.GetSanitizeMap(ctx, tenantID, sessionID)
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestSessionSanitizeManager_ConcurrentMerge(t *testing.T) {
	client := setupTestRedis(t)
	mgr := NewSessionSanitizeManager(client, 5*time.Minute)
	ctx := context.Background()
	tenantID := "tenant-001"
	sessionID := "session-concurrent"

	// 并发合并多个不同占位符的映射表
	done := make(chan bool, 5)

	for i := 0; i < 5; i++ {
		go func(idx int) {
			defer func() { done <- true }()

			m := SanitizeMap{
				Placeholder{Type: TypePhone, Index: idx + 1}.String(): "1380013800" + string(rune('0'+idx)),
			}

			err := mgr.MergeSanitizeMap(ctx, tenantID, sessionID, m)
			assert.NoError(t, err)
		}(i)
	}

	// 等待所有goroutine完成
	for i := 0; i < 5; i++ {
		<-done
	}

	// 验证所有数据都已合并（Lua脚本保证原子性，不会丢失）
	result, err := mgr.GetSanitizeMap(ctx, tenantID, sessionID)
	require.NoError(t, err)
	assert.Len(t, result, 5, "并发合并后应包含全部5个条目")
}

func TestSessionSanitizeManager_NilManager(t *testing.T) {
	var mgr *SessionSanitizeManager
	ctx := context.Background()

	// nil manager 应返回error，不panic
	err := mgr.MergeSanitizeMap(ctx, "t1", "s1", SanitizeMap{"a": "b"})
	assert.Error(t, err)

	_, err = mgr.GetSanitizeMap(ctx, "t1", "s1")
	assert.Error(t, err)

	err = mgr.ExtendTTL(ctx, "t1", "s1")
	assert.Error(t, err)
}

func TestSessionSanitizeManager_EmptyMap(t *testing.T) {
	client := setupTestRedis(t)
	mgr := NewSessionSanitizeManager(client, 5*time.Minute)
	ctx := context.Background()

	// 空映射表不应触发Redis操作
	err := mgr.MergeSanitizeMap(ctx, "t1", "s1", SanitizeMap{})
	require.NoError(t, err)

	// 验证没有写入
	result, err := mgr.GetSanitizeMap(ctx, "t1", "s1")
	require.NoError(t, err)
	assert.Empty(t, result)
}
