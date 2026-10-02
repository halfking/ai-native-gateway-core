package streaming

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/middleware"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

// budgetFailClosedVerifier 让 CheckBudget 返回可注入的错误，用于钉死
// 「非 BudgetExceeded 错误 ≠ 放行」这条语义（12h 审计第二十九轮承继项：
// 旧代码把 DB 故障当成「没超预算」静默放行，预算闸门 fail-open）。
type budgetFailClosedVerifier struct {
	key       *authentication.KeyInfo
	budgetErr error
}

func (v budgetFailClosedVerifier) Enabled() bool { return true }
func (v budgetFailClosedVerifier) Verify(context.Context, string) (*authentication.KeyInfo, error) {
	return v.key, nil
}
func (v budgetFailClosedVerifier) VerifyByID(context.Context, int) (*authentication.KeyInfo, error) {
	return v.key, nil
}
func (v budgetFailClosedVerifier) CheckBudget(context.Context, int) error { return v.budgetErr }
func (v budgetFailClosedVerifier) LookupKeyMeta(context.Context, string) (*authentication.KeyLookupMeta, error) {
	return nil, nil
}

// DB 故障等未知错误必须 503 fail-closed，绝不放行到执行器。
func TestBudgetCheckUnknownErrorFailsClosedChat(t *testing.T) {
	capture := &correlationCaptureExecutor{}
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	h.setRequestKeyVerifierForTest(budgetFailClosedVerifier{
		key:       &authentication.KeyInfo{ID: 42, TenantID: "tenant-1", ApplicationID: 7},
		budgetErr: errors.New("db connection refused"),
	})
	h.executor = &executors.Executor{}
	h.provider = durableEndpointResolver{}
	h.SetRequestSurvival(func(string) bool { return true }, SurvivalOptions{})
	h.survivalAttemptExec = capture

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	rec := httptest.NewRecorder()
	middleware.NewRequestIDMiddleware().Wrap(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 fail-closed; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "budget_unavailable") {
		t.Fatalf("body missing budget_unavailable: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "insufficient_quota") {
		t.Fatalf("unknown error must not render as budget exhausted: %s", rec.Body.String())
	}
	if capture.params != nil {
		t.Fatal("request must be rejected before the executor runs")
	}
}

// 对照组：真正的超预算仍然 402，未被 fail-closed 改写。
func TestBudgetExceededStillReturns402Chat(t *testing.T) {
	capture := &correlationCaptureExecutor{}
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	h.setRequestKeyVerifierForTest(budgetFailClosedVerifier{
		key:       &authentication.KeyInfo{ID: 42, TenantID: "tenant-1", ApplicationID: 7},
		budgetErr: &authentication.BudgetExceededError{KeyID: 42, Budget: 1, Spent: 2},
	})
	h.executor = &executors.Executor{}
	h.provider = durableEndpointResolver{}
	h.SetRequestSurvival(func(string) bool { return true }, SurvivalOptions{})
	h.survivalAttemptExec = capture

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	rec := httptest.NewRecorder()
	middleware.NewRequestIDMiddleware().Wrap(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "insufficient_quota") {
		t.Fatalf("body missing insufficient_quota: %s", rec.Body.String())
	}
	if capture.params != nil {
		t.Fatal("exhausted budget must be rejected before the executor runs")
	}
}

// embeddings 面同款语义：未知错误 503 fail-closed。
func TestBudgetCheckUnknownErrorFailsClosedEmbeddings(t *testing.T) {
	h := NewEmbeddingsHandler(&embeddingResolverStub{}, upstream.New())
	h.keyVerifier = budgetFailClosedVerifier{
		key:       &authentication.KeyInfo{ID: 42, TenantID: "tenant-1"},
		budgetErr: errors.New("db connection refused"),
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
		strings.NewReader(`{"model":"text-embedding-3-small","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 fail-closed; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "budget_unavailable") {
		t.Fatalf("body missing budget_unavailable: %s", rec.Body.String())
	}
}

// 对照组：embeddings 面真正超预算仍 402。
func TestBudgetExceededStillReturns402Embeddings(t *testing.T) {
	h := NewEmbeddingsHandler(&embeddingResolverStub{}, upstream.New())
	h.keyVerifier = budgetFailClosedVerifier{
		key:       &authentication.KeyInfo{ID: 42, TenantID: "tenant-1"},
		budgetErr: &authentication.BudgetExceededError{KeyID: 42, Budget: 1, Spent: 2},
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
		strings.NewReader(`{"model":"text-embedding-3-small","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "insufficient_quota") {
		t.Fatalf("body missing insufficient_quota: %s", rec.Body.String())
	}
}
