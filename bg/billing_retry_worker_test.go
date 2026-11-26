package bg

import (
	"context"
	"testing"
	"time"
)

func TestBillingRetryWorker_Creation(t *testing.T) {
	worker := NewBillingRetryWorker(nil, 0, 0)

	if worker == nil {
		t.Fatal("expected worker to be created")
	}

	// 验证默认值
	if worker.interval != 5*time.Minute {
		t.Errorf("expected default interval 5m, got %v", worker.interval)
	}

	if worker.batchSize != 100 {
		t.Errorf("expected default batch size 100, got %d", worker.batchSize)
	}
}

func TestBillingRetryWorker_CustomValues(t *testing.T) {
	customInterval := 2 * time.Minute
	customBatchSize := 50

	worker := NewBillingRetryWorker(nil, customInterval, customBatchSize)

	if worker.interval != customInterval {
		t.Errorf("expected interval %v, got %v", customInterval, worker.interval)
	}

	if worker.batchSize != customBatchSize {
		t.Errorf("expected batch size %d, got %d", customBatchSize, worker.batchSize)
	}
}

func TestBillingRetryWorker_StopBeforeStart(t *testing.T) {
	worker := NewBillingRetryWorker(nil, 1*time.Second, 10)

	// 在启动前调用Stop不应panic
	worker.Stop()
}

func TestBillingRetryWorker_StartAndStop(t *testing.T) {
	worker := NewBillingRetryWorker(nil, 100*time.Millisecond, 10)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 启动worker（在goroutine中）
	go worker.Start(ctx)

	// 等待一小段时间
	time.Sleep(50 * time.Millisecond)

	// 停止worker
	cancel()

	// 等待stopped channel关闭
	select {
	case <-worker.stopped:
		// 成功停止
	case <-time.After(1 * time.Second):
		t.Error("worker did not stop within timeout")
	}
}

func TestBillingRetryWorker_SetStatsLog(t *testing.T) {
	worker := NewBillingRetryWorker(nil, 1*time.Second, 10)

	// 默认启用
	if !worker.statsLog {
		t.Error("expected statsLog to be enabled by default")
	}

	// 禁用
	worker.SetStatsLog(false)
	if worker.statsLog {
		t.Error("expected statsLog to be disabled")
	}

	// 重新启用
	worker.SetStatsLog(true)
	if !worker.statsLog {
		t.Error("expected statsLog to be enabled")
	}
}

// 注意：完整的集成测试需要真实数据库连接和MaaS服务
// 这里仅测试worker的基本行为
