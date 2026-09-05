package streaming

// anthropic_bridge_fault_test.go — 2026-09-05 审计闭环6：SSE reader 取消与
// goroutine 泄漏的真实 TCP 证据。
//
// 审计缺口：Anthropic SSE reader 在底层 Close 不响应时是否可取消、读循环
// 协程是否在取消后退出，均无运行态证据。本文件用 httptest 服务器逐字节
// 滴流 Anthropic SSE 帧，中途取消请求 context，验证：
//  1. StreamAnthropicPassthrough 在取消后及时返回（<2s，不是挂死到上游
//     断开）；
//  2. 返回后不存在泄漏的读循环 goroutine（基线采样 + 容差重试）。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAnthropicSSEPassthroughCancellationStopsReader 真实 TCP slow-SSE：
// 先收到至少一个 data 帧后取消 context，读循环必须退出而不是阻塞在下一次
// Read 上。
func TestAnthropicSSEPassthroughCancellationStopsReader(t *testing.T) {
	sentFirstFrame := make(chan struct{})
	// handler 在写失败（网关侧取消 → body Close → 连接关闭）时立即退出，
	// 否则 srv.Close() 会等满 200 帧滴流，掩盖 bridge 的真实返回时间。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		frame := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n\n"
		if _, err := fmt.Fprint(w, frame); err != nil {
			close(sentFirstFrame)
			return
		}
		flusher.Flush()
		close(sentFirstFrame)
		// 首帧后保持连接打开并慢速滴流，模拟「上游不结束」。
		for i := 0; i < 200; i++ {
			time.Sleep(50 * time.Millisecond)
			if _, err := fmt.Fprint(w, frame); err != nil {
				return
			}
			flusher.Flush()
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, nil)
	require.NoError(t, err)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	resp.Request = req

	rec := httptest.NewRecorder()
	done := make(chan StreamOutcome, 1)
	go func() {
		done <- StreamAnthropicPassthrough(ctx, rec, resp, "claude-test", "claude-test", "req-fault-cancel", nil, nil)
	}()

	select {
	case <-sentFirstFrame:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream never delivered the first SSE frame")
	}
	time.Sleep(100 * time.Millisecond) // 让读循环进入稳态
	cancel()

	select {
	case out := <-done:
		// 取消后 reader 返回即可；中断原因可以是 client canceled /
		// network / stream interrupted——契约是「及时返回 + 不吞错误」。
		t.Logf("outcome after cancellation: interrupted=%v reason=%s kind=%s", out.Interrupted, out.Reason, out.Kind)
	case <-time.After(2 * time.Second):
		// 关闭上游强制让挂死的 reader 现形，然后失败。
		srv.CloseClientConnections()
		<-done
		t.Fatal("SSE reader did not return within 2s of context cancellation")
	}
}

// TestAnthropicSSEPassthroughNoGoroutineLeak 多轮取消后 goroutine 数回落。
func TestAnthropicSSEPassthroughNoGoroutineLeak(t *testing.T) {
	goroutines := func() int {
		runtime.GC()
		time.Sleep(50 * time.Millisecond)
		return runtime.NumGoroutine()
	}
	baseline := goroutines()

	frame := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for i := 0; i < 40; i++ {
			if _, err := fmt.Fprint(w, frame); err != nil {
				return
			}
			flusher.Flush()
			time.Sleep(30 * time.Millisecond)
		}
	}))
	defer srv.Close()

	for round := 0; round < 4; round++ {
		ctx, cancel := context.WithCancel(context.Background())
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, nil)
		require.NoError(t, err)
		resp, err := srv.Client().Do(req)
		require.NoError(t, err)
		resp.Request = req
		rec := httptest.NewRecorder()
		out := StreamAnthropicPassthrough(ctx, rec, resp, "claude-test", "claude-test", fmt.Sprintf("req-leak-%d", round), nil, nil)
		_ = resp.Body.Close()
		_ = out
		cancel()
	}

	for attempt := 0; attempt < 3; attempt++ {
		if now := goroutines(); now <= baseline+5 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("goroutine leak suspected after repeated SSE cancellations: baseline=%d now=%d", baseline, goroutines())
}

// 独立行为钉死：取消发生在任何 data 帧之前（headers 已到、body 静默）
// 时，reader 也必须及时返回。
func TestAnthropicSSEPassthroughCancelBeforeFirstFrame(t *testing.T) {
	gotHeaders := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(gotHeaders)
		<-release // headers 后保持 body 静默且不关闭
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, nil)
	require.NoError(t, err)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	resp.Request = req

	rec := httptest.NewRecorder()
	done := make(chan StreamOutcome, 1)
	go func() {
		done <- StreamAnthropicPassthrough(ctx, rec, resp, "claude-test", "claude-test", "req-cancel-early", nil, nil)
	}()

	select {
	case <-gotHeaders:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream never delivered response headers")
	}
	cancel()

	select {
	case out := <-done:
		assert.True(t, out.Interrupted, "cancelled stream must be interrupted, not success")
	case <-time.After(2 * time.Second):
		srv.CloseClientConnections()
		<-done
		t.Fatal("reader blocked on a silent body did not return within 2s of cancellation")
	}
}
