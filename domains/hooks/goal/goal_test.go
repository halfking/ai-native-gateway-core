package goal

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

// fakeStore is an in-memory GoalStore for testing the hook state machine
// without a database. It implements the AtomicAutoContinue CAS faithfully so
// the continue-budget tests reflect real concurrency behaviour.
type fakeStore struct {
	mu                  sync.Mutex
	sessions            map[string]*Session
	autoContinueCount   map[string]int
	atomicWonCalls      int
	atomicLostCalls     int
	auditUpdated        bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		sessions:          make(map[string]*Session),
		autoContinueCount: make(map[string]int),
	}
}

func (s *fakeStore) seed(sess *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.SessionID] = sess
}

func (s *fakeStore) GetSession(_ context.Context, sessionID string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[sessionID]; ok {
		cp := *sess
		return &cp, nil
	}
	return nil, nil
}

func (s *fakeStore) CreateSession(_ context.Context, sess *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.SessionID] = sess
	return nil
}

func (s *fakeStore) UpdateSessionState(_ context.Context, sessionID string, state State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[sessionID]; ok {
		sess.State = state
	}
	return nil
}

func (s *fakeStore) IncrementAutoContinueCount(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.autoContinueCount[sessionID]++
	if sess, ok := s.sessions[sessionID]; ok {
		sess.AutoContinueCount++
	}
	return nil
}

func (s *fakeStore) IncrementDecisionCount(_ context.Context, sessionID string) error { return nil }

// RecordResponse mirrors PGStore: bump repeat_count when the hash matches the
// stored one, else reset to 1. Tracks the last hash in-memory.
func (s *fakeStore) RecordResponse(_ context.Context, sessionID, responseHash string, resetOnProgress bool) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	if !ok {
		return 0, errors.New("session not found")
	}
	if sess.LastResponseHash == responseHash {
		sess.RepeatCount++
	} else if resetOnProgress {
		sess.RepeatCount = 1
	} else {
		sess.RepeatCount++
	}
	sess.LastResponseHash = responseHash
	return sess.RepeatCount, nil
}

// AtomicModelSwitch mirrors PGStore: bump model_switch_count under maxAllowed,
// reset auto_continue_count, record the new model.
func (s *fakeStore) AtomicModelSwitch(_ context.Context, sessionID, newModel string, maxAllowed int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	if !ok {
		return false, errors.New("session not found")
	}
	if maxAllowed > 0 && sess.ModelSwitchCount >= maxAllowed {
		return false, nil
	}
	sess.ModelSwitchCount++
	sess.AutoContinueCount = 0
	sess.CurrentModel = newModel
	return true, nil
}

func (s *fakeStore) UpdateSessionAudit(_ context.Context, sessionID string, auditResult []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[sessionID]; ok && len(sess.AuditResult) == 0 {
		sess.AuditResult = auditResult
		s.auditUpdated = true
		return true, nil
	}
	return false, nil
}

// AtomicAutoContinue mirrors PGStore's WHERE auto_continue_count < $2 guard.
func (s *fakeStore) AtomicAutoContinue(_ context.Context, sessionID string, maxAllowed int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.autoContinueCount[sessionID]
	if cur >= maxAllowed {
		s.atomicLostCalls++
		return false, nil
	}
	s.autoContinueCount[sessionID] = cur + 1
	if sess, ok := s.sessions[sessionID]; ok {
		sess.AutoContinueCount = cur + 1
	}
	s.atomicWonCalls++
	return true, nil
}

// stubLLMCaller returns canned responses keyed by an index, capturing the
// messages it was called with.
type stubLLMCaller struct {
	mu       sync.Mutex
	responses []string
	idx      int
	calls    []stubCall
}

type stubCall struct {
	Model    string
	Messages []map[string]string
}

func (c *stubLLMCaller) CallLLM(_ context.Context, model string, messages []map[string]string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, stubCall{Model: model, Messages: messages})
	if c.idx >= len(c.responses) {
		return "", errors.New("stub: no more canned responses")
	}
	resp := c.responses[c.idx]
	c.idx++
	return resp, nil
}

// newTestHook builds a ModeHook wired with fakes and the keyword detection
// mode so activation + completion don't depend on an LLM endpoint. Tests that
// need to flip a specific config flag (e.g. disable model switching) can just
// mutate hook.config after construction.
func newTestHook(t *testing.T, store GoalStore, llm LLMCaller) *ModeHook {
	t.Helper()
	cfg := ModeConfig{
		Enabled:                true,
		DetectionMode:          ModeKeyword,
		AutoContinueOnPause:    true,
		MaxAutoContinueCount:   3,
		SettingsGetter:         nil, // falls back to config defaults
		UseAutorouteForAudit:   false,
		ModelSwitchOnLoop:      true,
		MaxModelSwitchCount:    2,
		FallbackModels:         []string{"gpt-4o", "claude-3-5-sonnet"},
		RepeatDetectionEnabled: true,
		RepeatThreshold:        3,
		CompletionConfidence:   DefaultCompletionConfidence,
	}
	return &ModeHook{
		config:    cfg,
		db:        store,
		llmCaller: llm,
		detector:  NewCompletionDetector(store, llm),
		history:   NoopHistoryStore(),
	}
}

// ── InterceptNonStream: stop-but-incomplete triggers a continue follow-up ──

func TestInterceptNonStream_StopButIncomplete_InjectsContinue(t *testing.T) {
	store := newFakeStore()
	store.seed(&Session{SessionID: "s1", TenantID: "t1", State: StateActive})

	hook := newTestHook(t, store, nil) // no LLM caller -> keyword-only completion detection

	// finish_reason="stop", content is work-in-progress (no completion keyword),
	// so shouldAutoContinue should fire and inject a "请继续" follow-up.
	body := `{"choices":[{"message":{"role":"assistant","content":"I'm still working on step 2 of the refactor."},"finish_reason":"stop"}]}`
	req := &response.InterceptRequest{
		SessionID:    "s1",
		TenantID:     "t1",
		ResponseBody: []byte(body),
		FinishReason: "stop",
	}

	res, err := hook.InterceptNonStream(context.Background(), req)
	if err != nil {
		t.Fatalf("InterceptNonStream error: %v", err)
	}
	if res == nil {
		t.Fatal("expected a continue follow-up, got nil result")
	}
	if res.Action != "goal_continue" {
		t.Fatalf("Action = %q, want goal_continue", res.Action)
	}
	if len(res.InjectFollowUp) == 0 {
		t.Fatal("InjectFollowUp is empty")
	}
	// The continue budget should have been consumed exactly once.
	if got := store.autoContinueCount["s1"]; got != 1 {
		t.Fatalf("autoContinueCount = %d, want 1", got)
	}
}

// ── InterceptNonStream: completed task does NOT continue ────────────────────

func TestInterceptNonStream_Completed_NoContinue(t *testing.T) {
	store := newFakeStore()
	store.seed(&Session{SessionID: "s2", TenantID: "t1", State: StateActive})

	hook := newTestHook(t, store, nil)

	// Keyword "任务完成" + "成功" -> completion detector returns true, so no
	// continue should be injected.
	body := `{"choices":[{"message":{"role":"assistant","content":"任务完成，所有文件都已成功重构。"},"finish_reason":"stop"}]}`
	req := &response.InterceptRequest{
		SessionID:    "s2",
		TenantID:     "t1",
		ResponseBody: []byte(body),
		FinishReason: "stop",
	}

	res, _ := hook.InterceptNonStream(context.Background(), req)
	// With UseAutorouteForAudit=false the hook returns nil after marking
	// completed — definitely no continue.
	if res != nil && res.Action == "goal_continue" {
		t.Fatalf("did not expect a continue for a completed task, got action=%q", res.Action)
	}
	// Session should be marked completed.
	sess, _ := store.GetSession(context.Background(), "s2")
	if sess.State != StateCompleted {
		t.Fatalf("session state = %q, want completed", sess.State)
	}
}

// ── Continue budget exhaustion: no follow-up past MaxAutoContinueCount ──────

// Continue budget exhausted with model switching DISABLED → no follow-up
// (the "give up" path). When switching IS enabled, a model rotation happens
// instead — that's covered by TestInterceptNonStream_BudgetExhausted_SwitchesModel.
func TestInterceptNonStream_ContinueBudgetExhausted(t *testing.T) {
	store := newFakeStore()
	store.seed(&Session{SessionID: "s3", TenantID: "t1", State: StateActive, AutoContinueCount: 3})

	hook := newTestHook(t, store, nil)
	hook.config.ModelSwitchOnLoop = false // isolate the no-switch give-up path

	body := `{"choices":[{"message":{"role":"assistant","content":"still going..."},"finish_reason":"length"}]}`
	req := &response.InterceptRequest{
		SessionID:    "s3",
		TenantID:     "t1",
		ResponseBody: []byte(body),
		FinishReason: "length",
	}

	if res, _ := hook.InterceptNonStream(context.Background(), req); res != nil {
		t.Fatalf("expected no follow-up when budget exhausted (switch disabled), got action=%q", res.Action)
	}
}

// ── tool_calls finish_reason does NOT auto-continue (client drives tools) ───

func TestInterceptNonStream_ToolCallsFinish_NoContinue(t *testing.T) {
	store := newFakeStore()
	store.seed(&Session{SessionID: "s4", TenantID: "t1", State: StateActive})

	hook := newTestHook(t, store, nil)

	body := `{"choices":[{"message":{"role":"assistant","content":"calling a tool now"},"finish_reason":"tool_calls"}]}`
	req := &response.InterceptRequest{
		SessionID:    "s4",
		TenantID:     "t1",
		ResponseBody: []byte(body),
		FinishReason: "tool_calls",
	}

	if res, _ := hook.InterceptNonStream(context.Background(), req); res != nil {
		t.Fatalf("expected no continue for tool_calls finish_reason, got action=%q", res.Action)
	}
}

// ── Concurrent continues: only one wins the CAS, exactly one follow-up ─────
//
// This exercises the AtomicAutoContinue guard that prevents two concurrent
// requests from each injecting a continue for the same session.
func TestAtomicAutoContinue_OnlyOneWins(t *testing.T) {
	store := newFakeStore()
	store.seed(&Session{SessionID: "s5", TenantID: "t1", State: StateActive})

	const max = 3
	var wg sync.WaitGroup
	var wins int
	var mu sync.Mutex
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			won, _ := store.AtomicAutoContinue(context.Background(), "s5", max)
			if won {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// Only `max` callers can win (count goes 0->1->2->3, the 4th and 5th lose).
	if wins != max {
		t.Fatalf("concurrent wins = %d, want %d", wins, max)
	}
	if store.atomicLostCalls != 5-max {
		t.Fatalf("lost calls = %d, want %d", store.atomicLostCalls, 5-max)
	}
}

// ── chatLLMCaller request body shape ────────────────────────────────────────

func TestChatLLMCaller_UnconfiguredEndpoint_Errors(t *testing.T) {
	// Zero-value config: endpoint empty. ApplyHTTPLlmCallerDefaults fills
	// timeout/model/client but leaves Endpoint blank, so CallLLM should error.
	cfg := autoroute.HTTPLlmCallerConfig{}
	ApplyHTTPLlmCallerDefaults(&cfg)
	c := NewChatLLMCaller(cfg)
	_, err := c.CallLLM(context.Background(), "auto", []map[string]string{{"role": "user", "content": "hi"}})
	if err == nil {
		t.Fatal("expected error when endpoint unconfigured")
	}
}

// ── HistoryStore parsing helpers ────────────────────────────────────────────

func TestParseRequestMessages(t *testing.T) {
	msgs := parseRequestMessages(`{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"}]}`)
	if len(msgs) != 2 {
		t.Fatalf("len = %d, want 2", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hello" {
		t.Errorf("msg[0] = %+v", msgs[0])
	}
}

func TestParseRequestMessages_Empty(t *testing.T) {
	if got := parseRequestMessages(""); got != nil {
		t.Errorf("expected nil for empty input, got %v", got)
	}
	if got := parseRequestMessages("not json"); got != nil {
		t.Errorf("expected nil for invalid json, got %v", got)
	}
}

func TestParseAssistantReply(t *testing.T) {
	am := parseAssistantReply(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`)
	if am == nil || am.Content != "done" {
		t.Fatalf("unexpected reply: %+v", am)
	}
	if parseAssistantReply(`{"choices":[]}`) != nil {
		t.Fatal("expected nil for empty choices")
	}
	if parseAssistantReply("") != nil {
		t.Fatal("expected nil for empty body")
	}
}

func TestTruncate(t *testing.T) {
	short := "abc"
	if got := truncate(short, 10); got != short {
		t.Errorf("short string should be unchanged, got %q", got)
	}
	long := "abcdefghij"
	got := truncate(long, 5)
	if len([]rune(got)) <= 5 && !containsRune(got, '…') {
		// truncated output should be shorter and carry the ellipsis marker
		t.Errorf("expected truncation marker, got %q", got)
	}
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}

func TestFormatHistoryForPrompt(t *testing.T) {
	if got := FormatHistoryForPrompt(nil); got != "(no prior history available)" {
		t.Errorf("empty history = %q", got)
	}
	out := FormatHistoryForPrompt([]HistoryMessage{{Role: "user", Content: "hi"}})
	if out != "user: hi\n\n" {
		t.Errorf("format = %q", out)
	}
}

// ── NoopHistoryStore ────────────────────────────────────────────────────────

func TestNoopHistoryStore(t *testing.T) {
	msgs, err := NoopHistoryStore().FetchBySession(context.Background(), "s", "t", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgs != nil {
		t.Fatalf("expected nil msgs, got %v", msgs)
	}
}

// ── Loop detection & model switching ────────────────────────────────────────

// Budget exhausted + model switching enabled → the follow-up should target a
// fallback model and the continue budget should be reset.
func TestInterceptNonStream_BudgetExhausted_SwitchesModel(t *testing.T) {
	store := newFakeStore()
	store.seed(&Session{
		SessionID:        "ms1",
		TenantID:         "t1",
		State:            StateActive,
		AutoContinueCount: 3, // == MaxAutoContinueCount (3)
		CurrentModel:     "gpt-4o",
	})

	hook := newTestHook(t, store, nil)

	body := `{"choices":[{"message":{"role":"assistant","content":"still working on step 5"},"finish_reason":"stop"}]}`
	req := &response.InterceptRequest{
		SessionID: "ms1", TenantID: "t1",
		ResponseBody: []byte(body), FinishReason: "stop",
	}

	res, _ := hook.InterceptNonStream(context.Background(), req)
	if res == nil {
		t.Fatal("expected a model-switched continue follow-up, got nil")
	}
	if res.Action != "goal_model_switch" {
		t.Fatalf("Action = %q, want goal_model_switch", res.Action)
	}
	// The follow-up body should target a different model (claude-3-5-sonnet,
	// the first fallback that differs from gpt-4o).
	var parsed struct {
		Model string `json:"model"`
	}
	if err := jsonParse(res.InjectFollowUp, &parsed); err != nil {
		t.Fatalf("parse follow-up: %v", err)
	}
	if parsed.Model == "gpt-4o" {
		t.Fatalf("model not switched, still %q", parsed.Model)
	}

	sess, _ := store.GetSession(context.Background(), "ms1")
	if sess.ModelSwitchCount != 1 {
		t.Fatalf("ModelSwitchCount = %d, want 1", sess.ModelSwitchCount)
	}
	// After the audit fix the model-switch rotation does NOT consume a continue
	// slot (AtomicModelSwitch reset the count to 0; the follow-up is built
	// directly rather than via tryAtomicContinue). So the count stays 0 and the
	// new model gets the full max budget for its subsequent turns.
	if sess.AutoContinueCount != 0 {
		t.Fatalf("AutoContinueCount = %d, want 0 (rotation should not consume budget)", sess.AutoContinueCount)
	}
	if sess.CurrentModel == "gpt-4o" {
		t.Fatalf("CurrentModel not updated, still gpt-4o")
	}
}

// Repeated responses (RepeatCount >= threshold) → model switch even when budget
// remains, because repetition is the clearest stuck-model signal.
func TestInterceptNonStream_RepeatedResponse_SwitchesModel(t *testing.T) {
	store := newFakeStore()
	store.seed(&Session{
		SessionID:    "ms2",
		TenantID:     "t1",
		State:        StateActive,
		CurrentModel: "gpt-4o",
	})
	// Simulate the model already having produced the same reply twice
	// (repeat_count=2). One more identical response hits the threshold (3).
	store.sessions["ms2"].RepeatCount = 2
	store.sessions["ms2"].LastResponseHash = hashResponse([]byte(`{"choices":[{"message":{"role":"assistant","content":"same reply"}}]}`))

	hook := newTestHook(t, store, nil)

	body := `{"choices":[{"message":{"role":"assistant","content":"same reply"},"finish_reason":"stop"}]}`
	req := &response.InterceptRequest{
		SessionID: "ms2", TenantID: "t1",
		ResponseBody: []byte(body), FinishReason: "stop",
	}

	res, _ := hook.InterceptNonStream(context.Background(), req)
	if res == nil {
		t.Fatal("expected a model-switch follow-up on repeated response")
	}
	sess, _ := store.GetSession(context.Background(), "ms2")
	if sess.ModelSwitchCount != 1 {
		t.Fatalf("ModelSwitchCount = %d, want 1", sess.ModelSwitchCount)
	}
}

// When model switching is disabled, a budget-exhausted session gives up rather
// than looping forever on the same model.
func TestInterceptNonStream_SwitchDisabled_GivesUp(t *testing.T) {
	store := newFakeStore()
	store.seed(&Session{
		SessionID:        "ms3",
		TenantID:         "t1",
		State:            StateActive,
		AutoContinueCount: 3,
		CurrentModel:     "gpt-4o",
	})

	hook := newTestHook(t, store, nil)
	hook.config.ModelSwitchOnLoop = false // disable switching for this case

	body := `{"choices":[{"message":{"role":"assistant","content":"stuck"},"finish_reason":"stop"}]}`
	req := &response.InterceptRequest{
		SessionID: "ms3", TenantID: "t1",
		ResponseBody: []byte(body), FinishReason: "stop",
	}

	if res, _ := hook.InterceptNonStream(context.Background(), req); res != nil {
		t.Fatalf("expected no follow-up when switching disabled and budget exhausted, got action=%q", res.Action)
	}
}

// Switch budget exhausted → give up even though fallback models are configured.
func TestInterceptNonStream_SwitchBudgetExhausted_GivesUp(t *testing.T) {
	store := newFakeStore()
	store.seed(&Session{
		SessionID:         "ms4",
		TenantID:          "t1",
		State:             StateActive,
		AutoContinueCount: 3,
		ModelSwitchCount:  2, // == MaxModelSwitchCount (2)
		CurrentModel:      "gpt-4o",
	})

	hook := newTestHook(t, store, nil) // MaxModelSwitchCount=2

	body := `{"choices":[{"message":{"role":"assistant","content":"still stuck"},"finish_reason":"stop"}]}`
	req := &response.InterceptRequest{
		SessionID: "ms4", TenantID: "t1",
		ResponseBody: []byte(body), FinishReason: "stop",
	}

	if res, _ := hook.InterceptNonStream(context.Background(), req); res != nil {
		t.Fatalf("expected give-up when switch budget exhausted, got action=%q", res.Action)
	}
}

// pickNextModel should skip the currently-used model.
func TestPickNextModel_SkipsCurrent(t *testing.T) {
	hook := &ModeHook{
		config: ModeConfig{FallbackModels: []string{"gpt-4o", "claude-3-5-sonnet", "gemini-pro"}},
	}
	cases := []struct {
		current string
		want    string
	}{
		{"gpt-4o", "claude-3-5-sonnet"},
		{"claude-3-5-sonnet", "gpt-4o"},
		{"gemini-pro", "gpt-4o"},
		{"unknown-model", "gpt-4o"},
	}
	for _, c := range cases {
		sess := &Session{CurrentModel: c.current}
		if got := hook.pickNextModel(sess); got != c.want {
			t.Errorf("pickNextModel(current=%q) = %q, want %q", c.current, got, c.want)
		}
	}
}

// pickNextModel with no configured list falls back to "auto" (autoroute).
func TestPickNextModel_EmptyListFallsBackToAuto(t *testing.T) {
	hook := &ModeHook{config: ModeConfig{}}
	if got := hook.pickNextModel(&Session{CurrentModel: "gpt-4o"}); got != "auto" {
		t.Fatalf("expected auto fallback, got %q", got)
	}
}

// hashResponse is stable for identical content and differs for distinct content.
func TestHashResponse_StableAndDistinct(t *testing.T) {
	a := `{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`
	b := `{"choices":[{"message":{"role":"assistant","content":"world"}}]}`
	h1 := hashResponse([]byte(a))
	h2 := hashResponse([]byte(a))
	h3 := hashResponse([]byte(b))
	if h1 != h2 {
		t.Fatal("identical content should hash identically")
	}
	if h1 == h3 {
		t.Fatal("distinct content should hash differently")
	}
}

// RecordResponse increments repeat_count on identical hash, resets on new.
func TestRecordResponse_RepeatTracking(t *testing.T) {
	store := newFakeStore()
	store.seed(&Session{SessionID: "rr1", State: StateActive})

	ctx := context.Background()
	n, _ := store.RecordResponse(ctx, "rr1", "hashA", true)
	if n != 1 {
		t.Fatalf("first response repeat_count = %d, want 1", n)
	}
	n, _ = store.RecordResponse(ctx, "rr1", "hashA", true)
	if n != 2 {
		t.Fatalf("identical response repeat_count = %d, want 2", n)
	}
	n, _ = store.RecordResponse(ctx, "rr1", "hashB", true)
	if n != 1 {
		t.Fatalf("new response should reset repeat_count to 1, got %d", n)
	}
}

// jsonParse is a tiny test helper wrapping json.Unmarshal.
func jsonParse(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
