package compression

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
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

func TestRecoveryCoordinator_LargeSessionUsesIncrementalCache(t *testing.T) {
	for _, turns := range []int{200, 1000} {
		t.Run(fmt.Sprintf("turns_%d", turns), func(t *testing.T) {
			body := makeLargeRecoveryBody(turns)
			cache := NewSessionCache(nil, nil)
			rc := NewRecoveryCoordinator(RecoveryDeps{Cache: cache})

			first := rc.Recover(context.Background(), body, "openai", 8000, "tenant", "large-session", 0)
			if !first.ShouldRetry || first.Strategy != "smart_window_mechanical" {
				t.Fatalf("first recovery failed: strategy=%q reason=%q", first.Strategy, first.Reason)
			}

			body = appendRecoveryTurn(t, body, turns)
			second := rc.Recover(context.Background(), body, "openai", 8000, "tenant", "large-session", 0)
			if !second.ShouldRetry || second.Strategy != "incremental_cache" {
				t.Fatalf("expected incremental cache reuse, got strategy=%q reason=%q", second.Strategy, second.Reason)
			}
			if second.EstTokensAfter >= second.EstTokensBefore {
				t.Fatalf("incremental build did not reduce tokens: %d >= %d", second.EstTokensAfter, second.EstTokensBefore)
			}
			if !strings.Contains(string(second.NewBody), "initial-task-anchor") {
				t.Fatal("incremental build lost the pinned first user request")
			}
		})
	}
}

func TestRecoveryCoordinator_RepeatedRecoveryDoesNotLeakGoroutines(t *testing.T) {
	body := makeLargeRecoveryBody(200)
	before := runtime.NumGoroutine()

	for i := 0; i < 100; i++ {
		cache := NewSessionCache(nil, nil)
		rc := NewRecoveryCoordinator(RecoveryDeps{Cache: cache})
		res := rc.Recover(context.Background(), body, "openai", 8000, "tenant", fmt.Sprintf("session-%d", i), 0)
		if !res.ShouldRetry {
			t.Fatalf("recovery %d failed: %s", i, res.Reason)
		}
	}

	runtime.GC()
	time.Sleep(20 * time.Millisecond)
	after := runtime.NumGoroutine()
	if delta := after - before; delta > 2 {
		t.Fatalf("goroutine count grew by %d (before=%d after=%d)", delta, before, after)
	}
}

func BenchmarkRecoveryCoordinator_LargeSession(b *testing.B) {
	body := makeLargeRecoveryBody(1000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rc := NewRecoveryCoordinator(RecoveryDeps{})
		if res := rc.Recover(context.Background(), body, "openai", 8000, "", "", 0); !res.ShouldRetry {
			b.Fatalf("recovery failed: %s", res.Reason)
		}
	}
}

func makeLargeRecoveryBody(turns int) []byte {
	messages := make([]map[string]any, 0, 1+turns*2)
	messages = append(messages, map[string]any{"role": "system", "content": "system"})
	for i := 0; i < turns; i++ {
		user := strings.Repeat(fmt.Sprintf("turn-%d-user ", i), 40)
		if i == 0 {
			user = "initial-task-anchor " + user
		}
		messages = append(messages,
			map[string]any{"role": "user", "content": user},
			map[string]any{"role": "assistant", "content": strings.Repeat(fmt.Sprintf("turn-%d-assistant ", i), 40)},
		)
	}
	body, _ := json.Marshal(map[string]any{"model": "test", "messages": messages})
	return body
}

func appendRecoveryTurn(t *testing.T, body []byte, turn int) []byte {
	t.Helper()
	var request struct {
		Model    string           `json:"model"`
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatal(err)
	}
	request.Messages = append(request.Messages,
		map[string]any{"role": "user", "content": fmt.Sprintf("new-user-%d", turn)},
		map[string]any{"role": "assistant", "content": fmt.Sprintf("new-assistant-%d", turn)},
	)
	updated, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return updated
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

// TestNewSummaryFunc_WiredIntoRecovery confirms the integration: a
// RecoveryCoordinator built with NewSummaryFunc(deps) consults the summarizer.
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
