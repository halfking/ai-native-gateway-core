package mockprobe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/kaixuan/llm-gateway-go/internal/observability"
	"github.com/kaixuan/llm-gateway-go/internal/providers/mock"
	"github.com/kaixuan/llm-gateway-go/internal/shutdown"
)

func counter(supplier string, stream, ok bool) float64 {
	streamLabel := "false"
	if stream {
		streamLabel = "true"
	}
	status := "error"
	if ok {
		status = "ok"
	}
	return testutil.ToFloat64(observability.MockProbeRequestTotal.WithLabelValues(
		observability.ScopeMockProbe, supplier, streamLabel, status))
}

// waitFor 轮询条件直至超时（探测是异步调度，断言需等待）。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

// TestRunnerRounds2x2：interval 周期内每轮恰好 4 次探测（fast×2 +
// slow×2），指标按通道累计（验收：每 MockProbeInterval 跑 4 次）。
func TestRunnerRounds2x2(t *testing.T) {
	srv := newMockGateway(t)
	defer srv.Close()

	runner := NewRunner(NewClient(srv.URL), 120*time.Millisecond, 3, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !runner.Start(ctx) {
		t.Fatal("runner failed to start")
	}
	defer runner.Stop(context.Background())

	// 等至少 2 轮（t=0 立即一轮 + ticker 一轮）。一轮含 2 个 slow 探测
	// （各 600-1000ms），约 1.2-2s/轮，预算 8s 覆盖 CI 抖动。
	if !waitFor(t, 8*time.Second, func() bool {
		return counter(mock.CodeFast, false, true) >= 2 &&
			counter(mock.CodeFast, true, true) >= 2 &&
			counter(mock.CodeSlow, false, true) >= 2 &&
			counter(mock.CodeSlow, true, true) >= 2
	}) {
		t.Fatalf("expected >=2 ok probes per channel, got fast/ns=%v fast/s=%v slow/ns=%v slow/s=%v",
			counter(mock.CodeFast, false, true), counter(mock.CodeFast, true, true),
			counter(mock.CodeSlow, false, true), counter(mock.CodeSlow, true, true))
	}
	if runner.FailureCount() != 0 {
		t.Fatalf("healthy gateway should have zero failures, got %d", runner.FailureCount())
	}
}

// TestRunnerFailureStreakAndAlert：网关故障时失败累计、连续失败 streak
// 达阈值、失败计数递增；恢复后 streak 归零。
func TestRunnerFailureStreakAndAlert(t *testing.T) {
	// 立即 500 的坏网关。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	runner := NewRunner(NewClient(srv.URL), 60*time.Millisecond, 3, nil, nil)
	if !runner.Start(context.Background()) {
		t.Fatal("runner failed to start")
	}
	defer runner.Stop(context.Background())

	if !waitFor(t, 3*time.Second, func() bool {
		return runner.FailureCount() >= 3*4 // 3 轮 × 4 通道
	}) {
		t.Fatalf("failures should accumulate, got %d", runner.FailureCount())
	}
	if counter(mock.CodeFast, false, false) < 3 {
		t.Fatalf("error counter should track failures: %v", counter(mock.CodeFast, false, false))
	}

	// streak 语义（通道独立、成功清零）：用独立合成通道验证，避免与
	// 上面真实探测轮的 streak 状态耦合。
	const ch = "test:channel"
	for i := 1; i <= 3; i++ {
		if got := runner.trackStreak(ch, false); got != i {
			t.Fatalf("failure %d: streak = %d, want %d", i, got, i)
		}
	}
	if got := runner.trackStreak(ch, true); got != 0 {
		t.Fatalf("success should reset streak, got %d", got)
	}
}

// TestRunnerStopIdempotent：Stop 幂等、可重复调用、goroutine 退出。
func TestRunnerStopIdempotent(t *testing.T) {
	srv := newMockGateway(t)
	defer srv.Close()

	runner := NewRunner(NewClient(srv.URL), 50*time.Millisecond, 3, nil, nil)
	if !runner.Start(context.Background()) {
		t.Fatal("runner failed to start")
	}
	time.Sleep(80 * time.Millisecond)
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		runner.Stop(ctx)
		cancel()
	}
	// 再 Start 不应复活（started 标记）。
	if runner.Start(context.Background()) {
		t.Fatal("stopped runner must not restart")
	}
}

// TestRunnerShutdownManagerIntegration：注册为 NonStream；Shutdown 后
// 新 runner 拒绝启动（Register 返回 false 路径）。
func TestRunnerShutdownManagerIntegration(t *testing.T) {
	srv := newMockGateway(t)
	defer srv.Close()

	mgr := shutdown.NewManager()
	runner := NewRunner(NewClient(srv.URL), time.Hour, 3, nil, mgr)
	if !runner.Start(context.Background()) {
		t.Fatal("runner failed to start")
	}
	if snap := mgr.Snapshot(); snap.NonStreams != 1 {
		t.Fatalf("runner should register as NonStream, snapshot=%+v", snap)
	}

	// 关闭流程：mgr 进入 started → 新 runner 不能注册/启动。
	done := make(chan struct{})
	go func() {
		mgr.Shutdown(context.Background(), 2*time.Second, 2*time.Second)
		close(done)
	}()
	<-done
	runner2 := NewRunner(NewClient(srv.URL), time.Hour, 3, nil, mgr)
	if runner2.Start(context.Background()) {
		t.Fatal("runner must refuse to start after shutdown began")
	}

	// 原 runner Stop 后从 mgr 注销。
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	runner.Stop(ctx)
	cancel()
	if snap := mgr.Snapshot(); snap.NonStreams != 0 {
		t.Fatalf("runner should unregister on Stop, snapshot=%+v", snap)
	}
}

// TestRunnerWithoutManager：mgr=nil（无停机框架装配）时正常启停。
func TestRunnerWithoutManager(t *testing.T) {
	srv := newMockGateway(t)
	defer srv.Close()
	runner := NewRunner(NewClient(srv.URL), 80*time.Millisecond, 0, nil, nil) // threshold 0 → 3
	if !runner.Start(context.Background()) {
		t.Fatal("runner failed to start")
	}
	if !waitFor(t, time.Second, func() bool { return counter(mock.CodeFast, false, true) > 0 }) {
		t.Fatal("runner without manager should still probe")
	}
	runner.Stop(context.Background())
}
