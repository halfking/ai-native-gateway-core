package executors

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/identity"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestSoleCandidate_FailOpenOnCircuitOpen pins the 2026-08-08 P0 fix at
// executor.go:2380-2410: when the router returns ONE candidate AND its
// circuit breaker is OPEN, the executor must fail-open and still attempt
// the upstream. Skipping on circuit produces an immediate 503 for
// sole-candidate models (apiclaude/apigpt have exactly one routable
// sibling after manual_disabled tightened the candidate pool).
func TestSoleCandidate_FailOpenOnCircuitOpen(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n"))
	}))
	defer upstream.Close()

	exec := newOverloadTestExecutor()
	cand := overloadTestCandidate(upstream.URL)
	exec.Circuit.RecordFailure(cand.ProviderID, cand.CredentialID, errorsx.KindTransient)
	exec.Circuit.RecordFailure(cand.ProviderID, cand.CredentialID, errorsx.KindTransient)
	if exec.Circuit.Allow(cand.ProviderID, cand.CredentialID) {
		t.Fatal("setup: circuit must be OPEN after 2 transient failures")
	}

	_, _ = exec.Execute(&ExecParams{
		W:              httptest.NewRecorder(),
		R:              httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:      []byte(`{"model":"gpt-5.6-luna","messages":[],"stream":true}`),
		IsStream:       true,
		ClientProtocol: "openai-completions",
		ClientModel:    "gpt-5.6-luna",
		ClientID:       identity.ClientIdentity{IdentityHash: "test"},
		Candidates:     []provider.Candidate{cand},
		Policy:         &provider.Policy{TierFallbackMax: 4, RetryPerCredential: 0},
	})

	if got := calls.Load(); got == 0 {
		t.Fatal("sole-candidate fail-open: upstream was never called; circuit-open skip swallowed the only candidate")
	}
}
