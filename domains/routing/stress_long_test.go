//go:build routing_long

package routing

// 本文件包含 routing 包的长时间压力测试。默认被构建标签排除（避免普通
// `go test ./...` 超时），需要主动触发时使用：
//
//	go test -tags routing_long ./domains/routing/... -run <TestName> -v
//
// 设计说明：60s/30s/30s/20s 四个测试串行总时长 ≈ 140s，远超 Makefile 默认
// 300s 全量测试的安全余量下限，因此隔离到独立构建标签。如需在常规 CI 跑全套，
// 请使用 `make test-short`（与普通 `make test` 行为一致时，本文件依旧不参与）。

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestRouting_LongRunningStressTest 路由模块长时间运行压力测试
func TestRouting_LongRunningStressTest(t *testing.T) {
	r := NewRoundRobinRouter()

	// 100个候选节点
	candidates := make([]*Candidate, 100)
	for i := range candidates {
		candidates[i] = &Candidate{
			CredentialID: string(rune('A'+(i%26))) + string(rune('0'+(i/26))),
			Provider:     "openai",
			Model:        "gpt-4",
		}
	}
	ctx := Context{Candidates: candidates}

	// 记录初始状态
	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)
	initialGoroutines := runtime.NumGoroutine()

	// 运行参数
	const (
		duration   = 60 * time.Second // 运行60秒
		goroutines = 100              // 100个并发goroutine
	)

	var (
		totalRequests atomic.Uint64
		errors        atomic.Uint64
		stopFlag      atomic.Bool
	)

	// 启动工作goroutine
	var wg sync.WaitGroup
	wg.Add(goroutines)

	start := time.Now()
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for !stopFlag.Load() {
				decision, err := r.Route(ctx)
				if err != nil {
					errors.Add(1)
					continue
				}
				if decision == nil || decision.Selected == nil {
					errors.Add(1)
					continue
				}
				totalRequests.Add(1)
			}
		}()
	}

	// 监控goroutine（每5秒报告一次）
	stopMonitor := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				elapsed := time.Since(start)
				requests := totalRequests.Load()
				qps := float64(requests) / elapsed.Seconds()
				currentGoroutines := runtime.NumGoroutine()

				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				allocMB := float64(m.Alloc) / 1024 / 1024

				t.Logf("[%.0fs] Requests=%d, QPS=%.0f, Goroutines=%d, Mem=%.2f MB",
					elapsed.Seconds(), requests, qps, currentGoroutines, allocMB)
			case <-stopMonitor:
				return
			}
		}
	}()

	// 运行指定时间
	time.Sleep(duration)
	stopFlag.Store(true)
	wg.Wait()
	close(stopMonitor)

	elapsed := time.Since(start)

	// 等待异步操作完成
	time.Sleep(1 * time.Second)

	// 记录最终状态
	runtime.GC()
	var m2 runtime.MemStats
	runtime.ReadMemStats(&m2)
	finalGoroutines := runtime.NumGoroutine()

	// 统计
	requests := totalRequests.Load()
	errs := errors.Load()
	avgQPS := float64(requests) / elapsed.Seconds()

	allocDiff := int64(m2.Alloc) - int64(m1.Alloc)
	allocMB := float64(allocDiff) / 1024 / 1024

	goroutineLeak := finalGoroutines - initialGoroutines

	// 报告
	t.Logf("\n=== 长时间压力测试结果 ===")
	t.Logf("运行时间: %v", duration)
	t.Logf("并发数: %d goroutines", goroutines)
	t.Logf("总请求: %d", requests)
	t.Logf("错误数: %d", errs)
	t.Logf("平均QPS: %.0f", avgQPS)
	t.Logf("初始内存: %.2f MB", float64(m1.Alloc)/1024/1024)
	t.Logf("最终内存: %.2f MB", float64(m2.Alloc)/1024/1024)
	t.Logf("内存增长: %.2f MB", allocMB)
	t.Logf("初始Goroutines: %d", initialGoroutines)
	t.Logf("最终Goroutines: %d", finalGoroutines)
	t.Logf("Goroutine泄漏: %d", goroutineLeak)

	// 验证
	if errs > 0 {
		t.Errorf("出现错误: %d", errs)
	}

	// 内存增长应该 < 50MB（60秒高负载）
	if allocMB > 50 {
		t.Errorf("可能存在内存泄漏: 增长 %.2f MB > 50 MB", allocMB)
	}

	// Goroutine应该没有泄漏（允许±5的误差）
	if goroutineLeak > 5 {
		t.Errorf("可能存在goroutine泄漏: %d > 5", goroutineLeak)
	}

	// QPS应该 > 100K
	if avgQPS < 100000 {
		t.Errorf("QPS过低: %.0f < 100K", avgQPS)
	}
}

// TestRouting_MemoryStressWithFluctuatingLoad 波动负载下的内存压力测试
func TestRouting_MemoryStressWithFluctuatingLoad(t *testing.T) {
	r := NewRoundRobinRouter()

	candidates := make([]*Candidate, 50)
	for i := range candidates {
		candidates[i] = &Candidate{
			CredentialID: string(rune('A' + i)),
			Provider:     "openai",
		}
	}
	ctx := Context{Candidates: candidates}

	// 记录初始内存
	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)

	// 模拟波动负载：10个周期，每个周期3秒
	const cycles = 10
	var totalRequests uint64

	for cycle := 0; cycle < cycles; cycle++ {
		// 高负载（1秒，100 goroutines）
		var wg sync.WaitGroup
		wg.Add(100)

		for g := 0; g < 100; g++ {
			go func() {
				defer wg.Done()
				deadline := time.Now().Add(1 * time.Second)
				for time.Now().Before(deadline) {
					_, _ = r.Route(ctx)
					atomic.AddUint64(&totalRequests, 1)
				}
			}()
		}
		wg.Wait()

		// 低负载（2秒，10 goroutines）
		wg.Add(10)
		for g := 0; g < 10; g++ {
			go func() {
				defer wg.Done()
				deadline := time.Now().Add(2 * time.Second)
				for time.Now().Before(deadline) {
					_, _ = r.Route(ctx)
					atomic.AddUint64(&totalRequests, 1)
					time.Sleep(10 * time.Millisecond)
				}
			}()
		}
		wg.Wait()

		// 每个周期后检查内存
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		allocMB := float64(m.Alloc) / 1024 / 1024

		t.Logf("Cycle %d: Mem=%.2f MB, Requests=%d", cycle+1, allocMB, totalRequests)
	}

	// 最终检查
	runtime.GC()
	var m2 runtime.MemStats
	runtime.ReadMemStats(&m2)

	allocDiff := int64(m2.Alloc) - int64(m1.Alloc)
	allocMB := float64(allocDiff) / 1024 / 1024

	t.Logf("\n=== 波动负载测试结果 ===")
	t.Logf("周期数: %d", cycles)
	t.Logf("总请求: %d", totalRequests)
	t.Logf("初始内存: %.2f MB", float64(m1.Alloc)/1024/1024)
	t.Logf("最终内存: %.2f MB", float64(m2.Alloc)/1024/1024)
	t.Logf("内存增长: %.2f MB", allocMB)

	// 波动负载下内存增长应该 < 20MB
	if allocMB > 20 {
		t.Errorf("可能存在内存泄漏: 增长 %.2f MB > 20 MB", allocMB)
	}
}

// TestRouting_StressTestWithRandomFailures 随机故障下的压力测试
func TestRouting_StressTestWithRandomFailures(t *testing.T) {
	r := NewRoundRobinRouter()

	// 初始10个候选
	candidates := make([]*Candidate, 10)
	for i := range candidates {
		candidates[i] = &Candidate{
			CredentialID: string(rune('A' + i)),
			Provider:     "openai",
		}
	}

	var (
		currentCandidates atomic.Value
		totalRequests     atomic.Uint64
		errors            atomic.Uint64
		stopFlag          atomic.Bool
	)

	currentCandidates.Store(candidates)

	// 工作goroutine
	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for !stopFlag.Load() {
				cands := currentCandidates.Load().([]*Candidate)
				ctx := Context{Candidates: cands}

				decision, err := r.Route(ctx)
				if err != nil || decision == nil {
					errors.Add(1)
				} else {
					totalRequests.Add(1)
				}
			}
		}()
	}

	// 故障注入goroutine（模拟节点随机故障）
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for !stopFlag.Load() {
			<-ticker.C

			// 随机移除2-4个节点
			cands := currentCandidates.Load().([]*Candidate)
			numRemove := 2 + (len(cands) % 3)
			if numRemove > len(cands)-2 {
				numRemove = len(cands) - 2
			}

			newCands := make([]*Candidate, len(cands)-numRemove)
			copy(newCands, cands[numRemove:])
			currentCandidates.Store(newCands)

			t.Logf("模拟故障: 移除%d个节点, 剩余%d个", numRemove, len(newCands))

			// 1秒后恢复
			time.Sleep(1 * time.Second)
			currentCandidates.Store(candidates)
			t.Logf("恢复: 恢复到%d个节点", len(candidates))
		}
	}()

	// 运行30秒
	time.Sleep(30 * time.Second)
	stopFlag.Store(true)
	wg.Wait()

	// 统计
	requests := totalRequests.Load()
	errs := errors.Load()
	successRate := float64(requests) / float64(requests+errs) * 100

	t.Logf("\n=== 随机故障压力测试结果 ===")
	t.Logf("总请求: %d", requests)
	t.Logf("错误数: %d", errs)
	t.Logf("成功率: %.2f%%", successRate)

	// 成功率应该 > 95%（故障期间少量错误可接受）
	if successRate < 95 {
		t.Errorf("成功率过低: %.2f%% < 95%%", successRate)
	}
}

// TestSticky_LongRunningWithCachePressure Sticky路由缓存压力测试
func TestSticky_LongRunningWithCachePressure(t *testing.T) {
	fallback := NewRoundRobinRouter()
	r := NewStickyRouter(fallback)

	candidates := make([]*Candidate, 20)
	for i := range candidates {
		candidates[i] = &Candidate{
			CredentialID: string(rune('A' + i)),
			Provider:     "openai",
		}
	}

	// 记录初始内存
	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)

	// 模拟100个不同的preferred_credential，轮换
	const numPreferred = 100
	var totalRequests atomic.Uint64
	var stopFlag atomic.Bool

	var wg sync.WaitGroup
	wg.Add(50)

	for g := 0; g < 50; g++ {
		go func(id int) {
			defer wg.Done()
			for !stopFlag.Load() {
				// 轮换preferred
				preferred := string(rune('A' + ((id + int(totalRequests.Load()/1000)) % 20)))

				ctx := Context{
					Candidates: candidates,
					Metadata:   map[string]any{"preferred_credential": preferred},
				}

				_, _ = r.Route(ctx)
				totalRequests.Add(1)
			}
		}(g)
	}

	// 运行20秒
	time.Sleep(20 * time.Second)
	stopFlag.Store(true)
	wg.Wait()

	// 最终内存
	runtime.GC()
	var m2 runtime.MemStats
	runtime.ReadMemStats(&m2)

	allocDiff := int64(m2.Alloc) - int64(m1.Alloc)
	allocMB := float64(allocDiff) / 1024 / 1024

	requests := totalRequests.Load()

	t.Logf("\n=== Sticky缓存压力测试结果 ===")
	t.Logf("总请求: %d", requests)
	t.Logf("内存增长: %.2f MB", allocMB)

	// Sticky路由无缓存，内存增长应该很小
	if allocMB > 10 {
		t.Errorf("内存增长过多: %.2f MB > 10 MB", allocMB)
	}
}