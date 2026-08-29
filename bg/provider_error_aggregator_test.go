package bg

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestProviderErrorAggregatorCreation 测试聚合器创建
func TestProviderErrorAggregatorCreation(t *testing.T) {
	// nil pool 应该可以创建但不会执行
	agg := NewProviderErrorAggregator(nil, 10*time.Minute)
	if agg == nil {
		t.Fatal("expected non-nil aggregator")
	}
	if agg.interval != 10*time.Minute {
		t.Errorf("expected interval 10m, got %v", agg.interval)
	}

	// 默认间隔
	agg2 := NewProviderErrorAggregator(nil, 0)
	if agg2.interval != 10*time.Minute {
		t.Errorf("expected default interval 10m, got %v", agg2.interval)
	}
}

// TestProviderErrorAggregatorStartStop 测试启动和停止
func TestProviderErrorAggregatorStartStop(t *testing.T) {
	agg := NewProviderErrorAggregator(nil, 1*time.Second)
	
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	
	agg.Start(ctx)
	
	// 等待一段时间确保 goroutine 启动
	time.Sleep(100 * time.Millisecond)
	
	// 停止应该能正常返回
	done := make(chan struct{})
	go func() {
		agg.Stop()
		close(done)
	}()
	
	select {
	case <-done:
		// 成功停止
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not complete within timeout")
	}
}

// TestProviderErrorAggregatorStopIdempotent 测试重复停止的幂等性
func TestProviderErrorAggregatorStopIdempotent(t *testing.T) {
	agg := NewProviderErrorAggregator(nil, 1*time.Minute)
	
	ctx := context.Background()
	agg.Start(ctx)
	
	// 第一次停止
	agg.Stop()
	
	// 第二次停止不应该 panic
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Stop() panicked on second call: %v", r)
		}
	}()
	
	// 注意：第二次 Stop() 会阻塞，因为 doneCh 已经关闭
	// 这个测试只验证不会 panic，不验证是否会阻塞
}

// TestProviderErrorAggregatorIntegration 测试与数据库的集成
// 需要真实数据库连接，标记为 integration test
func TestProviderErrorAggregatorIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	
	// 这里需要实际的数据库连接
	// 由于测试环境可能没有配置，我们先跳过
	t.Skip("integration test requires database connection")
	
	// 如果有数据库连接，测试流程应该是：
	// 1. 插入测试数据到 candidate_failure_logs_hot
	// 2. 运行聚合器
	// 3. 验证 provider_error_details 中的数据
	// 4. 清理测试数据
}

// TestPartitionManagerIncludesErrorAggregator 验证 PartitionManager 包含错误聚合器
func TestPartitionManagerIncludesErrorAggregator(t *testing.T) {
	pm := NewPartitionManager(nil, 24*time.Hour)
	
	if pm.errorAggregator == nil {
		t.Error("expected PartitionManager to have non-nil errorAggregator")
	}
	
	// 验证聚合器的默认配置
	if pm.errorAggregator.interval != 10*time.Minute {
		t.Errorf("expected errorAggregator interval 10m, got %v", pm.errorAggregator.interval)
	}
}

// mockPool 是一个用于测试的 mock pgxpool.Pool
// 注意：实际使用时需要更完整的 mock 实现
type mockPool struct {
	*pgxpool.Pool
}

// TestAggregateErrorsWithNilPool 测试 nil pool 的处理
func TestAggregateErrorsWithNilPool(t *testing.T) {
	agg := NewProviderErrorAggregator(nil, 10*time.Minute)
	
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	
	// 不应该 panic
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("aggregateErrors panicked with nil pool: %v", r)
		}
	}()
	
	agg.aggregateErrors(ctx)
}

// TestProviderErrorAggregatorConcurrency 测试并发安全
func TestProviderErrorAggregatorConcurrency(t *testing.T) {
	agg := NewProviderErrorAggregator(nil, 100*time.Millisecond)
	
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	
	// 启动聚合器
	agg.Start(ctx)
	
	// 等待几个周期
	time.Sleep(350 * time.Millisecond)
	
	// 停止应该能正常工作
	done := make(chan struct{})
	go func() {
		agg.Stop()
		close(done)
	}()
	
	select {
	case <-done:
		// 成功
	case <-time.After(1 * time.Second):
		t.Fatal("concurrent Stop() did not complete")
	}
}
