package security_engine

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/security"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// HighPressureMetrics 高压测试指标
type HighPressureMetrics struct {
	TotalRequests    int64
	SuccessCount     int64
	FailureCount     int64
	TotalDurationSec float64
	AvgLatencyMs     float64
	ThroughputPerSec float64
	GoroutineStart   int
	GoroutineEnd     int
	MemoryStartMB    float64
	MemoryPeakMB     float64
	MemoryEndMB      float64
	MemoryLeakMB     float64
}

// TestSecurityEngine_HighPressure 高压测试（500万次请求）
func TestSecurityEngine_HighPressure(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过高压测试（使用 -short 标志）")
	}

	totalRequests := int64(5000000) // 500万次请求
	concurrency := 500              // 500并发
	requestsPerGoroutine := int(totalRequests / int64(concurrency))

	t.Logf("========== 高压测试 ==========")
	t.Logf("总请求数: %d", totalRequests)
	t.Logf("并发数: %d", concurrency)
	t.Logf("每个goroutine请求数: %d", requestsPerGoroutine)

	// 记录初始状态
	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	memoryStartMB := float64(memStats.Alloc) / 1024 / 1024
	goroutineStart := runtime.NumGoroutine()

	t.Logf("初始状态: Goroutines=%d, Memory=%.2f MB", goroutineStart, memoryStartMB)

	hook := security.NewSecurityHook(settings.Global)
	testCases := GetTestDataset()

	var metrics HighPressureMetrics
	metrics.TotalRequests = totalRequests
	metrics.GoroutineStart = goroutineStart
	metrics.MemoryStartMB = memoryStartMB
	metrics.MemoryPeakMB = memoryStartMB

	var wg sync.WaitGroup
	var totalLatency int64

	startTime := time.Now()

	// 启动并发goroutine
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for j := 0; j < requestsPerGoroutine; j++ {
				tc := testCases[(workerID*requestsPerGoroutine+j)%len(testCases)]

				reqStart := time.Now()
				ctx := context.Background()
				env := domain.NewRequestEnvelope(ctx, nil)
				env.Metadata = map[string]any{"user_content": tc.Content}

				err := hook.Execute(ctx, env)
				latency := time.Since(reqStart).Milliseconds()
				atomic.AddInt64(&totalLatency, latency)

				if err == nil || (err != nil && tc.ExpectedAllow == false) {
					atomic.AddInt64(&metrics.SuccessCount, 1)
				} else {
					atomic.AddInt64(&metrics.FailureCount, 1)
				}
			}
		}(i)
	}

	// 监控内存峰值（异步）
	stopMonitor := make(chan bool)
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				var mem runtime.MemStats
				runtime.ReadMemStats(&mem)
				memMB := float64(mem.Alloc) / 1024 / 1024
				if memMB > metrics.MemoryPeakMB {
					metrics.MemoryPeakMB = memMB
				}
			case <-stopMonitor:
				return
			}
		}
	}()

	wg.Wait()
	stopMonitor <- true

	totalDuration := time.Since(startTime)
	metrics.TotalDurationSec = totalDuration.Seconds()
	metrics.AvgLatencyMs = float64(totalLatency) / float64(totalRequests)
	metrics.ThroughputPerSec = float64(totalRequests) / totalDuration.Seconds()

	// 最终内存检查
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	runtime.ReadMemStats(&memStats)
	metrics.MemoryEndMB = float64(memStats.Alloc) / 1024 / 1024
	metrics.GoroutineEnd = runtime.NumGoroutine()
	metrics.MemoryLeakMB = metrics.MemoryEndMB - metrics.MemoryStartMB

	// 打印结果
	t.Logf("\n========== 高压测试结果 ==========")
	t.Logf("总请求数: %d", metrics.TotalRequests)
	t.Logf("成功: %d (%.2f%%)", metrics.SuccessCount, float64(metrics.SuccessCount)/float64(metrics.TotalRequests)*100)
	t.Logf("失败: %d (%.2f%%)", metrics.FailureCount, float64(metrics.FailureCount)/float64(metrics.TotalRequests)*100)
	t.Logf("总耗时: %.2f 秒", metrics.TotalDurationSec)
	t.Logf("平均延迟: %.4f ms", metrics.AvgLatencyMs)
	t.Logf("吞吐量: %.2f req/s", metrics.ThroughputPerSec)
	t.Logf("\n资源检查:")
	t.Logf("  Goroutines (开始): %d", metrics.GoroutineStart)
	t.Logf("  Goroutines (结束): %d", metrics.GoroutineEnd)
	t.Logf("  Goroutine 泄漏: %d", metrics.GoroutineEnd-metrics.GoroutineStart)
	t.Logf("  内存 (开始): %.2f MB", metrics.MemoryStartMB)
	t.Logf("  内存 (峰值): %.2f MB", metrics.MemoryPeakMB)
	t.Logf("  内存 (结束): %.2f MB", metrics.MemoryEndMB)
	t.Logf("  内存增长: %.2f MB", metrics.MemoryLeakMB)

	// 验证
	if metrics.GoroutineEnd-metrics.GoroutineStart > 10 {
		t.Errorf("⚠️  检测到 Goroutine 泄漏: %d", metrics.GoroutineEnd-metrics.GoroutineStart)
	} else {
		t.Logf("✓ 无 Goroutine 泄漏")
	}

	if metrics.MemoryLeakMB > 100 {
		t.Errorf("⚠️  可能存在内存泄漏: %.2f MB", metrics.MemoryLeakMB)
	} else {
		t.Logf("✓ 内存增长可控: %.2f MB", metrics.MemoryLeakMB)
	}

	if float64(metrics.FailureCount)/float64(metrics.TotalRequests)*100 > 10 {
		t.Errorf("⚠️  错误率过高: %.2f%%", float64(metrics.FailureCount)/float64(metrics.TotalRequests)*100)
	} else {
		t.Logf("✓ 错误率可接受: %.2f%%", float64(metrics.FailureCount)/float64(metrics.TotalRequests)*100)
	}
}

// TestSecurityEngine_ExtremeConcurrency 极限并发测试（1000并发）
func TestSecurityEngine_ExtremeConcurrency(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过极限并发测试（使用 -short 标志）")
	}

	concurrency := 1000 // 1000并发
	requestsPerGoroutine := 100
	totalRequests := int64(concurrency * requestsPerGoroutine)

	t.Logf("========== 极限并发测试 ==========")
	t.Logf("并发数: %d", concurrency)
	t.Logf("每个goroutine请求数: %d", requestsPerGoroutine)
	t.Logf("总请求数: %d", totalRequests)

	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	memoryStartMB := float64(memStats.Alloc) / 1024 / 1024
	goroutineStart := runtime.NumGoroutine()

	t.Logf("初始状态: Goroutines=%d, Memory=%.2f MB", goroutineStart, memoryStartMB)

	hook := security.NewSecurityHook(settings.Global)
	testCases := GetTestDataset()

	var successCount, failureCount int64
	var wg sync.WaitGroup

	startTime := time.Now()

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for j := 0; j < requestsPerGoroutine; j++ {
				tc := testCases[(workerID*requestsPerGoroutine+j)%len(testCases)]

				ctx := context.Background()
				env := domain.NewRequestEnvelope(ctx, nil)
				env.Metadata = map[string]any{"user_content": tc.Content}

				err := hook.Execute(ctx, env)

				if err == nil || (err != nil && tc.ExpectedAllow == false) {
					atomic.AddInt64(&successCount, 1)
				} else {
					atomic.AddInt64(&failureCount, 1)
				}
			}
		}(i)
	}

	wg.Wait()
	totalDuration := time.Since(startTime)

	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	runtime.ReadMemStats(&memStats)
	memoryEndMB := float64(memStats.Alloc) / 1024 / 1024
	goroutineEnd := runtime.NumGoroutine()

	t.Logf("\n========== 极限并发测试结果 ==========")
	t.Logf("总请求数: %d", totalRequests)
	t.Logf("成功: %d (%.2f%%)", successCount, float64(successCount)/float64(totalRequests)*100)
	t.Logf("失败: %d (%.2f%%)", failureCount, float64(failureCount)/float64(totalRequests)*100)
	t.Logf("总耗时: %s", totalDuration)
	t.Logf("吞吐量: %.2f req/s", float64(totalRequests)/totalDuration.Seconds())
	t.Logf("\n资源检查:")
	t.Logf("  Goroutines: %d → %d (泄漏: %d)", goroutineStart, goroutineEnd, goroutineEnd-goroutineStart)
	t.Logf("  内存: %.2f MB → %.2f MB (增长: %.2f MB)", memoryStartMB, memoryEndMB, memoryEndMB-memoryStartMB)

	if goroutineEnd-goroutineStart > 20 {
		t.Errorf("⚠️  检测到 Goroutine 泄漏")
	} else {
		t.Logf("✓ 无 Goroutine 泄漏")
	}

	if memoryEndMB-memoryStartMB > 100 {
		t.Errorf("⚠️  可能存在内存泄漏")
	} else {
		t.Logf("✓ 内存增长可控")
	}
}

// TestSecurityEngine_MultiRoundMemoryLeak 多轮内存泄漏检测
func TestSecurityEngine_MultiRoundMemoryLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过多轮内存泄漏测试（使用 -short 标志）")
	}

	t.Log("========== 多轮内存泄漏检测 ==========")

	hook := security.NewSecurityHook(settings.Global)
	testCases := GetTestDataset()

	// 预热
	for i := 0; i < 1000; i++ {
		tc := testCases[i%len(testCases)]
		ctx := context.Background()
		env := domain.NewRequestEnvelope(ctx, nil)
		env.Metadata = map[string]any{"user_content": tc.Content}
		_ = hook.Execute(ctx, env)
	}

	runtime.GC()
	time.Sleep(200 * time.Millisecond)
	var memStatsStart runtime.MemStats
	runtime.ReadMemStats(&memStatsStart)
	memoryStartMB := float64(memStatsStart.Alloc) / 1024 / 1024

	t.Logf("基线内存: %.2f MB", memoryStartMB)

	// 执行30轮，每轮10000次请求
	rounds := 30
	requestsPerRound := 10000
	memoryReadings := []float64{}

	for round := 0; round < rounds; round++ {
		for i := 0; i < requestsPerRound; i++ {
			tc := testCases[i%len(testCases)]
			ctx := context.Background()
			env := domain.NewRequestEnvelope(ctx, nil)
			env.Metadata = map[string]any{"user_content": tc.Content}
			_ = hook.Execute(ctx, env)
		}

		runtime.GC()
		time.Sleep(50 * time.Millisecond)

		var memStats runtime.MemStats
		runtime.ReadMemStats(&memStats)
		memoryMB := float64(memStats.Alloc) / 1024 / 1024
		memoryReadings = append(memoryReadings, memoryMB)

		growthMB := memoryMB - memoryStartMB

		if (round+1)%5 == 0 {
			t.Logf("轮次 %d/%d: 内存=%.2f MB, 增长=%.2f MB",
				round+1, rounds, memoryMB, growthMB)
		}

		if growthMB > 150 {
			t.Errorf("⚠️  内存持续增长: %.2f MB，可能存在内存泄漏", growthMB)
			break
		}
	}

	runtime.GC()
	time.Sleep(200 * time.Millisecond)
	var memStatsEnd runtime.MemStats
	runtime.ReadMemStats(&memStatsEnd)
	memoryEndMB := float64(memStatsEnd.Alloc) / 1024 / 1024
	totalGrowthMB := memoryEndMB - memoryStartMB

	// 计算内存增长趋势
	avgGrowth := totalGrowthMB / float64(rounds)

	t.Logf("\n========== 多轮内存泄漏检测结果 ==========")
	t.Logf("初始内存: %.2f MB", memoryStartMB)
	t.Logf("最终内存: %.2f MB", memoryEndMB)
	t.Logf("总增长: %.2f MB", totalGrowthMB)
	t.Logf("平均每轮增长: %.4f MB", avgGrowth)
	t.Logf("总请求数: %d", rounds*requestsPerRound)

	if totalGrowthMB < 50 {
		t.Logf("✓ 内存增长可控，无明显泄漏")
	} else if totalGrowthMB < 100 {
		t.Logf("⚠️  内存增长较高，建议进一步分析")
	} else {
		t.Errorf("❌ 检测到严重内存泄漏: %.2f MB", totalGrowthMB)
	}
}
