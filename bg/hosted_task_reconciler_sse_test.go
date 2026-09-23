package bg

import (
	"bufio"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hostedtask"
)

// slogTest 返回测试 logger，规避 import conflict。
func slogTest() *slog.Logger { return slog.Default() }

// TestEnsureStreamSurvivesPassBudgetContextCancel 是 R62 审计发现的回归钉桩：
// R36 给 reconciler.tick() 加了 passCtx 预算后，passCtx 被传入 ensureStream。
// ensureStream 内 streamCtx = context.WithCancel(ctx) 以 passCtx 为 parent，
// cancelPass() 每 tick 级联取消所有 SSE 长连接——SSE 流存活不过一个 tick。
//
// 本测试用一个持有连接的假 SSE server：
//   - 连接 handler 阻塞在 r.Context().Done()；
//   - cancelPass() 后流必须仍存活（不级联取消）；
//   - workerCtx cancel 后流必须退出。
//
// 注：第一个 TestEnsureStreamCtxIsPinnedToWorkerNotPassCtx 已覆盖核心钉桩。
// 本测试是早期 R62 调查阶段的二次确认——两个测试都要在 main 上保留，便于
// 后续审计轮直接拿 R62 关键不变量做回归检查。
func TestEnsureStreamSurvivesPassBudgetContextCancel(t *testing.T) {
	sseEntered := make(chan struct{}, 1)
	accSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		flusher.Flush()
		select {
		case sseEntered <- struct{}{}:
		default:
		}
		// 阻塞等 ctx cancel；只有 worker 取消（不是 passCtx cancelPass）才
		// 能让这里退出 —— 这正是 R62 要钉的不变量。
		<-r.Context().Done()
	}))
	defer accSrv.Close()

	r := &HostedTaskReconciler{
		BaseWorker: NewBaseWorker("sse-ctx-pinning-test"),
		acc:        hostedtask.NewACCClient(accSrv.URL, "tok", "rt-1", accSrv.Client()),
		interval:   50 * time.Millisecond,
		logger:     slogTest(),
		streams:    map[string]*streamHandle{},
	}
	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()
	r.workerCtx = workerCtx

	// 模拟 R36 tick()：两次连续 cancelPass()。如果 streamCtx 被级联取消，
	// streams 会被清空；但修复后 streamCtx parent 是 workerCtx，cancelPass
	// 不应杀掉流。
	task := hostedtask.Task{ID: "ht_x", TenantID: "tenant-A", AccRunID: "run-1", Status: hostedtask.StatusRunning}
	for i := 0; i < 2; i++ {
		passCtx, cancelPass := context.WithTimeout(workerCtx, 30*time.Second)
		// tick2 时同 runID 已被注册，ensureStream 应直接返回（streams map 不变）。
		r.ensureStream(passCtx, task)
		cancelPass()
		time.Sleep(50 * time.Millisecond)
	}

	// 等连接建起。
	select {
	case <-sseEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("SSE server never observed the connection")
	}

	// 验证 1：两次 cancelPass 后，streams map 仍保留 handle（修复前会因
	// 第一次 cancelPass 把 streamCtx 取消 → stream goroutine 退出 → defer
	// delete streams map，然后 tick2 再 ensureStream 会注册新流）。
	r.mu.Lock()
	streamCount := len(r.streams)
	r.mu.Unlock()
	if streamCount != 1 {
		t.Errorf("after 2x cancelPass, expected exactly 1 SSE stream (no reconnect), got %d", streamCount)
	}

	// 验证 2：workerCtx 取消后必须退出。
	workerCancel()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		n := len(r.streams)
		r.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.mu.Lock()
	leftN := len(r.streams)
	r.mu.Unlock()
	t.Fatalf("workerCtx cancel must drain streams, but %d remain", leftN)
}

// TestEnsureStreamCtxIsPinnedToWorkerNotPassCtx 钉桩核心不变量：
// 一次 ensureStream 后，外部 cancel 传参 ctx（模拟 R36 passCtx cancelPass）
// 不应影响 streamCtx 状态；只有 workerCtx 取消才能让 stream goroutine 退出。
//
// 用法：用 httptest 起一个"接受 SSE 连接后立刻让 r.Context() 阻塞退出"的
// 假 server。store 留 nil——确保 SSE 重连分支不会触发（GetTask nil deref 是
// 测试产物，与 R62 无关）。
func TestEnsureStreamCtxIsPinnedToWorkerNotPassCtx(t *testing.T) {
	sseEntered := make(chan struct{}, 1)
	sseExit := make(chan struct{})
	accSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		flusher.Flush()
		select {
		case sseEntered <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		close(sseExit)
	}))
	defer accSrv.Close()

	r := &HostedTaskReconciler{
		BaseWorker: NewBaseWorker("sse-ctx-pinning-test2"),
		acc:        hostedtask.NewACCClient(accSrv.URL, "tok", "rt-1", accSrv.Client()),
		interval:   50 * time.Millisecond,
		logger:     slogTest(),
		streams:    map[string]*streamHandle{},
	}
	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()
	r.workerCtx = workerCtx

	passCtx, cancelPass := context.WithTimeout(workerCtx, 30*time.Second)
	defer cancelPass()
	task := hostedtask.Task{ID: "ht_x", TenantID: "tenant-A", AccRunID: "run-1", Status: hostedtask.StatusRunning}
	r.ensureStream(passCtx, task)

	// 等 SSE server 进入 r.Context().Done() 等待（即连接建好）。
	select {
	case <-sseEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("SSE server never observed the connection (StreamEvents did not call our handler)")
	}

	// R62 修复前：cancelPass() 级联取消 streamCtx；R62 修复后：streamCtx
	// 仍存活（parent 是 workerCtx）。
	cancelPass()
	time.Sleep(200 * time.Millisecond)

	r.mu.Lock()
	n := len(r.streams)
	streamAlive := n == 1
	r.mu.Unlock()

	// 验证 1：stream 仍存活（cancelPass 不应级联）。
	if !streamAlive {
		t.Fatal("cancelPass() must NOT cancel the SSE stream (R62 regression: stream ctx was inherited from passCtx)")
	}

	// 验证 2：workerCtx 取消后 stream 必须退出 —— streamHandle 从 streams
	// map 移除。
	workerCancel()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		n := len(r.streams)
		r.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.mu.Lock()
	leftN := len(r.streams)
	r.mu.Unlock()
	t.Fatalf("workerCtx cancel must drain streams, but %d remain", leftN)
}

// bufio 导红一次（保留的辅助引用，确保 imports 不被清理；R62 调试用）
var _ = (*bufio.Reader)(nil)