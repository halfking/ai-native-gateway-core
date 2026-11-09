package executors_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/identity"
	"github.com/kaixuan/llm-gateway-go/domains/nodehealth"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/internal/contextbudget"
	"github.com/kaixuan/llm-gateway-go/internal/sessioncontext"
	"github.com/kaixuan/llm-gateway-go/provider"
)

const budgetPipelineTask = "Use the stored release 1047 facts. Return the exact path and total; do not invent values."

type budgetPipelineSeen struct {
	mu        sync.Mutex
	bodies    [][]byte
	auth      []string
	decisions []nodehealth.Decision
}

func (s *budgetPipelineSeen) ApplyNodeHealthDecision(_ context.Context, d nodehealth.Decision) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.decisions = append(s.decisions, d)
	return nil
}

func (s *budgetPipelineSeen) server(t *testing.T, failFirst bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		s.mu.Lock()
		s.bodies, s.auth = append(s.bodies, body), append(s.auth, r.Header.Get("Authorization"))
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if failFirst && r.Header.Get("Authorization") == "Bearer bucket-a" {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"rate limit exceeded"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"ok","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func budgetPipelineBody() []byte {
	msgs := []map[string]string{{"role": "system", "content": "Preserve exact facts."}, {"role": "user", "content": "Investigate the recorded deployment release."}}
	for i := 0; i < 80; i++ {
		msgs = append(msgs, map[string]string{"role": "assistant", "content": strings.Repeat("unrelated earlier transcript ", 60)})
		if i == 39 {
			msgs = append(msgs, map[string]string{"role": "assistant", "content": "release 1047: path=/srv/app/config.yaml; total=42.75"})
		}
	}
	msgs = append(msgs, map[string]string{"role": "user", "content": budgetPipelineTask})
	body, _ := json.Marshal(map[string]any{"model": "budget-model", "messages": msgs, "max_tokens": 512})
	return body
}

func budgetPipelineCandidate(url, key string, providerID, credentialID, window int) provider.Candidate {
	c := protocolCandidate(url, "openai-completions", "openai", "budget-model", key, providerID, credentialID)
	c.ContextWindow = &window
	return c
}

func budgetPipelineParams(t *testing.T, body []byte, candidates ...provider.Candidate) *executors.ExecParams {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)
	return &executors.ExecParams{W: httptest.NewRecorder(), R: httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body)).WithContext(ctx), BodyBytes: body,
		RequestID: t.Name(), TenantID: "default", Model: "budget-model", ClientModel: "budget-model", ClientProtocol: "openai-completions",
		ClientID: identity.ClientIdentity{IdentityHash: t.Name()}, Candidates: candidates,
		Policy: &provider.Policy{TierFallbackMax: 4, RetryPerCredential: 0}, DispatchAllowProviderChange: true,
		UpstreamAttempts: executors.NewUpstreamAttemptBudget(10)}
}

func TestContextBudgetPipelineRateLimitThenSmallerBucket(t *testing.T) {
	seen := &budgetPipelineSeen{}
	srv := seen.server(t, true)
	e := newProtocolFailoverExecutor(t)
	e.EnforceContextBudget, e.FitContext = true, sessioncontext.Fit
	body := budgetPipelineBody()
	before := append([]byte(nil), body...)
	params := budgetPipelineParams(t, body, budgetPipelineCandidate(srv.URL, "bucket-a", 9101, 91001, 1000000), budgetPipelineCandidate(srv.URL, "bucket-b", 9102, 91002, 64000))
	result, err := e.Execute(params)
	if err != nil || result == nil || result.Candidate.CredentialID != 91002 {
		t.Fatalf("result=%v err=%v", result, err)
	}
	seen.mu.Lock()
	defer seen.mu.Unlock()
	if len(seen.bodies) != 2 || seen.auth[0] != "Bearer bucket-a" || seen.auth[1] != "Bearer bucket-b" {
		t.Fatalf("physical call sequence=%v", seen.auth)
	}
	for i, window := range []int{1000000, 64000} {
		if _, err := contextbudget.Check(seen.bodies[i], contextbudget.Spec{ContextWindow: window}); err != nil {
			t.Fatalf("bucket %d actual wire budget: %v", i, err)
		}
		for _, fact := range []string{budgetPipelineTask, "/srv/app/config.yaml", "42.75"} {
			if !bytes.Contains(seen.bodies[i], []byte(fact)) {
				t.Fatalf("bucket %d lost protected task/relevant exact fact %q", i, fact)
			}
		}
	}
	if len(seen.bodies[1]) >= len(seen.bodies[0]) || !bytes.Equal(body, before) || !bytes.Equal(params.BodyBytes, before) || params.UpstreamAttempts.Used() != 2 {
		t.Fatal("smaller-bucket fit must shrink its own body, preserve the source, and meter exactly two physical calls")
	}
}

func TestContextBudgetPipelineLocalRefusalDoesNotPolluteHealth(t *testing.T) {
	seen := &budgetPipelineSeen{}
	srv := seen.server(t, false)
	e := newProtocolFailoverExecutor(t)
	e.EnforceContextBudget, e.FitContext, e.NodeHealthAdapter = true, sessioncontext.Fit, seen
	params := budgetPipelineParams(t, budgetPipelineBody(), budgetPipelineCandidate(srv.URL, "bucket-a", 9201, 92001, 200), budgetPipelineCandidate(srv.URL, "bucket-b", 9202, 92002, 1000000))
	result, err := e.Execute(params)
	if err != nil || result == nil || result.Candidate.CredentialID != 92002 {
		t.Fatalf("local capacity refusal did not switch: result=%v err=%v", result, err)
	}
	seen.mu.Lock()
	defer seen.mu.Unlock()
	if len(seen.auth) != 1 || seen.auth[0] != "Bearer bucket-b" || params.UpstreamAttempts.Used() != 1 {
		t.Fatalf("local refusal reached supplier/meter: auth=%v physical=%d", seen.auth, params.UpstreamAttempts.Used())
	}
	for _, d := range seen.decisions {
		if d.Node.CredentialID == 92001 {
			t.Fatalf("local refusal entered health authority: %+v", d)
		}
	}
	if stats := e.Circuit.Get(9201, 92001).Stats(); stats["total_failures"] != int32(0) {
		t.Fatalf("local refusal polluted breaker: %v", stats)
	}
}

func TestContextBudgetPipelineAllRefusedPreservesTypedError(t *testing.T) {
	seen := &budgetPipelineSeen{}
	srv := seen.server(t, false)
	e := newProtocolFailoverExecutor(t)
	e.EnforceContextBudget, e.FitContext, e.NodeHealthAdapter = true, sessioncontext.Fit, seen
	params := budgetPipelineParams(t, budgetPipelineBody(), budgetPipelineCandidate(srv.URL, "bucket-a", 9301, 93001, 200), budgetPipelineCandidate(srv.URL, "bucket-b", 9302, 93002, 400))
	result, err := e.Execute(params)
	if result != nil || !executors.IsContextBudgetError(err) {
		t.Fatalf("all-refused lost typed local error: result=%v err=%v", result, err)
	}
	seen.mu.Lock()
	defer seen.mu.Unlock()
	if len(seen.bodies) != 0 || len(seen.decisions) != 0 || params.UpstreamAttempts.Used() != 0 {
		t.Fatalf("all local refusals must be unmetered and unpenalized: calls=%d decisions=%d physical=%d", len(seen.bodies), len(seen.decisions), params.UpstreamAttempts.Used())
	}
}
