package streaming

import (
	"encoding/json"
	"testing"
)

// TestConvertAnthropicBodyToOpenAI_ParallelToolResultsAllEmitted guards
// R12 候选4: a user message answering N parallel tool calls is ONE Anthropic
// message with N tool_result blocks. The retired convertBlockMessage
// returned at the FIRST tool_result, silently dropping every remaining
// result and any trailing text. Each result must now become its own
// {"role":"tool"} message, with trailing text preserved.
func TestConvertAnthropicBodyToOpenAI_ParallelToolResultsAllEmitted(t *testing.T) {
	body := []byte(`{
		"model": "claude-x",
		"max_tokens": 128,
		"messages": [
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "call_a", "content": "result A"},
				{"type": "tool_result", "tool_use_id": "call_b", "content": [{"type": "text", "text": "result B"}]},
				{"type": "text", "text": "both tools finished"}
			]}
		]
	}`)
	out, err := ConvertAnthropicBodyToOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Messages []struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Messages) != 3 {
		t.Fatalf("got %d messages, want 3 (tool A + tool B + trailing text): %s", len(v.Messages), out)
	}
	if v.Messages[0].Role != "tool" || v.Messages[0].ToolCallID != "call_a" {
		t.Errorf("msg0 = role=%s tool_call_id=%s, want tool/call_a", v.Messages[0].Role, v.Messages[0].ToolCallID)
	}
	if string(v.Messages[0].Content) != `"result A"` {
		t.Errorf("msg0 content = %s, want result A", v.Messages[0].Content)
	}
	if v.Messages[1].Role != "tool" || v.Messages[1].ToolCallID != "call_b" {
		t.Errorf("msg1 = role=%s tool_call_id=%s, want tool/call_b", v.Messages[1].Role, v.Messages[1].ToolCallID)
	}
	if string(v.Messages[1].Content) != `"result B"` {
		t.Errorf("msg1 content = %s, want result B (block-array content flattened)", v.Messages[1].Content)
	}
	if v.Messages[2].Role != "user" || string(v.Messages[2].Content) != `"both tools finished"` {
		t.Errorf("msg2 = role=%s content=%s, want trailing user text preserved", v.Messages[2].Role, v.Messages[2].Content)
	}
}

// TestConvertAnthropicBodyToOpenAI_BlockArraySystem guards R12 候选4: the
// request body's system field accepts both a plain string and an array of
// content blocks (e.g. with cache_control). Declaring it string made any
// block-array system hard-fail the whole body unmarshal.
func TestConvertAnthropicBodyToOpenAI_BlockArraySystem(t *testing.T) {
	body := []byte(`{
		"model": "claude-x",
		"max_tokens": 64,
		"system": [
			{"type": "text", "text": "You are terse.", "cache_control": {"type": "ephemeral"}},
			{"type": "text", "text": "Always answer in Chinese."}
		],
		"messages": [{"role": "user", "content": "hi"}]
	}`)
	out, err := ConvertAnthropicBodyToOpenAI(body)
	if err != nil {
		t.Fatalf("block-array system must not fail the body unmarshal: %v", err)
	}
	var v struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Messages) != 2 {
		t.Fatalf("got %d messages, want 2 (system + user): %s", len(v.Messages), out)
	}
	if v.Messages[0].Role != "system" {
		t.Errorf("msg0 role = %s, want system", v.Messages[0].Role)
	}
	if v.Messages[0].Content != "You are terse.\nAlways answer in Chinese." {
		t.Errorf("system content = %q, want joined block texts", v.Messages[0].Content)
	}
	if v.Messages[1].Role != "user" || v.Messages[1].Content != "hi" {
		t.Errorf("user message mangled: %+v", v.Messages[1])
	}
}

// TestConvertAnthropicBodyToOpenAI_StringSystemStillWorks pins the plain
// string system shape on the RawMessage-typed field.
func TestConvertAnthropicBodyToOpenAI_StringSystemStillWorks(t *testing.T) {
	body := []byte(`{
		"model": "claude-x",
		"max_tokens": 64,
		"system": "You are terse.",
		"messages": [{"role": "user", "content": "hi"}]
	}`)
	out, err := ConvertAnthropicBodyToOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Messages) != 2 || v.Messages[0].Role != "system" || v.Messages[0].Content != "You are terse." {
		t.Fatalf("string system broken: %s", out)
	}
}
