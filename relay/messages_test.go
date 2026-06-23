package relay

import (
	"encoding/json"
	"testing"
)

// TestConvertChatResponseToAnthropic_ReasoningContent verifies that
// OpenAI-style reasoning_content emitted by thinking upstreams
// (minimax-M3, DeepSeek-R1, etc.) is surfaced as a standalone Anthropic
// `thinking` content block before the visible text — so SDK clients
// render the trace instead of silently truncating it.
func TestConvertChatResponseToAnthropic_ReasoningContent(t *testing.T) {
	body := []byte(`{
		"id":"chatcmpl-r1",
		"object":"chat.completion",
		"model":"minimax-m3",
		"choices":[{
			"index":0,
			"message":{
				"role":"assistant",
				"content":"HELLO",
				"reasoning_content":"thinking step by step"
			},
			"finish_reason":"stop"
		}],
		"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}
	}`)
	out := convertChatResponseToAnthropic(body, "minimax-m3", "req-test-r1")
	var got struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"content"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output not valid JSON: %v body=%s", err, out)
	}
	if len(got.Content) != 2 {
		t.Fatalf("expected 2 content blocks (thinking + text); got %d: %s", len(got.Content), out)
	}
	if got.Content[0].Type != "thinking" {
		t.Errorf("content[0].type = %q, want \"thinking\" (reasoning must come first)", got.Content[0].Type)
	}
	if got.Content[0].Thinking != "thinking step by step" {
		t.Errorf("content[0].thinking = %q, want \"thinking step by step\"", got.Content[0].Thinking)
	}
	if got.Content[1].Type != "text" {
		t.Errorf("content[1].type = %q, want \"text\"", got.Content[1].Type)
	}
	if got.Content[1].Text != "HELLO" {
		t.Errorf("content[1].text = %q, want \"HELLO\"", got.Content[1].Text)
	}
}

// TestConvertChatResponseToAnthropic_ReasoningEmpty covers the case
// where reasoning_content is present-but-empty: must not emit an empty
// thinking block (would produce a visible-but-empty block in SDK UIs).
func TestConvertChatResponseToAnthropic_ReasoningEmpty(t *testing.T) {
	body := []byte(`{
		"id":"chatcmpl-r2",
		"choices":[{"index":0,"message":{"role":"assistant","content":"HI","reasoning_content":""},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
	}`)
	out := convertChatResponseToAnthropic(body, "m", "req-empty")
	var got struct {
		Content []map[string]any `json:"content"`
	}
	_ = json.Unmarshal(out, &got)
	for _, b := range got.Content {
		if b["type"] == "thinking" {
			t.Errorf("empty reasoning_content must not produce a thinking block; got: %s", out)
		}
	}
	if len(got.Content) != 1 || got.Content[0]["type"] != "text" {
		t.Errorf("expected exactly one text block; got %s", out)
	}
}

// TestConvertChatResponseToAnthropic_ReasoningOnly covers a response
// where the upstream produced only reasoning and no visible text (e.g.
// reasoning model that hit max_tokens while still thinking). Without
// this fix the gateway would emit an empty text block; with the fix it
// emits just the thinking block.
func TestConvertChatResponseToAnthropic_ReasoningOnly(t *testing.T) {
	body := []byte(`{
		"id":"chatcmpl-r3",
		"choices":[{
			"index":0,
			"message":{"role":"assistant","content":"","reasoning_content":"truncated thought"},
			"finish_reason":"length"
		}],
		"usage":{"prompt_tokens":1,"completion_tokens":50,"total_tokens":51}
	}`)
	out := convertChatResponseToAnthropic(body, "minimax-m3", "req-ro")
	var got struct {
		Content []struct {
			Type     string `json:"type"`
			Thinking string `json:"thinking"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	_ = json.Unmarshal(out, &got)
	if len(got.Content) != 1 || got.Content[0].Type != "thinking" {
		t.Errorf("expected exactly one thinking block (no empty text); got %s", out)
	}
	if got.Content[0].Thinking != "truncated thought" {
		t.Errorf("thinking content lost: %s", out)
	}
	if got.StopReason != "max_tokens" {
		t.Errorf("stop_reason = %q, want max_tokens (length maps to max_tokens)", got.StopReason)
	}
}

// TestConvertChatResponseToAnthropic_NoReasoningRegression covers the
// pre-Phase-2 baseline: a response without reasoning_content must still
// produce a single text block (no thinking block, no extra wrapping).
func TestConvertChatResponseToAnthropic_NoReasoningRegression(t *testing.T) {
	body := []byte(`{
		"id":"chatcmpl-x",
		"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
	}`)
	out := convertChatResponseToAnthropic(body, "m", "req-noreg")
	var got struct {
		Content []struct {
			Type string `json:"type"`
		} `json:"content"`
	}
	_ = json.Unmarshal(out, &got)
	if len(got.Content) != 1 {
		t.Fatalf("expected 1 content block (no reasoning); got %d: %s", len(got.Content), out)
	}
	if got.Content[0].Type != "text" {
		t.Errorf("content[0].type = %q, want \"text\"", got.Content[0].Type)
	}
}

// TestConvertChatResponseToAnthropic_SplitsEmbeddedThink covers the
// Phase 4 non-stream variant: minimax OpenAI packs the reasoning
// trace into the `content` field as `<think>...</think>` rather than
// using a separate `reasoning_content` field. The gateway must split
// this into independent thinking + text blocks.
func TestConvertChatResponseToAnthropic_SplitsEmbeddedThink(t *testing.T) {
	body := []byte(`{
		"id":"chatcmpl-mx",
		"choices":[{
			"index":0,
			"message":{
				"role":"assistant",
				"content":"<think>step by step</think>\n\n17 * 23 = 391"
			},
			"finish_reason":"stop"
		}],
		"usage":{"prompt_tokens":1,"completion_tokens":2}
	}`)
	out := convertChatResponseToAnthropic(body, "minimax-m3", "req-split")
	var got struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"content"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, out)
	}
	if len(got.Content) != 2 {
		t.Fatalf("expected 2 blocks (thinking + text); got %d: %s body=%s", len(got.Content), got.Content, out)
	}
	if got.Content[0].Type != "thinking" {
		t.Errorf("block[0].type = %q, want thinking", got.Content[0].Type)
	}
	if got.Content[0].Thinking != "step by step" {
		t.Errorf("block[0].thinking = %q, want %q", got.Content[0].Thinking, "step by step")
	}
	if got.Content[1].Type != "text" {
		t.Errorf("block[1].type = %q, want text", got.Content[1].Type)
	}
	if got.Content[1].Text != "17 * 23 = 391" {
		t.Errorf("block[1].text = %q, want %q", got.Content[1].Text, "17 * 23 = 391")
	}
}

// TestConvertChatResponseToAnthropic_EmbeddedThinkEmptyAfter covers
// the case where <think> captures everything (no visible text after).
// Result should be a thinking block only, no trailing empty text block.
func TestConvertChatResponseToAnthropic_EmbeddedThinkEmptyAfter(t *testing.T) {
	body := []byte(`{
		"id":"chatcmpl-empty",
		"choices":[{
			"index":0,
			"message":{
				"role":"assistant",
				"content":"<think>only thinking</think>"
			},
			"finish_reason":"stop"
		}],
		"usage":{"prompt_tokens":1,"completion_tokens":1}
	}`)
	out := convertChatResponseToAnthropic(body, "m", "req-empty")
	var got struct {
		Content []map[string]any `json:"content"`
	}
	_ = json.Unmarshal(out, &got)
	if len(got.Content) != 1 {
		t.Fatalf("expected 1 thinking block only; got %d: %s", len(got.Content), out)
	}
	if got.Content[0]["type"] != "thinking" {
		t.Errorf("block[0].type = %v, want thinking", got.Content[0]["type"])
	}
	if got.Content[0]["thinking"] != "only thinking" {
		t.Errorf("block[0].thinking = %v", got.Content[0]["thinking"])
	}
}

// TestConvertChatResponseToAnthropic_ReasoningContentWinsOverEmbedded
// verifies that when an upstream emits BOTH reasoning_content AND
// content with embedded <think>, the gateway prefers the structured
// reasoning_content field. This avoids double-counting thinking.
func TestConvertChatResponseToAnthropic_ReasoningContentWinsOverEmbedded(t *testing.T) {
	body := []byte(`{
		"id":"chatcmpl-both",
		"choices":[{
			"index":0,
			"message":{
				"role":"assistant",
				"reasoning_content":"structured reasoning",
				"content":"<think>embedded thinking</think>\n\nvisible"
			},
			"finish_reason":"stop"
		}],
		"usage":{"prompt_tokens":1,"completion_tokens":2}
	}`)
	out := convertChatResponseToAnthropic(body, "m", "req-both")
	var got struct {
		Content []struct {
			Type     string `json:"type"`
			Thinking string `json:"thinking"`
			Text     string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(out, &got)
	if len(got.Content) != 2 {
		t.Fatalf("expected 2 blocks; got %d: %s", len(got.Content), out)
	}
	if got.Content[0].Type != "thinking" || got.Content[0].Thinking != "structured reasoning" {
		t.Errorf("expected first block to use reasoning_content; got %+v", got.Content[0])
	}
	if got.Content[1].Type != "text" || got.Content[1].Text != "<think>embedded thinking</think>\n\nvisible" {
		t.Errorf("expected second block to keep raw content (no split); got %+v", got.Content[1])
	}
}

// TestNormalizeModelForStickyKey verifies that model name variants
// (case differences, version suffixes) are normalized to the same
// sticky key, preventing session binding breakage.
// Root cause: session ses_10bf0d6e4ffeKTnHBNBwN0CnTx exhibited
// alternating success/transient failures because the client sent
// different model name variants, generating different sticky keys.
func TestNormalizeModelForStickyKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Basic normalization
		{"minimax-m3", "minimax-m3"},
		{"MiniMax-M3", "minimax-m3"},     // case insensitive
		{"  minimax-m3  ", "minimax-m3"}, // trim spaces
		{"", "default"},                  // empty → default
		{"  ", "default"},                // blank → default

		// Version suffix removal
		{"minimax-m3-v2", "minimax-m3"},
		{"minimax-m3-v1", "minimax-m3"},
		{"gpt-4-turbo-preview", "gpt-4-turbo"},
		{"claude-3-opus-latest", "claude-3-opus"},
		{"qwen-plus-stable", "qwen-plus"},

		// Mixed case + version suffix
		{"MiniMax-M3-V2", "minimax-m3"},
		{"GPT-4-Turbo-Preview", "gpt-4-turbo"},

		// No suffix (should not change)
		{"gpt-4", "gpt-4"},
		{"claude-3-sonnet", "claude-3-sonnet"},

		// Edge cases
		{"v2", "v2"},            // "v2" alone is a valid model name, keep as-is
		{"-preview", "default"}, // pure suffix → empty → default
	}

	for _, tt := range tests {
		got := normalizeModelForStickyKey(tt.input)
		// Special handling for edge cases that become empty
		if tt.want == "" {
			tt.want = "default"
		}
		if got != tt.want {
			t.Errorf("normalizeModelForStickyKey(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestBuildRouteStickyKey_ModelNormalization verifies that
// buildRouteStickyKey produces the same sticky key for model
// name variants within the same session.
func TestBuildRouteStickyKey_ModelNormalization(t *testing.T) {
	tenantID := "tenant-1"
	appID := intPtr(100)
	apiKeyID := intPtr(200)
	profile := "Cursor"
	sessionID := "ses_test123"
	endUser := "user-abc"
	fpSeed := "fp-seed-xyz"

	// These model variants should all produce the SAME sticky key
	variants := []string{
		"minimax-m3",
		"MiniMax-M3",
		"MINIMAX-M3",
		"minimax-m3-v2",
		"MiniMax-M3-V1",
		"  minimax-m3  ",
	}

	var keys []string
	for _, model := range variants {
		key := buildRouteStickyKey(tenantID, appID, apiKeyID, profile, sessionID, endUser, fpSeed, model)
		keys = append(keys, key)
	}

	// All keys should be identical
	first := keys[0]
	for i, key := range keys {
		if key != first {
			t.Errorf("variant[%d] (%q) produced different key:\n  got:  %s\n  want: %s",
				i, variants[i], key, first)
		}
	}

	// Verify the key contains the normalized model name (client-scoped, no session)
	want := "tenant-1:100:200:cursor:minimax-m3"
	if first != want {
		t.Errorf("sticky key = %q, want %q", first, want)
	}
}

// TestBuildRouteStickyKey_DifferentModels verifies that
// genuinely different models (not just variants) produce
// different sticky keys, preserving the original design intent.
func TestBuildRouteStickyKey_DifferentModels(t *testing.T) {
	tenantID := "tenant-1"
	appID := intPtr(100)
	apiKeyID := intPtr(200)
	profile := "Cursor"
	sessionID := "ses_test123"
	endUser := "user-abc"
	fpSeed := "fp-seed-xyz"

	key1 := buildRouteStickyKey(tenantID, appID, apiKeyID, profile, sessionID, endUser, fpSeed, "minimax-m3")
	key2 := buildRouteStickyKey(tenantID, appID, apiKeyID, profile, sessionID, endUser, fpSeed, "gpt-4")

	if key1 == key2 {
		t.Errorf("different models should produce different sticky keys:\n  minimax-m3: %s\n  gpt-4:      %s",
			key1, key2)
	}
}
