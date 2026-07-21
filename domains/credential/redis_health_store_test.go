package credential

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRedisHealthStore_BasicOperations 基础操作测试
func TestRedisHealthStore_BasicOperations(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	store := NewRedisHealthStore(client, slog.Default())

	// 创建credential
	cred := &Credential{
		ID:         "test-1",
		TenantID:   "tenant-1",
		ProviderID: "openai",
		Status:     StatusActive,
	}

	// Save
	err := store.Save(cred)
	require.NoError(t, err)

	// Get (内存读取)
	retrieved, ok, err := store.Get("test-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, StatusActive, retrieved.Status)

	// 等待异步Redis写入
	time.Sleep(100 * time.Millisecond)

	// 验证Redis中有数据
	exists := mr.Exists(store.key("test-1"))
	assert.True(t, exists, "Redis应该包含健康状态")
}

// TestRedisHealthStore_AtomicMarkFailure 原子失败标记测试
func TestRedisHealthStore_AtomicMarkFailure(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	store := NewRedisHealthStore(client, slog.Default())

	cred := &Credential{
		ID:         "test-1",
		TenantID:   "tenant-1",
		ProviderID: "openai",
		Status:     StatusActive,
	}
	_ = store.Save(cred)
	time.Sleep(100 * time.Millisecond)

	// 第1次失败 -> Degraded
	err := store.MarkFailure("test-1", 3)
	require.NoError(t, err)

	retrieved, _, _ := store.Get("test-1")
	assert.Equal(t, StatusDegraded, retrieved.Status)
	assert.Equal(t, 1, retrieved.ConsecutiveFails)

	// 第2次失败 -> 仍Degraded
	_ = store.MarkFailure("test-1", 3)
	retrieved, _, _ = store.Get("test-1")
	assert.Equal(t, StatusDegraded, retrieved.Status)
	assert.Equal(t, 2, retrieved.ConsecutiveFails)

	// 第3次失败 -> Unhealthy
	_ = store.MarkFailure("test-1", 3)
	retrieved, _, _ = store.Get("test-1")
	assert.Equal(t, StatusUnhealthy, retrieved.Status)
	assert.Equal(t, 3, retrieved.ConsecutiveFails)
}

// TestRedisHealthStore_AtomicMarkSuccess 原子成功标记测试
func TestRedisHealthStore_AtomicMarkSuccess(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	store := NewRedisHealthStore(client, slog.Default())

	cred := &Credential{
		ID:               "test-1",
		TenantID:         "tenant-1",
		ProviderID:       "openai",
		Status:           StatusUnhealthy,
		ConsecutiveFails: 4,
	}
	_ = store.Save(cred)
	time.Sleep(100 * time.Millisecond)

	// 第1次成功 -> 仍Unhealthy (fails=3)
	err := store.MarkSuccess("test-1", 2)
	require.NoError(t, err)

	retrieved, _, _ := store.Get("test-1")
	assert.Equal(t, StatusUnhealthy, retrieved.Status)
	assert.Equal(t, 3, retrieved.ConsecutiveFails)

	// 第2次成功 -> Degraded (fails=2)
	_ = store.MarkSuccess("test-1", 2)
	retrieved, _, _ = store.Get("test-1")
	assert.Equal(t, StatusDegraded, retrieved.Status)
	assert.Equal(t, 2, retrieved.ConsecutiveFails)

	// 继续成功 -> Active
	_ = store.MarkSuccess("test-1", 2)
	_ = store.MarkSuccess("test-1", 2)
	retrieved, _, _ = store.Get("test-1")
	assert.Equal(t, StatusActive, retrieved.Status)
	assert.Equal(t, 0, retrieved.ConsecutiveFails)
}

// TestRedisHealthStore_ConcurrentWrites 并发写入一致性测试
func TestRedisHealthStore_ConcurrentWrites(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	store := NewRedisHealthStore(client, slog.Default())

	// 创建10个credentials
	for i := 1; i <= 10; i++ {
		cred := &Credential{
			ID:         fmt.Sprintf("cred-%d", i),
			TenantID:   "tenant-1",
			ProviderID: "openai",
			Status:     StatusActive,
		}
		_ = store.Save(cred)
	}
	time.Sleep(200 * time.Millisecond)

	const goroutines = 100
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)

	start := time.Now()
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			credID := fmt.Sprintf("cred-%d", (id%10)+1)

			for i := 0; i < iterations; i++ {
				if (id+i)%3 == 0 {
					_ = store.MarkSuccess(credID, 2)
				} else {
					_ = store.MarkFailure(credID, 3)
				}
			}
		}(g)
	}

	wg.Wait()
	elapsed := time.Since(start)

	totalOps := goroutines * iterations
	t.Logf("并发写入测试: %d goroutines × %d ops = %d total",
		goroutines, iterations, totalOps)
	t.Logf("耗时: %v", elapsed)
	t.Logf("吞吐量: %.0f ops/s", float64(totalOps)/elapsed.Seconds())

	// 验证最终状态一致性
	time.Sleep(500 * time.Millisecond) // 等待异步Redis写入完成

	for i := 1; i <= 10; i++ {
		credID := fmt.Sprintf("cred-%d", i)
		cred, ok, err := store.Get(credID)
		require.NoError(t, err)
		require.True(t, ok)

		// 验证状态与ConsecutiveFails一致
		expectedStatus := StatusActive
		if cred.ConsecutiveFails >= 3 {
			expectedStatus = StatusUnhealthy
		} else if cred.ConsecutiveFails > 0 {
			expectedStatus = StatusDegraded
		}

		assert.Equal(t, expectedStatus, cred.Status,
			"credential %s 状态应该一致", credID)

		t.Logf("  %s: Status=%v, Fails=%d", credID, cred.Status, cred.ConsecutiveFails)
	}
}

// TestRedisHealthStore_ReadPerformance 读取性能基准
func TestRedisHealthStore_ReadPerformance(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	store := NewRedisHealthStore(client, slog.Default())

	// 预热：创建100个credentials
	for i := 1; i <= 100; i++ {
		cred := &Credential{
			ID:         fmt.Sprintf("cred-%d", i),
			TenantID:   "tenant-1",
			ProviderID: "openai",
			Status:     StatusActive,
		}
		_ = store.Save(cred)
	}
	time.Sleep(200 * time.Millisecond)

	// 测试读取性能
	const reads = 100000
	start := time.Now()

	for i := 0; i < reads; i++ {
		credID := fmt.Sprintf("cred-%d", (i%100)+1)
		_, _, _ = store.Get(credID)
	}

	elapsed := time.Since(start)
	avgLatency := elapsed / time.Duration(reads)

	t.Logf("读取性能测试: %d reads", reads)
	t.Logf("总耗时: %v", elapsed)
	t.Logf("平均延迟: %v", avgLatency)
	t.Logf("吞吐量: %.0f reads/s", float64(reads)/elapsed.Seconds())

	// 性能要求：平均延迟 < 1μs (内存读取)
	if avgLatency > time.Microsecond {
		t.Errorf("读取延迟过高: %v > 1μs", avgLatency)
	}
}

// TestRedisHealthStore_LoadFromRedis 启动恢复测试
func TestRedisHealthStore_LoadFromRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	store1 := NewRedisHealthStore(client, slog.Default())

	// 第一阶段：写入一些状态
	for i := 1; i <= 5; i++ {
		cred := &Credential{
			ID:         fmt.Sprintf("cred-%d", i),
			TenantID:   "tenant-1",
			ProviderID: "openai",
			Status:     StatusActive,
		}
		_ = store1.Save(cred)
	}

	// 标记一些失败
	_ = store1.MarkFailure("cred-1", 3)
	_ = store1.MarkFailure("cred-1", 3)
	_ = store1.MarkFailure("cred-2", 3)

	time.Sleep(200 * time.Millisecond)

	// 第二阶段：模拟服务重启，创建新store
	store2 := NewRedisHealthStore(client, slog.Default())

	// 先注册credentials到内存（模拟从数据库加载）
	for i := 1; i <= 5; i++ {
		cred := &Credential{
			ID:         fmt.Sprintf("cred-%d", i),
			TenantID:   "tenant-1",
			ProviderID: "openai",
			Status:     StatusActive, // 默认active
		}
		_ = store2.memory.Save(cred)
	}

	// 从Redis恢复健康状态
	ctx := context.Background()
	loaded, err := store2.LoadFromRedis(ctx)
	require.NoError(t, err)
	assert.Equal(t, 5, loaded, "应该恢复5个健康状态")

	// 验证恢复的状态
	cred1, _, _ := store2.Get("cred-1")
	assert.Equal(t, StatusDegraded, cred1.Status)
	assert.Equal(t, 2, cred1.ConsecutiveFails)

	cred2, _, _ := store2.Get("cred-2")
	assert.Equal(t, StatusDegraded, cred2.Status)
	assert.Equal(t, 1, cred2.ConsecutiveFails)

	t.Log("✓ 重启后成功从Redis恢复健康状态")
}

// TestRedisHealthStore_RedisFallback Redis故障降级测试
func TestRedisHealthStore_RedisFallback(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	store := NewRedisHealthStore(client, slog.Default())

	cred := &Credential{
		ID:         "test-1",
		TenantID:   "tenant-1",
		ProviderID: "openai",
		Status:     StatusActive,
	}
	_ = store.Save(cred)

	// 模拟Redis故障
	mr.Close()

	// 操作应该仍然成功（降级到内存）
	err := store.MarkFailure("test-1", 3)
	require.NoError(t, err)

	retrieved, ok, err := store.Get("test-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, StatusDegraded, retrieved.Status)
	assert.Equal(t, 1, retrieved.ConsecutiveFails)

	t.Log("✓ Redis故障时成功降级到内存模式")
}

// TestRedisHealthStore_MemoryLeak 内存泄漏检测
func TestRedisHealthStore_MemoryLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过长时间运行测试")
	}

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	store := NewRedisHealthStore(client, slog.Default())

	// 预热
	for i := 1; i <= 10; i++ {
		cred := &Credential{
			ID:         fmt.Sprintf("cred-%d", i),
			TenantID:   "tenant-1",
			ProviderID: "openai",
			Status:     StatusActive,
		}
		_ = store.Save(cred)
	}

	// 记录初始内存
	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)

	// 运行10,000次操作
	const iterations = 10000
	for i := 0; i < iterations; i++ {
		credID := fmt.Sprintf("cred-%d", (i%10)+1)

		if i%3 == 0 {
			_ = store.MarkSuccess(credID, 2)
		} else {
			_ = store.MarkFailure(credID, 3)
		}

		if i%100 == 0 {
			_, _, _ = store.Get(credID)
		}
	}

	// 等待异步goroutine完成
	time.Sleep(1 * time.Second)

	// 记录最终内存
	runtime.GC()
	var m2 runtime.MemStats
	runtime.ReadMemStats(&m2)

	allocDiff := int64(m2.Alloc) - int64(m1.Alloc)
	allocMB := float64(allocDiff) / 1024 / 1024

	t.Logf("内存泄漏测试: %d iterations", iterations)
	t.Logf("初始内存: %.2f MB", float64(m1.Alloc)/1024/1024)
	t.Logf("最终内存: %.2f MB", float64(m2.Alloc)/1024/1024)
	t.Logf("内存增长: %.2f MB", allocMB)

	// 内存增长应该 < 10MB（合理范围）
	if allocMB > 10 {
		t.Errorf("可能存在内存泄漏: 增长 %.2f MB > 10 MB", allocMB)
	}
}

// BenchmarkRedisHealthStore_Get 读取性能基准
func BenchmarkRedisHealthStore_Get(b *testing.B) {
	mr, _ := miniredis.Run()
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	store := NewRedisHealthStore(client, slog.Default())

	cred := &Credential{
		ID:         "bench-1",
		TenantID:   "tenant-1",
		ProviderID: "openai",
		Status:     StatusActive,
	}
	_ = store.Save(cred)
	time.Sleep(100 * time.Millisecond)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = store.Get("bench-1")
	}
}

// BenchmarkRedisHealthStore_MarkFailure 失败标记性能基准
func BenchmarkRedisHealthStore_MarkFailure(b *testing.B) {
	mr, _ := miniredis.Run()
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	store := NewRedisHealthStore(client, slog.Default())

	cred := &Credential{
		ID:         "bench-1",
		TenantID:   "tenant-1",
		ProviderID: "openai",
		Status:     StatusActive,
	}
	_ = store.Save(cred)
	time.Sleep(100 * time.Millisecond)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = store.MarkFailure("bench-1", 3)
	}
}

// BenchmarkRedisHealthStore_MarkSuccess 成功标记性能基准
func BenchmarkRedisHealthStore_MarkSuccess(b *testing.B) {
	mr, _ := miniredis.Run()
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	store := NewRedisHealthStore(client, slog.Default())

	cred := &Credential{
		ID:               "bench-1",
		TenantID:         "tenant-1",
		ProviderID:       "openai",
		Status:           StatusUnhealthy,
		ConsecutiveFails: 3,
	}
	_ = store.Save(cred)
	time.Sleep(100 * time.Millisecond)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = store.MarkSuccess("bench-1", 2)
	}
}
