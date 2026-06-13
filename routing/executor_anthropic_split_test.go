package routing

import (
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/textsplit"
)

// TestSplitLeadingThinkBlock_Basic covers the simple "extract leading
// <think>...</think>" case.
func TestSplitLeadingThinkBlock_Basic(t *testing.T) {
	think, rest, ok := textsplit.SplitLeadingThink("<think>plan</think>\n\nHELLO")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if think != "plan" {
		t.Errorf("think = %q, want %q", think, "plan")
	}
	if rest != "HELLO" {
		t.Errorf("rest = %q, want %q", rest, "HELLO")
	}
}

// TestSplitLeadingThinkBlock_NoThinkTag verifies no transformation when
// the prefix is missing (so legitimate XML/HTML content in text blocks
// is never touched).
func TestSplitLeadingThinkBlock_NoThinkTag(t *testing.T) {
	think, rest, ok := textsplit.SplitLeadingThink("just a normal response")
	if ok {
		t.Error("expected ok=false when <think> prefix missing")
	}
	if rest != "just a normal response" {
		t.Errorf("rest should be unchanged; got %q", rest)
	}
	if think != "" {
		t.Errorf("think should be empty; got %q", think)
	}
}

// TestSplitLeadingThinkBlock_UnclosedThink guards against partial tags
// (would mean malformed upstream response). We do not promote — must
// leave content untouched so the client can render it as-is.
func TestSplitLeadingThinkBlock_UnclosedThink(t *testing.T) {
	_, rest, ok := textsplit.SplitLeadingThink("<think>never closes\ntext continues")
	if ok {
		t.Error("expected ok=false for unclosed <think>")
	}
	if rest != "<think>never closes\ntext continues" {
		t.Errorf("rest should be unchanged on unclosed; got %q", rest)
	}
}

// TestSplitEmbeddedThinkTags_HappyPath covers the realistic minimax
// upstream response shape: one text block whose text begins with
// `<think>...</think>` followed by the visible answer.
func TestSplitEmbeddedThinkTags_HappyPath(t *testing.T) {
	in := []byte(`{
		"id":"msg_x",
		"type":"message",
		"role":"assistant",
		"model":"minimax-m3",
		"content":[{"type":"text","text":"<think>plan</think>\n\nHELLO"}],
		"stop_reason":"end_turn",
		"usage":{"input_tokens":1,"output_tokens":2}
	}`)
	out := splitEmbeddedThinkTags(in)
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
		t.Fatalf("expected 2 blocks (thinking + text); got %d: %s", len(got.Content), out)
	}
	if got.Content[0].Type != "thinking" {
		t.Errorf("content[0].type = %q, want \"thinking\"", got.Content[0].Type)
	}
	if got.Content[0].Thinking != "plan" {
		t.Errorf("content[0].thinking = %q, want \"plan\"", got.Content[0].Thinking)
	}
	if got.Content[1].Type != "text" {
		t.Errorf("content[1].type = %q, want \"text\"", got.Content[1].Type)
	}
	if got.Content[1].Text != "HELLO" {
		t.Errorf("content[1].text = %q, want \"HELLO\"", got.Content[1].Text)
	}
}

// TestSplitEmbeddedThinkTags_NoThinkPreserved confirms a text block
// without `<think>` is passed through unchanged (no extra wrapping).
func TestSplitEmbeddedThinkTags_NoThinkPreserved(t *testing.T) {
	in := []byte(`{
		"id":"msg_y","type":"message","role":"assistant","model":"m",
		"content":[{"type":"text","text":"normal reply"}],
		"stop_reason":"end_turn"
	}`)
	out := splitEmbeddedThinkTags(in)
	var got struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(out, &got)
	if len(got.Content) != 1 || got.Content[0].Type != "text" || got.Content[0].Text != "normal reply" {
		t.Errorf("expected single unchanged text block; got %s", out)
	}
}

// TestSplitEmbeddedThinkTags_ToolUseUnchanged ensures tool_use blocks
// are passed through unchanged even when they appear alongside text.
func TestSplitEmbeddedThinkTags_ToolUseUnchanged(t *testing.T) {
	in := []byte(`{
		"id":"msg_z","type":"message","role":"assistant","model":"m",
		"content":[
			{"type":"text","text":"<think>call get_weather</think>"},
			{"type":"tool_use","id":"call_1","name":"get_weather","input":{"city":"Beijing"}}
		],
		"stop_reason":"tool_use"
	}`)
	out := splitEmbeddedThinkTags(in)
	var got struct {
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output not valid JSON: %v body=%s", err, out)
	}
	if len(got.Content) != 3 {
		t.Fatalf("expected 3 blocks (thinking + text + tool_use); got %d: %s", len(got.Content), out)
	}
	if got.Content[0]["type"] != "thinking" {
		t.Errorf("block[0].type = %v, want thinking", got.Content[0]["type"])
	}
	if got.Content[0]["thinking"] != "call get_weather" {
		t.Errorf("block[0].thinking = %v", got.Content[0]["thinking"])
	}
	if got.Content[1]["type"] != "text" {
		t.Errorf("block[1].type = %v, want text", got.Content[1]["type"])
	}
	if got.Content[1]["text"] != "" {
		t.Errorf("block[1].text = %v, want empty (think was the only content)", got.Content[1]["text"])
	}
	if got.Content[2]["type"] != "tool_use" {
		t.Errorf("block[2].type = %v, want tool_use (must be untouched)", got.Content[2]["type"])
	}
	if _, ok := got.Content[2]["input"]; !ok {
		t.Errorf("block[2].input lost: %s", out)
	}
}

// TestSplitEmbeddedThinkTags_MultilineThink covers a multi-line thinking
// block (typical for reasoning models that produce paragraph-long plans).
func TestSplitEmbeddedThinkTags_MultilineThink(t *testing.T) {
	in := []byte(`{
		"id":"msg_ml","type":"message","role":"assistant","model":"minimax-m3",
		"content":[{"type":"text","text":"<think>step 1\nstep 2\nstep 3</think>\n\nANSWER"}],
		"stop_reason":"end_turn"
	}`)
	out := splitEmbeddedThinkTags(in)
	var got struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"content"`
	}
	_ = json.Unmarshal(out, &got)
	if len(got.Content) != 2 {
		t.Fatalf("expected 2 blocks; got %d: %s", len(got.Content), out)
	}
	if got.Content[0].Thinking != "step 1\nstep 2\nstep 3" {
		t.Errorf("multiline thinking lost: %q", got.Content[0].Thinking)
	}
	if got.Content[1].Text != "ANSWER" {
		t.Errorf("text = %q, want ANSWER", got.Content[1].Text)
	}
}

// TestSplitEmbeddedThinkTags_InvalidJSON guards that bodies we can't
// parse (e.g. truncated SSE fragments in tests) are returned unchanged
// rather than zeroed out, so an unrelated test or upstream error path
// never produces a garbage Anthropic response to the client.
func TestSplitEmbeddedThinkTags_InvalidJSON(t *testing.T) {
	in := []byte(`not json at all`)
	out := splitEmbeddedThinkTags(in)
	if string(out) != string(in) {
		t.Errorf("invalid JSON must round-trip unchanged; got %s", out)
	}
}

// TestSplitEmbeddedThinkTags_PreservesOtherFields ensures we don't drop
// id, model, stop_reason, usage, etc. when we rebuild the body. These
// fields are critical for downstream SDK rendering and billing.
func TestSplitEmbeddedThinkTags_PreservesOtherFields(t *testing.T) {
	in := []byte(`{
		"id":"msg_keep",
		"type":"message",
		"role":"assistant",
		"model":"minimax-m3",
		"content":[{"type":"text","text":"<think>p</think>\n\nOK"}],
		"stop_reason":"end_turn",
		"stop_sequence":null,
		"usage":{"input_tokens":7,"output_tokens":11}
	}`)
	out := splitEmbeddedThinkTags(in)
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output not valid JSON: %v body=%s", err, out)
	}
	for _, k := range []string{"id", "type", "role", "model", "stop_reason", "stop_sequence", "usage"} {
		if _, ok := got[k]; !ok {
			t.Errorf("field %q lost after split: %s", k, out)
		}
	}
	if got["id"] != "msg_keep" {
		t.Errorf("id mutated: %v", got["id"])
	}
	if got["model"] != "minimax-m3" {
		t.Errorf("model mutated: %v", got["model"])
	}
}