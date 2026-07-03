package ursm

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLayerCache_MemoryCache(t *testing.T) {
	// 创建仅内存缓存的LayerCache
	cache := NewLayerCache[ProviderState](
		nil, // 无Redis
		nil, // 无DB
		1*time.Second,
		5*time.Minute,
		"test",
		nil,
	)

	ctx := context.Background()
	testKey := "provider:1"
	testValue := &ProviderState{
		ProviderID:  1,
		Enabled:     true,
		DisplayName: "Test Provider",
		UpdatedAt:   time.Now(),
	}

	// 测试Set和Get
	err := cache.Set(ctx, testKey, testValue)
	require.NoError(t, err)

	result, err := cache.Get(ctx, testKey)
	require.NoError(t, err)
	assert.Equal(t, testValue.ProviderID, result.ProviderID)
	assert.Equal(t, testValue.Enabled, result.Enabled)
	assert.Equal(t, testValue.DisplayName, result.DisplayName)
}

func TestLayerCache_MemoryCacheTTL(t *testing.T) {
	cache := NewLayerCache[ProviderState](
		nil,
		nil,
		100*time.Millisecond, // 短TTL用于测试
		5*time.Minute,
		"test",
		nil,
	)

	ctx := context.Background()
	testKey := "provider:2"
	testValue := &ProviderState{
		ProviderID:  2,
		Enabled:     true,
		DisplayName: "TTL Test",
		UpdatedAt:   time.Now(),
	}

	// 写入缓存
	err := cache.Set(ctx, testKey, testValue)
	require.NoError(t, err)

	// 立即读取应该成功
	result, err := cache.Get(ctx, testKey)
	require.NoError(t, err)
	assert.Equal(t, testValue.ProviderID, result.ProviderID)

	// 等待TTL过期
	time.Sleep(150 * time.Millisecond)

	// 再次读取应该失败（无Redis和DB作为fallback）
	_, err = cache.Get(ctx, testKey)
	assert.Error(t, err)
}

func TestLayerCache_Invalidate(t *testing.T) {
	cache := NewLayerCache[CredentialState](
		nil,
		nil,
		10*time.Second,
		5*time.Minute,
		"test",
		nil,
	)

	ctx := context.Background()
	testKey := "credential:100"
	testValue := &CredentialState{
		CredentialID:      100,
		Status:            "active",
		AvailabilityState: "ready",
		UpdatedAt:         time.Now(),
	}

	// 写入缓存
	err := cache.Set(ctx, testKey, testValue)
	require.NoError(t, err)

	// 验证存在
	result, err := cache.Get(ctx, testKey)
	require.NoError(t, err)
	assert.Equal(t, testValue.CredentialID, result.CredentialID)

	// 失效缓存
	err = cache.Invalidate(ctx, testKey)
	require.NoError(t, err)

	// 再次读取应该失败
	_, err = cache.Get(ctx, testKey)
	assert.Error(t, err)
}

func TestLayerCache_Clear(t *testing.T) {
	cache := NewLayerCache[ModelState](
		nil,
		nil,
		10*time.Second,
		5*time.Minute,
		"test",
		nil,
	)

	ctx := context.Background()

	// 写入多个条目
	for i := 1; i <= 5; i++ {
		key := "model:" + string(rune(i))
		value := &ModelState{
			CredentialID:     i,
			RawModel:         "gpt-4",
			OfferAvailable:   true,
			BindingAvailable: true,
			ProbeState:       "healthy_confirmed",
			UpdatedAt:        time.Now(),
		}
		err := cache.Set(ctx, key, value)
		require.NoError(t, err)
	}

	// 清空缓存
	cache.Clear()

	// 验证所有条目都已清空
	for i := 1; i <= 5; i++ {
		key := "model:" + string(rune(i))
		_, err := cache.Get(ctx, key)
		assert.Error(t, err)
	}
}

func TestLayerCache_ConcurrentAccess(t *testing.T) {
	cache := NewLayerCache[NodeState](
		nil,
		nil,
		10*time.Second,
		5*time.Minute,
		"test",
		nil,
	)

	ctx := context.Background()
	testKey := "node:1:gpt-4"
	testValue := &NodeState{
		CredentialID:        1,
		RawModel:            "gpt-4",
		ConsecutiveFailures: 0,
		SuccessCount:        100,
		FailureCount:        5,
		UpdatedAt:           time.Now(),
	}

	// 写入初始值
	err := cache.Set(ctx, testKey, testValue)
	require.NoError(t, err)

	// 并发读取
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- true }()
			result, err := cache.Get(ctx, testKey)
			assert.NoError(t, err)
			if err == nil {
				assert.Equal(t, testValue.CredentialID, result.CredentialID)
				assert.Equal(t, testValue.RawModel, result.RawModel)
			}
		}()
	}

	// 等待所有goroutine完成
	for i := 0; i < 10; i++ {
		<-done
	}
}
