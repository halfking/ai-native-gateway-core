package routing

import (
	"runtime"
	"testing"
	"time"
)

// TestRouting_GoroutineLeakDetection Goroutine泄漏专项检测
func TestRouting_GoroutineLeakDetection(t *testing.T) {
	r := NewRoundRobinRouter()

	candidates := []*Candidate{
		{CredentialID: "c1", Provider: "openai"},
		{CredentialID: "c2", Provider: "openai"},
		{CredentialID: "c3", Provider: "openai"},
	}
	ctx := Context{Candidates: candidates}

	initialGoroutines := runtime.NumGoroutine()
	t.Logf("初始Goroutines: %d", initialGoroutines)

	// 运行1000次操作
	for i := 0; i < 1000; i++ {
		_, _ = r.Route(ctx)
	}

	// 短暂等待
	time.Sleep(100 * time.Millisecond)
	runtime.GC()

	finalGoroutines := runtime.NumGoroutine()
	t.Logf("最终Goroutines: %d", finalGoroutines)

	leak := finalGoroutines - initialGoroutines
	if leak > 0 {
		t.Errorf("检测到goroutine泄漏: %d", leak)
	} else {
		t.Logf("✓ 未检测到goroutine泄漏")
	}
}

// BenchmarkRouting_UnderLoad 高负载下的性能基准
func BenchmarkRouting_UnderLoad(b *testing.B) {
	r := NewRoundRobinRouter()

	candidates := make([]*Candidate, 100)
	for i := range candidates {
		candidates[i] = &Candidate{
			CredentialID: string(rune(i)),
			Provider:     "openai",
		}
	}
	ctx := Context{Candidates: candidates}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = r.Route(ctx)
		}
	})
}
