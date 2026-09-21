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

// Audit R20 (2026-09-13): the unknown-block recording must cover ALL
// response parsers, not only Anthropic. Previously ParseResponsesResponse
// had no default branch (web_search_call / mcp_call / ... were silently
// dropped), parseOpenAIResponseContentBlock dropped unknown payloads without
// recording, and ParseGeminiResponse had no unknown-part handling — an
// unknown-only payload collapsed into an empty-looking parse.
func TestParseResponsesResponse_UnknownOutputItems(t *testing.T) {
	body := []byte(`{
		"id": "resp_unknown",
		"object": "response",
		"created_at": 1700000000,
		"status": "completed",
		"model": "gpt-fake",
		"output": [
			{"type": "web_search_call", "id": "ws_1", "status": "completed"},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "answer"}]},
			{"type": "mcp_call", "id": "mcp_1", "status": "completed"}
		],
		"usage": {"input_tokens": 3, "output_tokens": 2, "total_tokens": 5}
	}`)

	parsed, err := ParseResponsesResponse(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, want := parsed.UnknownBlockTypes, []string{"web_search_call", "mcp_call"}; !equalStrings(got, want) {
		t.Fatalf("UnknownBlockTypes = %v, want %v", got, want)
	}
	if parsed.OnlyUnsupportedBlocks() {
		t.Fatal("mixed response must not be classified unknown-only")
	}
}

func TestParseResponsesResponse_UnknownOnlyOutput(t *testing.T) {
	body := []byte(`{
		"id": "resp_unknown_only",
		"object": "response",
		"created_at": 1700000000,
		"status": "completed",
		"model": "gpt-fake",
		"output": [{"type": "file_search_call", "id": "fs_1", "status": "completed"}],
		"usage": {"input_tokens": 3, "output_tokens": 2, "total_tokens": 5}
	}`)

	parsed, err := ParseResponsesResponse(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !parsed.OnlyUnsupportedBlocks() {
		t.Fatal("unknown-only response must be classified unsupported, not empty")
	}
}

func TestParseOpenAIResponse_UnknownContentBlockTypes(t *testing.T) {
	body := []byte(`{
		"id": "chatcmpl_unknown",
		"object": "chat.completion",
		"created": 1700000000,
		"model": "gpt-fake",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": [
					{"type": "image_url", "image_url": {"url": "https://example.com/x.png"}},
					{"type": "text", "text": "answer"}
				]
			},
			"finish_reason": "stop"
		}],
		"usage": {"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}
	}`)

	parsed, err := ParseOpenAIResponse(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, want := parsed.UnknownBlockTypes, []string{"image_url"}; !equalStrings(got, want) {
		t.Fatalf("UnknownBlockTypes = %v, want %v", got, want)
	}
	// Pre-existing shape: the unsupported block stays on Content as a lossy
	// shell (type kept, payload dropped) — now at least the loss is visible
	// via UnknownBlockTypes.
	if len(parsed.Content) != 2 || parsed.Content[0].Type != "image_url" || parsed.Content[1].Type != "text" {
		t.Fatalf("blocks must keep parsing: Content = %+v", parsed.Content)
	}
	if parsed.OnlyUnsupportedBlocks() {
		t.Fatal("mixed response must not be classified unknown-only")
	}
}

func TestParseGeminiResponse_UnknownParts(t *testing.T) {
	body := []byte(`{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [
					{"text": "answer"},
					{"inlineData": {"mimeType": "image/png", "data": "aGk="}},
					{"executableCode": {"language": "PYTHON", "code": "print(1)"}}
				]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 3, "candidatesTokenCount": 2, "totalTokenCount": 5}
	}`)

	parsed, err := ParseGeminiResponse(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, want := parsed.UnknownBlockTypes, []string{"inlineData", "gemini_part_unrecognized"}; !equalStrings(got, want) {
		t.Fatalf("UnknownBlockTypes = %v, want %v", got, want)
	}
	if len(parsed.Content) != 1 || parsed.Content[0].Type != "text" {
		t.Fatalf("known parts must keep parsing: Content = %+v", parsed.Content)
	}
}
