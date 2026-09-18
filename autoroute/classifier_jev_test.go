package autoroute

// classifier_jev_test.go — TypeSafe Jev fallback classifier tests.
// All network traffic stays on httptest servers; the production endpoint
// is never contacted. The fail-open contract itself (decider keeps the
// heuristic result when Classify errors) is pinned by
// TestDecide_FallbackError_KeepsHeuristic in decision_test.go (R44 补钉，
// 此前该分支零覆盖且本头注指向不实); here we pin that Classify errors on
// every failure mode.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// jevTestClassifier builds a classifier pointed at an httptest server.
func jevTestClassifier(t *testing.T, handler http.HandlerFunc) (*JevClassifier, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &JevClassifier{
		endpoint: srv.URL + "/v1/systemone",
		apiKey:   "ts-test-key",
		model:    jevDefaultModel,
		timeout:  2 * time.Second,
		http:     srv.Client(),
	}, srv
}

// TestJevClassifier_Success pins the happy path: request shape (endpoint
// path, auth header, one choice question over the full allowlist) and
// the Classification produced from the choice answer.
func TestJevClassifier_Success(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any

	clf, _ := jevTestClassifier(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model": "jev-latest",
			"answers": {
				"task": {
					"type": "choice",
					"choice": "code",
					"probabilities": {"code": 0.82, "agent": 0.11, "chat": 0.07},
					"confidence": 0.91
				}
			}
		}`))
	})

	sigs := ClassificationSignals{
		SystemPrompt:   "You are a coding assistant",
		LastUserPrompt: "fix the nil pointer in main.go",
		ToolCount:      1,
		HasCodeBlock:   true,
	}
	cls, err := clf.Classify(context.Background(), sigs)
	if err != nil {
		t.Fatalf("Classify returned error: %v", err)
	}
	if gotPath != "/v1/systemone" {
		t.Errorf("endpoint path = %q, want /v1/systemone", gotPath)
	}
	if gotAuth != "Bearer ts-test-key" {
		t.Errorf("Authorization = %q, want bearer key", gotAuth)
	}
	questions, _ := gotBody["questions"].(map[string]any)
	task, ok := questions["task"].(map[string]any)
	if !ok {
		t.Fatalf("request missing questions.task: %s", string(mustJSON(t, gotBody)))
	}
	if task["type"] != "choice" {
		t.Errorf("question type = %v, want choice", task["type"])
	}
	criteria, _ := task["criteria"].(map[string]any)
	if len(criteria) != len(AllTaskTypes) {
		t.Errorf("choice criteria has %d options, want %d (one per allowlist task type)", len(criteria), len(AllTaskTypes))
	}

	if cls.Primary != TaskCode {
		t.Errorf("Primary = %q, want code", cls.Primary)
	}
	if cls.Classifier != "jev" {
		t.Errorf("Classifier = %q, want jev", cls.Classifier)
	}
	if cls.Confidence < 0.9 || cls.Confidence > 0.92 {
		t.Errorf("Confidence = %v, want ~0.91 from answer payload", cls.Confidence)
	}
	if len(cls.Secondary) == 0 || cls.Secondary[0].Task != TaskAgent {
		t.Errorf("Secondary[0] = %+v, want agent (second-highest probability)", cls.Secondary)
	}
}

// TestJevClassifier_StateCappedAndCarriesSignals pins the privacy
// envelope: bounded system/user text in the state payload plus the
// structural signals as named fields.
func TestJevClassifier_StateCappedAndCarriesSignals(t *testing.T) {
	var gotState map[string]any

	clf, _ := jevTestClassifier(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req jevRequest
		_ = json.Unmarshal(body, &req)
		stateBytes, _ := json.Marshal(req.State)
		_ = json.Unmarshal(stateBytes, &gotState)
		_, _ = w.Write([]byte(`{"answers":{"task":{"type":"choice","choice":"chat","probabilities":{"chat":0.6},"confidence":0.5}}}`))
	})

	long := strings.Repeat("x", llmFallbackUserPromptLimit+500)
	sigs := ClassificationSignals{
		SystemPrompt:    strings.Repeat("s", llmFallbackSystemPromptLimit+500),
		LastUserPrompt:  long,
		MessageCount:    4,
		EstimatedTokens: 9000,
		ToolCount:       2,
		HasToolResults:  true,
		ClientType:      "cursor",
		Language:        "zh",
	}
	if _, err := clf.Classify(context.Background(), sigs); err != nil {
		t.Fatalf("Classify returned error: %v", err)
	}
	if got, ok := gotState["user_prompt"].(string); ok && len(got) > llmFallbackUserPromptLimit+32 {
		t.Errorf("user_prompt state length %d exceeds cap %d", len(got), llmFallbackUserPromptLimit+32)
	}
	if got, ok := gotState["system_prompt"].(string); ok && len(got) > llmFallbackSystemPromptLimit+32 {
		t.Errorf("system_prompt state length %d exceeds cap %d", len(got), llmFallbackSystemPromptLimit+32)
	}
	if gotState["client_type"] != "cursor" || gotState["language"] != "zh" {
		t.Errorf("state missing structural signals: %v", gotState)
	}
	if gotState["has_tool_results"] != true {
		t.Errorf("has_tool_results = %v, want true", gotState["has_tool_results"])
	}
}

// TestJevClassifier_FailModes pins that every failure surfaces as an
// error (decider fail-open keeps the heuristic result — never a guess).
func TestJevClassifier_FailModes(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"http_500", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}},
		{"missing_answer", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"answers":{}}`))
		}},
		{"unknown_task", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"answers":{"task":{"type":"choice","choice":"quantum_gravity","probabilities":{"quantum_gravity":1.0},"confidence":0.9}}}`))
		}},
		{"bad_json", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`not-json`))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clf, _ := jevTestClassifier(t, tc.handler)
			cls, err := clf.Classify(context.Background(), ClassificationSignals{LastUserPrompt: "hi"})
			if err == nil {
				t.Fatalf("expected error, got classification %+v", cls)
			}
		})
	}
}

// TestJevClassifier_BreakerOpens pins the 5-failure / 30s breaker: the
// 6th call fails fast without touching the server.
func TestJevClassifier_BreakerOpens(t *testing.T) {
	var hits int
	clf, _ := jevTestClassifier(t, func(w http.ResponseWriter, _ *http.Request) {
		hits++
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	for i := 0; i < jevBreakerFailures; i++ {
		if _, err := clf.Classify(context.Background(), ClassificationSignals{}); err == nil {
			t.Fatalf("call %d: expected failure", i+1)
		}
	}
	if hits != jevBreakerFailures {
		t.Fatalf("server hits = %d, want %d", hits, jevBreakerFailures)
	}
	if _, err := clf.Classify(context.Background(), ClassificationSignals{}); !errors.Is(err, ErrLLMCircuitOpen) {
		t.Fatalf("6th call err = %v, want ErrLLMCircuitOpen without a server hit", err)
	}
	if hits != jevBreakerFailures {
		t.Errorf("server hits after open = %d, want still %d (breaker must short-circuit)", hits, jevBreakerFailures)
	}
}

// TestBuildJevClassifierFromEnv_Gating pins the default-OFF contract and
// the required-pair rule: gate without API key stays off, missing gate
// with API key stays off.
func TestBuildJevClassifierFromEnv_Gating(t *testing.T) {
	env := func(kv map[string]string) func(string) string {
		return func(k string) string { return kv[k] }
	}

	if clf, ok := BuildJevClassifierFromEnv(env(map[string]string{})); ok {
		t.Fatalf("default env must not enable Jev, got %+v", clf)
	}
	if clf, ok := BuildJevClassifierFromEnv(env(map[string]string{
		"LLM_GATEWAY_JEV_CLASSIFIER": "1",
	})); ok {
		t.Fatalf("gate without TYPESAFE_API_KEY must not enable Jev, got %+v", clf)
	}
	if clf, ok := BuildJevClassifierFromEnv(env(map[string]string{
		"TYPESAFE_API_KEY": "ts-k",
	})); ok {
		t.Fatalf("API key without gate must not enable Jev, got %+v", clf)
	}

	clf, ok := BuildJevClassifierFromEnv(env(map[string]string{
		"LLM_GATEWAY_JEV_CLASSIFIER": "enabled",
		"TYPESAFE_API_KEY":           "ts-k",
		"LLM_GATEWAY_JEV_MODEL":      "jev-1.13.0",
		"LLM_GATEWAY_JEV_TIMEOUT":    "1.5",
	}))
	if !ok {
		t.Fatal("gate + key must enable Jev")
	}
	if clf.Name() != "jev" {
		t.Errorf("Name = %q, want jev", clf.Name())
	}
	if clf.endpoint != jevDefaultBaseURL+"/v1/systemone" {
		t.Errorf("endpoint = %q, want default base + /v1/systemone", clf.endpoint)
	}
	if clf.model != "jev-1.13.0" {
		t.Errorf("model = %q, want jev-1.13.0", clf.model)
	}
	if clf.timeout != 1500*time.Millisecond {
		t.Errorf("timeout = %v, want 1.5s", clf.timeout)
	}

	base, ok := BuildJevClassifierFromEnv(env(map[string]string{
		"LLM_GATEWAY_JEV_CLASSIFIER": "true",
		"TYPESAFE_API_KEY":           "ts-k",
		"TYPESAFE_BASE_URL":          "http://127.0.0.1:1/",
	}))
	if !ok || !strings.HasPrefix(base.endpoint, "http://127.0.0.1:1") {
		t.Errorf("TYPESAFE_BASE_URL override not honored: %+v", base)
	}

	// Timeout clamp: misconfig must not push past jevMaxTimeout.
	clamped, ok := BuildJevClassifierFromEnv(env(map[string]string{
		"LLM_GATEWAY_JEV_CLASSIFIER": "true",
		"TYPESAFE_API_KEY":           "ts-k",
		"LLM_GATEWAY_JEV_TIMEOUT":    "999",
	}))
	if !ok || clamped.timeout != jevMaxTimeout {
		t.Errorf("timeout clamp failed: got %v, want %v", clamped.timeout, jevMaxTimeout)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
