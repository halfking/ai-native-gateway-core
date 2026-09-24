package mock

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/auth"
)

func serve(h http.HandlerFunc, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/mock", strings.NewReader(body))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h(rec, r)
	return rec
}

func mustJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response not JSON: %v body=%q", err, rec.Body.String())
	}
	return m
}

// TestFastNonStream：立即返回（<150ms）、OpenAI 形状、id 带 mock 标记、
// 回复包含供应商标记与 token 估算。
func TestFastNonStream(t *testing.T) {
	start := time.Now()
	rec := serve(ChatCompletionsFast(),
		`{"model":"gpt-4o","messages":[{"role":"user","content":"hello world, this is a probe"}]}`,
		map[string]string{"Content-Type": "application/json"})
	elapsed := time.Since(start)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if elapsed > 150*time.Millisecond {
		t.Fatalf("fast handler took %v, want <150ms", elapsed)
	}
	body := mustJSON(t, rec)
	if body["object"] != "chat.completion" {
		t.Fatalf("object = %v", body["object"])
	}
	id, _ := body["id"].(string)
	if !strings.Contains(id, "mock") {
		t.Fatalf("id %q should contain mock marker", id)
	}
	choices := body["choices"].([]any)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	content, _ := msg["content"].(string)
	if !strings.Contains(content, "[mock-fast]") {
		t.Fatalf("reply should tag supplier: %q", content)
	}
	if choices[0].(map[string]any)["finish_reason"] != "stop" {
		t.Fatal("finish_reason should be stop")
	}
}

// TestFastStream：SSE 头、至少一个内容 delta、[DONE] 收尾、全量延迟小。
func TestFastStream(t *testing.T) {
	start := time.Now()
	rec := serve(ChatCompletionsFast(),
		`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"ping"}]}`, nil)
	elapsed := time.Since(start)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	if elapsed > 150*time.Millisecond {
		t.Fatalf("fast stream took %v", elapsed)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "\"delta\"") || !strings.Contains(out, "chat.completion.chunk") {
		t.Fatalf("stream body missing chunks: %q", out)
	}
	if !strings.Contains(out, "[DONE]") {
		t.Fatal("stream must end with [DONE]")
	}
	if strings.Index(out, "data:") > strings.Index(out, "[DONE]") {
		t.Fatal("[DONE] must be last")
	}
}

// TestFastRejectsBadBody：非 JSON / 多 JSON 值 → 400。
func TestFastRejectsBadBody(t *testing.T) {
	rec := serve(ChatCompletionsFast(), `not-json`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON should 400, got %d", rec.Code)
	}
	rec = serve(ChatCompletionsFast(), `{"a":1} {"b":2}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("trailing JSON should 400, got %d", rec.Code)
	}
}

// TestFastImageEcho：OpenAI image_url（data URI base64）与 Anthropic
// source base64 都回显接收字节数（设计 §3.4）。
func TestFastImageEcho(t *testing.T) {
	rec := serve(ChatCompletionsFast(), `{
		"model":"gpt-4o",
		"messages":[{"role":"user","content":[
			{"type":"text","text":"look at this"},
			{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJDREVG"}}
		]}]}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	content := mustJSON(t, rec)["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
	if !strings.Contains(content, "image") || !strings.Contains(content, "bytes") {
		t.Fatalf("image echo missing: %q", content)
	}
	// "QUJDREVG" 8 base64 chars → 8*3/4 = 6 字节
	if !strings.Contains(content, "6 bytes") {
		t.Fatalf("base64 size estimate wrong: %q", content)
	}

	// Anthropic source 形态
	rec = serve(MessagesFast(), `{
		"model":"claude-3",
		"messages":[{"role":"user","content":[
			{"type":"text","text":"check"},
			{"type":"image","source":{"type":"base64","data":"QUJDREVG","media_type":"image/png"}}
		]}]}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("anthropic status = %d", rec.Code)
	}
	text := mustJSON(t, rec)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "6 bytes") {
		t.Fatalf("anthropic image echo missing: %q", text)
	}
}

// TestFastAnthropicMessages：非流式 message 形状 + 流式事件序列。
func TestFastAnthropicMessages(t *testing.T) {
	rec := serve(MessagesFast(), `{"model":"claude-3","messages":[{"role":"user","content":"hi"}]}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := mustJSON(t, rec)
	if body["type"] != "message" || body["role"] != "assistant" || body["stop_reason"] != "end_turn" {
		t.Fatalf("anthropic envelope wrong: %v", body)
	}

	rec = serve(MessagesFast(), `{"model":"claude-3","stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
	out := rec.Body.String()
	for _, ev := range []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"} {
		if !strings.Contains(out, "event: "+ev) {
			t.Fatalf("stream missing event %s: %q", ev, out)
		}
	}
}

// TestSupplierContractAlignment：mock 包供应商集合必须与 auth 侧 scope
// 白名单一致（防漂移——两处常量各自维护）。
func TestSupplierContractAlignment(t *testing.T) {
	for _, s := range Suppliers() {
		if !auth.EnforceMockProbeScope(s) {
			t.Errorf("supplier %q not allowed by auth scope whitelist", s)
		}
		if seg, ok := Segment(s); !ok || (seg != "fast" && seg != "slow") {
			t.Errorf("Segment(%q) = %q,%v", s, seg, ok)
		}
	}
	if !IsSupplier(CodeFast) || !IsSupplier(CodeSlow) || IsSupplier("mock-other") {
		t.Fatal("IsSupplier contract broken")
	}
}

// TestBodyCap：超限请求体 → 413。
func TestBodyCap(t *testing.T) {
	big := `{"model":"m","messages":[{"role":"user","content":"` + strings.Repeat("x", mockMaxBodyBytes) + `"}]}`
	rec := serve(ChatCompletionsFast(), big, nil)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body should 413, got %d", rec.Code)
	}
}
