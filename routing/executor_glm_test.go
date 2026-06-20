package routing

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/kaixuan/llm-gateway-go/circuit"
	"github.com/kaixuan/llm-gateway-go/identity"
	"github.com/kaixuan/llm-gateway-go/limiter"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func TestPrepareRequestBody_GLM51_Zhipu(t *testing.T) {
	clientBody := `{"model":"glm-5.1","messages":[{"role":"user","content":"Reply OK"}],"max_tokens":8,"stream":false}`

	cand := provider.Candidate{
		CredentialID: 55,
		ProviderID:   7,
		BaseURL:      "https://open.bigmodel.cn/api/paas/v4",
		Protocol:     "openai-completions",
		CatalogCode:  "zhipu",
		RawModel:     "glm-5.1",
		Tier:         2,
	}

	params := &ExecParams{
		BodyBytes:      []byte(clientBody),
		ClientModel:    "glm-5.1",
		IsStream:       false,
		ClientProtocol: "openai-completions",
	}

	got := prepareRequestBody(params, cand)

	var obj map[string]interface{}
	if err := json.Unmarshal(got, &obj); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}

	if v, ok := obj["model"]; !ok || v != "glm-5.1" {
		t.Errorf("model field = %v, want glm-5.1 (Zhipu raw offer name)", v)
	}
}

func TestPrepareRequestBody_GLM51_Nvidia(t *testing.T) {
	// Client uses standardized name; nvidia-build offer keeps vendor prefix.
	clientBody := `{"model":"glm-5.1","messages":[{"role":"user","content":"Reply OK"}],"max_tokens":8}`

	cand := provider.Candidate{
		CredentialID: 175,
		ProviderID:   49,
		BaseURL:      "https://integrate.api.nvidia.com/v1",
		Protocol:     "openai-completions",
		CatalogCode:  "nvidia",
		RawModel:     "z-ai/glm-5.1",
		Tier:         2,
	}

	params := &ExecParams{
		BodyBytes:      []byte(clientBody),
		ClientModel:    "glm-5.1",
		IsStream:       false,
		ClientProtocol: "openai-completions",
	}

	got := prepareRequestBody(params, cand)

	var obj map[string]interface{}
	if err := json.Unmarshal(got, &obj); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}

	modelVal, _ := obj["model"].(string)
	if modelVal != "z-ai/glm-5.1" {
		t.Errorf("model sent to NVIDIA = %q, want z-ai/glm-5.1 (offer raw_model_name)", modelVal)
	}
}

func TestResolveOutboundModel_ExplicitTransformWins(t *testing.T) {
	params := &ExecParams{
		ClientModel:   "glm-5.1",
		OutboundModel: "custom-upstream-name",
	}
	cand := provider.Candidate{RawModel: "z-ai/glm-5.1"}

	got := resolveOutboundModel(params, cand)
	if got != "custom-upstream-name" {
		t.Errorf("resolveOutboundModel() = %q, want transform override", got)
	}
}

func TestExecute_GLM51_TriesThirdCandidateAfterTwoModelNotFound(t *testing.T) {
	// 2026-06-20: Internal retry for model_not_found means each credential
	// is tried twice (original + retry) before moving to the next candidate.
	// So with 3 credentials where first 2 fail with model_not_found:
	// - Credential 101: call 1 (fail) → retry call 2 (fail) → move to next
	// - Credential 102: call 3 (fail) → retry call 4 (fail) → move to next
	// - Credential 103: call 5 (success)
	// Total upstream calls: 5
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		t.Logf("Test server: call %d", call)
		// First 4 calls return model_not_found (2 per credential × 2 credentials)
		if call <= 4 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"model glm-5.1 not found"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","object":"chat.completion","model":"glm-5.1","choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop","index":0}]}`))
	}))
	defer server.Close()

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	policy := provider.DefaultPolicy()
	policy.RetryPerCredential = 0
	policy.TierFallbackMax = 3

	exec := NewExecutor(
		NewRouter(NewStickyCache(), limiter.New()),
		circuit.NewManager(),
		limiter.New(),
		pool.NewPoolManager(nil),
		nil,
		func(chunk []byte, isStream bool) []byte { return chunk },
		nil,
		nil,
	)
	result, err := exec.Execute(&ExecParams{
		W:              recorder,
		R:              req,
		BodyBytes:      []byte(`{"model":"glm-5.1","messages":[{"role":"user","content":"hi"}],"stream":false}`),
		ClientModel:    "glm-5.1",
		ClientID:       identity.ClientIdentity{IdentityHash: "test"},
		Candidates:     glm51Candidates(server.URL),
		Policy:         policy,
		ClientProtocol: "openai-completions",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result == nil {
		t.Fatal("Execute returned nil result")
	}
	// With internal retry: 2 calls per credential × 2 failing credentials + 1 success = 5
	if got := calls.Load(); got != 5 {
		t.Fatalf("upstream calls = %d, want 5", got)
	}
	if result.Candidate.CredentialID != 103 {
		t.Fatalf("credential_id = %d, want 103", result.Candidate.CredentialID)
	}
	if result.Trace == nil || len(result.Trace.PlannedCandidates) != 3 {
		t.Fatalf("planned candidates = %+v, want 3", result.Trace)
	}
	if len(result.Trace.BlockedCandidates) != 0 {
		t.Fatalf("blocked candidates = %+v, want no router-filtered candidates", result.Trace.BlockedCandidates)
	}
}

func glm51Candidates(baseURL string) []provider.Candidate {
	return []provider.Candidate{
		glm51Candidate(101, 1, baseURL, "glm-5.1", 1),
		glm51Candidate(102, 2, baseURL, "z-ai/glm-5.1", 2),
		glm51Candidate(103, 3, baseURL, "glm-5.1", 3),
	}
}

func glm51Candidate(credentialID, providerID int, baseURL, rawModel string, tier int) provider.Candidate {
	return provider.Candidate{
		CredentialID:      credentialID,
		ProviderID:        providerID,
		BaseURL:           baseURL,
		Protocol:          "openai-completions",
		CatalogCode:       "zhipu",
		RawModel:          rawModel,
		Tier:              tier,
		Weight:            100,
		BillingMode:       "token_plan",
		Routable:          true,
		LifecycleStatus:   "active",
		AvailabilityState: "ready",
		QuotaState:        "ok",
		CircuitState:      "closed",
		APIKey:            "sk-test",
	}
}
