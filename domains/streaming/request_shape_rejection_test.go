package streaming

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// request_shape_rejection_test.go — 2026-11-11（claude-fable-5-2 事故轮 ·
// 第三层）：请求**形态**拒绝（anthropic-messages 格式门 fail-closed ⇒
// KindClientBug）必须回 **4xx**，而不是 503「All providers unavailable」。
//
// 缺口本体（211 真机实测，2.5.9-532f5fa2 在役版本）：
//
//	POST /v1/messages {"model":"claude-fable-5-2","max_tokens":1024,
//	                    "messages":[{"content":"no role here"}]}
//	⇒ 503 {"error":{"message":"All providers unavailable","type":"overloaded_error"}}
//	⇒ request_logs.error_kind = model_not_found
//
// ★ 根因不是 kind 丢失（executor 层 kind 是对的：client_bug），而是
// **协议面没接**：2026-11-10 的 P1-B 审计只给 chat 面（handler.go）加了
// KindClientBug 分支，anthropic-messages 面（messages.go）与 responses 面
// （responses.go）没有 ⇒ 两面都落到通用兜底 503。
//
// ⇒ 本测试必须覆盖到**协议面**这一层（只测判据函数抓不到「某个面忘了
// 接线」——那正是本缺口的形态）。

// ---------------------------------------------------------------------------
// stubs
// ---------------------------------------------------------------------------

// shapeRejectResolver returns exactly one candidate so the request actually
// reaches the executor. A zero-candidate resolver would short-circuit in the
// face's own no-candidate branch (its own 503) before the executor runs —
// which would make the test pass for the wrong reason.
type shapeRejectResolver struct{}

func (shapeRejectResolver) Enabled() bool { return true }

func (shapeRejectResolver) GetCandidates(context.Context, string, string, string) ([]provider.Candidate, *provider.Policy, error) {
	return shapeRejectCandidates(), nil, nil
}

func (shapeRejectResolver) GetCandidatesByModality(context.Context, string, string, string, string) ([]provider.Candidate, *provider.Policy, error) {
	return shapeRejectCandidates(), nil, nil
}

func (shapeRejectResolver) ModelKnown(context.Context, string) bool { return true }

func (shapeRejectResolver) ModelModalityMismatch(context.Context, string, string, string, string) (*provider.ModalityMismatch, error) {
	return nil, nil
}

func shapeRejectCandidates() []provider.Candidate {
	return []provider.Candidate{{
		CredentialID:     31,
		ProviderID:       587,
		Protocol:         "anthropic-messages",
		RawModel:         "claude-fable-5-2",
		StandardizedName: "claude-fable-5-2",
	}}
}

// shapeRejectKeyVerifier accepts any "sk-test" key.
type shapeRejectKeyVerifier struct{}

func (shapeRejectKeyVerifier) Enabled() bool { return true }
func (shapeRejectKeyVerifier) Verify(_ context.Context, rawKey string) (*authentication.KeyInfo, error) {
	return &authentication.KeyInfo{ID: 1, TenantID: "tenant-shape-reject", ApplicationID: 1}, nil
}
func (shapeRejectKeyVerifier) VerifyByID(context.Context, int) (*authentication.KeyInfo, error) {
	return &authentication.KeyInfo{ID: 1, TenantID: "tenant-shape-reject", ApplicationID: 1}, nil
}
func (shapeRejectKeyVerifier) CheckBudget(context.Context, int) error { return nil }
func (shapeRejectKeyVerifier) LookupKeyMeta(context.Context, string) (*authentication.KeyLookupMeta, error) {
	return nil, nil
}

// shapeRejectLogSink captures the emitted request-log entries so the test can
// assert the **persisted** error_kind (the operator-facing classification),
// not just the HTTP status.
type shapeRejectLogSink struct {
	mu      sync.Mutex
	entries []*telemetry.RequestLogEntry
}

func (s *shapeRejectLogSink) add(e *telemetry.RequestLogEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, e)
}

func (s *shapeRejectLogSink) errorKinds() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.entries))
	for _, e := range s.entries {
		if e == nil || e.ErrorKind == nil {
			continue
		}
		out = append(out, *e.ErrorKind)
	}
	return out
}

func newShapeRejectHandler(t *testing.T, execErr error) (*ChatHandler, *shapeRejectLogSink) {
	t.Helper()
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	// The face gates on h.executor != nil; the override below short-circuits
	// the real dispatch pipeline with the synthetic failure.
	h.executor = &executors.Executor{}
	h.setRequestKeyVerifierForTest(shapeRejectKeyVerifier{})
	h.provider = shapeRejectResolver{}
	h.setExecutorOverrideForTest(func(*executors.ExecParams) (*executors.ExecuteResult, error) {
		return nil, execErr
	})
	sink := &shapeRejectLogSink{}
	h.SetRequestLogHook(sink.add)
	return h, sink
}

// ---------------------------------------------------------------------------
// 1. 判据函数（纯函数层）
// ---------------------------------------------------------------------------

func TestRequestShapeRejection_ClientBugIsRequestShape(t *testing.T) {
	status, errType, msg, code, ok := requestShapeRejection(&executors.ExecuteError{
		LastKind:  errorsx.KindClientBug,
		Exhausted: true,
		LastErr:   context.DeadlineExceeded,
	})
	if !ok {
		t.Fatal("requestShapeRejection must recognize KindClientBug as a request-shape rejection")
	}
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
	if errType != "invalid_request_error" {
		t.Errorf("errType = %q, want invalid_request_error", errType)
	}
	if code != "client_bug" {
		t.Errorf("code = %q, want client_bug", code)
	}
	if !strings.Contains(msg, "request rejected") {
		t.Errorf("message = %q, want it to say the request was rejected", msg)
	}
}

// TestRequestShapeRejection_NotExhaustedStillRejected pins that the
// "request shape" verdict does NOT depend on the candidate set having been
// exhausted — a malformed request is malformed on the first candidate too.
func TestRequestShapeRejection_NotExhaustedStillRejected(t *testing.T) {
	_, _, _, _, ok := requestShapeRejection(&executors.ExecuteError{
		LastKind:  errorsx.KindClientBug,
		Exhausted: false,
	})
	if !ok {
		t.Fatal("client_bug must be rejected regardless of Exhausted")
	}
}

// TestRequestShapeRejection_NegativeControls pins that every other kind keeps
// the pre-existing path (zero regression surface).
func TestRequestShapeRejection_NegativeControls(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"transient", &executors.ExecuteError{LastKind: errorsx.KindTransient, Exhausted: true}},
		{"network", &executors.ExecuteError{LastKind: errorsx.KindNetwork, Exhausted: true}},
		{"auth", &executors.ExecuteError{LastKind: errorsx.KindAuth, Exhausted: true}},
		{"quota", &executors.ExecuteError{LastKind: errorsx.KindQuota, Exhausted: true}},
		{"model_not_found", &executors.ExecuteError{LastKind: errorsx.KindModelNotFound, Exhausted: true}},
		{"rate_limit", &executors.ExecuteError{LastKind: errorsx.KindRateLimit, Exhausted: true}},
		// content_filter has its own 400 branch on both faces — the shape
		// verdict must NOT hijack it.
		{"content_filter", &executors.ExecuteError{LastKind: errorsx.KindContentFilter, Exhausted: true}},
		{"nil", nil},
		{"non_execute_error", context.Canceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, _, _, ok := requestShapeRejection(tc.err); ok {
				t.Fatalf("%s must NOT be treated as a request-shape rejection", tc.name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 2. 协议面（端到端层）—— 这一层才是本缺口的所在
// ---------------------------------------------------------------------------

// TestRequestShapeRejection_AnthropicFace_Returns400 is the钉测 for the
// original gap: /v1/messages must answer 400 (not 503) and persist
// error_kind=client_bug (not model_not_found).
func TestRequestShapeRejection_AnthropicFace_Returns400(t *testing.T) {
	h, sink := newShapeRejectHandler(t, &executors.ExecuteError{
		LastKind:  errorsx.KindClientBug,
		Exhausted: true,
		LastErr:   context.DeadlineExceeded,
	})
	srv := httptest.NewServer(NewMessagesHandler(h))
	defer srv.Close()

	body := `{"model":"claude-fable-5-2","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`
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
		t.Fatalf("status = %d, want 400 (a permanently malformed request must not be reported as upstream overload)", resp.StatusCode)
	}
	if kind := resp.Header.Get("X-Gateway-Last-Kind"); kind != "client_bug" {
		t.Errorf("X-Gateway-Last-Kind = %q, want client_bug", kind)
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
		t.Errorf("error.type = %q, want invalid_request_error", env.Error.Type)
	}
	if !strings.Contains(env.Error.Message, "request rejected") {
		t.Errorf("error.message = %q, want it to say the request was rejected", env.Error.Message)
	}

	// The persisted classification must be client_bug — the operator-facing
	// column, not just the HTTP body.
	kinds := sink.errorKinds()
	if len(kinds) == 0 {
		t.Fatal("no request-log entry was emitted")
	}
	for _, k := range kinds {
		if k == "model_not_found" {
			t.Fatalf("error_kind must not be the misleading model_not_found; got %v", kinds)
		}
	}
	if kinds[0] != "client_bug" {
		t.Errorf("error_kind = %v, want client_bug", kinds)
	}
}

// TestRequestShapeRejection_ResponsesFace_Returns400 pins the same verdict on
// the /v1/responses face (the second face that was missing the branch).
func TestRequestShapeRejection_ResponsesFace_Returns400(t *testing.T) {
	h, sink := newShapeRejectHandler(t, &executors.ExecuteError{
		LastKind:  errorsx.KindClientBug,
		Exhausted: true,
		LastErr:   context.DeadlineExceeded,
	})
	srv := httptest.NewServer(NewResponsesHandler(h))
	defer srv.Close()

	body := `{"model":"claude-fable-5-2","input":"hi"}`
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
	if kind := resp.Header.Get("X-Gateway-Last-Kind"); kind != "client_bug" {
		t.Errorf("X-Gateway-Last-Kind = %q, want client_bug", kind)
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

// TestRequestShapeRejection_NegativeControl_TransientStill503 is the阴性对照:
// the pre-existing behaviour for a non-request-shape failure must be
// untouched (still 503 "All providers unavailable" + model_not_found).
func TestRequestShapeRejection_NegativeControl_TransientStill503(t *testing.T) {
	h, sink := newShapeRejectHandler(t, &executors.ExecuteError{
		LastKind:  errorsx.KindTransient,
		Exhausted: true,
		LastErr:   context.DeadlineExceeded,
	})
	srv := httptest.NewServer(NewMessagesHandler(h))
	defer srv.Close()

	body := `{"model":"claude-fable-5-2","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("x-api-key", "sk-test")
	req.Header.Set("content-type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (transient exhaustion keeps its pre-existing mapping)", resp.StatusCode)
	}
	kinds := sink.errorKinds()
	if len(kinds) == 0 {
		t.Fatal("no request-log entry was emitted")
	}
	if kinds[0] != "model_not_found" {
		t.Errorf("error_kind = %v, want model_not_found (unchanged for transient exhaustion)", kinds)
	}
}

// TestRequestShapeRejection_NegativeControl_ContentFilterKeepsOwnBranch pins
// that content_filter still renders through its own 400 branch (and is not
// swallowed by the shape verdict).
func TestRequestShapeRejection_NegativeControl_ContentFilterKeepsOwnBranch(t *testing.T) {
	h, sink := newShapeRejectHandler(t, &executors.ExecuteError{
		LastKind:  errorsx.KindContentFilter,
		Exhausted: true,
		LastErr:   context.DeadlineExceeded,
	})
	srv := httptest.NewServer(NewMessagesHandler(h))
	defer srv.Close()

	body := `{"model":"claude-fable-5-2","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("x-api-key", "sk-test")
	req.Header.Set("content-type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (content_filter has always been a 400)", resp.StatusCode)
	}
	if kind := resp.Header.Get("X-Gateway-Last-Kind"); kind != "" && kind != "client_bug" {
		t.Errorf("X-Gateway-Last-Kind = %q, want it unset or client_bug", kind)
	}
	kinds := sink.errorKinds()
	for _, k := range kinds {
		if k == "client_bug" {
			t.Fatalf("content_filter must not be classified as client_bug; got %v", kinds)
		}
	}
}
