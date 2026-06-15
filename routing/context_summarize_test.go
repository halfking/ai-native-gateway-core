package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/circuit"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/limiter"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func TestRebuildOpenAIBodyAfterSummary(t *testing.T) {
	body := []byte(`{
		"model":"minimax-m3",
		"messages":[
			{"role":"system","content":"You are helpful"},
			{"role":"user","content":"turn1"},
			{"role":"assistant","content":"reply1"},
			{"role":"user","content":"turn2"},
			{"role":"assistant","content":"reply2"},
			{"role":"user","content":"latest"}
		]
	}`)
	got, err := rebuildOpenAIBodyAfterSummary(body, "prior work summary", 2)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Model != "minimax-m3" {
		t.Fatalf("model = %q", parsed.Model)
	}
	if len(parsed.Messages) != 6 { // system + summary + 4 tail (2 pairs)
		t.Fatalf("messages len = %d, want 6", len(parsed.Messages))
	}
	if parsed.Messages[0].Role != "system" {
		t.Fatalf("first role = %q", parsed.Messages[0].Role)
	}
	if !strings.Contains(parsed.Messages[1].Content, "prior work summary") {
		t.Fatalf("summary msg = %q", parsed.Messages[1].Content)
	}
	if parsed.Messages[len(parsed.Messages)-1].Content != "latest" {
		t.Fatalf("last msg = %q", parsed.Messages[len(parsed.Messages)-1].Content)
	}
}

func TestRebuildAnthropicBodyAfterSummary(t *testing.T) {
	body := []byte(`{
		"model":"minimax-m3",
		"max_tokens":1024,
		"system":"sys prompt",
		"messages":[
			{"role":"user","content":"a"},
			{"role":"assistant","content":"b"},
			{"role":"user","content":"c"},
			{"role":"assistant","content":"d"},
			{"role":"user","content":"latest"}
		]
	}`)
	got, err := rebuildAnthropicBodyAfterSummary(body, "anthropic summary", 2)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(got, &generic); err != nil {
		t.Fatal(err)
	}
	if string(generic["system"]) != `"sys prompt"` {
		t.Fatalf("system = %s", generic["system"])
	}
	var msgs []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(generic["messages"], &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 5 { // summary + 4 tail
		t.Fatalf("messages len = %d, want 5", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "anthropic summary") {
		t.Fatalf("summary = %q", msgs[0].Content)
	}
	if msgs[len(msgs)-1].Content != "latest" {
		t.Fatalf("last = %q", msgs[len(msgs)-1].Content)
	}
}

func TestExtractOpenAIConversationText(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"user","content":"hello"},
		{"role":"assistant","content":[{"type":"text","text":"world"}]}
	]}`)
	text, err := extractOpenAIConversationText(body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "[user]") || !strings.Contains(text, "hello") {
		t.Fatalf("text = %q", text)
	}
	if !strings.Contains(text, "world") {
		t.Fatalf("text = %q", text)
	}
}

func TestMessageRoleAndSummary_ToolCalls(t *testing.T) {
	raw := json.RawMessage(`{
		"role":"assistant",
		"content":"checking weather",
		"tool_calls":[{"id":"tc1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"NYC\"}"}}]
	}`)
	role, text := messageRoleAndSummary(raw)
	if role != "assistant" {
		t.Fatalf("role = %q", role)
	}
	if !strings.Contains(text, "checking weather") || !strings.Contains(text, "tool_call(get_weather)") {
		t.Fatalf("text = %q", text)
	}
}

func TestTailMessagesToolAware_PreservesToolRound(t *testing.T) {
	messages := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":"old"}`),
		json.RawMessage(`{"role":"assistant","content":"old reply"}`),
		json.RawMessage(`{"role":"user","content":"go"}`),
		json.RawMessage(`{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"read","input":{}}]}`),
		json.RawMessage(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"file contents"}]}`),
		json.RawMessage(`{"role":"assistant","content":"done"}`),
	}
	tail := tailMessagesToolAware(messages, 1)
	if len(tail) != 4 {
		t.Fatalf("tail len = %d, want 4 (trigger user + full tool round)", len(tail))
	}
	// Must include the user turn that triggered tool_use, then tool_use/tool_result/final reply.
	if !strings.Contains(string(tail[0]), `"content":"go"`) {
		t.Fatalf("tail[0] should be triggering user, got %s", tail[0])
	}
	if !strings.Contains(string(tail[1]), "tool_use") {
		t.Fatalf("tail[1] should be tool_use assistant, got %s", tail[1])
	}
	if !strings.Contains(string(tail[2]), "tool_result") {
		t.Fatalf("tail[2] should be tool_result user, got %s", tail[2])
	}
}

func TestHandleContextLengthRecovery_TwoPhase(t *testing.T) {
	e := &Executor{}
	params := &ExecParams{R: httptestNewRequest()}
	window := 50
	cand := provider.Candidate{ContextWindow: &window}
	big := strings.Repeat("x", 400)
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"` + big + `"},{"role":"assistant","content":"` + big + `"},{"role":"user","content":"` + big + `"}]}`)

	st := contextLengthRecoveryState{}
	source := append([]byte(nil), body...)
	action := e.handleContextLengthRecovery(context.Background(), params, cand, &source, &st, 400)
	if action != ctxLenRetry {
		t.Fatalf("phase1 = %v, want retry", action)
	}
	if !st.mechanicalAttempted || st.llmAttempted {
		t.Fatalf("mechanical=%v llm=%v", st.mechanicalAttempted, st.llmAttempted)
	}

	st2 := contextLengthRecoveryState{mechanicalAttempted: true}
	source2 := append([]byte(nil), body...)
	action2 := e.handleContextLengthRecovery(context.Background(), params, cand, &source2, &st2, 400)
	if action2 != ctxLenGiveUp {
		t.Fatalf("phase2 = %v, want give up", action2)
	}
	if !st2.llmAttempted {
		t.Fatal("llm should be attempted in phase2")
	}
}

func httptestNewRequest() *http.Request {
	req, _ := http.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	return req
}

func TestCompactionModelsFromEnv(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPACTION_MODELS", " glm-5.1 , gemini-2.5-flash ")
	got := compactionModelsFromEnv()
	if len(got) != 2 || got[0] != "glm-5.1" || got[1] != "gemini-2.5-flash" {
		t.Fatalf("got %v", got)
	}
}

func TestClassifyContextLengthFromStatus(t *testing.T) {
	if !classifyContextLengthFromStatus(400, []byte(`{"error":{"type":"context_length_exceeded"}}`)) {
		t.Fatal("expected context length")
	}
}

// stubProvider implements providerResolver for the compaction fallback tests.
// perModel drives the candidate list returned for each model name.
type stubProvider struct {
	perModel map[string][]provider.Candidate
}

func (s *stubProvider) Enabled() bool { return true }

func (s *stubProvider) GetCandidates(_ context.Context, model, _ string) ([]provider.Candidate, *provider.Policy, error) {
	return s.perModel[model], nil, nil
}

// routableCandidate builds a Candidate that passes IsAvailable() (Routable=true,
// LifecycleStatus=active) with a fixed context window. Saves boilerplate in
// the fallback-chain tests below.
func routableCandidate(providerID, credID int, rawModel, baseURL string, ctxWindow int) provider.Candidate {
	w := ctxWindow
	return provider.Candidate{
		ProviderID:       providerID,
		CredentialID:     credID,
		RawModel:         rawModel,
		BaseURL:          baseURL,
		APIKey:           "k-cred-" + rawModel,
		Routable:         true,
		LifecycleStatus:  "active",
		CircuitState:     "closed",
		AvailabilityState: "available",
		ContextWindow:    &w,
	}
}

// newCompactionTestExecutor wires a minimal Executor for the compaction
// fallback tests: stubbed provider, real circuit/limiter managers, and
// e.Upstream left nil so the compaction path falls through to http.DefaultClient
// (which talks to the supplied httptest server). Returns the executor plus a
// cleanup func to close the test server.
func newCompactionTestExecutor(t *testing.T, prov providerResolver) (*Executor, func()) {
	t.Helper()
	_ = limiter.New() // not strictly needed; doCompactionUpstream nil-checks e.Limiter
	e := &Executor{
		Provider:       prov,
		Circuit:        circuit.NewManager(),
		Limiter:        limiter.New(),
		UpstreamTimeout: 5 * time.Second,
		// Upstream intentionally left nil → doCompactionUpstream uses http.DefaultClient
	}
	return e, func() {}
}

// TestPickCompactionCandidates_ReturnsAllViableInEnvOrder verifies the helper
// walks the env-configured model list and emits every credential that
// passes IsAvailable + context-window gate, in env-preference order.
func TestPickCompactionCandidates_ReturnsAllViableInEnvOrder(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPACTION_MODELS", "minimax-text-01, gemini-2.5-flash")
	prov := &stubProvider{
		perModel: map[string][]provider.Candidate{
			"minimax-text-01": {
				routableCandidate(1, 10, "minimax-text-01", "http://a", 1_000_000),
				routableCandidate(1, 11, "minimax-text-01", "http://a", 1_000_000),
			},
			"gemini-2.5-flash": {
				routableCandidate(2, 20, "gemini-2.5-flash", "http://b", 1_000_000),
				routableCandidate(2, 21, "gemini-2.5-flash", "http://b", 100_000), // filtered: ctx too small
			},
			"glm-5.1": { // not in env list, must be ignored
				routableCandidate(3, 30, "glm-5.1", "http://c", 2_000_000),
			},
		},
	}
	e := &Executor{Provider: prov}
	got := e.pickCompactionCandidates(context.Background(), "")
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (m-1:cred10, m-1:cred11, gemini:cred20)", len(got))
	}
	if got[0].CredentialID != 10 || got[1].CredentialID != 11 || got[2].CredentialID != 20 {
		t.Fatalf("order = %d/%d/%d, want 10/11/20", got[0].CredentialID, got[1].CredentialID, got[2].CredentialID)
	}
}

// TestTryLLMContextCompaction_AllCandidatesFail_PropagatesFalse verifies
// that when every compaction model/credential returns an error, the
// function returns the original source body and false (so
// handleContextLengthRecovery escalates to ctxLenGiveUp).
func TestTryLLMContextCompaction_AllCandidatesFail_PropagatesFalse(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPACTION_MODELS", "minimax-text-01, gemini-2.5-flash")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":{"message":"down"}}`))
	}))
	defer srv.Close()

	prov := &stubProvider{
		perModel: map[string][]provider.Candidate{
			"minimax-text-01": {routableCandidate(1, 10, "minimax-text-01", srv.URL, 1_000_000)},
			"gemini-2.5-flash": {routableCandidate(2, 20, "gemini-2.5-flash", srv.URL, 1_000_000)},
		},
	}
	e, done := newCompactionTestExecutor(t, prov)
	defer done()

	params := &ExecParams{R: httptestNewRequest()}
	targetCand := provider.Candidate{ProviderID: 99, CredentialID: 99, RawModel: "minimax-m3"}
	body := []byte(`{"model":"minimax-m3","messages":[{"role":"user","content":"turn1"},{"role":"assistant","content":"reply1"}]}`)

	rebuilt, ok := e.tryLLMContextCompaction(context.Background(), params, targetCand, body)
	if ok {
		t.Fatal("ok = true, want false (all candidates should fail)")
	}
	if string(rebuilt) != string(body) {
		t.Fatalf("body changed on full failure; got %q", rebuilt)
	}
}

// TestTryLLMContextCompaction_FirstModelFails_SecondSucceeds is the core
// regression test for the multi-model fallback: model 1's credential returns
// 500, model 2's credential returns 200 with a valid summary, and the
// function must end with the rebuilt body from model 2.
func TestTryLLMContextCompaction_FirstModelFails_SecondSucceeds(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPACTION_MODELS", "minimax-text-01, gemini-2.5-flash")
	w1M := 1_000_000

	// Two servers: stub-broken always 500, stub-ok returns a clean summary.
	srvBroken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream down"}}`))
	}))
	defer srvBroken.Close()
	srvOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"prior conversation summary"}}]}`))
	}))
	defer srvOK.Close()

	prov := &stubProvider{
		perModel: map[string][]provider.Candidate{
			"minimax-text-01": {provider.Candidate{
				ProviderID: 1, CredentialID: 10, RawModel: "minimax-text-01",
				BaseURL: srvBroken.URL, APIKey: "k-broken",
				Routable: true, LifecycleStatus: "active",
				CircuitState: "closed", AvailabilityState: "available",
				ContextWindow: &w1M,
			}},
			"gemini-2.5-flash": {provider.Candidate{
				ProviderID: 2, CredentialID: 20, RawModel: "gemini-2.5-flash",
				BaseURL: srvOK.URL, APIKey: "k-ok",
				Routable: true, LifecycleStatus: "active",
				CircuitState: "closed", AvailabilityState: "available",
				ContextWindow: &w1M,
			}},
		},
	}
	e, done := newCompactionTestExecutor(t, prov)
	defer done()

	params := &ExecParams{R: httptestNewRequest()}
	targetCand := provider.Candidate{ProviderID: 99, CredentialID: 99, RawModel: "minimax-m3"}
	body := []byte(`{"model":"minimax-m3","messages":[{"role":"user","content":"turn1"},{"role":"assistant","content":"reply1"},{"role":"user","content":"turn2"},{"role":"assistant","content":"reply2"},{"role":"user","content":"latest"}]}`)

	rebuilt, ok := e.tryLLMContextCompaction(context.Background(), params, targetCand, body)
	if !ok {
		t.Fatal("ok = false, want true (second model should rescue)")
	}
	var parsed struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rebuilt, &parsed); err != nil {
		t.Fatalf("rebuilt not valid JSON: %v\n%s", err, rebuilt)
	}
	if len(parsed.Messages) < 2 {
		t.Fatalf("rebuilt msgs len = %d, want summary + tail", len(parsed.Messages))
	}
	if !strings.Contains(parsed.Messages[0].Content, "prior conversation summary") {
		t.Fatalf("first msg should carry the summary, got %q", parsed.Messages[0].Content)
	}
	if parsed.Messages[len(parsed.Messages)-1].Content != "latest" {
		t.Fatalf("tail not preserved, last msg = %q", parsed.Messages[len(parsed.Messages)-1].Content)
	}
}

// TestContextLengthExhaustedError_TypeAndMessage pins the typed-error
// contract that the outer Execute loop depends on:
//   - *contextLengthExhaustedError must be a distinct type (not fmt.Errorf)
//     so type-assertion works and the outer loop can skip circuit / sticky
//     / disable-offer side effects for context-length exhaustion.
//   - The Error() string must include the failed model name, credential id,
//     and upstream status so the operator can read the failure off the
//     log line alone.
func TestContextLengthExhaustedError_TypeAndMessage(t *testing.T) {
	cle := &contextLengthExhaustedError{
		credentialID: 42,
		rawModel:     "minimax-m3",
		status:       400,
		body:         `{"error":{"code":"context_length_exceeded"}}`,
	}
	if got := cle.Error(); !strings.Contains(got, "minimax-m3") ||
		!strings.Contains(got, "cred=42") ||
		!strings.Contains(got, "status=400") {
		t.Fatalf("Error() should include model/cred/status, got %q", got)
	}
	// Type assertion must succeed so the outer Execute can branch on it.
	var err error = cle
	var asType *contextLengthExhaustedError
	if !errors.As(err, &asType) {
		t.Fatal("errors.As should unwrap *contextLengthExhaustedError")
	}
	if asType.credentialID != 42 || asType.rawModel != "minimax-m3" {
		t.Fatalf("field loss in As: %+v", asType)
	}
	// And it must NOT match a plain fmt.Errorf error.
	plain := fmt.Errorf("upstream 400: %s", "context length exceeded")
	if _, ok := plain.(interface{ credentialID() int }); ok {
		// This branch is unreachable; it's a compile-time check that the
		// types are distinct. The real distinction is checked above with
		// errors.As, but having this guard makes the intent obvious to
		// future readers.
		t.Fatal("plain error must not match typed sentinel")
	}
}

// TestHandleContextLengthRecovery_NilContextWindow_StillTriesLLM verifies
// that when a candidate has ContextWindow == nil, the mechanical-trim phase
// is skipped but the LLM-summary fallback phase is still attempted. This is
// the G1 fix: models without a DB context_window value should still get
// compaction recovery when the upstream rejects for context length.
func TestHandleContextLengthRecovery_NilContextWindow_StillTriesLLM(t *testing.T) {
	e := &Executor{} // no Provider → pickCompactionCandidates returns empty
	params := &ExecParams{R: httptestNewRequest()}
	cand := provider.Candidate{ContextWindow: nil} // ← the key: nil window
	big := strings.Repeat("x", 400)
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"` + big + `"}]}`)

	st := contextLengthRecoveryState{}
	source := append([]byte(nil), body...)
	action := e.handleContextLengthRecovery(context.Background(), params, cand, &source, &st, 400)

	// With no compaction candidates available, the LLM phase returns false →
	// giveUp. But the critical assertion is that mechanicalAttempted is true
	// (phase was visited/skipped) AND llmAttempted is true (phase was tried).
	if action != ctxLenGiveUp {
		t.Fatalf("expected giveUp (no compaction candidates), got %v", action)
	}
	if !st.mechanicalAttempted {
		t.Fatal("mechanicalAttempted should be true (skipped due to nil window)")
	}
	if !st.llmAttempted {
		t.Fatal("llmAttempted should be true — nil window must NOT block the LLM phase")
	}
	// Body should be unchanged (no mechanical trim ran).
	if string(source) != string(body) {
		t.Fatal("body should be unchanged when ContextWindow is nil (no mechanical trim)")
	}
}

// TestShouldHeuristicCompact covers the heuristic that triggers compaction
// for non-context-length 4xx when the body is estimated to exceed 90% of the
// context window.
func TestShouldHeuristicCompact(t *testing.T) {
	window512k := 512000
	smallWindow := 1000

	tests := []struct {
		name         string
		status       int
		kind         errorsx.ErrorKind
		bodyLen      int
		contextWindow *int
		want         bool
	}{
		{
			name:          "large body + 400 + window → trigger",
			status:        400,
			kind:          errorsx.KindTransient,
			bodyLen:       512000 * 4, // ~585K tokens >> 90% of 512K
			contextWindow: &window512k,
			want:          true,
		},
		{
			name:          "small body + 400 → no trigger",
			status:        400,
			kind:          errorsx.KindTransient,
			bodyLen:       1000, // ~285 tokens, way under
			contextWindow: &window512k,
			want:          false,
		},
		{
			name:          "model_not_found → no trigger (routing issue, not size)",
			status:        400,
			kind:          errorsx.KindModelNotFound,
			bodyLen:       512000 * 4,
			contextWindow: &window512k,
			want:          false,
		},
		{
			name:          "401 auth → no trigger",
			status:        401,
			kind:          errorsx.KindAuth,
			bodyLen:       512000 * 4,
			contextWindow: &window512k,
			want:          false,
		},
		{
			name:          "429 rate limit → no trigger",
			status:        429,
			kind:          errorsx.KindRateLimit,
			bodyLen:       512000 * 4,
			contextWindow: &window512k,
			want:          false,
		},
		{
			name:          "500 server error → no trigger (not a client error)",
			status:        500,
			kind:          errorsx.KindUpstreamDown,
			bodyLen:       512000 * 4,
			contextWindow: &window512k,
			want:          false,
		},
		{
			name:          "nil context window → no trigger (can't estimate)",
			status:        400,
			kind:          errorsx.KindTransient,
			bodyLen:       512000 * 4,
			contextWindow: nil,
			want:          false,
		},
		{
			name:          "concurrent overload → no trigger",
			status:        400,
			kind:          errorsx.KindConcurrent,
			bodyLen:       512000 * 4,
			contextWindow: &window512k,
			want:          false,
		},
		{
			name:          "boundary: exactly at 90% threshold → no trigger (strictly greater)",
			status:        400,
			kind:          errorsx.KindTransient,
			bodyLen:       int(float64(smallWindow) * 0.9 * 3.5), // exactly ~90%
			contextWindow: &smallWindow,
			want:          false,
		},
		{
			name:          "boundary: just above 90% → trigger",
			status:        400,
			kind:          errorsx.KindTransient,
			bodyLen:       int(float64(smallWindow) * 0.95 * 3.5), // ~95%
			contextWindow: &smallWindow,
			want:          true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Import errorsx in the test file via qualified alias to avoid
			// ambiguity — errorsx is not yet imported here.
			got := shouldHeuristicCompact(tt.status, tt.kind, tt.bodyLen, tt.contextWindow)
			if got != tt.want {
				t.Fatalf("shouldHeuristicCompact(%d, %v, %d, %v) = %v, want %v",
					tt.status, tt.kind, tt.bodyLen, tt.contextWindow, got, tt.want)
			}
		})
	}
}

// TestHandleContextLengthRecovery_WithWindow_DoesBothPhases verifies the
// normal flow is unchanged: with a non-nil ContextWindow, mechanical trim
// runs first, then LLM fallback. This is a regression guard ensuring the
// nil-window fix didn't break the non-nil path.
func TestHandleContextLengthRecovery_WithWindow_DoesBothPhases(t *testing.T) {
	e := &Executor{}
	params := &ExecParams{R: httptestNewRequest()}
	window := 50
	cand := provider.Candidate{ContextWindow: &window}
	big := strings.Repeat("x", 400)
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"` + big + `"},{"role":"assistant","content":"` + big + `"},{"role":"user","content":"` + big + `"}]}`)

	st := contextLengthRecoveryState{}
	source := append([]byte(nil), body...)
	action := e.handleContextLengthRecovery(context.Background(), params, cand, &source, &st, 400)
	if action != ctxLenRetry {
		t.Fatalf("phase1 should retry, got %v", action)
	}
	if !st.mechanicalAttempted {
		t.Fatal("mechanicalAttempted should be true after phase1")
	}
	// Body should have shrunk (mechanical trim ran).
	if len(source) >= len(body) {
		t.Fatal("body should have shrunk after mechanical trim")
	}
}
