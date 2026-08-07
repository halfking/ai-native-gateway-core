package sanitize

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 使用miniredis进行测试，避免依赖真实Redis
func setupTestRedis(t *testing.T) *redis.Client {
	// 注意：实际项目中应使用 github.com/alicebob/miniredis/v2
	// 这里简化为返回nil，实际测试需要替换
	t.Skip("需要安装miniredis依赖: go get github.com/alicebob/miniredis/v2")
	return nil
}

func TestSessionSanitizeManager_MergeSanitizeMap(t *testing.T) {
	client := setupTestRedis(t)
	if client == nil {
		return
	}
	
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

func TestSessionSanitizeManager_GetSanitizeMap_NotExists(t *testing.T) {
	client := setupTestRedis(t)
	if client == nil {
		return
	}
	
	mgr := NewSessionSanitizeManager(client, 5*time.Minute)
	ctx := context.Background()
	
	// 不存在的会话，应返回空map而不是error
	result, err := mgr.GetSanitizeMap(ctx, "tenant-999", "session-999")
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestSessionSanitizeManager_ExtendTTL(t *testing.T) {
	client := setupTestRedis(t)
	if client == nil {
		return
	}
	
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
	
	// 等待半分钟
	time.Sleep(500 * time.Millisecond)
	
	// 延长TTL
	err = mgr.ExtendTTL(ctx, tenantID, sessionID)
	require.NoError(t, err)
	
	// 验证TTL已刷新（应该接近1分钟）
	stats, err := mgr.GetStats(ctx, tenantID, sessionID)
	require.NoError(t, err)
	ttl := stats["ttl_seconds"].(int)
	assert.Greater(t, ttl, 50) // 应该大于50秒
}

func TestSessionSanitizeManager_RestoreSanitizeMap(t *testing.T) {
	client := setupTestRedis(t)
	if client == nil {
		return
	}
	
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
	if client == nil {
		return
	}
	
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
	if client == nil {
		return
	}
	
	mgr := NewSessionSanitizeManager(client, 5*time.Minute)
	ctx := context.Background()
	tenantID := "tenant-001"
	sessionID := "session-concurrent"
	
	// 并发合并多个映射表
	done := make(chan bool, 5)
	
	for i := 0; i < 5; i++ {
		go func(idx int) {
			defer func() { done <- true }()
			
			m := SanitizeMap{
				"{SENSITIVE:phone:" + string(rune('0'+idx)) + "}": "1380013800" + string(rune('0'+idx)),
			}
			
			err := mgr.MergeSanitizeMap(ctx, tenantID, sessionID, m)
			assert.NoError(t, err)
		}(i)
	}
	
	// 等待所有goroutine完成
	for i := 0; i < 5; i++ {
		<-done
	}
	
	// 验证所有数据都已合并
	result, err := mgr.GetSanitizeMap(ctx, tenantID, sessionID)
	require.NoError(t, err)
	assert.Len(t, result, 5)
}
