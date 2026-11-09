package executors

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestBumpReasoningBudget covers the helper's escalation ladder and its
// refusals (no cap field / non-numeric / at cap / no-op).
func TestBumpReasoningBudget(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		want   int64
		bumped bool
	}{
		{"small max_tokens doubles to 2048", `{"model":"m","max_tokens":60,"messages":[]}`, 2048, true},
		{"mid value doubles", `{"model":"m","max_tokens":3000,"messages":[]}`, 6000, true},
		{"cap at 32768", `{"model":"m","max_tokens":30000,"messages":[]}`, 32768, true},
		{"already at cap", `{"model":"m","max_tokens":32768,"messages":[]}`, 32768, false},
		{"no cap field untouched", `{"model":"m","messages":[]}`, 0, false},
		{"non-numeric untouched", `{"model":"m","max_tokens":"x","messages":[]}`, 0, false},
		{"max_completion_tokens honored", `{"model":"m","max_completion_tokens":100,"messages":[]}`, 2048, true},
	}
	for _, tc := range cases {
		out, bumped := bumpReasoningBudget([]byte(tc.body))
		if bumped != tc.bumped {
			t.Fatalf("%s: bumped=%t want %t", tc.name, bumped, tc.bumped)
		}
		if !tc.bumped {
			continue
		}
		var root struct {
			MaxTokens           int64 `json:"max_tokens"`
			MaxCompletionTokens int64 `json:"max_completion_tokens"`
		}
		if err := json.Unmarshal(out, &root); err != nil {
			t.Fatalf("%s: output not valid JSON: %v", tc.name, err)
		}
		got := root.MaxTokens
		if strings.Contains(tc.body, "max_completion_tokens") {
			got = root.MaxCompletionTokens
		}
		if got != tc.want {
			t.Fatalf("%s: max=%d want %d", tc.name, got, tc.want)
		}
	}
}

// TestExecutor_ReasoningBurnRetriesWithDoubledMaxTokens pins the end-to-end
// behavior: first attempt burns the whole completion budget on reasoning
// (finish_reason=length, reasoning==completion, zero visible content) →
// the executor retries the SAME candidate once with doubled max_tokens →
// the second attempt (which the stub answers with real content) succeeds —
// no empty_response failover, no all-candidates 503.
func TestExecutor_ReasoningBurnRetriesWithDoubledMaxTokens(t *testing.T) {
	var sentMax []int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxTokens int64 `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sentMax = append(sentMax, body.MaxTokens)
		w.Header().Set("Content-Type", "application/json")
		if body.MaxTokens <= 300 {
			// Reasoning burns the entire budget: zero visible content.
			_, _ = io.WriteString(w, `{"id":"c1","choices":[{"message":{"role":"assistant","content":""},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":300,"completion_tokens_details":{"reasoning_tokens":300}}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"c2","choices":[{"message":{"role":"assistant","content":"visible answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":40}}`)
	}))
	defer srv.Close()

	e := newTestExecutor()
	e.UpstreamTimeout = 5 * time.Second
	params := &ExecParams{
		R:             httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{}")),
		W:             httptest.NewRecorder(),
		BodyBytes:     []byte(`{"model":"m","max_tokens":300,"messages":[{"role":"user","content":"hi"}]}`),
		IsStream:      false,
		ClientModel:   "m",
		OutboundModel: "m",
		Model:         "m",
		RequestID:     "reasoning-burn",
	}
	cand := provider.Candidate{ProviderID: 14, CredentialID: 21, RawModel: "m", BaseURL: srv.URL, Protocol: "openai-completions", APIKey: "k"}

	res, err := e.executeOpenAI(params, cand, 2, time.Now(), nil)
	if err != nil || res == nil {
		t.Fatalf("executeOpenAI: result=%v err=%v", res, err)
	}
	if len(sentMax) != 2 || sentMax[0] != 300 || sentMax[1] != 2048 {
		t.Fatalf("sent max_tokens ladder = %v, want [300 2048]", sentMax)
	}
	if res.RequestBody != nil && !strings.Contains(string(res.RequestBody), "2048") {
		t.Fatalf("recorded request body lost the bumped cap: %s", res.RequestBody[:200])
	}
}

// TestExecutor_ReasoningBurnSecondBurnFallsOver pins the bound: a second burn
// on the bumped budget falls through to the existing empty failover semantics
// (KindEmptyResponse), it must not loop.
func TestExecutor_ReasoningBurnSecondBurnFallsOver(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c","choices":[{"message":{"role":"assistant","content":""},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":32000,"completion_tokens_details":{"reasoning_tokens":32000}}}`)
	}))
	defer srv.Close()

	e := newTestExecutor()
	e.UpstreamTimeout = 5 * time.Second
	params := &ExecParams{
		R:             httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{}")),
		W:             httptest.NewRecorder(),
		BodyBytes:     []byte(`{"model":"m","max_tokens":300,"messages":[{"role":"user","content":"hi"}]}`),
		IsStream:      false,
		ClientModel:   "m",
		OutboundModel: "m",
		Model:         "m",
		RequestID:     "reasoning-burn-loop",
	}
	cand := provider.Candidate{ProviderID: 14, CredentialID: 21, RawModel: "m", BaseURL: srv.URL, Protocol: "openai-completions", APIKey: "k"}

	_, err := e.executeOpenAI(params, cand, 2, time.Now(), nil)
	if err == nil {
		t.Fatal("second burn must surface the empty failover error")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty_response semantics, got: %v", err)
	}
}
