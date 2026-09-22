package bg

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestBaseWorkerStartStopIdempotent 验证 Start/Stop 多次调用安全。
func TestBaseWorkerStartStopIdempotent(t *testing.T) {
	b := NewBaseWorker("test")
	var runs int32
	started := b.Start(context.Background(), func(ctx context.Context) {
		atomic.AddInt32(&runs, 1)
		<-ctx.Done()
		b.NotifyStopped()
	})
	if !started {
		t.Fatal("first Start must succeed")
	}
	// 第二次 Start 应被拒绝（已 started）
	if b.Start(context.Background(), func(ctx context.Context) {}) {
		t.Fatal("second Start must be rejected")
	}
	// 让 goroutine 有机会退出
	time.Sleep(20 * time.Millisecond)
	b.Stop()
	b.Stop() // 重复 Stop 安全
	if atomic.LoadInt32(&runs) != 1 {
		t.Fatalf("run count = %d, want 1", runs)
	}
	if !b.Stopped() {
		t.Fatal("Stopped() must report true after Stop()")
	}
}

// TestBaseWorkerStartAfterStopRejected 验证已 Stop 后不能再 Start。
func TestBaseWorkerStartAfterStopRejected(t *testing.T) {
	b := NewBaseWorker("test")
	_ = b.Start(context.Background(), func(ctx context.Context) {
		<-ctx.Done()
		b.NotifyStopped()
	})
	b.Stop()
	if b.Start(context.Background(), func(ctx context.Context) {}) {
		t.Fatal("Start after Stop must be rejected")
	}
}

// TestBaseWorkerStopBeforeStartSafe 验证未启动时 Stop 安全。
func TestBaseWorkerStopBeforeStartSafe(t *testing.T) {
	b := NewBaseWorker("test")
	b.Stop() // 未 Start 直接 Stop
	if b.Stopped() {
		t.Fatal("Stop() without Start must not flip Stopped flag")
	}
}

// TestBaseWorkerConcurrentStartStop 验证高并发 Start/Stop 不会 panic。
func TestBaseWorkerConcurrentStartStop(t *testing.T) {
	b := NewBaseWorker("test")
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = b.Start(context.Background(), func(ctx context.Context) {
				<-ctx.Done()
				b.NotifyStopped()
			})
			b.Stop()
		}()
	}
	wg.Wait()
}

// TestBaseWorkerCancelPropagates 验证 Stop 调用后 ctx 被 cancel，runFn 收到信号。
func TestBaseWorkerCancelPropagates(t *testing.T) {
	b := NewBaseWorker("test")
	canceled := make(chan struct{})
	_ = b.Start(context.Background(), func(ctx context.Context) {
		defer b.NotifyStopped()
		<-ctx.Done()
		close(canceled)
	})
	time.Sleep(10 * time.Millisecond) // 等待 goroutine 启动
	b.Stop()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("runFn did not observe cancel")
	}
}

// TestBaseWorkerNilReceiverSafe 验证 nil receiver 上调用安全（不 panic）。
func TestBaseWorkerNilReceiverSafe(t *testing.T) {
	var b *BaseWorker
	if b.Start(context.Background(), func(ctx context.Context) {}) {
		t.Fatal("nil Start must return false")
	}
	b.Stop()          // 不 panic
	b.NotifyStopped() // 不 panic
	if b.Started() || b.Stopped() {
		t.Fatal("nil status must be false")
	}
	if b.Name() != "" {
		t.Fatal("nil Name must be empty")
	}
}

// TestBaseWorkerRunFnError 验证 runFn 即使 return error 也正常退出（BaseWorker 不关心返回值）。
func TestBaseWorkerRunFnError(t *testing.T) {
	b := NewBaseWorker("test")
	_ = b.Start(context.Background(), func(ctx context.Context) {
		defer b.NotifyStopped()
		_ = errors.New("test error from runFn")
	})
	stopDone := make(chan struct{})
	go func() {
		b.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("Stop did not return even though runFn exited")
	}
}

// TestBaseWorkerStartedRace 验证两个并发 Start 中只有一个胜出。
func TestBaseWorkerStartedRace(t *testing.T) {
	b := NewBaseWorker("test")
	var winners int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.Start(context.Background(), func(ctx context.Context) {
				<-ctx.Done()
				b.NotifyStopped()
			}) {
				atomic.AddInt32(&winners, 1)
			}
		}()
	}
	wg.Wait()
	if atomic.LoadInt32(&winners) != 1 {
		t.Fatalf("winners = %d, want exactly 1", winners)
	}
	b.Stop()
}

// ── R53 / R51-F13: 自愈重启（supervise 循环）钉桩 ──────────────────────────

// shrinkRestartBackoff 把退避参数收缩到测试尺度，返回恢复函数。
func shrinkRestartBackoff(t *testing.T, d time.Duration) func() {
	t.Helper()
	oldInit, oldMax := workerRestartBackoff, workerRestartMaxBackoff
	workerRestartBackoff = d
	workerRestartMaxBackoff = d * 2
	return func() {
		workerRestartBackoff, workerRestartMaxBackoff = oldInit, oldMax
	}
}

// TestBaseWorkerPanicRestarts 运行一次即 panic 的 runFn 必须被自愈重启：
// 前两次 panic、第三次正常长跑，最终 runs=3、Restarts()=2、Stop 正常返回。
func TestBaseWorkerPanicRestarts(t *testing.T) {
	restore := shrinkRestartBackoff(t, time.Millisecond)
	defer restore()

	b := NewBaseWorker("panic-restart-test")
	var runs int32
	started := b.Start(context.Background(), func(ctx context.Context) {
		n := atomic.AddInt32(&runs, 1)
		if n <= 2 {
			panic("boom")
		}
		<-ctx.Done() // 第三次起正常长跑直到 Stop
	})
	if !started {
		t.Fatal("Start must succeed")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&runs) < 3 {
		time.Sleep(2 * time.Millisecond)
	}
	if atomic.LoadInt32(&runs) != 3 {
		t.Fatalf("runs = %d, want 3 (two panics + healthy run)", runs)
	}
	if got := b.Restarts(); got != 2 {
		t.Fatalf("Restarts() = %d, want 2", got)
	}
	b.Stop()
	b.Stop() // 重复 Stop 安全
}

// TestBaseWorkerNormalReturnNotRestarted 验证 runFn 正常 return 不触发重启
// （有意退出语义）。
func TestBaseWorkerNormalReturnNotRestarted(t *testing.T) {
	restore := shrinkRestartBackoff(t, time.Millisecond)
	defer restore()

	b := NewBaseWorker("normal-return-test")
	var runs int32
	_ = b.Start(context.Background(), func(ctx context.Context) {
		atomic.AddInt32(&runs, 1)
		// 立即正常返回：监督循环必须随之终止，不得重启。
	})
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&runs); got != 1 {
		t.Fatalf("runs = %d, want 1 (normal return must not restart)", got)
	}
	if got := b.Restarts(); got != 0 {
		t.Fatalf("Restarts() = %d, want 0", got)
	}
	// done 已关闭：Stop 立即返回。
	done := make(chan struct{})
	go func() { b.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop blocked after normal runFn return")
	}
}

// TestBaseWorkerStopDuringBackoffUnblocks 验证 panic 退避睡眠期间调用 Stop
// 能立即解除阻塞（不会等满退避窗口）。
func TestBaseWorkerStopDuringBackoffUnblocks(t *testing.T) {
	restore := shrinkRestartBackoff(t, 50*time.Millisecond)
	defer restore()

	b := NewBaseWorker("stop-during-backoff-test")
	_ = b.Start(context.Background(), func(ctx context.Context) {
		panic("always")
	})
	time.Sleep(20 * time.Millisecond) // 进入第一次 panic → 退避睡眠
	start := time.Now()
	b.Stop()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Stop took %v during backoff sleep; must unblock via ctx cancel", elapsed)
	}
	if !b.Stopped() {
		t.Fatal("Stopped() must be true")
	}
}
