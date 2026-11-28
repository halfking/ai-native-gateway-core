package streaming

// rate_limit_exhausted_test.go —— 2026-11-28：「全部候选都被上游限流」必须在
// **三个协议面**都回 429 + Retry-After + retryable，而不是落进通用兜底的
// overloaded_error / server_error "All providers unavailable" + 落库
// model_not_found。
//
// 211 实测（glm-5.3-flash，14:47Z）：/v1/messages 把单候选 429 渲染成
// overloaded_error ⇒ ZCode 显示 provider_code=PROVIDER_BUSINESS_ERROR
// retryable=false ⇒ 整轮失败；库里记的是 model_not_found。
//
// ★ 缺口形态与 2026-11-18「熔断打空」（provider_circuit_open_test.go）完全
// 同型：kind 没错，是协议面没接——chat 面早有专属出口，另外两面没有。
// ⇒ 判据必须覆盖到协议面这一层。
//
// 复用 request_shape_rejection_test.go 的 stub 设施（newShapeRejectHandler）。

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// rateLimitExecError 复刻「唯一候选回 429」的终态形态（无上游 Retry-After
// 提示 ⇒ 取默认退避 5s）。
func rateLimitExecError() *executors.ExecuteError {
	return &executors.ExecuteError{
		LastKind:  errorsx.KindRateLimit,
		Exhausted: true,
		Tried:     1,
		LastErr:   errors.New("upstream 429"),
	}
}

// ---------------------------------------------------------------------------
// 1. 判据函数（纯函数层）
// ---------------------------------------------------------------------------

func TestRateLimitExhaustedRejection_Recognizes(t *testing.T) {
	surf, ok := rateLimitExhaustedRejection(rateLimitExecError(), "glm-5.3-flash")
	if !ok {
		t.Fatal("rateLimitExhaustedRejection must recognize an exhausted rate_limit")
	}
	if surf.Status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", surf.Status)
	}
	if surf.Code != RateLimitExhaustedCode {
		t.Errorf("code = %q, want %q", surf.Code, RateLimitExhaustedCode)
	}
	if surf.RetryAfter <= 0 {
		t.Fatalf("retry_after = %d, want a positive backoff (0 invites an immediate hot retry)", surf.RetryAfter)
	}
	if !strings.Contains(surf.Message, "Rate limited") || !strings.Contains(surf.Message, "glm-5.3-flash") {
		t.Errorf("message = %q, want it to name the rate limit and the model", surf.Message)
	}
}

// TestRateLimitExhaustedRejection_NegativeControls pins that every other
// shape keeps its pre-existing path (zero regression surface): other kinds,
// a non-exhausted rate limit, and nil.
func TestRateLimitExhaustedRejection_NegativeControls(t *testing.T) {
	cases := []struct {
		name string
		err  *executors.ExecuteError
	}{
		{"transient", &executors.ExecuteError{LastKind: errorsx.KindTransient, Exhausted: true}},
		{"circuit_open", &executors.ExecuteError{LastKind: errorsx.KindCircuitOpen, Exhausted: true}},
		{"request_rejected", &executors.ExecuteError{LastKind: errorsx.KindRequestRejected, Exhausted: true}},
		{"model_not_found", &executors.ExecuteError{LastKind: errorsx.KindModelNotFound, Exhausted: true}},
		{"rate_limit_not_exhausted", &executors.ExecuteError{LastKind: errorsx.KindRateLimit, Exhausted: false}},
		{"nil", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := rateLimitExhaustedRejection(tc.err, "gpt-4o"); ok {
				t.Fatalf("%s must NOT be treated as a rate-limit exhaustion", tc.name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 2. 协议面（端到端层）—— 这一层才是本缺口的所在
// ---------------------------------------------------------------------------

func assertNoMisleadingClassification(t *testing.T, sink *shapeRejectLogSink) {
	t.Helper()
	kinds := sink.errorKinds()
	if len(kinds) == 0 {
		t.Fatal("no request-log entry was emitted")
	}
	if kinds[0] != RateLimitExhaustedCode {
		t.Errorf("error_kind = %v, want %q", kinds, RateLimitExhaustedCode)
	}
	for _, k := range kinds {
		if k == "model_not_found" {
			t.Fatalf("error_kind must not be the misleading model_not_found; got %v", kinds)
		}
	}
}

func postJSON(t *testing.T, url string, headers map[string]string, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, string(raw)
}

// TestRateLimitExhausted_ChatFace_Returns429 pins the pre-existing chat
// behaviour the other two faces are aligned to.
func TestRateLimitExhausted_ChatFace_Returns429(t *testing.T) {
	h, sink := newShapeRejectHandler(t, rateLimitExecError())
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, body := postJSON(t, srv.URL+"/v1/chat/completions",
		map[string]string{"Authorization": "Bearer sk-test", "content-type": "application/json"},
		`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Retry-After"); got == "" {
		t.Error("Retry-After header missing — SDK backoff logic special-cases 429 and needs it")
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode envelope: %v (%s)", err, body)
	}
	if env.Error.Code != RateLimitExhaustedCode || env.Error.Type != "rate_limit_error" {
		t.Errorf("chat envelope = code %q type %q, want rate_limit / rate_limit_error", env.Error.Code, env.Error.Type)
	}
	assertNoMisleadingClassification(t, sink)
}

// TestRateLimitExhausted_AnthropicFace_Returns429 is the钉测 for the 211
// incident: /v1/messages must not render a rate limit as overloaded_error.
func TestRateLimitExhausted_AnthropicFace_Returns429(t *testing.T) {
	h, sink := newShapeRejectHandler(t, rateLimitExecError())
	srv := httptest.NewServer(NewMessagesHandler(h))
	defer srv.Close()

	resp, body := postJSON(t, srv.URL+"/v1/messages",
		map[string]string{"x-api-key": "sk-test", "content-type": "application/json", "anthropic-version": "2023-06-01"},
		`{"model":"glm-5.3-flash","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Retry-After"); got == "" {
		t.Error("Retry-After header missing")
	}
	if got := resp.Header.Get("X-Gateway-Last-Kind"); got != string(errorsx.KindRateLimit) {
		t.Errorf("X-Gateway-Last-Kind = %q, want %q", got, errorsx.KindRateLimit)
	}
	var env struct {
		Error struct {
			Type       string `json:"type"`
			Code       string `json:"code"`
			Retryable  bool   `json:"retryable"`
			RetryAfter int    `json:"retry_after"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode envelope: %v (%s)", err, body)
	}
	if env.Error.Type != "rate_limit_error" {
		t.Errorf("error.type = %q, want rate_limit_error (overloaded_error reads as \"no provider\" to agent clients)", env.Error.Type)
	}
	if env.Error.Code != RateLimitExhaustedCode || !env.Error.Retryable || env.Error.RetryAfter <= 0 {
		t.Errorf("error = code %q retryable %v retry_after %d, want rate_limit / true / positive", env.Error.Code, env.Error.Retryable, env.Error.RetryAfter)
	}
	assertNoMisleadingClassification(t, sink)
}

// TestRateLimitExhausted_AnthropicFace_PrewarmedStreamCarriesRetryable pins
// the shape ZCode actually hit: the stream was already prewarmed (HTTP 200),
// so response headers cannot reach the client — retryable / retry_after must
// ride inside the in-stream error frame.
func TestRateLimitExhausted_AnthropicFace_PrewarmedStreamCarriesRetryable(t *testing.T) {
	isolateResponsesRuntimeConfig(t)
	h, sink := newShapeRejectHandler(t, rateLimitExecError())
	srv := httptest.NewServer(NewMessagesHandler(h))
	defer srv.Close()

	resp, body := postJSON(t, srv.URL+"/v1/messages",
		map[string]string{"x-api-key": "sk-test", "content-type": "application/json", "anthropic-version": "2023-06-01"},
		`{"model":"glm-5.3-flash","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("expected a prewarmed SSE response, got status %d content-type %q; body=%s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	if !strings.Contains(body, "event: error") {
		t.Fatalf("body has no in-stream error event: %q", body)
	}
	for _, want := range []string{`"type":"rate_limit_error"`, `"code":"rate_limit"`, `"retryable":true`, `"retry_after":`} {
		if !strings.Contains(body, want) {
			t.Errorf("in-stream error frame lacks %s: %q", want, body)
		}
	}
	if strings.Contains(body, "overloaded_error") || strings.Contains(body, "All providers unavailable") {
		t.Errorf("in-stream error frame still uses the pre-fix overloaded envelope: %q", body)
	}
	assertNoMisleadingClassification(t, sink)
}

// TestRateLimitExhausted_ResponsesFace_Returns429 pins the same verdict on
// /v1/responses, both envelopes.
func TestRateLimitExhausted_ResponsesFace(t *testing.T) {
	h, sink := newShapeRejectHandler(t, rateLimitExecError())
	srv := httptest.NewServer(NewResponsesHandler(h))
	defer srv.Close()

	resp, body := postJSON(t, srv.URL+"/v1/responses",
		map[string]string{"Authorization": "Bearer sk-test", "content-type": "application/json"},
		`{"model":"gpt-4o","input":"hi"}`)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Retry-After"); got == "" {
		t.Error("Retry-After header missing")
	}
	var env struct {
		Error struct {
			Code      string `json:"code"`
			Type      string `json:"type"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode envelope: %v (%s)", err, body)
	}
	if env.Error.Code != RateLimitExhaustedCode || env.Error.Type != "rate_limit_error" || !env.Error.Retryable {
		t.Errorf("responses envelope = code %q type %q retryable %v", env.Error.Code, env.Error.Type, env.Error.Retryable)
	}
	assertNoMisleadingClassification(t, sink)

	h2, sink2 := newShapeRejectHandler(t, rateLimitExecError())
	srv2 := httptest.NewServer(NewResponsesHandler(h2))
	defer srv2.Close()
	resp2, body2 := postJSON(t, srv2.URL+"/v1/responses",
		map[string]string{"Authorization": "Bearer sk-test", "content-type": "application/json"},
		`{"model":"gpt-4o","input":"hi","stream":true}`)
	if resp2.StatusCode != http.StatusOK || !strings.Contains(body2, "event: response.failed") {
		t.Fatalf("expected a prewarmed SSE failure event, got status %d; body=%s", resp2.StatusCode, body2)
	}
	for _, want := range []string{`"code":"rate_limit"`, `"retryable":true`} {
		if !strings.Contains(body2, want) {
			t.Errorf("in-stream failure frame lacks %s: %q", want, body2)
		}
	}
	assertNoMisleadingClassification(t, sink2)
}
