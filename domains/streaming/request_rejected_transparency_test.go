package streaming

// request_rejected_transparency_test.go — 2026-11-13（claude-opus-5-5
// adaptive-only 事故轮 · 第二层）：KindRequestRejected（上游 4xx 拒收请求
// 本身）在三个协议面都必须回 **400 + invalid_request_error + 上游 reason
// 透传**，而不是 503 "No available provider for model" / "All providers
// unavailable"。
//
// 缺口本体（211 真机实测，2.5.9-6a9335855 在役版本）：
//
//	POST /v1/chat/completions {"model":"claude-opus-5-5",
//	      "thinking":{"type":"enabled","budget_tokens":1024}, ...}
//	⇒ 503 {"error":{"code":"model_not_found","type":"server_error",
//	      "message":"No available provider for model 'claude-opus-5-5'.
//	      All 3 candidates failed."}}
//	⇒ 而上游真实原因是 400 invalid_request_error："claude-opus-5-5 requires
//	  adaptive thinking; omit thinking or use thinking.type=adaptive and
//	  output_config.effort" —— 被完全丢弃。
//
// 客户端把「自己的参数被上游拒了」误读成「网关/供应商没有可用节点」——
// owner 原话：「这类的错误应该返回给调用方，不要造成误解」。
//
// ★ 归因（error_kind=request_rejected 落库）在 2026-11-02 已修；缺的是
// **客户端响应面**（与 2026-11-11 fable-5-2 形态门缺口同构：kind 是对的，
// 协议面没接）。测试复用 request_shape_rejection_test.go 的 stub 设施。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

// adaptiveOnlyExecError 复刻 211 事故形态的 ExecuteError：三候选全部
// request_rejected，LastErr 包装真实上游 400 错误体。
func adaptiveOnlyExecError() *executors.ExecuteError {
	return &executors.ExecuteError{
		LastKind:  errorsx.KindRequestRejected,
		Exhausted: true,
		Tried:     3,
		LastErr: &upstreampkg.Error{
			Kind:       errorsx.KindRequestRejected,
			Message:    "upstream 400: invalid_request_error",
			StatusCode: http.StatusBadRequest,
			Body:       []byte(`{"error":{"message":"claude-opus-5-5 requires adaptive thinking; omit thinking or use thinking.type=adaptive and output_config.effort","type":"invalid_request_error"}}`),
		},
	}
}

// TestRequestRejectedTransparency_ChatFace_Returns400WithUpstreamReason：
// /v1/chat/completions 面（用户事故的真实入口）。
func TestRequestRejectedTransparency_ChatFace_Returns400WithUpstreamReason(t *testing.T) {
	h, sink := newShapeRejectHandler(t, adaptiveOnlyExecError())
	srv := httptest.NewServer(h)
	defer srv.Close()

	body := `{"model":"claude-opus-5-5","max_tokens":2000,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"hi"}]}`
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("content-type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (a request the upstream rejects must not be reported as \"no available provider\" 503)", resp.StatusCode)
	}
	if kind := resp.Header.Get("X-Gateway-Last-Kind"); kind != "request_rejected" {
		t.Errorf("X-Gateway-Last-Kind = %q, want request_rejected", kind)
	}

	var env struct {
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if env.Error.Type != "invalid_request_error" {
		t.Errorf("error.type = %q, want invalid_request_error (not server_error)", env.Error.Type)
	}
	if env.Error.Code != "request_rejected" {
		t.Errorf("error.code = %q, want request_rejected (not model_not_found)", env.Error.Code)
	}
	if !strings.Contains(env.Error.Message, "requires adaptive thinking") {
		t.Errorf("error.message = %q, want it to carry the upstream's own reason", env.Error.Message)
	}
	if strings.Contains(env.Error.Message, "No available provider") {
		t.Errorf("error.message = %q, must NOT claim \"No available provider\"", env.Error.Message)
	}

	kinds := sink.errorKinds()
	if len(kinds) == 0 {
		t.Fatal("no request-log entry was emitted")
	}
	if kinds[0] != "request_rejected" {
		t.Errorf("error_kind = %v, want request_rejected", kinds)
	}
}

// TestRequestRejectedTransparency_AnthropicFace_Returns400WithUpstreamReason：
// /v1/messages 面同款。
func TestRequestRejectedTransparency_AnthropicFace_Returns400WithUpstreamReason(t *testing.T) {
	h, sink := newShapeRejectHandler(t, adaptiveOnlyExecError())
	srv := httptest.NewServer(NewMessagesHandler(h))
	defer srv.Close()

	body := `{"model":"claude-opus-5-5","max_tokens":2000,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"hi"}]}`
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("x-api-key", "sk-test")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if kind := resp.Header.Get("X-Gateway-Last-Kind"); kind != "request_rejected" {
		t.Errorf("X-Gateway-Last-Kind = %q, want request_rejected", kind)
	}

	var env struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if env.Error.Type != "invalid_request_error" {
		t.Errorf("error.type = %q, want invalid_request_error (not overloaded_error)", env.Error.Type)
	}
	if !strings.Contains(env.Error.Message, "requires adaptive thinking") {
		t.Errorf("error.message = %q, want it to carry the upstream's own reason", env.Error.Message)
	}
	if strings.Contains(env.Error.Message, "All providers unavailable") {
		t.Errorf("error.message = %q, must NOT claim \"All providers unavailable\"", env.Error.Message)
	}

	kinds := sink.errorKinds()
	if len(kinds) == 0 {
		t.Fatal("no request-log entry was emitted")
	}
	for _, k := range kinds {
		if k == "model_not_found" {
			t.Fatalf("error_kind must not be the misleading model_not_found; got %v", kinds)
		}
	}
}

// TestRequestRejectedTransparency_ResponsesFace_Returns400WithUpstreamReason：
// /v1/responses 面同款。
func TestRequestRejectedTransparency_ResponsesFace_Returns400WithUpstreamReason(t *testing.T) {
	h, _ := newShapeRejectHandler(t, adaptiveOnlyExecError())
	srv := httptest.NewServer(NewResponsesHandler(h))
	defer srv.Close()

	body := `{"model":"claude-opus-5-5","input":"hi"}`
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("content-type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if kind := resp.Header.Get("X-Gateway-Last-Kind"); kind != "request_rejected" {
		t.Errorf("X-Gateway-Last-Kind = %q, want request_rejected", kind)
	}
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if !strings.Contains(env.Error.Message, "requires adaptive thinking") {
		t.Errorf("error.message = %q, want it to carry the upstream's own reason", env.Error.Message)
	}
}

// TestRequestRejectedTransparency_NegativeControl_TransientStill503：
// 阴性对照——非请求侧错误维持既有 503 映射（回归面保护）。
func TestRequestRejectedTransparency_NegativeControl_TransientStill503(t *testing.T) {
	h, _ := newShapeRejectHandler(t, &executors.ExecuteError{
		LastKind:  errorsx.KindTransient,
		Exhausted: true,
		LastErr:   context.DeadlineExceeded,
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	body := `{"model":"claude-opus-5-5","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("content-type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (transient exhaustion keeps its pre-existing mapping)", resp.StatusCode)
	}
}
