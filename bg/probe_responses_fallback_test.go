// Package bg — probe_responses_fallback_test.go（2026-09-28 vapeur 轮）。
//
// 回归目标：openai-responses 供应商的探针在 /v1/responses 被上游判
// 「不支持 Responses API」（400/502）时，必须向同一 base_url 降级补一发
// chat 探针；chat 绿则本轮按成功回报。没有降级时，vapeur 的 claude/qwen
// 全系被探红 → URSM v2 视图 available=0 → 路由整体排除该凭据。
package bg

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// vapeurClaudeResponses400 复刻 2026-09-28 实测的 vapeur claude 400 body。
const vapeurClaudeResponses400 = `{"error":{"message":"该供应商不支持 Responses API","type":"invalid_request_error","code":"unsupported_operation"}}`

// TestResponsesChatFallbackPing_WireShape 钉住 chat 降级探针的线形态：
// POST base/chat/completions、Bearer、messages 载荷、max_tokens=10
// （=1 会在推理模型上 400，2026-09-25 实测）。
func TestResponsesChatFallbackPing_WireShape(t *testing.T) {
	var gotPath, gotAuthz string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthz = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	t.Cleanup(srv.Close)

	status, body, latency, ok := responsesChatFallbackPing(t.Context(), &http.Client{Timeout: 10 * time.Second}, "sk-test", srv.URL, "claude-sonnet-5")
	if !ok || status != 200 {
		t.Fatalf("ok=%v status=%d body=%s", ok, status, body)
	}
	if latency < 0 {
		t.Fatalf("negative latency %d", latency)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("fallback path = %q, want /v1/chat/completions", gotPath)
	}
	if gotAuthz != "Bearer sk-test" {
		t.Fatalf("Authorization = %q", gotAuthz)
	}
	if mt, _ := gotBody["max_tokens"].(float64); mt != float64(responsesFallbackChatMaxTokens) {
		t.Fatalf("max_tokens = %v, want %d", gotBody["max_tokens"], responsesFallbackChatMaxTokens)
	}
	if _, has := gotBody["input"]; has {
		t.Fatalf("chat fallback must not carry responses 'input' field")
	}
}

// TestProbeWithRetry_ResponsesUnsupportedFallsBackToChat：Layer 4 responses
// 探针 400「不支持」→ chat 降级复探 → 成功按 probeCategoryOK 回报。
func TestProbeWithRetry_ResponsesUnsupportedFallsBackToChat(t *testing.T) {
	var responsesHits, chatHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			responsesHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(vapeurClaudeResponses400))
		case "/v1/chat/completions":
			chatHits.Add(1)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
		default:
			t.Errorf("unexpected probe path %q", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	target := probeTarget{
		CredentialID: 126,
		RawModel:     "claude-sonnet-5",
		BaseURL:      srv.URL,
		Protocol:     "openai-responses",
		APIKey:       "sk-test",
	}
	desc := probeDescriptorFor(target.Protocol)
	result := probeWithRetry(t.Context(), desc, target, ProbeModeResponses)

	if responsesHits.Load() != 1 {
		t.Fatalf("responses probe hits = %d, want 1", responsesHits.Load())
	}
	if chatHits.Load() != 1 {
		t.Fatalf("chat fallback hits = %d, want 1", chatHits.Load())
	}
	if result.category != probeCategoryOK || result.status != "ok" {
		t.Fatalf("status = %q category = %v, want ok — chat fallback did not rescue the round", result.status, result.category)
	}
	if result.httpStatus != 200 {
		t.Fatalf("httpStatus = %d, want 200 (chat leg)", result.httpStatus)
	}
	if !strings.Contains(result.errMsg, "Responses API unsupported for this model") || !strings.Contains(result.errMsg, "chat fallback probe OK") {
		t.Fatalf("errMsg missing fallback annotation: %q", result.errMsg)
	}
}

// TestProbeWithRetry_ResponsesUnsupportedChatAlsoFails：chat 降级也挂时，
// 维持失败结论（不得把 400 伪装成 4xx 之外的类别），且错误信息带上
// fallback 结果。
func TestProbeWithRetry_ResponsesUnsupportedChatAlsoFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(vapeurClaudeResponses400))
		case "/v1/chat/completions":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
		}
	}))
	t.Cleanup(srv.Close)

	target := probeTarget{
		CredentialID: 126,
		RawModel:     "claude-sonnet-5",
		BaseURL:      srv.URL,
		Protocol:     "openai-responses",
		APIKey:       "sk-test",
	}
	desc := probeDescriptorFor(target.Protocol)
	result := probeWithRetry(t.Context(), desc, target, ProbeModeResponses)

	if result.category == probeCategoryOK {
		t.Fatalf("chat fallback failed (401) but round reported ok")
	}
	if !strings.Contains(result.errMsg, "chat fallback probe failed") {
		t.Fatalf("errMsg missing failed-fallback annotation: %q", result.errMsg)
	}
}

// TestActiveProbeExecutor_ResponsesUnsupportedFallsBackToChat：主动探针
// 执行器同款降级——ActiveProbeWorker 会把结果回灌状态管理器，不降级会把
// 经 chat 可用的节点压红。
func TestActiveProbeExecutor_ResponsesUnsupportedFallsBackToChat(t *testing.T) {
	var chatHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(vapeurClaudeResponses400))
		case "/v1/chat/completions":
			chatHits.Add(1)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}],"usage":{"total_tokens":9}}`))
		default:
			t.Errorf("unexpected probe path %q", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	e := &ActiveProbeExecutor{httpClient: &http.Client{Timeout: 10 * time.Second}}
	res := e.Run(t.Context(), &ProbeTarget{
		CredentialID: 126,
		ProviderID:   36,
		RawModel:     "claude-sonnet-5",
		BaseURL:      srv.URL,
		Protocol:     "openai-responses",
		APIKey:       "sk-test",
	})

	if chatHits.Load() != 1 {
		t.Fatalf("chat fallback hits = %d, want 1 (status=%q errCode=%q httpStatus=%d errMsg=%q)", chatHits.Load(), res.Status, res.ErrCode, res.HTTPStatus, res.ErrMsg)
	}
	if res.Status != ProbeStatusSuccess {
		t.Fatalf("Status = %q, want success after chat fallback (errMsg=%s)", res.Status, res.ErrMsg)
	}
	if res.HTTPStatus != 200 {
		t.Fatalf("HTTPStatus = %d, want 200 (chat leg)", res.HTTPStatus)
	}
	if res.TotalTokens != 9 {
		t.Fatalf("TotalTokens = %d, want 9 (parsed from chat leg usage)", res.TotalTokens)
	}
	if !strings.Contains(res.ErrMsg, "chat fallback probe OK") {
		t.Fatalf("ErrMsg missing fallback annotation: %q", res.ErrMsg)
	}
}

// TestMiniResponses_WireShape：credential_probe_v2 step2 对
// openai-responses 供应商必须发 responses 原生载荷（此前 chat 体打
// /responses，被 400 "Unsupported parameter: 'messages'" 拒绝）。
func TestMiniResponses_WireShape(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("miniResponses path = %q, want /responses", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","output":[]}`))
	}))
	t.Cleanup(srv.Close)

	c := &CredentialProbeV2{}
	ok, errMsg, status, _ := c.miniResponses(t.Context(), srv.Client(), "sk-test", "gpt-5.6-terra", srv.URL+"/responses")
	if !ok || status != 200 || errMsg != "" {
		t.Fatalf("ok=%v status=%d errMsg=%q", ok, status, errMsg)
	}
	if gotBody["input"] != "ping" {
		t.Fatalf("input = %#v, want ping", gotBody["input"])
	}
	if mt, _ := gotBody["max_output_tokens"].(float64); mt < 16 {
		t.Fatalf("max_output_tokens = %#v, want >= 16", gotBody["max_output_tokens"])
	}
	if _, has := gotBody["messages"]; has {
		t.Fatalf("responses probe must not carry chat 'messages' field")
	}
}
