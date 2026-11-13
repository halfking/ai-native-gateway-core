package executors

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/contextbudget"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func TestContextBudgetDiscoveredWindowGovernsRecoveryRetry(t *testing.T) {
	body, window := largeChatBody(), 1000000
	cand := provider.Candidate{CredentialID: 22, ProviderID: 32, RawModel: "minimax-m3", Protocol: "openai-completions", ContextWindow: &window}
	e := &Executor{EnforceContextBudget: true}
	params := newRecoveryParams()
	if e.handleContextLengthRecovery(t.Context(), params, cand, &body, &contextLengthRecoveryState{}, 400,
		[]byte(`{"error":{"message":"This model's maximum context length is 200 tokens."}}`)) != ctxLenRetry {
		t.Fatal("fixture must exercise a context recovery retry")
	}
	if _, err := contextbudget.Check(body, contextbudget.Spec{ContextWindow: 200}); err == nil {
		t.Fatal("fixture must exceed the discovered half-window")
	}
	if err := e.checkCandidateContextBudget(params, cand, "chat", body); !IsContextBudgetError(err) {
		t.Fatalf("recovery retry uses stale catalog window: bytes=%d err=%v", len(body), err)
	}
}

func TestContextBudgetDiscoveryIsSharedOnlyWithinExactRequestBucket(t *testing.T) {
	e := &Executor{EnforceContextBudget: true}
	params := newRecoveryParams()
	window := 1000000
	cand := provider.Candidate{CredentialID: 22, ProviderID: 32, RawModel: "model", Protocol: "chat", BaseURL: "https://one.invalid", ContextWindow: &window}
	// Dispatch copies parameters for each candidate, but discoveries must
	// survive those copies when a later attempt revisits the same bucket.
	// r1051（2026-11-13）：发现限额从 200 收到 64——半窗帽移除后 200-token
	// 窗的输入预算 = 200−预留，小夹具（est≈180）不再必拒；64 之下必拒。
	initContextBudgetWindows(params)
	copyParams := *params
	recordDiscoveredContextLimit(&copyParams, cand, 64)
	body := contextBudgetBody("important value=42")
	if err := e.checkCandidateContextBudget(params, cand, "chat", body); !IsContextBudgetError(err) {
		t.Fatal("discovery was lost across a dispatch parameter copy")
	}
	for _, change := range []func(*provider.Candidate){
		func(c *provider.Candidate) { c.CredentialID++ },
		func(c *provider.Candidate) { c.RawModel = "other" },
		func(c *provider.Candidate) { c.Protocol = "responses" },
		func(c *provider.Candidate) { c.BaseURL = "https://two.invalid" },
	} {
		other := cand
		change(&other)
		if err := e.checkCandidateContextBudget(params, other, "chat", body); err != nil {
			t.Fatalf("another bucket inherited the discovered limit: %+v: %v", other, err)
		}
	}
	if err := e.checkCandidateContextBudget(newRecoveryParams(), cand, "chat", body); err != nil {
		t.Fatalf("another request inherited local discovery: %v", err)
	}
}

func TestContextBudgetDiscoveryOnlyTightensAndIncludesSmallMismatch(t *testing.T) {
	e := &Executor{EnforceContextBudget: true}
	params := newRecoveryParams()
	cand := contextBudgetCandidate(1000000)
	body := largeChatBody()
	e.handleContextLengthRecovery(t.Context(), params, cand, &body, &contextLengthRecoveryState{}, 400,
		[]byte(`{"error":{"message":"This model's maximum context length is 980000 tokens."}}`))
	spec, _ := e.candidateBudgetSpec(params, cand)
	if spec.ContextWindow != 980000 {
		t.Fatalf("hard limit discarded a mismatch below the old 5%% heuristic: %d", spec.ContextWindow)
	}
	recordDiscoveredContextLimit(params, cand, 1200000)
	spec, _ = e.candidateBudgetSpec(params, cand)
	if spec.ContextWindow != 980000 {
		t.Fatalf("later discovery expanded the validated limit: %d", spec.ContextWindow)
	}
}

func TestContextBudgetDiscoveredHTTP400BlocksInvalidRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"This model's maximum context length is 200 tokens."}}`)
	}))
	defer srv.Close()
	body := largeChatBody()
	params := newRecoveryParams()
	params.W, params.BodyBytes = httptest.NewRecorder(), body
	params.ClientModel, params.Model, params.OutboundModel = "m", "m", "m"
	params.UpstreamAttempts = NewUpstreamAttemptBudget(5)
	cand := contextBudgetCandidate(1000000)
	cand.BaseURL, cand.Protocol, cand.RawModel = srv.URL, "openai-completions", "m"
	e := newTestExecutor()
	e.EnforceContextBudget = true
	_, err := e.executeOpenAI(params, cand, 2, time.Now(), nil)
	if !IsContextBudgetError(err) || calls.Load() != 1 || params.UpstreamAttempts.Used() != 1 {
		t.Fatalf("discovered limit did not stop retry: err=%v calls=%d attempts=%d", err, calls.Load(), params.UpstreamAttempts.Used())
	}
	if *cand.ContextWindow != 1000000 {
		t.Fatal("request-local discovery mutated the shared catalog candidate")
	}
}
