package routing

import (
	"sync"
	"testing"
	"time"
)

// BenchmarkRoundRobin_SingleNode 测试单节点路由性能
func BenchmarkRoundRobin_SingleNode(b *testing.B) {
	r := NewRoundRobinRouter()
	ctx := Context{
		Candidates: []*Candidate{
			{CredentialID: "c1", Provider: "openai", Model: "gpt-4"},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Route(ctx)
	}
}

// BenchmarkRoundRobin_10Nodes 测试10节点路由性能
func BenchmarkRoundRobin_10Nodes(b *testing.B) {
	r := NewRoundRobinRouter()
	candidates := make([]*Candidate, 10)
	for i := range candidates {
		candidates[i] = &Candidate{
			CredentialID: string(rune('A' + i)),
			Provider:     "openai",
			Model:        "gpt-4",
		}
	}
	ctx := Context{Candidates: candidates}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Route(ctx)
	}
}

// BenchmarkRoundRobin_100Nodes 测试100节点路由性能
func BenchmarkRoundRobin_100Nodes(b *testing.B) {
	r := NewRoundRobinRouter()
	candidates := make([]*Candidate, 100)
	for i := range candidates {
		candidates[i] = &Candidate{
			CredentialID: string(rune(i)),
			Provider:     "openai",
			Model:        "gpt-4",
		}
	}
	ctx := Context{Candidates: candidates}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Route(ctx)
	}
}

// BenchmarkSticky_Hit 测试sticky路由命中性能
func BenchmarkSticky_Hit(b *testing.B) {
	fallback := NewRoundRobinRouter()
	r := NewStickyRouter(fallback)

	ctx := Context{
		Candidates: []*Candidate{
			{CredentialID: "c1", Provider: "openai"},
			{CredentialID: "c2", Provider: "openai"},
			{CredentialID: "c3", Provider: "openai"},
		},
		Metadata: map[string]any{"preferred_credential": "c1"},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Route(ctx)
	}
}

// BenchmarkSticky_Miss 测试sticky路由未命中性能（fallback到RoundRobin）
func BenchmarkSticky_Miss(b *testing.B) {
	fallback := NewRoundRobinRouter()
	r := NewStickyRouter(fallback)

	ctx := Context{
		Candidates: []*Candidate{
			{CredentialID: "c2", Provider: "openai"},
			{CredentialID: "c3", Provider: "openai"},
		},
		Metadata: map[string]any{"preferred_credential": "c1"}, // c1不在候选中
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Route(ctx)
	}
}

// TestRoundRobin_ConcurrentAccess 测试并发访问下的正确性和性能
func TestRoundRobin_ConcurrentAccess(t *testing.T) {
	r := NewRoundRobinRouter()
	candidates := []*Candidate{
		{CredentialID: "c1", Provider: "openai"},
		{CredentialID: "c2", Provider: "openai"},
		{CredentialID: "c3", Provider: "openai"},
		{CredentialID: "c4", Provider: "openai"},
		{CredentialID: "c5", Provider: "openai"},
	}

	const goroutines = 100
	const iterations = 1000

	var wg sync.WaitGroup
	wg.Add(goroutines)

	results := make([][]string, goroutines)
	errors := make([]error, goroutines)

	start := time.Now()
	for g := 0; g < goroutines; g++ {
		g := g
		go func() {
			defer wg.Done()
			results[g] = make([]string, iterations)
			ctx := Context{Candidates: candidates}
			for i := 0; i < iterations; i++ {
				decision, err := r.Route(ctx)
				if err != nil {
					errors[g] = err
					return
				}
				if decision == nil || decision.Selected == nil {
					t.Errorf("goroutine %d iteration %d: nil decision", g, i)
					return
				}
				results[g][i] = decision.Selected.CredentialID
			}
		}()
	}

	wg.Wait()
	elapsed := time.Since(start)

	// 检查错误
	for g, err := range errors {
		if err != nil {
			t.Fatalf("goroutine %d failed: %v", g, err)
		}
	}

	totalRequests := goroutines * iterations
	t.Logf("并发测试: %d goroutines × %d iterations = %d total requests",
		goroutines, iterations, totalRequests)
	t.Logf("耗时: %v", elapsed)
	t.Logf("吞吐量: %.0f req/s", float64(totalRequests)/elapsed.Seconds())

	// 统计分布
	distribution := make(map[string]int)
	for _, res := range results {
		for _, selected := range res {
			distribution[selected]++
		}
	}

	t.Logf("负载分布:")
	for _, cand := range candidates {
		count := distribution[cand.CredentialID]
		percent := float64(count) * 100 / float64(totalRequests)
		t.Logf("  节点 %s: %d 次 (%.2f%%)", cand.CredentialID, count, percent)
	}

	// 验证分布相对均匀（每个节点应该接近20%）
	expectedPerNode := totalRequests / len(candidates)
	tolerance := float64(expectedPerNode) * 0.15 // 允许15%偏差（并发下会有些波动）

	for _, cand := range candidates {
		count := distribution[cand.CredentialID]
		diff := float64(count - expectedPerNode)
		if diff < 0 {
			diff = -diff
		}
		if diff > tolerance {
			t.Errorf("节点 %s 负载不均: got %d, want %d±%.0f",
				cand.CredentialID, count, expectedPerNode, tolerance)
		}
	}
}

// TestRoundRobin_LoadBalancingFairness 测试负载均衡公平性
func TestRoundRobin_LoadBalancingFairness(t *testing.T) {
	tests := []struct {
		name       string
		numNodes   int
		iterations int
	}{
		{"3节点-3000次", 3, 3000},
		{"5节点-10000次", 5, 10000},
		{"10节点-10000次", 10, 10000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRoundRobinRouter()

			// 生成候选
			candidates := make([]*Candidate, tt.numNodes)
			for i := 0; i < tt.numNodes; i++ {
				candidates[i] = &Candidate{
					CredentialID: string(rune('A' + i)),
					Provider:     "openai",
				}
			}

			ctx := Context{Candidates: candidates}
			distribution := make(map[string]int)

			for i := 0; i < tt.iterations; i++ {
				decision, err := r.Route(ctx)
				if err != nil {
					t.Fatalf("iteration %d: %v", i, err)
				}
				if decision == nil || decision.Selected == nil {
					t.Fatalf("iteration %d: nil decision", i)
				}
				distribution[decision.Selected.CredentialID]++
			}

			expectedPerNode := tt.iterations / tt.numNodes

			t.Logf("%s 负载分布:", tt.name)
			var sumSquaredDiff float64
			for _, cand := range candidates {
				count := distribution[cand.CredentialID]
				percent := float64(count) * 100 / float64(tt.iterations)
				expectedPercent := 100.0 / float64(tt.numNodes)
				deviation := percent - expectedPercent
				t.Logf("  节点 %s: %d 次 (%.2f%%, 偏差 %+.2f%%)",
					cand.CredentialID, count, percent, deviation)

				diff := float64(count - expectedPerNode)
				sumSquaredDiff += diff * diff
			}

			stdDev := sumSquaredDiff / float64(tt.numNodes)
			t.Logf("标准差²: %.2f (期望 0，越小越好)", stdDev)

			// RoundRobin应该完全均匀，标准差²应该接近0
			// 理论上stdDev²应该为0，但允许1以内的浮动（整数除法舍入误差）
			if stdDev > 1.0 {
				t.Errorf("负载分布不均匀: 标准差² %.2f > 1.0", stdDev)
			}
		})
	}
}

// TestSticky_ConcurrentSafety 测试sticky路由并发安全性
func TestSticky_ConcurrentSafety(t *testing.T) {
	fallback := NewRoundRobinRouter()
	r := NewStickyRouter(fallback)

	candidates := []*Candidate{
		{CredentialID: "c1", Provider: "openai"},
		{CredentialID: "c2", Provider: "openai"},
		{CredentialID: "c3", Provider: "openai"},
		{CredentialID: "c4", Provider: "openai"},
		{CredentialID: "c5", Provider: "openai"},
	}

	const goroutines = 50
	const iterations = 500

	var wg sync.WaitGroup
	wg.Add(goroutines)

	start := time.Now()
	for g := 0; g < goroutines; g++ {
		preferredID := candidates[g%len(candidates)].CredentialID
		go func(pref string) {
			defer wg.Done()
			ctx := Context{
				Candidates: candidates,
				Metadata:   map[string]any{"preferred_credential": pref},
			}
			for i := 0; i < iterations; i++ {
				decision, err := r.Route(ctx)
				if err != nil {
					t.Errorf("preferred %s iteration %d: %v", pref, i, err)
					return
				}
				if decision == nil || decision.Selected == nil {
					t.Errorf("preferred %s iteration %d: nil decision", pref, i)
					return
				}
				// Sticky应该总是返回preferred（在candidates中）
				if decision.Selected.CredentialID != pref {
					t.Errorf("expected %s, got %s", pref, decision.Selected.CredentialID)
				}
			}
		}(preferredID)
	}

	wg.Wait()
	elapsed := time.Since(start)

	totalRequests := goroutines * iterations
	t.Logf("Sticky并发测试: %d goroutines × %d iterations = %d requests",
		goroutines, iterations, totalRequests)
	t.Logf("耗时: %v", elapsed)
	t.Logf("吞吐量: %.0f req/s", float64(totalRequests)/elapsed.Seconds())
}

// TestRoundRobin_FailoverLatency 测试故障切换延迟
func TestRoundRobin_FailoverLatency(t *testing.T) {
	r := NewRoundRobinRouter()

	// 模拟从5节点变为4节点（一个节点故障）
	originalCandidates := []*Candidate{
		{CredentialID: "c1", Provider: "openai"},
		{CredentialID: "c2", Provider: "openai"},
		{CredentialID: "c3", Provider: "openai"},
		{CredentialID: "c4", Provider: "openai"},
		{CredentialID: "c5", Provider: "openai"},
	}

	// 模拟c3故障被移除
	afterFailover := []*Candidate{
		{CredentialID: "c1", Provider: "openai"},
		{CredentialID: "c2", Provider: "openai"},
		{CredentialID: "c4", Provider: "openai"},
		{CredentialID: "c5", Provider: "openai"},
	}

	// 先用原始候选跑1000次
	ctx := Context{Candidates: originalCandidates}
	for i := 0; i < 1000; i++ {
		_, _ = r.Route(ctx)
	}

	// 测试切换延迟
	start := time.Now()
	ctx.Candidates = afterFailover
	decision, err := r.Route(ctx)
	latency := time.Since(start)

	if err != nil {
		t.Fatalf("failover failed: %v", err)
	}
	if decision == nil || decision.Selected == nil {
		t.Fatal("failover returned nil decision")
	}

	t.Logf("故障切换延迟: %v", latency)
	t.Logf("切换后选中节点: %s", decision.Selected.CredentialID)

	// 验证切换后仍然能正常分配
	distribution := make(map[string]int)
	for i := 0; i < 10000; i++ {
		d, _ := r.Route(ctx)
		distribution[d.Selected.CredentialID]++
	}

	t.Logf("切换后负载分布:")
	for _, cand := range afterFailover {
		count := distribution[cand.CredentialID]
		t.Logf("  节点 %s: %d 次", cand.CredentialID, count)
	}

	// 故障切换延迟应该 < 1ms（纯内存操作）
	if latency > time.Millisecond {
		t.Errorf("故障切换延迟过高: %v > 1ms", latency)
	}
}
