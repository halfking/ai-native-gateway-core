package ir

import (
	"bytes"
	"encoding/json"
	"testing"
)

// TestToolUseNameFromID pins the prefix/suffix-stripping contract used by the
// Gemini serializer when reconstructing functionResponse.name from a
// tool_use_id. The 2026-09-01 P0-1 fix appended "_<partIdx>" to disambiguate
// parallel functionCall parts; the serializer must strip that suffix on
// round-trip so the parsed name still matches.
func TestToolUseNameFromID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"gemini_call_lookup", "lookup"},
		{"gemini_call_lookup_0", "lookup"},
		{"gemini_call_lookup_1", "lookup"},
		{"gemini_call_lookup_42", "lookup"},
		// Name itself contains underscore: "get_weather" — keep it intact.
		{"gemini_call_get_weather_0", "get_weather"},
		// No prefix: pass through.
		{"call_abc", "call_abc"},
		// Empty: pass through.
		{"", ""},
		// Prefix only (degenerate): pass through (no name to extract).
		{"gemini_call_", "gemini_call_"},
	}
	for _, tc := range cases {
		got := toolUseNameFromID(tc.in)
		if got != tc.want {
			t.Errorf("toolUseNameFromID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// R52 回归钉桩：OpenAI 系上游（DeepSeek-R1/GLM）的 reasoning_content 只进
// ir.ReasoningContent、不产生 thinking Content 块，SerializeGeminiResponse
// 此前不读该字段 → Gemini 客户端非流式静默丢推理（流式有 thought part，
// 非流式不对称）。
func TestSerializeGeminiResponse_ReasoningContentEmittedAsThought(t *testing.T) {
	ir := &InternalResponse{
		Role:             "assistant",
		Model:            "deepseek-r1",
		ReasoningContent: "step one: think",
		Content: []ResponseContentBlock{
			{Type: "text", Text: "final answer"},
		},
	}
	raw, err := SerializeGeminiResponse(ir, "")
	if err != nil {
		t.Fatalf("SerializeGeminiResponse: %v", err)
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []map[string]any `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(out.Candidates))
	}
	parts := out.Candidates[0].Content.Parts
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2 (thought + text): %v", len(parts), parts)
	}
	if parts[0]["thought"] != true || parts[0]["text"] != "step one: think" {
		t.Fatalf("first part = %v, want thought=true text='step one: think'", parts[0])
	}
	if parts[1]["text"] != "final answer" {
		t.Fatalf("second part = %v, want text='final answer'", parts[1])
	}
}

// anthropic 上游的 thinking 已是 Content 块（且同样聚合进
// ReasoningContent）——不得双写 thought part。
func TestSerializeGeminiResponse_ThinkingBlockNotDoubled(t *testing.T) {
	ir := &InternalResponse{
		Role:             "assistant",
		Model:            "claude-x",
		ReasoningContent: "aggregated thinking",
		Content: []ResponseContentBlock{
			{Type: "thinking", Thinking: "block thinking"},
			{Type: "text", Text: "answer"},
		},
	}
	raw, err := SerializeGeminiResponse(ir, "")
	if err != nil {
		t.Fatalf("SerializeGeminiResponse: %v", err)
	}
	if got := bytes.Count(raw, []byte(`"thought":true`)); got != 1 {
		t.Fatalf("thought parts = %d, want 1 (no double emission): %s", got, raw)
	}
}
