package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/bucket/core"
)

// TestRateLimitProviderBasic 测试基本的准入控制
func TestRateLimitProviderBasic(t *testing.T) {
	config := DefaultConfig()
	config.Window = 1 * time.Second // 使用较长的窗口，确保测试在同一窗口内
	config.MaxQueueSize = 0         // 队列大小=limit，超过后立即拒绝
	provider := NewRateLimitProvider(config)

	// 使用固定时钟确保在同一窗口内
	fakeClock := &mockClock{now: time.Now().UTC()}
	provider.clock = fakeClock.Now

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	windowStart := fakeClock.Now().Truncate(config.Window)

	key := core.BucketKey{
		Type:       core.BucketTypeRateLimit,
		TenantID:   "tenant1",
		ResourceID: "key1",
		Window: core.TimeWindow{
			Start: windowStart,
			End:   windowStart.Add(config.Window),
			Size:  config.Window,
		},
	}

	limit := 3
	query := core.BucketQuery{
		Type:       core.BucketTypeRateLimit,
		TenantID:   "tenant1",
		ResourceID: "key1",
		Window:     key.Window,
		Params: map[string]interface{}{
			"limit": limit,
		},
	}

	// 前3个请求应该成功
	for i := 0; i < limit; i++ {
		result, err := provider.Query(ctx, query)
		if err != nil {
			t.Fatalf("Request %d failed: %v", i+1, err)
		}
		if admitted, ok := result.Metadata["admitted"].(bool); !ok || !admitted {
			t.Fatalf("Request %d should be admitted", i+1)
		}
		t.Logf("Request %d admitted", i+1)
	}

	// 第4个请求应该被拒绝（队列大小=limit，已满3个在队列）
	// 使用一个新的短超时context避免长时间等待
	quickCtx, quickCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer quickCancel()

	result, err := provider.Query(quickCtx, query)

	// 应该要么是队列满，要么是超时
	if err == nil {
		if admitted, ok := result.Metadata["admitted"].(bool); ok && admitted {
			t.Fatal("Request 4 should not be immediately admitted")
		}
	} else if err != core.ErrBucketExceeded && err != context.DeadlineExceeded {
		t.Logf("Got error (acceptable): %v", err)
	}

	t.Logf("Request 4 correctly handled: err=%v, admitted=%v", err, result.Metadata["admitted"])
}

// TestRateLimitProviderWindowReset 测试时间窗口重置
func TestRateLimitProviderWindowReset(t *testing.T) {
	config := DefaultConfig()
	config.Window = 50 * time.Millisecond
	config.MaxQueueSize = 0 // 队列=limit，满了立即拒绝
	provider := NewRateLimitProvider(config)

	// Mock clock for testing
	fakeClock := &mockClock{now: time.Now().UTC()}
	provider.clock = fakeClock.Now

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	windowStart := fakeClock.Now().Truncate(config.Window)

	key := core.BucketKey{
		Type:       core.BucketTypeRateLimit,
		TenantID:   "tenant1",
		ResourceID: "key1",
		Window: core.TimeWindow{
			Start: windowStart,
			End:   windowStart.Add(config.Window),
			Size:  config.Window,
		},
	}

	query := core.BucketQuery{
		Type:       core.BucketTypeRateLimit,
		TenantID:   "tenant1",
		ResourceID: "key1",
		Window:     key.Window,
		Params: map[string]interface{}{
			"limit": 2,
		},
	}

	// 使用完配额
	for i := 0; i < 2; i++ {
		_, err := provider.Query(ctx, query)
		if err != nil {
			t.Fatalf("Request %d failed: %v", i+1, err)
		}
	}

	// 第3个请求应该被拒绝（使用短超时）
	quickCtx, quickCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer quickCancel()

	result, err := provider.Query(quickCtx, query)
	if err == nil {
		if admitted, ok := result.Metadata["admitted"].(bool); ok && admitted {
			t.Fatal("Request should not be admitted")
		}
	}
	t.Logf("Request 3 rejected as expected: %v", err)

	// 前进到下一个窗口
	fakeClock.Advance(config.Window + time.Millisecond)
	newWindowStart := fakeClock.Now().Truncate(config.Window)
	query.Window = core.TimeWindow{
		Start: newWindowStart,
		End:   newWindowStart.Add(config.Window),
		Size:  config.Window,
	}

	// 新窗口应该可以再次请求
	result, err = provider.Query(ctx, query)
	if err != nil {
		t.Fatalf("Request in new window failed: %v", err)
	}
	if admitted, ok := result.Metadata["admitted"].(bool); !ok || !admitted {
		t.Fatal("Request in new window should be admitted")
	}
	t.Logf("Request in new window admitted successfully")
}

// TestRateLimitProviderConcurrency 测试并发请求
func TestRateLimitProviderConcurrency(t *testing.T) {
	config := DefaultConfig()
	config.Window = 1 * time.Second // 使用较长窗口确保在同一窗口内
	provider := NewRateLimitProvider(config)

	// 使用固定时钟
	fakeClock := &mockClock{now: time.Now().UTC()}
	provider.clock = fakeClock.Now

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	limit := 10
	concurrency := 20

	windowStart := fakeClock.Now().Truncate(config.Window)

	var admitted int32
	var rejected int32
	var wg sync.WaitGroup

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			key := core.BucketKey{
				Type:       core.BucketTypeRateLimit,
				TenantID:   "tenant1",
				ResourceID: "concurrent",
				Window: core.TimeWindow{
					Start: windowStart,
					End:   windowStart.Add(config.Window),
					Size:  config.Window,
				},
			}

			query := core.BucketQuery{
				Type:       core.BucketTypeRateLimit,
				TenantID:   "tenant1",
				ResourceID: "concurrent",
				Window:     key.Window,
				Params: map[string]interface{}{
					"limit": limit,
				},
			}

			result, err := provider.Query(ctx, query)
			if err == nil {
				if adm, ok := result.Metadata["admitted"].(bool); ok && adm {
					atomic.AddInt32(&admitted, 1)
					return
				}
			}
			atomic.AddInt32(&rejected, 1)
		}(i)
	}

	wg.Wait()

	t.Logf("Admitted: %d, Rejected: %d", admitted, rejected)

	// 由于使用了固定时钟和较长窗口，应该只有limit个被接受
	if admitted > int32(limit) {
		t.Errorf("Too many requests admitted: got %d, limit %d", admitted, limit)
	}

	if admitted+rejected != int32(concurrency) {
		t.Errorf("Total requests mismatch: admitted(%d) + rejected(%d) != concurrency(%d)", admitted, rejected, concurrency)
	}
}

// TestMinuteBucketAdmissionAdapter 测试适配器兼容性
func TestMinuteBucketAdmissionAdapter(t *testing.T) {
	config := DefaultConfig()
	// ★ 2026-11-12（48h 审计第五轮）窗口改 1h：★ 限流窗口是**绝对时间网格**
	// ★ （provider 侧 `p.sameWindow(bucket.start, now)` 按当前时间判是否换窗）
	// ★ ⇒ 只要两次调用之间跨过网格边界，配额就**已刷新** ⇒ 「超出 limit 应当被拒」的断言失真。
	// ★ 1s 窗口在负载下（12 路并行实测 1/12 假红）会跨界 ⇒ 用 1h 窗口把边界概率压到可忽略。
	config.Window = 1 * time.Hour
	admission := NewMinuteBucketAdmissionWithConfig(config)

	ctx := context.Background()

	keyID := 1
	limit := 5

	// 测试 Admit
	for i := 0; i < limit; i++ {
		result, err := admission.Admit(ctx, keyID, limit)
		if err != nil {
			t.Fatalf("Admit %d failed: %v", i+1, err)
		}
		if !result.Admitted {
			t.Fatalf("Request %d should be admitted", i+1)
		}
		if result.Limit != limit {
			t.Errorf("Expected limit %d, got %d", limit, result.Limit)
		}
		t.Logf("Request %d admitted, remaining=%d", i+1, result.Remaining)
	}

	// 下一个请求应该失败或排队，使用短超时避免长时间等待
	ctx2, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	result, err := admission.Admit(ctx2, keyID, limit)
	if err == nil && result.Admitted {
		t.Error("Request beyond limit should not be immediately admitted")
	} else {
		t.Logf("Request beyond limit correctly handled: admitted=%v, err=%v", result.Admitted, err)
	}
}

// TestMinuteBucketAdmissionWithWait 测试等待机制
func TestMinuteBucketAdmissionWithWait(t *testing.T) {
	config := DefaultConfig()
	// ★ 2026-11-12（48h 审计第五轮）窗口改长：★ 窗口是**绝对时间网格**（`now.Truncate(window)`）
	// ★ ⇒ 若两次调用之间跨过了窗口边界，第二次会落进**新窗口**⇒ 配额已刷新 ⇒ 断言失真。
	// ★ 100ms 窗口在负载下（8 路并行实测 1/8 假红）会跨边界 ⇒ 本测试用 1s 窗口
	// ★（与同文件 TestRateLimitProviderBasic / TestMinuteBucketAdmissionAdapter 同款口径）。
	config.Window = 1 * time.Second
	admission := NewMinuteBucketAdmissionWithConfig(config)

	ctx := context.Background()
	keyID := 2
	limit := 2

	// 使用完配额
	for i := 0; i < limit; i++ {
		_, err := admission.Admit(ctx, keyID, limit)
		if err != nil {
			t.Fatalf("Request %d failed: %v", i+1, err)
		}
	}

	// 下一个请求需要等待，使用短超时避免测试卡住
	ctx2, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	notified := false
	result, err := admission.AdmitRPMWithWait(ctx2, keyID, limit, func(ar AdmissionResult) {
		notified = true
		if !ar.Waiting {
			t.Error("Should be waiting")
		}
		if ar.Position <= 0 {
			t.Error("Position should be positive")
		}
		t.Logf("Notified: position=%d, estimated_wait=%ds", ar.Position, ar.EstimatedWaitSec)
	})

	// 应该超时或被通知
	if err != nil && err != context.DeadlineExceeded && err != ErrMinuteBucketWaitTimeout {
		t.Fatalf("Unexpected error: %v", err)
	}

	if !result.Admitted && !notified {
		t.Error("Should either be admitted or notified")
	}

	t.Logf("Test completed: admitted=%v, notified=%v, err=%v", result.Admitted, notified, err)
}

// TestMinuteBucketAdmissionWithBudget 测试预算控制
func TestMinuteBucketAdmissionWithBudget(t *testing.T) {
	config := DefaultConfig()
	// ★ 2026-11-12（48h 审计第五轮）窗口改长（本测试原为 100ms ⇒ **负载下必假红**）：
	// ★ 窗口是**绝对时间网格**（`now.Truncate(window)`）⇒ 两次 Admit 之间一旦跨过 100ms 边界，
	// ★ 第二次落进**新窗口**⇒ 配额已刷新 ⇒ **本该「预算超限」的请求被直接放行** ⇒
	// ★ 断言 `err == nil` 失败。★ 实测复现：串行 3/3 绿；**8 路并行 1/8 红**。
	// ★ 改用 1h 窗口（2026-11-12 批判审计订正：注释原写「1s 窗口」是手抄
	// ★ 漂移——代码实际是 1 * time.Hour，见下一行；窗口远长于测试时长 ⇒
	// ★ 窗口内不会跨界 ⇒ 断言确定）。
	config.Window = 1 * time.Hour
	config.MaxWaitTime = 50 * time.Millisecond
	admission := NewMinuteBucketAdmissionWithConfig(config)

	ctx := context.Background()
	keyID := 3
	limit := 1

	// 使用配额
	_, err := admission.Admit(ctx, keyID, limit)
	if err != nil {
		t.Fatalf("First request failed: %v", err)
	}

	// 设置一个很短的maxWait，应该快速失败
	maxWait := 10 * time.Millisecond
	result, err := admission.AdmitRPMWithBudget(ctx, keyID, limit, maxWait, nil)

	// 应该返回预算超限或队列满
	if err == nil {
		t.Error("Expected an error when budget is exceeded")
	}

	t.Logf("Got error (expected): %v", err)
	t.Logf("Result: admitted=%v, waiting=%v, position=%d, estimatedWait=%ds",
		result.Admitted, result.Waiting, result.Position, result.EstimatedWaitSec)

	if result.Admitted {
		t.Error("Request should not be admitted with short budget")
	}

	// EstimatedWaitSec 应该 > 0（如果请求被排队）或者被拒绝
	if err == ErrQueueBudgetExceeded && result.EstimatedWaitSec == 0 {
		t.Error("EstimatedWaitSec should be populated when budget exceeded")
	}
}

// TestProviderMetrics 测试指标收集
func TestProviderMetrics(t *testing.T) {
	config := DefaultConfig()
	config.EnableMetrics = true
	config.Window = 50 * time.Millisecond
	provider := NewRateLimitProvider(config)

	ctx := context.Background()
	now := time.Now().UTC()
	windowStart := now.Truncate(config.Window)

	key := core.BucketKey{
		Type:       core.BucketTypeRateLimit,
		TenantID:   "tenant1",
		ResourceID: "metrics-test",
		Window: core.TimeWindow{
			Start: windowStart,
			End:   windowStart.Add(config.Window),
			Size:  config.Window,
		},
	}

	query := core.BucketQuery{
		Type:       core.BucketTypeRateLimit,
		TenantID:   "tenant1",
		ResourceID: "metrics-test",
		Window:     key.Window,
		Params: map[string]interface{}{
			"limit": 2,
		},
	}

	// 发送一些请求
	provider.Query(ctx, query)
	provider.Query(ctx, query)
	provider.Query(ctx, query) // 这个应该被拒绝

	// 获取指标
	metrics, err := provider.GetMetrics(ctx)
	if err != nil {
		t.Fatalf("GetMetrics failed: %v", err)
	}

	if metrics.TotalRequests < 3 {
		t.Errorf("Expected at least 3 total requests, got %d", metrics.TotalRequests)
	}

	if metrics.SuccessRequests < 2 {
		t.Errorf("Expected at least 2 successful requests, got %d", metrics.SuccessRequests)
	}

	t.Logf("Metrics: Total=%d, Success=%d, Failed=%d",
		metrics.TotalRequests, metrics.SuccessRequests, metrics.FailedRequests)
}

// TestProviderReset 测试重置功能
func TestProviderReset(t *testing.T) {
	config := DefaultConfig()
	config.Window = 100 * time.Millisecond
	provider := NewRateLimitProvider(config)

	ctx := context.Background()
	now := time.Now().UTC()
	windowStart := now.Truncate(config.Window)

	key := core.BucketKey{
		Type:       core.BucketTypeRateLimit,
		TenantID:   "tenant1",
		ResourceID: "reset-test",
		Window: core.TimeWindow{
			Start: windowStart,
			End:   windowStart.Add(config.Window),
			Size:  config.Window,
		},
	}

	query := core.BucketQuery{
		Type:       core.BucketTypeRateLimit,
		TenantID:   "tenant1",
		ResourceID: "reset-test",
		Window:     key.Window,
		Params: map[string]interface{}{
			"limit": 2,
		},
	}

	// 使用配额
	provider.Query(ctx, query)
	provider.Query(ctx, query)

	// 重置
	err := provider.Reset(ctx, key)
	if err != nil {
		t.Fatalf("Reset failed: %v", err)
	}

	// 重置后应该可以再次请求
	result, err := provider.Query(ctx, query)
	if err != nil {
		t.Fatalf("Query after reset failed: %v", err)
	}
	if admitted, ok := result.Metadata["admitted"].(bool); !ok || !admitted {
		t.Error("Request after reset should be admitted")
	}
}

// mockClock 用于测试的模拟时钟
type mockClock struct {
	mu  sync.Mutex
	now time.Time
}

func (m *mockClock) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now
}

func (m *mockClock) Advance(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = m.now.Add(d)
}

// Benchmark tests

func BenchmarkRateLimitProviderAdmit(b *testing.B) {
	config := DefaultConfig()
	provider := NewRateLimitProvider(config)

	ctx := context.Background()
	now := time.Now().UTC()
	windowStart := now.Truncate(config.Window)

	query := core.BucketQuery{
		Type:       core.BucketTypeRateLimit,
		TenantID:   "tenant1",
		ResourceID: "bench",
		Window: core.TimeWindow{
			Start: windowStart,
			End:   windowStart.Add(config.Window),
			Size:  config.Window,
		},
		Params: map[string]interface{}{
			"limit": 1000000, // 大限制，避免被限流
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		provider.Query(ctx, query)
	}
}

func BenchmarkMinuteBucketAdmissionAdmit(b *testing.B) {
	admission := NewMinuteBucketAdmission()
	ctx := context.Background()
	limit := 1000000 // 大限制，避免被限流

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		keyID := i % 100 // 100个不同的key
		admission.Admit(ctx, keyID, limit)
	}
}

func BenchmarkConcurrentAdmit(b *testing.B) {
	config := DefaultConfig()
	provider := NewRateLimitProvider(config)

	ctx := context.Background()
	now := time.Now().UTC()
	windowStart := now.Truncate(config.Window)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			keyID := fmt.Sprintf("key-%d", i%10)
			query := core.BucketQuery{
				Type:       core.BucketTypeRateLimit,
				TenantID:   "tenant1",
				ResourceID: keyID,
				Window: core.TimeWindow{
					Start: windowStart,
					End:   windowStart.Add(config.Window),
					Size:  config.Window,
				},
				Params: map[string]interface{}{
					"limit": 1000,
				},
			}
			provider.Query(ctx, query)
			i++
		}
	})
}
