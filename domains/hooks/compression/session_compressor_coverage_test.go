// Package compression - session_compressor_coverage_test.go
//
// Coverage-gap tests for SessionCompressor.Prepare() and its helpers:
// fallbackResult, mechanicalTrim, injectSummaryMarker, applyToolsCaching,
// marshalHashes, mustExtractMessages, extractTaskType, WithTaskType,
// tryLLMSummary, rebuildBodyAfterSummary.

package compression

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// ─────────────────────────────────────────────────────────────────────────────
// Prepare() branches
// ─────────────────────────────────────────────────────────────────────────────

func TestPrepare_NilReceiver(t *testing.T) {
	var sc *SessionCompressor
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	res := sc.Prepare(context.Background(), body, "t", "s1", "openai", 0, false)
	if res == nil {
		t.Fatal("Prepare(nil receiver): nil result")
	}
	if res.OutboundBody != nil {
		t.Errorf("Prepare(nil receiver): expected no rewrite, got %v", res.OutboundBody)
	}
	if res.MsgCount == 0 {
		t.Error("Prepare(nil receiver): MsgCount should be set from client body")
	}
}

func TestPrepare_DisabledCompressor(t *testing.T) {
	sc := NewSessionCompressor(SessionCompressorDeps{Disabled: true})
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	res := sc.Prepare(context.Background(), body, "t", "sess-x", "openai", 0, false)
	if res == nil {
		t.Fatal("Prepare(disabled): nil result")
	}
	if res.CompressionStrategy != "" {
		t.Errorf("Prepare(disabled): unexpected strategy %q", res.CompressionStrategy)
	}
}

func TestPrepare_EmptySessionID(t *testing.T) {
	sc := NewSessionCompressor(SessionCompressorDeps{})
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	res := sc.Prepare(context.Background(), body, "t", "", "openai", 0, false)
	if res == nil || res.MsgCount == 0 {
		t.Fatal("Prepare(empty session id): expected fallback with msg count")
	}
}

func TestPrepare_InvalidSessionID(t *testing.T) {
	sc := NewSessionCompressor(SessionCompressorDeps{})
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	res := sc.Prepare(context.Background(), body, "t", "bad session with spaces", "openai", 0, false)
	if res == nil {
		t.Fatal("Prepare(invalid session id): nil result")
	}
	if res.OutboundBody != nil {
		t.Errorf("Prepare(invalid session id): expected no rewrite, got %v", res.OutboundBody)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// fallbackResult / mechanicalTrim / marshalHashes / mustExtractMessages
// ─────────────────────────────────────────────────────────────────────────────

func TestFallbackResult_NonEmptyBody(t *testing.T) {
	sc := NewSessionCompressor(SessionCompressorDeps{})
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	res := sc.fallbackResult(body, &PrepareResult{})
	if res.MsgCount != 1 {
		t.Errorf("fallbackResult MsgCount = %d, want 1", res.MsgCount)
	}
	if res.TokenEst <= 0 {
		t.Errorf("fallbackResult TokenEst = %d, want > 0", res.TokenEst)
	}
	if len(res.MsgHashes) == 0 {
		t.Error("fallbackResult MsgHashes: expected non-empty JSON")
	}
}

func TestFallbackResult_EmptyBody(t *testing.T) {
	sc := NewSessionCompressor(SessionCompressorDeps{})
	res := sc.fallbackResult(nil, &PrepareResult{})
	if res.MsgCount != 0 {
		t.Errorf("fallbackResult(empty) MsgCount = %d, want 0", res.MsgCount)
	}
	if len(res.MsgHashes) != 0 {
		t.Error("fallbackResult(empty): MsgHashes should be nil")
	}
}

func TestMarshalHashes_Empty(t *testing.T) {
	got := marshalHashes(nil)
	if got != nil {
		t.Errorf("marshalHashes(nil) = %s, want nil", got)
	}
	got = marshalHashes([]MsgHash{})
	if got != nil {
		t.Errorf("marshalHashes([]) = %s, want nil", got)
	}
}

func TestMarshalHashes_NonEmpty(t *testing.T) {
	hashes := []MsgHash{{Index: 0, SHA256: "abc"}}
	got := marshalHashes(hashes)
	if len(got) == 0 {
		t.Fatal("marshalHashes: empty result")
	}
	if !strings.Contains(string(got), "abc") {
		t.Errorf("marshalHashes: expected %q in output, got %s", "abc", got)
	}
}

func TestMustExtractMessages_InvalidJSON(t *testing.T) {
	msgs := mustExtractMessages([]byte("not json"))
	if len(msgs) != 0 {
		t.Errorf("mustExtractMessages(invalid) = %v, want empty", msgs)
	}
}

func TestMechanicalTrim_NonPositiveContext(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	got := mechanicalTrim(body, 0, "openai")
	if string(got) != string(body) {
		t.Errorf("mechanicalTrim(ctx=0): expected passthrough, got %s", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// injectSummaryMarker
// ─────────────────────────────────────────────────────────────────────────────

func TestInjectSummaryMarker_NoMessages(t *testing.T) {
	_, body := injectSummaryMarker([]byte(`{"messages":[]}`), "openai")
	if body != nil {
		t.Errorf("injectSummaryMarker(empty messages) body = %s, want nil", body)
	}
}

func TestInjectSummaryMarker_InvalidJSON(t *testing.T) {
	_, body := injectSummaryMarker([]byte("not json"), "openai")
	if body != nil {
		t.Errorf("injectSummaryMarker(invalid) body = %s, want nil", body)
	}
}

func TestInjectSummaryMarker_OpenAI(t *testing.T) {
	// Construct body with the summary as the first assistant message.
	body := []byte(`{"messages":[{"role":"assistant","content":"summary"},{"role":"user","content":"q"}]}`)
	marker, newBody := injectSummaryMarker(body, "openai")
	if marker == "" {
		t.Error("injectSummaryMarker(openai): empty marker")
	}
	if len(newBody) == 0 {
		t.Error("injectSummaryMarker(openai): empty body")
	}
	if !strings.HasPrefix(marker, "[smm_v1:") {
		t.Errorf("injectSummaryMarker(openai): marker %q lacks smm_v1 prefix", marker)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// applyToolsCaching
// ─────────────────────────────────────────────────────────────────────────────

func TestApplyToolsCaching_NilState(t *testing.T) {
	body := []byte(`{"tools":[]}`)
	got, cached := applyToolsCaching(body, nil)
	if cached || string(got) != string(body) {
		t.Errorf("applyToolsCaching(nil state) got=%s cached=%v, want passthrough", got, cached)
	}
}

func TestApplyToolsCaching_NoToolsKey(t *testing.T) {
	body := []byte(`{"messages":[]}`)
	st := &SessionState{}
	got, cached := applyToolsCaching(body, st)
	if cached {
		t.Error("applyToolsCaching: should not cache when body lacks tools")
	}
	if string(got) != string(body) {
		t.Error("applyToolsCaching: should pass body through unchanged")
	}
}

func TestApplyToolsCaching_FirstTimeSeesTools(t *testing.T) {
	body := []byte(`{"tools":[{"name":"a"}]}`)
	st := &SessionState{}
	got, cached := applyToolsCaching(body, st)
	if cached {
		t.Error("applyToolsCaching first call: should NOT cache (no prior hash)")
	}
	if st.ToolsHash == "" {
		t.Error("applyToolsCaching first call: state.ToolsHash should be set")
	}
	if string(got) != string(body) {
		t.Error("applyToolsCaching first call: body should pass through")
	}
}

func TestApplyToolsCaching_UnchangedReusesCache(t *testing.T) {
	body := []byte(`{"tools":[{"name":"a"}]}`)
	st := &SessionState{}
	_, _ = applyToolsCaching(body, st)
	got, cached := applyToolsCaching(body, st)
	if !cached {
		t.Fatal("applyToolsCaching second call: expected cache hit")
	}
	if strings.Contains(string(got), `"tools":`) {
		t.Errorf("applyToolsCaching: tools should be removed, got %s", got)
	}
	if !strings.Contains(string(got), `"_tools_cached":true`) {
		t.Errorf("applyToolsCaching: _tools_cached marker missing, got %s", got)
	}
}

func TestApplyToolsCaching_ToolsChangedUpdatesHash(t *testing.T) {
	body1 := []byte(`{"tools":[{"name":"a"}]}`)
	body2 := []byte(`{"tools":[{"name":"b"}]}`)
	st := &SessionState{}
	_, _ = applyToolsCaching(body1, st)
	hashAfterFirst := st.ToolsHash
	_, cached := applyToolsCaching(body2, st)
	if cached {
		t.Error("applyToolsCaching: changed tools should not cache hit")
	}
	if st.ToolsHash == hashAfterFirst {
		t.Error("applyToolsCaching: hash should change when tools change")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// extractTaskType / WithTaskType
// ─────────────────────────────────────────────────────────────────────────────

func TestExtractTaskType_NoValue(t *testing.T) {
	if got := extractTaskType(context.Background()); got != "" {
		t.Errorf("extractTaskType(no value) = %q, want empty", got)
	}
}

func TestExtractTaskType_WrongType(t *testing.T) {
	type wrongKey struct{}
	ctx := context.WithValue(context.Background(), wrongKey{}, "garbage")
	if got := extractTaskType(ctx); got != "" {
		t.Errorf("extractTaskType(wrong type) = %q, want empty", got)
	}
}

func TestWithTaskType_RoundTrip(t *testing.T) {
	ctx := WithTaskType(context.Background(), "code_debug")
	if got := extractTaskType(ctx); got != "code_debug" {
		t.Errorf("round-trip: got %q, want %q", got, "code_debug")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// tryLLMSummary
// ─────────────────────────────────────────────────────────────────────────────

func TestTryLLMSummary_NilCompactionDeps(t *testing.T) {
	sc := NewSessionCompressor(SessionCompressorDeps{})
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	got, ok := sc.tryLLMSummary(context.Background(), body, "t", "openai", "general")
	if ok || got != nil {
		t.Errorf("tryLLMSummary(nil deps) = (%v, %v), want (nil, false)", got, ok)
	}
}

func TestTryLLMSummary_InvalidJSON(t *testing.T) {
	sc := NewSessionCompressor(SessionCompressorDeps{CompactionDeps: &Dependencies{}})
	body := []byte("not json")
	got, ok := sc.tryLLMSummary(context.Background(), body, "t", "openai", "general")
	if ok || got != nil {
		t.Errorf("tryLLMSummary(invalid json) = (%v, %v), want (nil, false)", got, ok)
	}
}

func TestTryLLMSummary_EmptyConversation(t *testing.T) {
	sc := NewSessionCompressor(SessionCompressorDeps{CompactionDeps: &Dependencies{}})
	// Valid JSON but no extractable conversation text.
	body := []byte(`{"messages":[]}`)
	got, ok := sc.tryLLMSummary(context.Background(), body, "t", "openai", "general")
	if ok || got != nil {
		t.Errorf("tryLLMSummary(empty conv) = (%v, %v), want (nil, false)", got, ok)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// rebuildBodyAfterSummary
// ─────────────────────────────────────────────────────────────────────────────

func TestRebuildBodyAfterSummary_EmptyText(t *testing.T) {
	got, ok := rebuildBodyAfterSummary([]byte(`{"messages":[]}`), "", "openai")
	if ok || got != nil {
		t.Errorf("rebuildBodyAfterSummary(empty text) = (%v, %v), want (nil, false)", got, ok)
	}
}

func TestRebuildBodyAfterSummary_InvalidJSON(t *testing.T) {
	got, ok := rebuildBodyAfterSummary([]byte("not json"), "summary", "openai")
	if ok || got != nil {
		t.Errorf("rebuildBodyAfterSummary(invalid) = (%v, %v), want (nil, false)", got, ok)
	}
}

func TestRebuildBodyAfterSummary_OpenAI(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"old"}]}`)
	got, ok := rebuildBodyAfterSummary(body, "summary text", "openai")
	if !ok || got == nil {
		t.Fatal("rebuildBodyAfterSummary(openai): expected ok=true with body")
	}
	// Output should be valid JSON with messages.
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("rebuildBodyAfterSummary: invalid JSON output: %v", err)
	}
	if _, ok := parsed["messages"]; !ok {
		t.Error("rebuildBodyAfterSummary: output missing messages key")
	}
}

func TestRebuildBodyAfterSummary_Anthropic(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"old"}]}`)
	got, ok := rebuildBodyAfterSummary(body, "summary text", "anthropic-messages")
	if !ok || got == nil {
		t.Fatal("rebuildBodyAfterSummary(anthropic): expected ok=true with body")
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("rebuildBodyAfterSummary(anthropic): invalid JSON: %v", err)
	}
}

func TestRebuildBodyAfterSummary_AnthropicInvalidJSON(t *testing.T) {
	got, ok := rebuildBodyAfterSummary([]byte("not json"), "summary", "anthropic-messages")
	if ok || got != nil {
		t.Errorf("rebuildBodyAfterSummary(anthropic invalid) = (%v, %v), want (nil, false)", got, ok)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// sessionCacheL1Capacity clamp
// ─────────────────────────────────────────────────────────────────────────────

func TestSessionCacheL1Capacity_ClampBelowMin(t *testing.T) {
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })

	store := map[string][]byte{
		"cache.session_l1_capacity": []byte("10"), // below 64
	}
	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &fakeIntBackend{store: store})
	registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
	for _, sp := range settings.CompressionSpecs() {
		if sp.Key == "cache.session_l1_capacity" {
			registry.MustRegisterSpec(sp)
		}
	}
	settings.Global = registry

	got := sessionCacheL1Capacity()
	if got != 64 {
		t.Errorf("sessionCacheL1Capacity(clamped) = %d, want 64", got)
	}
}
