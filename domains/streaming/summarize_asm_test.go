// Package streaming — summarize_asm_test.go
//
// asm 在线优先委托器的行为锁定：
//   - asm 在线且端点可用 ⇒ 委托成功（engine=asm，网关 chat 桩零调用），
//     委托请求带 plugin-context 五件套头；
//   - asm 委托失败（500）⇒ 响亮降级走网关管线（engine=gateway）；
//   - asm 离线（探测连不上）⇒ 直接走网关管线；
//   - healthz 探测结果缓存 30s（第二个请求不再探测）。
package streaming

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// asmStub 是一个最小 asm：/api/v1/healthz + /api/v1/summarize（校验签名头）。
type asmStub struct {
	healthN    atomic.Int64
	summarizeN atomic.Int64
	failSum    bool
	gotSig     atomic.Bool
	gotText    string
}

func (a *asmStub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/healthz", func(w http.ResponseWriter, r *http.Request) {
		a.healthN.Add(1)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/api/v1/summarize", func(w http.ResponseWriter, r *http.Request) {
		a.summarizeN.Add(1)
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("X-Gateway-Context-Signature") != "" &&
			r.Header.Get("X-Gateway-Plugin-ID") == "ai-session-manager" &&
			r.Header.Get("X-Gateway-Tenant-ID") == "default" {
			a.gotSig.Store(true)
		}
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		a.gotText = body.Text
		if a.failSum {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"title":"asm 标题","summary":"# asm 标题\n\n## 一句话回顾\n委托成功\n\n## 时间线\n- x\n\n## 关键决议\n| 决议 |\n\n## 待办\n| 事项 |","quotes":["q"],"model":"stub-local","elapsed_ms":42}`))
	})
	return mux
}

// newASMTestServer：网关三件套（chat 桩记录调用）+ asm 桩。
func newASMTestServer(t *testing.T, asm *asmStub) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	chatCalls := &[]map[string]any{}
	svc := NewSummarizeService()
	h := NewSummarizeHandler(svc)
	mux := http.NewServeMux()
	mux.Handle("/v1/summarize", h)
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		*chatCalls = append(*chatCalls, body)
		_, _ = w.Write([]byte(`{"model":"gw-llm","choices":[{"message":{"content":"## 一句话回顾 a\n## 时间线 b\n## 关键决议 c\n## 待办 d"}}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	svc.baseURL = srv.URL

	if asm != nil {
		asmSrv := httptest.NewServer(asm.handler())
		t.Cleanup(asmSrv.Close)
		svc.SetASMDelegate(asmSrv.URL, "ai-session-manager", "default", []byte("shared-secret"))
	}
	return srv, chatCalls
}

func longText() string {
	return strings.Repeat("运维: 今天排查 252 出向流量问题。\n", 400)
}

func TestSummarizeASMDelegateHappyPath(t *testing.T) {
	asm := &asmStub{}
	srv, chatCalls := newASMTestServer(t, asm)
	resp, body := summarizePost(t, srv.URL, "/v1/summarize",
		fmt.Sprintf(`{"model":"qwen3.8:27b-mlx","map_model":"qwen2.5:7b","text":%q}`, longText()))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	var out SummarizeResult
	_ = json.Unmarshal(body, &out)
	if out.Engine != "asm" || out.Pipeline != "asm" {
		t.Fatalf("engine=%q pipeline=%q, want asm/asm", out.Engine, out.Pipeline)
	}
	if !strings.Contains(out.Summary, "委托成功") {
		t.Fatalf("summary = %q", out.Summary)
	}
	if out.Title != "asm 标题" {
		t.Fatalf("title = %q", out.Title)
	}
	if asm.summarizeN.Load() != 1 || !asm.gotSig.Load() {
		t.Fatalf("asm calls=%d signed=%v", asm.summarizeN.Load(), asm.gotSig.Load())
	}
	// 委托成功时网关 chat 桩必须零调用（这就是成本意义）。
	if len(*chatCalls) != 0 {
		t.Fatalf("gateway chat calls = %d, want 0 (delegated)", len(*chatCalls))
	}
	if asm.gotText == "" || !strings.Contains(asm.gotText, "252") {
		t.Fatalf("asm did not receive the text: %q", asm.gotText)
	}
}

func TestSummarizeASMFailureFallsBackToGateway(t *testing.T) {
	asm := &asmStub{failSum: true}
	srv, chatCalls := newASMTestServer(t, asm)
	resp, body := summarizePost(t, srv.URL, "/v1/summarize",
		fmt.Sprintf(`{"model":"gw-llm","text":%q}`, longText()))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s (fallback must succeed)", resp.StatusCode, body)
	}
	var out SummarizeResult
	_ = json.Unmarshal(body, &out)
	if out.Engine != "gateway" {
		t.Fatalf("engine = %q, want gateway after delegate failure", out.Engine)
	}
	if asm.summarizeN.Load() != 1 || len(*chatCalls) == 0 {
		t.Fatalf("delegate=%d chatCalls=%d", asm.summarizeN.Load(), len(*chatCalls))
	}
}

func TestSummarizeASMOfflineFallsBack(t *testing.T) {
	// 指向一个立即关闭的服务地址 = 探测连不上。
	svc := NewSummarizeService()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	svc.SetASMDelegate(deadURL, "ai-session-manager", "default", []byte("k"))

	chatCalled := false
	mux := http.NewServeMux()
	mux.Handle("/v1/summarize", NewSummarizeHandler(svc))
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		chatCalled = true
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"content":"## 一句话回顾 a\n## 时间线 b\n## 关键决议 c\n## 待办 d"}}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	svc.baseURL = srv.URL

	resp, body := summarizePost(t, srv.URL, "/v1/summarize",
		fmt.Sprintf(`{"model":"m","text":%q}`, longText()))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	var out SummarizeResult
	_ = json.Unmarshal(body, &out)
	if out.Engine != "gateway" || !chatCalled {
		t.Fatalf("engine=%q chatCalled=%v", out.Engine, chatCalled)
	}
}

func TestSummarizeASMHealthCache(t *testing.T) {
	asm := &asmStub{}
	svc := NewSummarizeService()
	asmSrv := httptest.NewServer(asm.handler())
	defer asmSrv.Close()
	svc.SetASMDelegate(asmSrv.URL, "ai-session-manager", "default", []byte("k"))
	if !svc.asm.available(t.Context()) {
		t.Fatal("first probe should be online")
	}
	if !svc.asm.available(t.Context()) {
		t.Fatal("second probe should be served from cache")
	}
	if asm.healthN.Load() != 1 {
		t.Fatalf("health probes = %d, want 1 (cached)", asm.healthN.Load())
	}
}
