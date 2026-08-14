package compression

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRecoveryCoordinator_MechanicalFallback(t *testing.T) {
	// Build a large body that needs compression.
	largeContent := strings.Repeat("This is a long message. ", 500)
	msgs := []map[string]any{
		{"role": "system", "content": "You are helpful."},
	}
	for i := 0; i < 10; i++ {
		msgs = append(msgs, map[string]any{"role": "user", "content": largeContent})
		msgs = append(msgs, map[string]any{"role": "assistant", "content": largeContent})
	}
	body, _ := json.Marshal(map[string]any{
		"model":    "test",
		"messages": msgs,
	})

	// No summarizer → mechanical fallback.
	deps := RecoveryDeps{
		Cache:      nil, // no cache
		Summarizer: nil,
		MaxRetries: 2,
	}
	rc := NewRecoveryCoordinator(deps)

	res := rc.Recover(
		context.Background(),
		body,
		"openai",
		5000, // small context window to force compression
		"tenant1",
		"session1",
		0,
	)

	if !res.ShouldRetry {
		t.Fatal("expected ShouldRetry=true")
	}
	if res.NewBody == nil {
		t.Fatal("expected non-nil NewBody")
	}
	if len(res.NewBody) >= len(body) {
		t.Errorf("expected compressed body to be smaller: %d vs %d", len(res.NewBody), len(body))
	}
	if res.Strategy != "smart_window_mechanical" {
		t.Errorf("expected strategy=smart_window_mechanical, got %s", res.Strategy)
	}
	if res.CutMarker == nil {
		t.Error("expected non-nil CutMarker")
	}
	t.Logf("recovery: strategy=%s bytes_before=%d bytes_after=%d reason=%s",
		res.Strategy, len(body), len(res.NewBody), res.Reason)
}

func TestRecoveryCoordinator_WithSummarizer(t *testing.T) {
	largeContent := strings.Repeat("Content here. ", 300)
	msgs := []map[string]any{
		{"role": "system", "content": "System."},
	}
	for i := 0; i < 8; i++ {
		msgs = append(msgs, map[string]any{"role": "user", "content": largeContent})
		msgs = append(msgs, map[string]any{"role": "assistant", "content": largeContent})
	}
	body, _ := json.Marshal(map[string]any{
		"model":    "test",
		"messages": msgs,
	})

	summarizer := func(ctx context.Context, b []byte, protocol string) (string, bool) {
		return "LLM-generated summary of prior conversation turns.", true
	}

	deps := RecoveryDeps{
		Cache:      nil,
		Summarizer: summarizer,
		MaxRetries: 2,
	}
	rc := NewRecoveryCoordinator(deps)

	res := rc.Recover(
		context.Background(),
		body,
		"openai",
		5000,
		"tenant1",
		"session1",
		0,
	)

	if !res.ShouldRetry {
		t.Fatal("expected ShouldRetry=true")
	}
	if res.Strategy != "smart_window_llm" {
		t.Errorf("expected strategy=smart_window_llm, got %s", res.Strategy)
	}
	if len(res.NewBody) >= len(body) {
		t.Errorf("expected compressed body to be smaller")
	}
}

func TestRecoveryCoordinator_NoCompressionNeeded(t *testing.T) {
	body := makeBodyAny(
		makeMsg("system", "Sys"),
		makeMsg("user", "Hi"),
		makeMsg("assistant", "Hello"),
	)

	deps := RecoveryDeps{MaxRetries: 2}
	rc := NewRecoveryCoordinator(deps)

	res := rc.Recover(
		context.Background(),
		body,
		"openai",
		128000,
		"t",
		"s",
		0,
	)

	if res.ShouldRetry {
		t.Error("expected ShouldRetry=false for small conversation")
	}
}

func TestRecoveryCoordinator_CachePersistence(t *testing.T) {
	largeContent := strings.Repeat("Long. ", 300)
	msgs := []map[string]any{
		{"role": "system", "content": "Sys"},
	}
	for i := 0; i < 6; i++ {
		msgs = append(msgs, map[string]any{"role": "user", "content": largeContent})
		msgs = append(msgs, map[string]any{"role": "assistant", "content": largeContent})
	}
	body, _ := json.Marshal(map[string]any{
		"model":    "test",
		"messages": msgs,
	})

	cache := NewSessionCache(nil, nil) // L1 only

	deps := RecoveryDeps{
		Cache:      cache,
		Summarizer: nil,
		MaxRetries: 2,
	}
	rc := NewRecoveryCoordinator(deps)

	res := rc.Recover(
		context.Background(),
		body,
		"openai",
		5000,
		"tenant1",
		"sess-cache-test",
		0,
	)

	if !res.ShouldRetry {
		t.Fatal("expected ShouldRetry=true")
	}

	// Verify the CutMarker was persisted to the cache.
	state, _, _ := cache.GetOrLoad(context.Background(), "tenant1", "sess-cache-test")
	if state == nil {
		t.Fatal("expected session state in cache after recovery")
	}
	if !state.HasCutMarker {
		t.Error("expected HasCutMarker=true in cached state")
	}
	if state.CutIndex <= 0 {
		t.Errorf("expected positive CutIndex, got %d", state.CutIndex)
	}
}

// TestNewSummaryFunc_NilDeps verifies the safety guarantee: when no LLM
// endpoint is configured (deps == nil), NewSummaryFunc returns a func that
// always reports ok=false. This is what lets RecoveryCoordinator fall back
// to mechanical trim on deployments without a compaction LLM — the exact
// behaviour this wiring must not regress.
func TestNewSummaryFunc_NilDeps(t *testing.T) {
	sf := NewSummaryFunc(nil)
	if sf == nil {
		t.Fatal("NewSummaryFunc(nil) returned nil func")
	}
	body, _ := json.Marshal(map[string]any{
		"model":    "test",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	summary, ok := sf(context.Background(), body, "openai")
	if ok {
		t.Errorf("expected ok=false when deps is nil, got summary=%q", summary)
	}
	if summary != "" {
		t.Errorf("expected empty summary when deps is nil, got %q", summary)
	}
}

// TestRecoveryCoordinator_MechanicalFallback_NoSmmMarker is the regression
// test for the 154 production bug where a single session produced 17
// different [smm_v1:HASH] markers in a row.
//
// Root cause: when the LLM summarizer failed and the coordinator fell back
// to mechanical extraction, it still called BuildSummaryMarker on the
// mechanical text and persisted the resulting marker to the cache. Because
// the mechanical text embeds user input and message counts, its first 128
// bytes changed between every pass and the marker hash changed every time.
//
// After the fix, the CutMarker emitted by the mechanical branch MUST have an
// empty SummaryMarker so callers can distinguish "lossy LLM summary was
// produced" from "lossless mechanical trim happened".
func TestRecoveryCoordinator_MechanicalFallback_NoSmmMarker(t *testing.T) {
	largeContent := strings.Repeat("Body of a long message. ", 500)
	msgs := []map[string]any{
		{"role": "system", "content": "Sys"},
	}
	for i := 0; i < 12; i++ {
		msgs = append(msgs,
			map[string]any{"role": "user", "content": "user-input-v" + largeContent},
			map[string]any{"role": "assistant", "content": largeContent},
		)
	}
	body, _ := json.Marshal(map[string]any{
		"model":    "test",
		"messages": msgs,
	})

	// No summarizer → mechanical fallback.
	rc := NewRecoveryCoordinator(RecoveryDeps{
		Cache:      nil,
		Summarizer: nil,
		MaxRetries: 2,
	})

	res := rc.Recover(context.Background(), body, "openai", 5000, "tenant1", "session-mech-no-marker", 0)
	if !res.ShouldRetry {
		t.Fatal("expected ShouldRetry=true")
	}
	if res.Strategy != "smart_window_mechanical" {
		t.Fatalf("expected strategy=smart_window_mechanical, got %s", res.Strategy)
	}
	if res.CutMarker == nil {
		t.Fatal("expected non-nil CutMarker")
	}
	if res.CutMarker.SummaryMarker != "" {
		t.Errorf("mechanical fallback must NOT emit an smm_v1 marker; got %q", res.CutMarker.SummaryMarker)
	}
	if res.CutMarker.SummaryText != "" {
		t.Errorf("mechanical fallback must NOT cache summary text; got %d bytes", len(res.CutMarker.SummaryText))
	}

	// Also verify a SECOND pass with slightly different message counts does
	// not retroactively produce a marker — this was the per-session symptom
	// (17 different hashes in a row). Markers are content-derived, so two
	// passes with different content + the no-llm path must both yield "".
	msgs2 := append([]map[string]any{}, msgs...)
	msgs2 = append(msgs2,
		map[string]any{"role": "user", "content": "DIFFERENT user-input-v" + largeContent},
		map[string]any{"role": "assistant", "content": largeContent},
	)
	body2, _ := json.Marshal(map[string]any{"model": "test", "messages": msgs2})

	res2 := rc.Recover(context.Background(), body2, "openai", 5000, "tenant1", "session-mech-no-marker", 0)
	if res2.CutMarker == nil {
		t.Fatal("second pass: expected non-nil CutMarker")
	}
	if res2.CutMarker.SummaryMarker != "" {
		t.Errorf("second pass: mechanical fallback must STILL NOT emit an smm_v1 marker; got %q", res2.CutMarker.SummaryMarker)
	}
}

// TestRecoveryCoordinator_LLMSummary_EmitsSmmMarker guards the positive case:
// when the LLM summarizer succeeds, the marker MUST be attached and MUST be
// stable across passes with identical summary text.
func TestRecoveryCoordinator_LLMSummary_EmitsSmmMarker(t *testing.T) {
	largeContent := strings.Repeat("Body. ", 500)
	msgs := []map[string]any{
		{"role": "system", "content": "Sys"},
	}
	for i := 0; i < 10; i++ {
		msgs = append(msgs,
			map[string]any{"role": "user", "content": "u-" + largeContent},
			map[string]any{"role": "assistant", "content": "a-" + largeContent},
		)
	}
	body, _ := json.Marshal(map[string]any{"model": "test", "messages": msgs})

	const llmSummary = "Deterministic LLM summary text for the regression test."
	summarizer := func(ctx context.Context, b []byte, protocol string) (string, bool) {
		return llmSummary, true
	}

	rc := NewRecoveryCoordinator(RecoveryDeps{
		Cache:      nil,
		Summarizer: summarizer,
		MaxRetries: 2,
	})

	res := rc.Recover(context.Background(), body, "openai", 5000, "t", "s-llm", 0)
	if res.Strategy != "smart_window_llm" {
		t.Fatalf("expected strategy=smart_window_llm, got %s", res.Strategy)
	}
	if res.CutMarker == nil {
		t.Fatal("expected non-nil CutMarker")
	}
	if res.CutMarker.SummaryMarker == "" {
		t.Fatal("LLM summary branch MUST emit an smm_v1 marker")
	}
	want := BuildSummaryMarker(llmSummary)
	if res.CutMarker.SummaryMarker != want {
		t.Errorf("LLM marker = %q, want %q", res.CutMarker.SummaryMarker, want)
	}
}

func TestRecoveryCoordinator_MechanicalFallback_CacheReuse(t *testing.T) {
	largeContent := strings.Repeat("cacheable body. ", 500)
	msgs := []map[string]any{{"role": "system", "content": "Sys"}}
	for i := 0; i < 10; i++ {
		msgs = append(msgs,
			map[string]any{"role": "user", "content": largeContent},
			map[string]any{"role": "assistant", "content": largeContent},
		)
	}
	body, _ := json.Marshal(map[string]any{"model": "test", "messages": msgs})
	cache := NewSessionCache(nil, nil)
	rc := NewRecoveryCoordinator(RecoveryDeps{Cache: cache, MaxRetries: 2})
	first := rc.Recover(context.Background(), body, "openai", 5000, "tenant", "reuse", 0)
	if first.Strategy != "smart_window_mechanical" || first.CutMarker == nil {
		t.Fatalf("first recovery = strategy=%q marker=%v", first.Strategy, first.CutMarker)
	}
	msgs = append(msgs,
		map[string]any{"role": "user", "content": "new turn"},
		map[string]any{"role": "assistant", "content": "new answer"},
	)
	body2, _ := json.Marshal(map[string]any{"model": "test", "messages": msgs})
	second := rc.Recover(context.Background(), body2, "openai", 5000, "tenant", "reuse", 0)
	if second.Strategy != "incremental_cache" {
		t.Fatalf("expected incremental cache reuse, got %q (%s)", second.Strategy, second.Reason)
	}
	if second.CutMarker == nil || second.CutMarker.SummaryMarker != "" {
		t.Fatalf("mechanical cache reuse carried SMM marker: %+v", second.CutMarker)
	}
}

func TestRecoveryCoordinator_LLMSummary_BodyUsesRecoveryPrefix(t *testing.T) {
	body := makeRecoveryTestBody()
	const summary = "recovery summary"
	rc := NewRecoveryCoordinator(RecoveryDeps{
		Summarizer: func(context.Context, []byte, string) (string, bool) { return summary, true },
		MaxRetries: 2,
	})
	res := rc.Recover(context.Background(), body, "openai", 5000, "tenant", "recovery-body", 0)
	bodyText := string(res.NewBody)
	if res.Strategy != "smart_window_llm" || !strings.Contains(bodyText, "Gateway compacted conversation summary") || !strings.Contains(bodyText, summary) {
		t.Fatalf("recovery body does not contain its summary prefix: strategy=%q body=%s", res.Strategy, res.NewBody)
	}
}

func makeRecoveryTestBody() []byte {
	largeContent := strings.Repeat("recovery body. ", 500)
	msgs := []map[string]any{{"role": "system", "content": "Sys"}}
	for i := 0; i < 10; i++ {
		msgs = append(msgs,
			map[string]any{"role": "user", "content": largeContent},
			map[string]any{"role": "assistant", "content": largeContent},
		)
	}
	body, _ := json.Marshal(map[string]any{"model": "test", "messages": msgs})
	return body
}

// We use a nil deps (→ ok=false) and assert the coordinator then degrades to
// the mechanical strategy — proving the wiring path is exercised and the
// fallback works end-to-end. A full LLM-call test lives behind compaction
// integration tests (needs network); here we lock in the contract.
func TestNewSummaryFunc_WiredIntoRecovery(t *testing.T) {
	largeContent := strings.Repeat("Long content. ", 300)
	msgs := []map[string]any{
		{"role": "system", "content": "Sys"},
	}
	for i := 0; i < 6; i++ {
		msgs = append(msgs, map[string]any{"role": "user", "content": largeContent})
		msgs = append(msgs, map[string]any{"role": "assistant", "content": largeContent})
	}
	body, _ := json.Marshal(map[string]any{"model": "test", "messages": msgs})

	// Wire Summarizer via the adapter with nil deps — it must report false,
	// forcing the coordinator onto the mechanical path.
	rc := NewRecoveryCoordinator(RecoveryDeps{
		Summarizer: NewSummaryFunc(nil),
		MaxRetries: 2,
	})
	res := rc.Recover(context.Background(), body, "openai", 5000, "t", "s", 0)

	if !res.ShouldRetry {
		t.Fatal("expected ShouldRetry=true")
	}
	// ok=false from Summarizer → must fall back to mechanical, not llm.
	if res.Strategy != "smart_window_mechanical" {
		t.Errorf("expected mechanical fallback when summarizer ok=false, got strategy=%s", res.Strategy)
	}
}
