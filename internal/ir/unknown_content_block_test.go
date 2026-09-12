package ir

import (
	"testing"
)

// 2026-09-12 audit P1: Anthropic responses carrying block types the IR does
// not model (server_tool_use, web_search_tool_result, ...) used to be
// silently dropped by the parse switch, flattening unknown-only responses
// into an "empty" parse that downstream guards misclassified as
// empty_response (provider demotion + billing refusal for real upstream
// work). The parser must record the types and expose the distinction.
func TestParseAnthropicResponse_UnknownBlockTypes(t *testing.T) {
	body := []byte(`{
		"id": "msg_unknown",
		"type": "message",
		"role": "assistant",
		"model": "claude-fake",
		"content": [
			{"type": "server_tool_use", "id": "stu_1", "name": "web_search", "input": {"query": "x"}},
			{"type": "text", "text": "answer"},
			{"type": "server_tool_use", "id": "stu_2", "name": "web_search", "input": {"query": "y"}},
			{"type": "web_search_tool_result", "tool_use_id": "stu_1", "content": []}
		],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 10, "output_tokens": 5}
	}`)

	parsed, err := ParseAnthropicResponse(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, want := parsed.UnknownBlockTypes, []string{"server_tool_use", "web_search_tool_result"}; !equalStrings(got, want) {
		t.Fatalf("UnknownBlockTypes = %v, want %v (deduped, first-seen order)", got, want)
	}
	if len(parsed.Content) != 1 || parsed.Content[0].Type != "text" {
		t.Fatalf("known blocks must keep parsing: Content = %+v", parsed.Content)
	}
	if parsed.OnlyUnsupportedBlocks() {
		t.Fatal("mixed response must not be classified unknown-only")
	}
}

func TestParseAnthropicResponse_UnknownOnlyBlocks(t *testing.T) {
	body := []byte(`{
		"id": "msg_unknown_only",
		"type": "message",
		"role": "assistant",
		"model": "claude-fake",
		"content": [{"type": "server_tool_use", "id": "stu_1", "name": "web_search", "input": {}}],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 10, "output_tokens": 5}
	}`)

	parsed, err := ParseAnthropicResponse(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !parsed.OnlyUnsupportedBlocks() {
		t.Fatal("unknown-only response must be classified OnlyUnsupportedBlocks")
	}
	// The classification is what keeps these out of the empty_response
	// bucket; verify the predicate is false the moment any representable
	// content exists.
	parsed.Content = append(parsed.Content, ResponseContentBlock{Type: "text", Text: "late text"})
	if parsed.OnlyUnsupportedBlocks() {
		t.Fatal("text content must clear the unknown-only classification")
	}
}

func TestParseAnthropicResponse_TrueEmptyNotFlaggedUnknown(t *testing.T) {
	body := []byte(`{
		"id": "msg_empty",
		"type": "message",
		"role": "assistant",
		"model": "claude-fake",
		"content": [],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 3, "output_tokens": 0}
	}`)

	parsed, err := ParseAnthropicResponse(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(parsed.UnknownBlockTypes) != 0 {
		t.Fatalf("truly empty response must not record unknown types, got %v", parsed.UnknownBlockTypes)
	}
	if parsed.OnlyUnsupportedBlocks() {
		t.Fatal("truly empty response is an empty_response, not unsupported blocks")
	}
}

func TestParseAnthropicResponse_UnknownBlockTypeCap(t *testing.T) {
	content := `{"type": "b0"},{"type": "b1"},{"type": "b2"},{"type": "b3"},{"type": "b4"},` +
		`{"type": "b5"},{"type": "b6"},{"type": "b7"},{"type": "b8"},{"type": "b9"}`
	body := []byte(`{
		"id": "msg_cap", "type": "message", "role": "assistant", "model": "m",
		"content": [` + content + `],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 1, "output_tokens": 1}
	}`)

	parsed, err := ParseAnthropicResponse(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(parsed.UnknownBlockTypes) != maxUnknownBlockTypes {
		t.Fatalf("UnknownBlockTypes length = %d, want capped at %d", len(parsed.UnknownBlockTypes), maxUnknownBlockTypes)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
