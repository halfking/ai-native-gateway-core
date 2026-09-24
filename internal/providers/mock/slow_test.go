package mock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// slowLatencyBounds：800ms±200ms 加调度/编码开销；CI 抖动余量后取
// [500ms, 2000ms] 为验收带（分布本身由 slowDelay 单测钉死）。
var slowLatencyBounds = [2]time.Duration{500 * time.Millisecond, 2000 * time.Millisecond}

func within(elapsed time.Duration, lo, hi time.Duration) bool {
	return elapsed >= lo && elapsed <= hi
}

// TestSlowDelayDistribution：抖动函数必须落在 [600ms, 1000ms]（base±200）。
func TestSlowDelayDistribution(t *testing.T) {
	for i := 0; i < 200; i++ {
		d := slowDelay()
		if d < 600*time.Millisecond || d > 1000*time.Millisecond {
			t.Fatalf("slowDelay out of [600ms,1000ms]: %v", d)
		}
	}
}

// TestSlowNonStream：延迟落在验收带、响应形状正确、id 带 mock 标记。
func TestSlowNonStream(t *testing.T) {
	start := time.Now()
	rec := serve(ChatCompletionsSlow(),
		`{"model":"gpt-4o","messages":[{"role":"user","content":"slow probe"}]}`, nil)
	elapsed := time.Since(start)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !within(elapsed, slowLatencyBounds[0], slowLatencyBounds[1]) {
		t.Fatalf("slow latency %v outside %v..%v", elapsed, slowLatencyBounds[0], slowLatencyBounds[1])
	}
	body := mustJSON(t, rec)
	if body["object"] != "chat.completion" {
		t.Fatalf("object = %v", body["object"])
	}
	content := body["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
	if !strings.Contains(content, "[mock-slow]") {
		t.Fatalf("reply should tag mock-slow: %q", content)
	}
}

// TestSlowStream：首字节延迟受 slow 抖动支配、SSE 完整（delta + [DONE]）。
func TestSlowStream(t *testing.T) {
	start := time.Now()
	rec := serve(ChatCompletionsSlow(),
		`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"slow stream"}]}`, nil)
	elapsed := time.Since(start)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !within(elapsed, slowLatencyBounds[0], slowLatencyBounds[1]) {
		t.Fatalf("slow stream latency %v outside bounds", elapsed)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "\"delta\"") || !strings.Contains(out, "[DONE]") {
		t.Fatalf("slow stream body invalid: %q", out)
	}
}

// TestSlowAnthropic：messages 协议同样吃 slow 延迟。
func TestSlowAnthropic(t *testing.T) {
	start := time.Now()
	rec := serve(MessagesSlow(), `{"model":"claude-3","messages":[{"role":"user","content":"hi"}]}`, nil)
	elapsed := time.Since(start)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !within(elapsed, slowLatencyBounds[0], slowLatencyBounds[1]) {
		t.Fatalf("slow anthropic latency %v outside bounds", elapsed)
	}
	if !strings.Contains(rec.Body.String(), "[mock-slow]") {
		t.Fatalf("body should tag mock-slow: %s", rec.Body.String())
	}
}

// TestSlowCtxAbort：客户端断连（ctx 取消）时 slow 睡眠立即中止，handler
// 及时返回（防 goroutine 悬挂 1s 等待）。
func TestSlowCtxAbort(t *testing.T) {
	srv := httptest.NewServer(ChatCompletionsSlow())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL,
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"x"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		errCh <- err
	}()
	time.Sleep(50 * time.Millisecond) // 请求已到服务端、slow 睡眠中
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("cancelled request should return transport error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client hung after cancel")
	}
}
