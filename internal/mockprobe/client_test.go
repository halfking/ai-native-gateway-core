package mockprobe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/auth"
	"github.com/kaixuan/llm-gateway-go/internal/providers/mock"
)

// newMockGateway 起一个挂好 4 个 mock 端点的 httptest 服务（带
// auth.MockEndpoint 守卫，等价于 gateway-v2 的装配形态）。
func newMockGateway(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/mock/v1/chat/completions/fast", auth.MockEndpoint(mock.CodeFast, mock.ChatCompletionsFast()))
	mux.Handle("/mock/v1/chat/completions/slow", auth.MockEndpoint(mock.CodeSlow, mock.ChatCompletionsSlow()))
	return httptest.NewServer(mux)
}

// TestClientProbe2x2：全矩阵探测成功——request_id 带 mock 标记、
// fast < slow、stream 与非流式都 OK。
func TestClientProbe2x2(t *testing.T) {
	srv := newMockGateway(t)
	defer srv.Close()
	c := NewClient(srv.URL)

	for _, supplier := range []string{mock.CodeFast, mock.CodeSlow} {
		for _, stream := range []bool{false, true} {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			res := c.Probe(ctx, supplier, stream)
			cancel()
			if !res.OK {
				t.Fatalf("probe %s stream=%v failed: code=%d err=%q",
					supplier, stream, res.StatusCode, res.ErrorCode)
			}
			if res.RequestID == "" || !containsMock(res.RequestID) {
				t.Fatalf("probe %s stream=%v missing mock request id: %q", supplier, stream, res.RequestID)
			}
			wantChannel := Channel(supplier, stream)
			if res.Channel != wantChannel {
				t.Fatalf("channel = %q, want %q", res.Channel, wantChannel)
			}
		}
	}
	// fast 必须显著快于 slow（fast≈0ms，slow≥600ms）。
	fastRes := c.Probe(context.Background(), mock.CodeFast, false)
	slowRes := c.Probe(context.Background(), mock.CodeSlow, false)
	if slowRes.Latency <= fastRes.Latency {
		t.Fatalf("slow (%v) should exceed fast (%v)", slowRes.Latency, fastRes.Latency)
	}
}

func containsMock(s string) bool {
	for i := 0; i+4 <= len(s); i++ {
		if s[i:i+4] == "mock" {
			return true
		}
	}
	return false
}

// TestClientProbeErrors：坏供应商名、404 端点、超时分类。
func TestClientProbeErrors(t *testing.T) {
	srv := newMockGateway(t)
	defer srv.Close()
	c := NewClient(srv.URL)

	if res := c.Probe(context.Background(), "mock-other", false); res.OK || res.ErrorCode != "bad_supplier" {
		t.Fatalf("bad supplier should fail: %+v", res)
	}

	// 服务端只有 /mock/v1/chat/completions/*；打不存在的协议路径 → 404。
	c2 := NewClient(srv.URL + "/nonexistent-prefix")
	res := c2.Probe(context.Background(), mock.CodeFast, false)
	if res.OK || res.StatusCode != http.StatusNotFound {
		t.Fatalf("404 path should fail: %+v", res)
	}
	if res.ErrorCode != "http_404" {
		t.Fatalf("error code = %q, want http_404", res.ErrorCode)
	}

	// 超时：50ms ctx 打 slow 通道（≥600ms）→ timeout 分类。
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	res = c.Probe(ctx, mock.CodeSlow, false)
	if res.OK || res.ErrorCode != "timeout" {
		t.Fatalf("timeout classification failed: %+v", res)
	}
}

// TestBaseURLFromListen：监听地址归一。
func TestBaseURLFromListen(t *testing.T) {
	cases := map[string]string{
		":8782":            "http://127.0.0.1:8782",
		"0.0.0.0:8782":     "http://127.0.0.1:8782",
		"127.0.0.1:9000":   "http://127.0.0.1:9000",
		"192.168.1.5:8782": "http://192.168.1.5:8782",
		"[::]:8782":        "http://127.0.0.1:8782",
		"garbage":          "http://127.0.0.1",
	}
	for in, want := range cases {
		if got := BaseURLFromListen(in); got != want {
			t.Errorf("BaseURLFromListen(%q) = %q, want %q", in, got, want)
		}
	}
}
