package ir

// R34 (2026-09-17 audit) regressions: Gemini wire-shape fidelity.
//
// The audit found the Gemini direction had drifted from the real wire
// protocol because every fixture used the gateway's own invented shapes:
//   - "thought" was modeled as a string carrying the thinking text, while
//     the real marker is a boolean beside "text" — a string unmarshal of
//     `true` failed the whole parse (request 400 / response 500);
//   - the response-side tool_use id was synthesized as "<idx>_<name>"
//     while the request side is "<name>_<idx>", so a second-turn tool
//     round-trip through an openai/anthropic client resolved the wrong
//     function name;
//   - the Gemini response serializer was the one refusal fix (692205664)
//     missed, collapsing refusal-only responses to empty parts.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseGemini_BoolThoughtMarker(t *testing.T) {
	body := []byte(`{
		"contents": [{"role": "model", "parts": [
			{"text": "let me think", "thought": true},
			{"text": "final answer"}
		]}]
	}`)
	ir, err := ParseGemini(body)
	if err != nil {
		t.Fatalf("ParseGemini with boolean thought marker failed: %v", err)
	}
	msg := ir.Messages[0]
	if len(msg.Content) != 2 {
		t.Fatalf("content blocks = %d, want 2 (%+v)", len(msg.Content), msg.Content)
	}
	if msg.Content[0].Type != "thinking" || msg.Content[0].Thinking == nil ||
		msg.Content[0].Thinking.Thinking != "let me think" {
		t.Errorf("block0 = %+v, want thinking 'let me think'", msg.Content[0])
	}
	if msg.Content[1].Type != "text" || msg.Content[1].Text != "final answer" {
		t.Errorf("block1 = %+v, want text 'final answer'", msg.Content[1])
	}
}

func TestParseGemini_LegacyStringThoughtStillTolerated(t *testing.T) {
	body := []byte(`{
		"contents": [{"role": "model", "parts": [{"thought": "legacy thinking"}]}]
	}`)
	ir, err := ParseGemini(body)
	if err != nil {
		t.Fatalf("ParseGemini legacy string thought failed: %v", err)
	}
	blocks := ir.Messages[0].Content
	if len(blocks) != 1 || blocks[0].Type != "thinking" ||
		blocks[0].Thinking == nil || blocks[0].Thinking.Thinking != "legacy thinking" {
		t.Errorf("blocks = %+v, want single thinking 'legacy thinking'", blocks)
	}
}

func TestParseGeminiResponse_BoolThoughtMarkerAndToolIDShape(t *testing.T) {
	body := []byte(`{
		"candidates": [{
			"content": {"role": "model", "parts": [
				{"text": "pondering", "thought": true},
				{"functionCall": {"name": "search", "args": {"q": "x"}}}
			]}
		}],
		"usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2}
	}`)
	resp, err := ParseGeminiResponse(body)
	if err != nil {
		t.Fatalf("ParseGeminiResponse with boolean thought failed: %v", err)
	}
	var sawThinking bool
	for _, c := range resp.Content {
		if c.Type == "thinking" && c.Thinking == "pondering" {
			sawThinking = true
		}
	}
	if !sawThinking || resp.ReasoningContent != "pondering" {
		t.Errorf("thinking block missing; content=%+v reasoning=%q", resp.Content, resp.ReasoningContent)
	}
	// Unified id shape: name first, index last (matches request side).
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.ToolCalls))
	}
	if !strings.HasPrefix(resp.ToolCalls[0].ID, "gemini_call_search_") {
		t.Errorf("tool id = %q, want gemini_call_search_<idx>", resp.ToolCalls[0].ID)
	}
	if got := toolUseNameFromID(resp.ToolCalls[0].ID); got != "search" {
		t.Errorf("toolUseNameFromID(%q) = %q, want search", resp.ToolCalls[0].ID, got)
	}
}

func TestToolUseNameFromID_LegacyIndexFirstName(t *testing.T) {
	cases := []struct{ id, want string }{
		{"gemini_call_lookup", "lookup"},
		{"gemini_call_lookup_0", "lookup"},
		{"gemini_call_0_lookup", "lookup"},            // legacy response-side shape
		{"gemini_call_12_get_weather", "get_weather"}, // legacy, multi-digit idx
		{"gemini_call_get_weather_0", "get_weather"},
		{"other_call_x", "other_call_x"},
	}
	for _, tc := range cases {
		if got := toolUseNameFromID(tc.id); got != tc.want {
			t.Errorf("toolUseNameFromID(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
}

func TestSerializeGeminiResponse_RefusalAndThoughtShape(t *testing.T) {
	ir := &InternalResponse{
		Role: "assistant",
		Content: []ResponseContentBlock{
			{Type: "thinking", Thinking: "hmm"},
			{Type: "refusal", Text: "I cannot help with that"},
		},
	}
	out, err := SerializeGeminiResponse(ir, "gemini-2.5-pro")
	if err != nil {
		t.Fatalf("SerializeGeminiResponse failed: %v", err)
	}
	var decoded struct {
		Candidates []struct {
			Content struct {
				Parts []map[string]any `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("decoded output: %v (%s)", err, out)
	}
	parts := decoded.Candidates[0].Content.Parts
	if len(parts) != 2 {
		t.Fatalf("parts = %d (%+v), want 2 (thinking + refusal text)", len(parts), parts)
	}
	if parts[0]["thought"] != true || parts[0]["text"] != "hmm" {
		t.Errorf("thinking part = %+v, want {text: hmm, thought: true}", parts[0])
	}
	if parts[1]["text"] != "I cannot help with that" {
		t.Errorf("refusal part = %+v, want refusal text preserved", parts[1])
	}
}
