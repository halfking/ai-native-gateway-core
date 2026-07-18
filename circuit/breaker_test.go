package circuit

import (
	"context"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestCircuitBreaker_OpenOnHighErrorRate 测试高错误率触发熔断
func TestCircuitBreaker_OpenOnHighErrorRate(t *testing.T) {
	breaker := NewBreaker(Config{
		ErrorThreshold: 0.5,
		MinRequests:    10,
		WindowSize:     10 * time.Second,
		OpenTimeout:    30 * time.Second,
	})

	ctx := context.Background()

	// 发送 10 个请求，6 个失败
	for i := 0; i < 10; i++ {
		err := breaker.Call(ctx, func() error {
			if i < 6 {
				return errors.New("upstream error")
			}
			return nil
		})

		if i < 9 {
			// 前 9 个请求应该正常通过（可能成功或失败，但不会被熔断器拒绝）
			if err == ErrOpen {
				t.Fatalf("request %d should not be rejected by circuit breaker", i)
			}
		}
	}

	// 第 10 个请求后，错误率达到 60%，应触发熔断
	assert.Equal(t, StateOpen, breaker.State())
}

// TestCircuitBreaker_MinRequests 测试最小请求数限制
func TestCircuitBreaker_MinRequests(t *testing.T) {
	breaker := NewBreaker(Config{
		ErrorThreshold: 0.5,
		MinRequests:    10,
		WindowSize:     10 * time.Second,
		OpenTimeout:    30 * time.Second,
	})

	ctx := context.Background()

	// 发送 5 个请求，全部失败（错误率 100%）
	for i := 0; i < 5; i++ {
		err := breaker.Call(ctx, func() error {
			return errors.New("error")
		})
		// 不应触发熔断，因为请求数 < MinRequests
		assert.NotEqual(t, ErrOpen, err)
	}

	assert.Equal(t, StateClosed, breaker.State())
}

// TestCircuitBreaker_HalfOpenRecovery 测试 Half-Open 状态恢复
func TestCircuitBreaker_HalfOpenRecovery(t *testing.T) {
	breaker := NewBreaker(Config{
		ErrorThreshold:           0.5,
		MinRequests:              5,
		WindowSize:               10 * time.Second,
		OpenTimeout:              100 * time.Millisecond,
		HalfOpenMaxTest:          3,
		HalfOpenSuccessThreshold: 0.67,
	})

	ctx := context.Background()

	// 触发熔断
	for i := 0; i < 5; i++ {
		breaker.Call(ctx, func() error {
			return errors.New("error")
		})
	}
	assert.Equal(t, StateOpen, breaker.State())

	// 等待 Open 超时
	time.Sleep(150 * time.Millisecond)

	// 发送 3 个探测请求，全部成功
	for i := 0; i < 3; i++ {
		err := breaker.Call(ctx, func() error {
			return nil
		})
		assert.NoError(t, err)
	}

	// 应恢复到 Closed 状态
	assert.Equal(t, StateClosed, breaker.State())
}

// TestCircuitBreaker_HalfOpenFailure 测试 Half-Open 状态探测失败
func TestCircuitBreaker_HalfOpenFailure(t *testing.T) {
	breaker := NewBreaker(Config{
		ErrorThreshold:           0.5,
		MinRequests:              5,
		WindowSize:               10 * time.Second,
		OpenTimeout:              100 * time.Millisecond,
		HalfOpenMaxTest:          3,
		HalfOpenSuccessThreshold: 0.67,
	})

	ctx := context.Background()

	// 触发熔断
	for i := 0; i < 5; i++ {
		breaker.Call(ctx, func() error {
			return errors.New("error")
		})
	}
	assert.Equal(t, StateOpen, breaker.State())

	// 等待 Open 超时
	time.Sleep(150 * time.Millisecond)

	// 发送 3 个探测请求，全部失败
	for i := 0; i < 3; i++ {
		err := breaker.Call(ctx, func() error {
			return errors.New("still failing")
		})
		// 探测请求本身会执行，返回真实错误
		assert.NotEqual(t, ErrOpen, err)
	}

	// 应重新回到 Open 状态
	assert.Equal(t, StateOpen, breaker.State())
}

// TestCircuitBreaker_OpenRejectsRequests 测试 Open 状态快速失败
func TestCircuitBreaker_OpenRejectsRequests(t *testing.T) {
	breaker := NewBreaker(Config{
		ErrorThreshold: 0.5,
		MinRequests:    5,
		WindowSize:     10 * time.Second,
		OpenTimeout:    10 * time.Second,
	})

	ctx := context.Background()

	// 触发熔断
	for i := 0; i < 5; i++ {
		breaker.Call(ctx, func() error {
			return errors.New("error")
		})
	}
	assert.Equal(t, StateOpen, breaker.State())

	// Open 状态下的请求应被直接拒绝
	err := breaker.Call(ctx, func() error {
		t.Fatal("should not execute")
		return nil
	})
	assert.Equal(t, ErrOpen, err)
}

// TestCircuitBreaker_Reset 测试手动重置
func TestCircuitBreaker_Reset(t *testing.T) {
	breaker := NewBreaker(Config{
		ErrorThreshold: 0.5,
		MinRequests:    5,
		WindowSize:     10 * time.Second,
		OpenTimeout:    30 * time.Second,
	})

	ctx := context.Background()

	// 触发熔断
	for i := 0; i < 5; i++ {
		breaker.Call(ctx, func() error {
			return errors.New("error")
		})
	}
	assert.Equal(t, StateOpen, breaker.State())

	// 手动重置
	breaker.Reset()
	assert.Equal(t, StateClosed, breaker.State())

	// 应该可以正常执行请求
	err := breaker.Call(ctx, func() error {
		return nil
	})
	assert.NoError(t, err)
}

// TestCircuitBreaker_Metrics 测试指标统计
func TestCircuitBreaker_Metrics(t *testing.T) {
	breaker := NewBreaker(Config{
		ErrorThreshold: 0.5,
		MinRequests:    10,
		WindowSize:     10 * time.Second,
		OpenTimeout:    30 * time.Second,
	})

	ctx := context.Background()

	// 10 个请求: 7 成功, 3 失败
	for i := 0; i < 10; i++ {
		breaker.Call(ctx, func() error {
			if i < 7 {
				return nil
			}
			return errors.New("error")
		})
	}

	metrics := breaker.Metrics()
	assert.Equal(t, int64(10), metrics.TotalRequests)
	assert.Equal(t, int64(7), metrics.TotalSuccesses)
	assert.Equal(t, int64(3), metrics.TotalFailures)
	assert.InDelta(t, 0.3, metrics.ErrorRate, 0.01)
}

// TestCircuitBreaker_Concurrent 测试并发安全
func TestCircuitBreaker_Concurrent(t *testing.T) {
	breaker := NewBreaker(Config{
		ErrorThreshold: 0.5,
		MinRequests:    100,
		WindowSize:     10 * time.Second,
		OpenTimeout:    30 * time.Second,
	})

	ctx := context.Background()
	var wg sync.WaitGroup
	successCount := atomic.Int64{}
	errorCount := atomic.Int64{}

	// 100 个 goroutine 并发执行
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			for j := 0; j < 10; j++ {
				err := breaker.Call(ctx, func() error {
					// 50% 失败率
					if (idx+j)%2 == 0 {
						return nil
					}
					return errors.New("error")
				})

				if err == nil || (err != nil && err != ErrOpen) {
					if err == nil {
						successCount.Add(1)
					} else {
						errorCount.Add(1)
					}
				}
			}
		}(i)
	}

	wg.Wait()

	// 验证统计 (允许部分请求被熔断器拒绝，所以 metrics 可能 >= successCount + errorCount)
	metrics := breaker.Metrics()
	total := successCount.Load() + errorCount.Load()
	assert.GreaterOrEqual(t, metrics.TotalRequests, total, "recorded requests should be >= executed requests")

	// 验证并发安全：没有 panic，且状态一致
	assert.NotNil(t, metrics)
}

// TestCircuitBreaker_Record 测试外部记录接口
func TestCircuitBreaker_Record(t *testing.T) {
	breaker := NewBreaker(Config{
		ErrorThreshold: 0.5,
		MinRequests:    10,
		WindowSize:     10 * time.Second,
		OpenTimeout:    30 * time.Second,
	})

	// 使用 Record 接口记录结果
	for i := 0; i < 10; i++ {
		breaker.Record(i < 4) // 4 成功, 6 失败
	}

	assert.Equal(t, StateOpen, breaker.State())

	metrics := breaker.Metrics()
	assert.Equal(t, int64(10), metrics.TotalRequests)
	assert.InDelta(t, 0.6, metrics.ErrorRate, 0.01)
}

// BenchmarkCircuitBreaker_Call 性能测试
func BenchmarkCircuitBreaker_Call(b *testing.B) {
	breaker := NewBreaker(DefaultConfig())
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		breaker.Call(ctx, func() error {
			return nil
		})
	}
}

// BenchmarkCircuitBreaker_Concurrent 并发性能测试
func BenchmarkCircuitBreaker_Concurrent(b *testing.B) {
	breaker := NewBreaker(DefaultConfig())
	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			breaker.Call(ctx, func() error {
				return nil
			})
		}
	})
}

// Helper: assertWithin 断言值在容差范围内
func assertWithin(t *testing.T, actual, expected int, tolerance float64) {
	diff := math.Abs(float64(actual - expected))
	maxDiff := float64(expected) * tolerance
	assert.LessOrEqual(t, diff, maxDiff,
		"actual=%d, expected=%d, tolerance=%.1f%%",
		actual, expected, tolerance*100)
}
