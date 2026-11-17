package streaming

// provider_circuit_open_test.go —— 2026-11-18（W1-A）：「上游熔断把候选打空」
// 必须在**三个协议面**都回 503 + 专属错误码 provider_circuit_open，而不是
// 落进通用兜底的 503 model_not_found / "All providers unavailable"。
//
// ── 缺口本体（源码核验，非推测）────────────────────────────────
//
// 熔断准入在 domains/credential/breaker.go（per-(provider, credential)）；
// 全部候选都被熔断拒掉时，请求落到 handler 的 Exhausted 分支。修前该分支
// 没有 KindCircuitOpen 的专属出口 ⇒ 熔断耗尽与「这个模型真没节点」共用
// 一个码 ⇒ 客户端分不清「退避后重试即可」与「换模型/上报配置」——
// 这两者对调用方的处置是**相反**的。
//
// ★ 缺口形态与 2026-11-11「请求形态拒绝」（request_shape_rejection_test.go）
// 以及 2026-11-13「请求被上游拒收」（request_rejected_transparency_test.go）
// **完全同型**：kind 没错，是**协议面没接**。只给 chat 面加分支，另外两面
// 仍落通用兜底 503。
//
// ⇒ 因此本测试必须覆盖到**协议面**这一层：只测判据函数抓不到「某个面忘
// 了接线」——那正是本缺口的形态。
//
// 复用 request_shape_rejection_test.go 的 stub 设施（newShapeRejectHandler）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// circuitOpenExecError 复刻「所有候选都被熔断拒掉」的终态形态。
//
// RetryAfterSeconds 由执行器层从熔断器的冷却截止算出（见
// executors/executor_circuit_open.go），这里给一个确定值便于钉测。
func circuitOpenExecError() *executors.ExecuteError {
	return &executors.ExecuteError{
		LastKind:          errorsx.KindCircuitOpen,
		Exhausted:         true,
		Tried:             2,
		RetryAfterSeconds: 42,
		LastErr:           context.DeadlineExceeded,
	}
}

// ---------------------------------------------------------------------------
// 1. 判据函数（纯函数层）
// ---------------------------------------------------------------------------

func TestProviderCircuitOpenRejection_Recognizes(t *testing.T) {
	surf, ok := providerCircuitOpenRejection(circuitOpenExecError(), "gpt-4o")
	if !ok {
		t.Fatal("providerCircuitOpenRejection must recognize KindCircuitOpen")
	}
	if surf.Status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", surf.Status)
	}
	if surf.Code != ProviderCircuitOpenCode {
		t.Errorf("code = %q, want %q", surf.Code, ProviderCircuitOpenCode)
	}
	if surf.RetryAfter != 42 {
		t.Errorf("retry_after = %d, want 42 (carried from the breaker cooling window)", surf.RetryAfter)
	}
	if !strings.Contains(surf.Message, "circuit-open") {
		t.Errorf("message = %q, want it to say the candidates are circuit-open", surf.Message)
	}
}

// TestProviderCircuitOpenRejection_DefaultRetryAfter pins the fallback: when
// the breaker gave no cooling window the client must still get a positive
// Retry-After — Retry-After: 0 would make it retry immediately and hit the
// same open breaker again.
func TestProviderCircuitOpenRejection_DefaultRetryAfter(t *testing.T) {
	surf, ok := providerCircuitOpenRejection(&executors.ExecuteError{
		LastKind:  errorsx.KindCircuitOpen,
		Exhausted: true,
		Tried:     1,
	}, "gpt-4o")
	if !ok {
		t.Fatal("providerCircuitOpenRejection must recognize KindCircuitOpen")
	}
	if surf.RetryAfter <= 0 {
		t.Fatalf("retry_after = %d, want a positive fallback", surf.RetryAfter)
	}
}

// TestProviderCircuitOpenRejection_NegativeControls pins that every other
// kind keeps the pre-existing path (zero regression surface).
func TestProviderCircuitOpenRejection_NegativeControls(t *testing.T) {
	cases := []struct {
		name string
		err  *executors.ExecuteError
	}{
		{"transient", &executors.ExecuteError{LastKind: errorsx.KindTransient, Exhausted: true}},
		{"network", &executors.ExecuteError{LastKind: errorsx.KindNetwork, Exhausted: true}},
		{"rate_limit", &executors.ExecuteError{LastKind: errorsx.KindRateLimit, Exhausted: true}},
		{"model_not_found", &executors.ExecuteError{LastKind: errorsx.KindModelNotFound, Exhausted: true}},
		{"request_rejected", &executors.ExecuteError{LastKind: errorsx.KindRequestRejected, Exhausted: true}},
		{"content_filter", &executors.ExecuteError{LastKind: errorsx.KindContentFilter, Exhausted: true}},
		{"nil", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := providerCircuitOpenRejection(tc.err, "gpt-4o"); ok {
				t.Fatalf("%s must NOT be treated as a circuit-open exhaustion", tc.name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 2. 协议面（端到端层）—— 这一层才是本缺口的所在
// ---------------------------------------------------------------------------

// TestProviderCircuitOpen_ChatFace_Returns503WithDedicatedCode is the钉测 for
// the original gap: /v1/chat/completions must answer 503 with
// error.code=provider_circuit_open (not model_not_found).
func TestProviderCircuitOpen_ChatFace_Returns503WithDedicatedCode(t *testing.T) {
	h, sink := newShapeRejectHandler(t, circuitOpenExecError())
	srv := httptest.NewServer(h)
	defer srv.Close()

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
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
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got != "42" {
		t.Errorf("Retry-After = %q, want 42 (the breaker cooling window)", got)
	}
	if got := resp.Header.Get("X-Gateway-Last-Kind"); got != string(errorsx.KindCircuitOpen) {
		t.Errorf("X-Gateway-Last-Kind = %q, want %q", got, errorsx.KindCircuitOpen)
	}

	var env struct {
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
			Kind    string `json:"kind"`
			Debug   struct {
				Retryable  bool   `json:"retryable"`
				RetryAfter int    `json:"retry_after"`
				Kind       string `json:"kind"`
				Tried      int    `json:"tried"`
			} `json:"gateway_debug"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if env.Error.Code != ProviderCircuitOpenCode {
		t.Errorf("error.code = %q, want %q (must be distinguishable from \"真无 provider\" 的 model_not_found)", env.Error.Code, ProviderCircuitOpenCode)
	}
	if env.Error.Kind != string(errorsx.KindCircuitOpen) {
		t.Errorf("error.kind = %q, want %q", env.Error.Kind, errorsx.KindCircuitOpen)
	}
	if !env.Error.Debug.Retryable {
		t.Error("gateway_debug.retryable = false, want true (a temporary circuit-open IS worth retrying after backoff)")
	}
	if env.Error.Debug.RetryAfter != 42 {
		t.Errorf("gateway_debug.retry_after = %d, want 42", env.Error.Debug.RetryAfter)
	}
	if env.Error.Debug.Tried != 2 {
		t.Errorf("gateway_debug.tried = %d, want 2", env.Error.Debug.Tried)
	}

	// 落库分类也必须是专属码（运维面，不是只有 HTTP 面）。
	kinds := sink.errorKinds()
	if len(kinds) == 0 {
		t.Fatal("no request-log entry was emitted")
	}
	if kinds[0] != ProviderCircuitOpenCode {
		t.Errorf("error_kind = %v, want %q", kinds, ProviderCircuitOpenCode)
	}
	for _, k := range kinds {
		if k == "model_not_found" {
			t.Fatalf("error_kind must not be the misleading model_not_found; got %v", kinds)
		}
	}
}

// TestProviderCircuitOpen_AnthropicFace_Returns503WithDedicatedCode pins the
// same verdict on /v1/messages.
func TestProviderCircuitOpen_AnthropicFace_Returns503WithDedicatedCode(t *testing.T) {
	h, sink := newShapeRejectHandler(t, circuitOpenExecError())
	srv := httptest.NewServer(NewMessagesHandler(h))
	defer srv.Close()

	body := `{"model":"gpt-4o","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
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

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got != "42" {
		t.Errorf("Retry-After = %q, want 42", got)
	}

	var env struct {
		Type  string `json:"type"`
		Error struct {
			Type       string `json:"type"`
			Message    string `json:"message"`
			Code       string `json:"code"`
			RetryAfter int    `json:"retry_after"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	// ★ Anthropic 信封本身没有 code 字段（writeAnthropicError 只写
	// type/message）⇒ code 走 extras 注入。客户端在两种协议下拿到的是
	// 同一个判别依据。
	if env.Error.Code != ProviderCircuitOpenCode {
		t.Errorf("error.code = %q, want %q", env.Error.Code, ProviderCircuitOpenCode)
	}
	if env.Error.RetryAfter != 42 {
		t.Errorf("error.retry_after = %d, want 42", env.Error.RetryAfter)
	}

	kinds := sink.errorKinds()
	if len(kinds) == 0 {
		t.Fatal("no request-log entry was emitted")
	}
	if kinds[0] != ProviderCircuitOpenCode {
		t.Errorf("error_kind = %v, want %q", kinds, ProviderCircuitOpenCode)
	}
}

// TestProviderCircuitOpen_ResponsesFace_Returns503WithDedicatedCode pins the
// same verdict on /v1/responses.
func TestProviderCircuitOpen_ResponsesFace_Returns503WithDedicatedCode(t *testing.T) {
	h, sink := newShapeRejectHandler(t, circuitOpenExecError())
	srv := httptest.NewServer(NewResponsesHandler(h))
	defer srv.Close()

	body := `{"model":"gpt-4o","input":"hi"}`
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

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got != "42" {
		t.Errorf("Retry-After = %q, want 42", got)
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
	if env.Error.Code != ProviderCircuitOpenCode {
		t.Errorf("error.code = %q, want %q", env.Error.Code, ProviderCircuitOpenCode)
	}

	kinds := sink.errorKinds()
	if len(kinds) == 0 {
		t.Fatal("no request-log entry was emitted")
	}
	if kinds[0] != ProviderCircuitOpenCode {
		t.Errorf("error_kind = %v, want %q", kinds, ProviderCircuitOpenCode)
	}
}

// ---------------------------------------------------------------------------
// 3. 阴性对照：非熔断的耗尽保持原样（零回归面）
// ---------------------------------------------------------------------------

// TestProviderCircuitOpen_NegativeControl_TransientStillModelNotFound pins
// that a non-circuit exhaustion keeps its pre-existing 503 model_not_found
// mapping — the dedicated code must not swallow other causes.
func TestProviderCircuitOpen_NegativeControl_TransientStillModelNotFound(t *testing.T) {
	h, sink := newShapeRejectHandler(t, &executors.ExecuteError{
		LastKind:  errorsx.KindTransient,
		Exhausted: true,
		Tried:     2,
		LastErr:   context.DeadlineExceeded,
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
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
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if env.Error.Code == ProviderCircuitOpenCode {
		t.Fatal("a transient exhaustion must NOT be labeled provider_circuit_open")
	}
	kinds := sink.errorKinds()
	if len(kinds) == 0 {
		t.Fatal("no request-log entry was emitted")
	}
	if kinds[0] == ProviderCircuitOpenCode {
		t.Errorf("error_kind = %v, want the pre-existing classification for a transient exhaustion", kinds)
	}
}
