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

// ConcurrencyTestMetrics 并发测试指标
type ConcurrencyTestMetrics struct {
	TotalRequests    int64
	SuccessCount     int64
	FailureCount     int64
	TotalDurationMs  int64
	AvgLatencyMs     float64
	MinLatencyMs     int64
	MaxLatencyMs     int64
	ThroughputPerSec float64
	GoroutineStart   int
	GoroutineEnd     int
	GoroutineLeak    int
	MemoryStartMB    float64
	MemoryEndMB      float64
	MemoryLeakMB     float64
	ErrorRate        float64
}

// TestSecurityEngine_Concurrency 并发压力测试
func TestSecurityEngine_Concurrency(t *testing.T) {
	// 测试配置
	concurrency := 100          // 并发数
	requestsPerGoroutine := 100 // 每个goroutine的请求数
	totalRequests := int64(concurrency * requestsPerGoroutine)

	t.Logf("========== 并发压力测试 ==========")
	t.Logf("并发数: %d", concurrency)
	t.Logf("每个goroutine请求数: %d", requestsPerGoroutine)
	t.Logf("总请求数: %d", totalRequests)

	// 记录初始状态
	runtime.GC() // 强制GC，获取准确的内存基线
	time.Sleep(100 * time.Millisecond)

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	memoryStartMB := float64(memStats.Alloc) / 1024 / 1024
	goroutineStart := runtime.NumGoroutine()

	t.Logf("初始状态: Goroutines=%d, Memory=%.2f MB", goroutineStart, memoryStartMB)

	// 初始化安全检测引擎
	hook := security.NewSecurityHook(settings.Global)

	// 准备测试数据（复用GetTestDataset）
	testCases := GetTestDataset()
	t.Logf("测试数据集大小: %d", len(testCases))

	// 并发测试指标
	var metrics ConcurrencyTestMetrics
	metrics.TotalRequests = totalRequests
	metrics.GoroutineStart = goroutineStart
	metrics.MemoryStartMB = memoryStartMB
	metrics.MinLatencyMs = 999999

	var wg sync.WaitGroup
	var mu sync.Mutex
	latencies := []int64{}

	startTime := time.Now()

	// 启动并发goroutine
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for j := 0; j < requestsPerGoroutine; j++ {
				// 循环使用测试用例
				tc := testCases[(workerID*requestsPerGoroutine+j)%len(testCases)]

				reqStart := time.Now()

				// 执行安全检测
				ctx := context.Background()
				env := domain.NewRequestEnvelope(ctx, nil)
				env.Metadata = map[string]any{
					"user_content": tc.Content,
				}

				err := hook.Execute(ctx, env)
				latency := time.Since(reqStart).Milliseconds()

				// 记录延迟
				mu.Lock()
				latencies = append(latencies, latency)
				if latency < metrics.MinLatencyMs {
					metrics.MinLatencyMs = latency
				}
				if latency > metrics.MaxLatencyMs {
					metrics.MaxLatencyMs = latency
				}
				mu.Unlock()

				// 统计成功/失败
				if err == nil || (err != nil && tc.ExpectedAllow == false) {
					atomic.AddInt64(&metrics.SuccessCount, 1)
				} else {
					atomic.AddInt64(&metrics.FailureCount, 1)
				}
			}
		}(i)
	}

	// 等待所有goroutine完成
	wg.Wait()
	totalDuration := time.Since(startTime)
	metrics.TotalDurationMs = totalDuration.Milliseconds()

	// 计算延迟统计
	var sumLatency int64
	for _, lat := range latencies {
		sumLatency += lat
	}
	metrics.AvgLatencyMs = float64(sumLatency) / float64(len(latencies))
	metrics.ThroughputPerSec = float64(totalRequests) / totalDuration.Seconds()
	metrics.ErrorRate = float64(metrics.FailureCount) / float64(totalRequests) * 100

	// 强制GC并检查泄漏
	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	runtime.ReadMemStats(&memStats)
	metrics.MemoryEndMB = float64(memStats.Alloc) / 1024 / 1024
	metrics.GoroutineEnd = runtime.NumGoroutine()
	metrics.GoroutineLeak = metrics.GoroutineEnd - metrics.GoroutineStart
	metrics.MemoryLeakMB = metrics.MemoryEndMB - metrics.MemoryStartMB

	// 打印结果
	printConcurrencyMetrics(t, &metrics)

	// 验证无泄漏
	if metrics.GoroutineLeak > 5 { // 允许5个goroutine的误差
		t.Errorf("⚠️  检测到 Goroutine 泄漏: %d (开始=%d, 结束=%d)",
			metrics.GoroutineLeak, metrics.GoroutineStart, metrics.GoroutineEnd)
	} else {
		t.Logf("✓ 无 Goroutine 泄漏")
	}

	if metrics.MemoryLeakMB > 50 { // 允许50MB的内存增长
		t.Errorf("⚠️  可能存在内存泄漏: %.2f MB (开始=%.2f MB, 结束=%.2f MB)",
			metrics.MemoryLeakMB, metrics.MemoryStartMB, metrics.MemoryEndMB)
	} else {
		t.Logf("✓ 内存增长可控: %.2f MB", metrics.MemoryLeakMB)
	}

	if metrics.ErrorRate > 10 { // 错误率不超过10%
		t.Errorf("⚠️  错误率过高: %.2f%%", metrics.ErrorRate)
	} else {
		t.Logf("✓ 错误率可接受: %.2f%%", metrics.ErrorRate)
	}
}

// TestSecurityEngine_StressTest 稳定性压测（长时间运行）
func TestSecurityEngine_StressTest(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过长时间压测（使用 -short 标志）")
	}

	iterations := 1000 // 1000次迭代
	concurrency := 50  // 每次50并发
	requestsPerGoroutine := 20

	t.Logf("========== 稳定性压测 ==========")
	t.Logf("迭代次数: %d", iterations)
	t.Logf("每次并发: %d", concurrency)
	t.Logf("总请求数: %d", iterations*concurrency*requestsPerGoroutine)

	hook := security.NewSecurityHook(settings.Global)
	testCases := GetTestDataset()

	var totalSuccess int64
	var totalFailure int64
	var totalLatency int64
	var maxMemoryMB float64

	startTime := time.Now()

	for iter := 0; iter < iterations; iter++ {
		var wg sync.WaitGroup
		var iterSuccess int64
		var iterFailure int64

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
						atomic.AddInt64(&iterSuccess, 1)
					} else {
						atomic.AddInt64(&iterFailure, 1)
					}
				}
			}(i)
		}

		wg.Wait()
		atomic.AddInt64(&totalSuccess, iterSuccess)
		atomic.AddInt64(&totalFailure, iterFailure)

		// 每100次迭代检查内存
		if (iter+1)%100 == 0 {
			runtime.GC()
			var memStats runtime.MemStats
			runtime.ReadMemStats(&memStats)
			memoryMB := float64(memStats.Alloc) / 1024 / 1024
			if memoryMB > maxMemoryMB {
				maxMemoryMB = memoryMB
			}
			t.Logf("迭代 %d/%d: 成功=%d, 失败=%d, 内存=%.2f MB",
				iter+1, iterations, iterSuccess, iterFailure, memoryMB)
		}
	}

	totalDuration := time.Since(startTime)
	totalRequests := totalSuccess + totalFailure
	avgLatencyMs := float64(totalLatency) / float64(totalRequests)
	throughput := float64(totalRequests) / totalDuration.Seconds()

	t.Logf("\n========== 稳定性压测结果 ==========")
	t.Logf("总请求数: %d", totalRequests)
	t.Logf("成功: %d (%.2f%%)", totalSuccess, float64(totalSuccess)/float64(totalRequests)*100)
	t.Logf("失败: %d (%.2f%%)", totalFailure, float64(totalFailure)/float64(totalRequests)*100)
	t.Logf("总耗时: %s", totalDuration)
	t.Logf("平均延迟: %.2f ms", avgLatencyMs)
	t.Logf("吞吐量: %.2f req/s", throughput)
	t.Logf("峰值内存: %.2f MB", maxMemoryMB)
	t.Logf("✓ 稳定性测试通过 - %d 次迭代无崩溃", iterations)
}

// TestSecurityEngine_MemoryLeak 内存泄漏专项测试
func TestSecurityEngine_MemoryLeak(t *testing.T) {
	t.Log("========== 内存泄漏检测 ==========")

	// 预热
	hook := security.NewSecurityHook(settings.Global)
	testCases := GetTestDataset()

	for i := 0; i < 100; i++ {
		tc := testCases[i%len(testCases)]
		ctx := context.Background()
		env := domain.NewRequestEnvelope(ctx, nil)
		env.Metadata = map[string]any{"user_content": tc.Content}
		_ = hook.Execute(ctx, env)
	}

	// 强制GC，获取基线
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	var memStatsStart runtime.MemStats
	runtime.ReadMemStats(&memStatsStart)
	memoryStartMB := float64(memStatsStart.Alloc) / 1024 / 1024

	t.Logf("基线内存: %.2f MB", memoryStartMB)

	// 执行10000次请求
	rounds := 10
	requestsPerRound := 1000

	for round := 0; round < rounds; round++ {
		for i := 0; i < requestsPerRound; i++ {
			tc := testCases[i%len(testCases)]
			ctx := context.Background()
			env := domain.NewRequestEnvelope(ctx, nil)
			env.Metadata = map[string]any{"user_content": tc.Content}
			_ = hook.Execute(ctx, env)
		}

		// 每轮强制GC并检查内存
		runtime.GC()
		time.Sleep(50 * time.Millisecond)

		var memStats runtime.MemStats
		runtime.ReadMemStats(&memStats)
		memoryMB := float64(memStats.Alloc) / 1024 / 1024
		growthMB := memoryMB - memoryStartMB

		t.Logf("轮次 %d/%d: 内存=%.2f MB, 增长=%.2f MB",
			round+1, rounds, memoryMB, growthMB)

		// 如果增长超过100MB，可能有泄漏
		if growthMB > 100 {
			t.Errorf("⚠️  内存持续增长: %.2f MB，可能存在内存泄漏", growthMB)
			break
		}
	}

	// 最终检查
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	var memStatsEnd runtime.MemStats
	runtime.ReadMemStats(&memStatsEnd)
	memoryEndMB := float64(memStatsEnd.Alloc) / 1024 / 1024
	totalGrowthMB := memoryEndMB - memoryStartMB

	t.Logf("\n========== 内存泄漏检测结果 ==========")
	t.Logf("初始内存: %.2f MB", memoryStartMB)
	t.Logf("最终内存: %.2f MB", memoryEndMB)
	t.Logf("总增长: %.2f MB", totalGrowthMB)
	t.Logf("总请求数: %d", rounds*requestsPerRound)

	if totalGrowthMB < 50 {
		t.Logf("✓ 内存增长可控，无明显泄漏")
	} else if totalGrowthMB < 100 {
		t.Logf("⚠️  内存增长较高，建议进一步分析")
	} else {
		t.Errorf("❌ 检测到严重内存泄漏: %.2f MB", totalGrowthMB)
	}
}

// printConcurrencyMetrics 打印并发测试指标
func printConcurrencyMetrics(t *testing.T, m *ConcurrencyTestMetrics) {
	t.Log("\n========== 并发压力测试结果 ==========")
	t.Logf("总请求数: %d", m.TotalRequests)
	t.Logf("成功: %d (%.2f%%)", m.SuccessCount, float64(m.SuccessCount)/float64(m.TotalRequests)*100)
	t.Logf("失败: %d (%.2f%%)", m.FailureCount, m.ErrorRate)
	t.Logf("总耗时: %d ms", m.TotalDurationMs)
	t.Log("\n性能指标:")
	t.Logf("  平均延迟: %.2f ms", m.AvgLatencyMs)
	t.Logf("  最小延迟: %d ms", m.MinLatencyMs)
	t.Logf("  最大延迟: %d ms", m.MaxLatencyMs)
	t.Logf("  吞吐量: %.2f req/s", m.ThroughputPerSec)
	t.Log("\n资源检查:")
	t.Logf("  Goroutine (开始): %d", m.GoroutineStart)
	t.Logf("  Goroutine (结束): %d", m.GoroutineEnd)
	t.Logf("  Goroutine 泄漏: %d", m.GoroutineLeak)
	t.Logf("  内存 (开始): %.2f MB", m.MemoryStartMB)
	t.Logf("  内存 (结束): %.2f MB", m.MemoryEndMB)
	t.Logf("  内存增长: %.2f MB", m.MemoryLeakMB)
}

// BenchmarkSecurityEngine 基准测试
func BenchmarkSecurityEngine(b *testing.B) {
	hook := security.NewSecurityHook(settings.Global)
	testCases := GetTestDataset()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tc := testCases[i%len(testCases)]
		ctx := context.Background()
		env := domain.NewRequestEnvelope(ctx, nil)
		env.Metadata = map[string]any{"user_content": tc.Content}
		_ = hook.Execute(ctx, env)
	}
}

// BenchmarkSecurityEngine_Parallel 并行基准测试
func BenchmarkSecurityEngine_Parallel(b *testing.B) {
	hook := security.NewSecurityHook(settings.Global)
	testCases := GetTestDataset()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			tc := testCases[i%len(testCases)]
			ctx := context.Background()
			env := domain.NewRequestEnvelope(ctx, nil)
			env.Metadata = map[string]any{"user_content": tc.Content}
			_ = hook.Execute(ctx, env)
			i++
		}
	})
}
