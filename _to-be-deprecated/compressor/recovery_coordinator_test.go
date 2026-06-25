
package compressor

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
