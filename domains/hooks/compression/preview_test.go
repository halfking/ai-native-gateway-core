package compression

import (
	"encoding/json"
	"strings"
	"testing"
)

// buildBody encodes a minimal OpenAI chat-completions body from a slice of
// (role, content) pairs. Used to construct test inputs without hand-coding
// the JSON.
func buildBody(messages ...string) []byte {
	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	var msgs []msg
	for i := 0; i+1 < len(messages); i += 2 {
		msgs = append(msgs, msg{Role: messages[i], Content: messages[i+1]})
	}
	b, _ := json.Marshal(map[string]any{"model": "gpt-4o", "messages": msgs})
	return b
}

// buildLargeBody constructs a body that reliably triggers the COUNT window
// (≥ DefaultMaxMsgCount = 50 messages) without relying on a particular
// context-window token estimate.
func buildLargeBody() []byte {
	args := make([]string, 0, 102)
	for i := 0; i < 51; i++ {
		args = append(args, "user", strings.Repeat("a", 50))
	}
	return buildBody(args...)
}

// TestPreview_NilAndEmpty — nil and empty bodies must return nil without panic.
func TestPreview_NilAndEmpty(t *testing.T) {
	if Preview(nil, PreviewOptions{}) != nil {
		t.Error("nil body must return nil PreviewResult")
	}
	if Preview([]byte{}, PreviewOptions{}) != nil {
		t.Error("empty body must return nil PreviewResult")
	}
}

// TestPreview_NoTrigger — a tiny body that is well under budget produces a
// zero-savings result with no stages applied.
func TestPreview_NoTrigger(t *testing.T) {
	body := buildBody("user", "hello")
	res := Preview(body, PreviewOptions{ContextWindow: 128000, Protocol: "openai"})
	if res == nil {
		t.Fatal("expected non-nil PreviewResult for non-empty body")
	}
	if res.WindowTriggered != "" {
		t.Errorf("expected no window trigger, got %q", res.WindowTriggered)
	}
	if res.Strategy != "" {
		t.Errorf("expected no strategy for tiny body, got %q", res.Strategy)
	}
	if res.BytesSaved != 0 {
		t.Errorf("expected 0 bytes saved for untouched body, got %d", res.BytesSaved)
	}
	if res.BytesRatio > 1.0 || res.BytesRatio <= 0 {
		t.Errorf("bytes_ratio = %.3f, should be in (0, 1]", res.BytesRatio)
	}
}

// TestPreview_WindowTrigger_MechanicalTrimFallback — a body large enough to
// trip the COUNT trigger (≥50 messages) should have:
//   - WindowTriggered set
//   - Strategy either "mechanical_trim" or "strip_only"
//   - WouldTryLLMSummary=true and SummarySkipped=true (honesty flag)
//   - BytesAfter ≤ BytesBefore (Preview never inflates)
func TestPreview_WindowTrigger_MechanicalTrimFallback(t *testing.T) {
	body := buildLargeBody()
	res := Preview(body, PreviewOptions{ContextWindow: 8192, Protocol: "openai"})
	if res == nil {
		t.Fatal("nil result for large body")
	}
	if res.WindowTriggered == "" {
		t.Fatalf("expected window trigger for %d-message body (context 8192), got none", res.MsgsBefore)
	}
	// After the window fires, the live path would attempt LLM summary.
	if !res.WouldTryLLMSummary {
		t.Error("WouldTryLLMSummary must be true when window fires and not degraded")
	}
	if !res.SummarySkipped {
		t.Error("SummarySkipped must be true — Preview never runs the LLM")
	}
	if res.BytesAfter > res.BytesBefore {
		t.Errorf("bytes_after (%d) > bytes_before (%d): Preview must not inflate the body",
			res.BytesAfter, res.BytesBefore)
	}
}

// TestPreview_StageBreakdown — every applied stage must have BytesAfter ≤ BytesBefore
// and the last stage's BytesAfter must equal the result's BytesAfter.
func TestPreview_StageBreakdown(t *testing.T) {
	body := buildLargeBody()
	res := Preview(body, PreviewOptions{ContextWindow: 8192, Protocol: "openai"})
	if res == nil || len(res.Stages) == 0 {
		t.Skip("no stages produced; window may not have fired")
	}
	for _, s := range res.Stages {
		if s.Applied && s.BytesAfter > s.BytesBefore {
			t.Errorf("stage %q inflated the body: before=%d after=%d", s.Name, s.BytesBefore, s.BytesAfter)
		}
	}
	last := res.Stages[len(res.Stages)-1]
	if last.Applied && last.BytesAfter != res.BytesAfter {
		t.Errorf("last applied stage BytesAfter=%d ≠ result BytesAfter=%d", last.BytesAfter, res.BytesAfter)
	}
}

// TestPreview_LossinessConsistency — the lossiness label must match what the
// live classifyLossiness function would produce for the same strategy.
// This guards against preview and production labels diverging.
func TestPreview_LossinessConsistency(t *testing.T) {
	body := buildLargeBody()
	res := Preview(body, PreviewOptions{ContextWindow: 8192, Protocol: "openai"})
	if res == nil {
		t.Skip("nil result")
	}
	want := classifyLossiness(res.Strategy, "") // Preview never injects a marker
	if res.Lossiness != want {
		t.Errorf("lossiness = %q, want %q (from classifyLossiness(%q, \"\"))",
			res.Lossiness, want, res.Strategy)
	}
}

// TestPreview_ModeSensitivity — ModeDeltaOnly produces no strip/trim stages.
func TestPreview_ModeSensitivity(t *testing.T) {
	body := buildLargeBody()
	m := ModeDeltaOnly
	res := Preview(body, PreviewOptions{ContextWindow: 8192, Mode: &m})
	if res == nil {
		t.Fatal("nil result")
	}
	for _, s := range res.Stages {
		if s.Applied {
			t.Errorf("ModeDeltaOnly must not produce applied stages; got %q", s.Name)
		}
	}
	if res.Strategy != "" {
		t.Errorf("ModeDeltaOnly strategy = %q, want empty", res.Strategy)
	}
}

// TestPreview_MediaPruneOption — PruneMedia:true adds a prune_media stage
// when the body contains media blocks.
func TestPreview_MediaPruneOption(t *testing.T) {
	// Build a body with many image blocks to guarantee pruning triggers.
	msgs := make([]any, 0, 6)
	for i := 0; i < 6; i++ {
		msgs = append(msgs, map[string]any{
			"role":    "user",
			"content": []map[string]any{{"type": "image", "text": "x"}},
		})
	}
	body, _ := json.Marshal(map[string]any{"messages": msgs})

	res := Preview(body, PreviewOptions{
		PruneMedia: true,
		KeepMedia:  2,
	})
	if res == nil {
		t.Fatal("nil result")
	}

	var found bool
	for _, s := range res.Stages {
		if s.Name == "prune_media" {
			found = true
			if s.Applied && s.Detail == nil {
				t.Error("prune_media stage applied but Detail is nil")
			}
			break
		}
	}
	if !found {
		t.Error("PruneMedia:true must produce a prune_media stage")
	}
}

// TestPreview_PrefixHashPopulated — CompressedPrefixHash must be non-empty
// when the body is valid JSON with messages.
func TestPreview_PrefixHashPopulated(t *testing.T) {
	body := buildBody("system", "You are helpful.", "user", "Hello!")
	res := Preview(body, PreviewOptions{})
	if res == nil {
		t.Fatal("nil result")
	}
	if res.CompressedPrefixHash == "" {
		t.Error("CompressedPrefixHash should be populated for a valid body with messages")
	}
}
