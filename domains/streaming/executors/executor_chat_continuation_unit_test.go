package executors

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// P0-3 (2026-10-14)：executor 侧续问单元判据（同层——直接驱动
// chatStreamContinuation.invoke 与 appendAssistantPrefillMessage）。
// 端到端拼接链路由 executor_chat_continuation_p03_test.go 覆盖。

func TestP03AppendAssistantPrefillMessage(t *testing.T) {
	base := `{"model":"m","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}],"vendor_extra":{"x":1}}`
	out, err := appendAssistantPrefillMessage([]byte(base), "one two")
	if err != nil {
		t.Fatalf("appendAssistantPrefillMessage: %v", err)
	}
	// 尾消息必须是 assistant prefill，content=已累计文本。
	tail, ok := p03InternalTailAssistant(string(out))
	if !ok {
		t.Fatalf("tail message is not assistant: %s", out)
	}
	if tail != "one two" {
		t.Fatalf("prefill content = %q, want %q", tail, "one two")
	}
	// 未知顶层字段（vendor 私有参数、stream_options）必须原样保留。
	for _, frag := range []string{`"stream_options":{"include_usage":true}`, `"vendor_extra":{"x":1}`, `"model":"m"`} {
		if !strings.Contains(string(out), frag) {
			t.Fatalf("unknown top-level field lost: %s (want fragment %s)", out, frag)
		}
	}
	// 原始 messages 必须仍在 prefill 之前。
	if !strings.Contains(string(out), `"role":"user","content":"hi"`) {
		t.Fatalf("original messages lost: %s", out)
	}
}

func p03InternalTailAssistant(body string) (string, bool) {
	// 轻量解析：找最后一个 "role":"assistant" 的 content。测试数据受控，
	// 用简单扫描即可。
	idx := strings.LastIndex(body, `"role":"assistant"`)
	if idx < 0 {
		return "", false
	}
	cIdx := strings.Index(body[idx:], `"content":"`)
	if cIdx < 0 {
		return "", false
	}
	rest := body[idx+cIdx+len(`"content":"`):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}

func TestP03InvokeBudgetIsAtomicExactlyOnce(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, upstream.URL, strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	s := &chatStreamContinuation{
		e:      &Executor{},
		params: &ExecParams{RequestID: "p03-unit"},
		body:   []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
		req:    req,
		client: upstream.Client(),
	}
	if _, err := s.invoke("prefix"); err != nil {
		t.Fatalf("first invoke: %v", err)
	}
	if _, err := s.invoke("prefix"); err == nil {
		t.Fatal("second invoke must fail: budget is exactly one (atomic CAS)")
	}
	if !s.used.Load() {
		t.Fatal("used flag must be latched after first invoke")
	}
}

func TestP03InvokeEmptyPrefixAndBadStatus(t *testing.T) {
	s := &chatStreamContinuation{e: &Executor{}, params: &ExecParams{}}
	if _, err := s.invoke(""); err == nil {
		t.Fatal("empty prefix must be refused (no committed text to prefill)")
	}

	badUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer badUpstream.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, badUpstream.URL, strings.NewReader("{}"))
	s2 := &chatStreamContinuation{
		e:      &Executor{},
		params: &ExecParams{},
		body:   []byte(`{"messages":[]}`),
		req:    req,
		client: badUpstream.Client(),
	}
	if _, err := s2.invoke("prefix"); err == nil {
		t.Fatal("non-200 continuation must fail (falls back to eof_without_done terminal)")
	}
	// 预算已被消耗：即使失败也不再重试。
	var used atomic.Bool
	used.Store(true)
	if s2.used.Load() != true {
		t.Fatal("failed invoke must still consume the budget")
	}
}
