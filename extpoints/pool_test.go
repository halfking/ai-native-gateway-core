package extpoints

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestExecutionContextPool 验证对象池基本功能。
func TestExecutionContextPool(t *testing.T) {
	// 获取一个 context
	ctx := AcquireExecutionContext()
	if ctx == nil {
		t.Fatal("AcquireExecutionContext returned nil")
	}
	
	// 验证初始状态
	if ctx.State == nil {
		t.Error("ExecutionContext.State should be initialized")
	}
	
	// 设置一些字段
	ctx.Phase = PhaseAuth
	ctx.PluginID = "test-plugin"
	ctx.RequestID = "req-123"
	ctx.State["key1"] = "value1"
	ctx.State["key2"] = 42
	
	// 归还到池中
	ReleaseExecutionContext(ctx)
	
	// 验证字段被清空
	if ctx.Phase != "" {
		t.Errorf("Phase should be cleared, got %s", ctx.Phase)
	}
	if ctx.PluginID != "" {
		t.Errorf("PluginID should be cleared, got %s", ctx.PluginID)
	}
	if ctx.RequestID != "" {
		t.Errorf("RequestID should be cleared, got %s", ctx.RequestID)
	}
	if len(ctx.State) != 0 {
		t.Errorf("State should be cleared, got %d keys", len(ctx.State))
	}
}

// TestExecutionContextPoolReuse 验证对象池复用。
func TestExecutionContextPoolReuse(t *testing.T) {
	// 获取第一个 context
	ctx1 := AcquireExecutionContext()
	ctx1.RequestID = "req-1"
	ptr1 := ctx1
	
	// 归还
	ReleaseExecutionContext(ctx1)
	
	// 再次获取（应该复用同一个实例）
	ctx2 := AcquireExecutionContext()
	ptr2 := ctx2
	
	// 验证是同一个对象被复用
	if ptr1 != ptr2 {
		t.Log("Warning: Pool might not be reusing objects immediately (this is non-deterministic)")
	}
	
	// 验证被正确清空
	if ctx2.RequestID != "" {
		t.Errorf("Reused context should be clean, got RequestID=%s", ctx2.RequestID)
	}
	
	ReleaseExecutionContext(ctx2)
}

// TestExecutionContextPoolConcurrent 验证并发安全性。
func TestExecutionContextPoolConcurrent(t *testing.T) {
	const numGoroutines = 100
	const iterations = 1000
	
	var wg sync.WaitGroup
	wg.Add(numGoroutines)
	
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			
			for j := 0; j < iterations; j++ {
				// 获取 context
				ctx := AcquireExecutionContext()
				
				// 使用它
				ctx.Phase = PhaseAuth
				ctx.RequestID = "concurrent-test"
				ctx.State["goroutine_id"] = id
				ctx.State["iteration"] = j
				
				// 验证 State 没有被其他 goroutine 污染
				if gid, ok := ctx.State["goroutine_id"].(int); ok && gid != id {
					t.Errorf("State pollution detected: expected goroutine_id=%d, got %d", id, gid)
				}
				
				// 归还
				ReleaseExecutionContext(ctx)
			}
		}(i)
	}
	
	wg.Wait()
}

// TestExecutionContextReset 验证 Reset 方法。
func TestExecutionContextReset(t *testing.T) {
	ctx := &ExecutionContext{
		Context:     context.Background(),
		Phase:       PhaseAuth,
		PluginID:    "test-plugin",
		State:       make(map[string]interface{}),
		RequestID:   "req-123",
		RequestTime: time.Now(),
		Timeout:     5 * time.Second,
	}
	
	// 添加一些 state
	ctx.State["key1"] = "value1"
	ctx.State["key2"] = 42
	ctx.State["key3"] = true
	
	// 重置
	ctx.Reset()
	
	// 验证所有字段被清空
	if ctx.Context != nil {
		t.Error("Context should be nil after reset")
	}
	if ctx.Phase != "" {
		t.Errorf("Phase should be empty, got %s", ctx.Phase)
	}
	if ctx.PluginID != "" {
		t.Errorf("PluginID should be empty, got %s", ctx.PluginID)
	}
	if ctx.RequestID != "" {
		t.Errorf("RequestID should be empty, got %s", ctx.RequestID)
	}
	if !ctx.RequestTime.IsZero() {
		t.Error("RequestTime should be zero")
	}
	if ctx.Timeout != 0 {
		t.Errorf("Timeout should be 0, got %v", ctx.Timeout)
	}
	if len(ctx.State) != 0 {
		t.Errorf("State should be empty, got %d keys", len(ctx.State))
	}
}

// TestExecutionContextResetLargeMap 验证大 map 的处理策略。
func TestExecutionContextResetLargeMap(t *testing.T) {
	ctx := &ExecutionContext{
		State: make(map[string]interface{}),
	}
	
	// 添加大量键（超过 16 个）
	for i := 0; i < 20; i++ {
		ctx.State[string(rune('a'+i))] = i
	}
	
	if len(ctx.State) != 20 {
		t.Fatalf("Expected 20 keys before reset, got %d", len(ctx.State))
	}
	
	// 重置
	ctx.Reset()
	
	// 验证新 map 为空
	if len(ctx.State) != 0 {
		t.Errorf("Map should be empty after reset, got %d keys", len(ctx.State))
	}
	
	// 验证 map 不为 nil（应该是新分配的）
	if ctx.State == nil {
		t.Error("Map should not be nil after reset")
	}
}

// TestInitExecutionContext 验证初始化函数。
func TestInitExecutionContext(t *testing.T) {
	ctx := AcquireExecutionContext()
	defer ReleaseExecutionContext(ctx)
	
	baseCtx := context.Background()
	phase := PhaseAuth
	pluginID := "test-plugin"
	requestID := "req-123"
	requestTime := time.Now()
	
	InitExecutionContext(ctx, baseCtx, phase, pluginID, requestID, requestTime)
	
	// 验证字段被正确设置
	if ctx.Context != baseCtx {
		t.Error("Context not set correctly")
	}
	if ctx.Phase != phase {
		t.Errorf("Phase not set correctly, got %s", ctx.Phase)
	}
	if ctx.PluginID != pluginID {
		t.Errorf("PluginID not set correctly, got %s", ctx.PluginID)
	}
	if ctx.RequestID != requestID {
		t.Errorf("RequestID not set correctly, got %s", ctx.RequestID)
	}
	if ctx.RequestTime != requestTime {
		t.Error("RequestTime not set correctly")
	}
}

// BenchmarkExecutionContextAllocation 基准测试：直接分配。
func BenchmarkExecutionContextAllocation(b *testing.B) {
	b.ReportAllocs()
	
	for i := 0; i < b.N; i++ {
		ctx := &ExecutionContext{
			Context:     context.Background(),
			Phase:       PhaseAuth,
			PluginID:    "bench-plugin",
			State:       make(map[string]interface{}),
			RequestID:   "req-bench",
			RequestTime: time.Now(),
		}
		
		// 使用 context
		ctx.State["key1"] = "value1"
		ctx.State["key2"] = 42
		
		// 模拟归还（实际场景中会清理）
		_ = ctx
	}
}

// BenchmarkExecutionContextPool 基准测试：使用对象池。
func BenchmarkExecutionContextPool(b *testing.B) {
	b.ReportAllocs()
	
	for i := 0; i < b.N; i++ {
		ctx := AcquireExecutionContext()
		
		InitExecutionContext(ctx, context.Background(), PhaseAuth, "bench-plugin", "req-bench", time.Now())
		
		// 使用 context
		ctx.State["key1"] = "value1"
		ctx.State["key2"] = 42
		
		ReleaseExecutionContext(ctx)
	}
}

// BenchmarkExecutionContextPoolParallel 基准测试：并行使用对象池。
func BenchmarkExecutionContextPoolParallel(b *testing.B) {
	b.ReportAllocs()
	
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			ctx := AcquireExecutionContext()
			
			InitExecutionContext(ctx, context.Background(), PhaseAuth, "bench-plugin", "req-bench", time.Now())
			
			ctx.State["key1"] = "value1"
			ctx.State["key2"] = 42
			
			ReleaseExecutionContext(ctx)
		}
	})
}

// BenchmarkExecutionContextReset 基准测试：Reset 方法性能。
func BenchmarkExecutionContextReset(b *testing.B) {
	ctx := &ExecutionContext{
		Context:     context.Background(),
		Phase:       PhaseAuth,
		PluginID:    "bench-plugin",
		State:       make(map[string]interface{}),
		RequestID:   "req-bench",
		RequestTime: time.Now(),
	}
	
	b.ResetTimer()
	b.ReportAllocs()
	
	for i := 0; i < b.N; i++ {
		// 添加一些数据
		ctx.State["key1"] = "value1"
		ctx.State["key2"] = 42
		ctx.State["key3"] = true
		
		// 重置
		ctx.Reset()
	}
}
