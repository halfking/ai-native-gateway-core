package ir

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseAnthropicResponse_TextOnly(t *testing.T) {
	body := []byte(`{
		"id": "msg_01A",
		"type": "message",
		"role": "assistant",
		"model": "claude-3-5-sonnet",
		"content": [{"type": "text", "text": "Hello world"}],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 10, "output_tokens": 5}
	}`)
	ir, err := ParseAnthropicResponse(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ir.ID != "msg_01A" {
		t.Errorf("id: got %q, want msg_01A", ir.ID)
	}
	if ir.SourceProtocol != ProtocolAnthropicMessages {
		t.Errorf("source: got %q, want %s", ir.SourceProtocol, ProtocolAnthropicMessages)
	}
	if len(ir.Content) != 1 || ir.Content[0].Type != "text" || ir.Content[0].Text != "Hello world" {
		t.Errorf("content: got %+v", ir.Content)
	}
	if ir.FinishReason != "stop" {
		t.Errorf("finish: got %q, want stop", ir.FinishReason)
	}
	if ir.Usage.PromptTokens != 10 || ir.Usage.CompletionTokens != 5 {
		t.Errorf("usage: got %+v", ir.Usage)
	}
}

func TestParseAnthropicResponse_WithThinking(t *testing.T) {
	body := []byte(`{
		"id": "msg_01B",
		"type": "message",
		"role": "assistant",
		"model": "claude-4",
		"content": [
			{"type": "thinking", "thinking": "Let me think..."},
			{"type": "text", "text": "The answer is 42."}
		],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 20, "output_tokens": 8}
	}`)
	ir, err := ParseAnthropicResponse(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ir.ReasoningContent != "Let me think..." {
		t.Errorf("reasoning: got %q", ir.ReasoningContent)
	}
	if len(ir.Content) != 2 {
		t.Errorf("content len: got %d", len(ir.Content))
	}
}

func TestParseAnthropicResponse_WithToolUse(t *testing.T) {
	body := []byte(`{
		"id": "msg_01C",
		"type": "message",
		"role": "assistant",
		"model": "claude-3-5-sonnet",
		"content": [
			{"type": "tool_use", "id": "toolu_01", "name": "get_weather", "input": {"city": "Beijing"}},
			{"type": "text", "text": "The weather is sunny."}
		],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 15, "output_tokens": 12}
	}`)
	ir, err := ParseAnthropicResponse(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ir.FinishReason != "tool_calls" {
		t.Errorf("finish: got %q, want tool_calls", ir.FinishReason)
	}
	if len(ir.ToolCalls) != 1 || ir.ToolCalls[0].ID != "toolu_01" || ir.ToolCalls[0].Name != "get_weather" {
		t.Errorf("toolcalls: got %+v", ir.ToolCalls)
	}
}

func TestParseOpenAIResponse_TextOnly(t *testing.T) {
	body := []byte(`{
		"id": "chatcmpl_01",
		"object": "chat.completion",
		"created": 1234567890,
		"model": "gpt-4o",
		"choices": [{
			"message": {"role": "assistant", "content": "Hi there!"},
			"finish_reason": "stop"
		}],
		"usage": {"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8}
	}`)
	ir, err := ParseOpenAIResponse(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ir.ID != "chatcmpl_01" {
		t.Errorf("id: got %q", ir.ID)
	}
	if ir.SourceProtocol != ProtocolOpenAIChat {
		t.Errorf("source: got %q", ir.SourceProtocol)
	}
	if len(ir.Content) != 1 || ir.Content[0].Text != "Hi there!" {
		t.Errorf("content: got %+v", ir.Content)
	}
	if ir.FinishReason != "stop" {
		t.Errorf("finish: got %q", ir.FinishReason)
	}
	if ir.Usage.TotalTokens != 8 {
		t.Errorf("usage: got %+v", ir.Usage)
	}
}

func TestParseOpenAIResponse_WithToolCalls(t *testing.T) {
	body := []byte(`{
		"id": "chatcmpl_02",
		"object": "chat.completion",
		"created": 1234567891,
		"model": "gpt-4o",
		"choices": [{
			"message": {
				"role": "assistant",
				"content": null,
				"tool_calls": [
					{"id": "call_abc", "type": "function", "function": {"name": "search", "arguments": "{\"query\":\"weather\"}"}}
				]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {"prompt_tokens": 8, "completion_tokens": 6, "total_tokens": 14}
	}`)
	ir, err := ParseOpenAIResponse(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ir.ToolCalls) != 1 {
		t.Fatalf("toolcalls: got %d", len(ir.ToolCalls))
	}
	if ir.ToolCalls[0].ID != "call_abc" || ir.ToolCalls[0].Name != "search" {
		t.Errorf("toolcall: got %+v", ir.ToolCalls[0])
	}
	if ir.FinishReason != "tool_calls" {
		t.Errorf("finish: got %q", ir.FinishReason)
	}
}

func TestParseOpenAIResponse_WithReasoningContent(t *testing.T) {
	body := []byte(`{
		"id": "chatcmpl_03",
		"object": "chat.completion",
		"created": 1234567892,
		"model": "o1-preview",
		"choices": [{
			"message": {
				"role": "assistant",
				"content": "42",
				"reasoning_content": "Let me compute 6*7..."
			},
			"finish_reason": "stop"
		}],
		"usage": {"prompt_tokens": 5, "completion_tokens": 10, "total_tokens": 15}
	}`)
	ir, err := ParseOpenAIResponse(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ir.ReasoningContent != "Let me compute 6*7..." {
		t.Errorf("reasoning: got %q", ir.ReasoningContent)
	}
}

func TestRoundTrip_AnthropicToOpenAI(t *testing.T) {
	anthropicBody := []byte(`{
		"id": "msg_round",
		"type": "message",
		"role": "assistant",
		"model": "claude-3-5-sonnet",
		"content": [
			{"type": "thinking", "thinking": "Thinking..."},
			{"type": "tool_use", "id": "toolu_1", "name": "bash", "input": {"cmd": "ls"}},
			{"type": "text", "text": "Done."}
		],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 10, "output_tokens": 7}
	}`)
	ir, err := ParseAnthropicResponse(anthropicBody)
	if err != nil {
		t.Fatalf("parse anthropic: %v", err)
	}

	openAIBody, err := SerializeOpenAIResponse(ir, "")
	if err != nil {
		t.Fatalf("serialize openai: %v", err)
	}

	// Parse the OpenAI output back
	ir2, err := ParseOpenAIResponse(openAIBody)
	if err != nil {
		t.Fatalf("parse openai roundtrip: %v\nbody: %s", err, string(openAIBody))
	}
	if ir2.ID != ir.ID {
		t.Errorf("id: got %q, want %q", ir2.ID, ir.ID)
	}
	if len(ir2.ToolCalls) != 1 {
		t.Errorf("toolcalls: got %d", len(ir2.ToolCalls))
	}
	if ir2.FinishReason != "tool_calls" {
		t.Errorf("finish: got %q", ir2.FinishReason)
	}
}

func TestRoundTrip_OpenAIToAnthropic(t *testing.T) {
	openAIBody := []byte(`{
		"id": "chat_round",
		"object": "chat.completion",
		"created": 1234567899,
		"model": "gpt-4o",
		"choices": [{
			"message": {
				"role": "assistant",
				"content": "Hello!",
				"tool_calls": [
					{"id": "call_1", "type": "function", "function": {"name": "greet", "arguments": "{\"name\":\"Alice\"}"}}
				]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {"prompt_tokens": 5, "completion_tokens": 10, "total_tokens": 15}
	}`)
	ir, err := ParseOpenAIResponse(openAIBody)
	if err != nil {
		t.Fatalf("parse openai: %v", err)
	}

	anthropicBody, err := SerializeAnthropicResponse(ir, "")
	if err != nil {
		t.Fatalf("serialize anthropic: %v", err)
	}

	ir2, err := ParseAnthropicResponse(anthropicBody)
	if err != nil {
		t.Fatalf("parse anthropic roundtrip: %v\nbody: %s", err, string(anthropicBody))
	}
	if ir2.ID != ir.ID {
		t.Errorf("id: got %q, want %q", ir2.ID, ir.ID)
	}
	if len(ir2.ToolCalls) != 1 || ir2.ToolCalls[0].Name != "greet" {
		t.Errorf("toolcalls: got %+v", ir2.ToolCalls)
	}
}

func TestSerializeOpenAIResponse_ClientModelOverride(t *testing.T) {
	ir := &InternalResponse{
		ID:             "resp_01",
		Model:          "upstream-model",
		Created:        1234567890,
		Role:           "assistant",
		Content:        []ResponseContentBlock{{Type: "text", Text: "Hi"}},
		FinishReason:   "stop",
		SourceProtocol: ProtocolAnthropicMessages,
	}
	body, err := SerializeOpenAIResponse(ir, "client-model-v3")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	if parsed["model"] != "client-model-v3" {
		t.Errorf("model override: got %v", parsed["model"])
	}
}

func TestSerializeAnthropicResponse_EmptyContent(t *testing.T) {
	ir := &InternalResponse{
		ID:             "resp_02",
		Model:          "claude-3",
		Role:           "assistant",
		Content:        []ResponseContentBlock{},
		ToolCalls:      []ResponseToolCall{},
		FinishReason:   "stop",
		SourceProtocol: ProtocolOpenAIChat,
	}
	body, err := SerializeAnthropicResponse(ir, "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("error: %v", err)
	}
	// Should have at least one empty text block
	contentRaw, ok := parsed["content"].([]any)
	if !ok || len(contentRaw) != 1 {
		t.Errorf("content: got %+v", parsed["content"])
		return
	}
	first, ok := contentRaw[0].(map[string]any)
	if !ok || first["type"] != "text" {
		t.Errorf("content[0]: got %+v", first)
	}
}

// ─── SerializeResponsesResponse Tests ────────────────────────────────────────
//
// Phase E (2026-07-01): non-stream Responses API serializer. Mirrors the
// shape that domains/streaming/responses.go:convertChatResponseToResponses
// previously hand-wrote, but driven entirely from the IR superset.

func TestSerializeResponsesResponse_Nil(t *testing.T) {
	_, err := SerializeResponsesResponse(nil, "gpt-4o")
	if err == nil {
		t.Error("nil IR should return error")
	}
}

func TestSerializeResponsesResponse_TextOnly(t *testing.T) {
	ir := &InternalResponse{
		ID:           "resp_abc123",
		Model:        "gpt-4o",
		Created:      1234567890,
		Role:         "assistant",
		Content:      []ResponseContentBlock{{Type: "text", Text: "Hello world"}},
		FinishReason: "stop",
		Usage:        ResponseUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}

	body, err := SerializeResponsesResponse(ir, "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}

	if parsed["object"] != "response" {
		t.Errorf("object = %v, want response", parsed["object"])
	}
	if parsed["status"] != "completed" {
		t.Errorf("status = %v, want completed", parsed["status"])
	}
	if parsed["model"] != "gpt-4o" {
		t.Errorf("model = %v, want gpt-4o", parsed["model"])
	}
	if parsed["created_at"] != float64(1234567890) {
		t.Errorf("created_at = %v, want 1234567890", parsed["created_at"])
	}
	if !strings.HasPrefix(parsed["id"].(string), "resp_") {
		t.Errorf("id should start with resp_, got %v", parsed["id"])
	}

	usage, ok := parsed["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage missing or wrong type: %v", parsed["usage"])
	}
	if usage["input_tokens"] != float64(10) {
		t.Errorf("input_tokens = %v, want 10", usage["input_tokens"])
	}
	if usage["output_tokens"] != float64(5) {
		t.Errorf("output_tokens = %v, want 5", usage["output_tokens"])
	}
	if usage["total_tokens"] != float64(15) {
		t.Errorf("total_tokens = %v, want 15", usage["total_tokens"])
	}

	output, ok := parsed["output"].([]any)
	if !ok {
		t.Fatalf("output missing or wrong type: %v", parsed["output"])
	}
	if len(output) != 1 {
		t.Fatalf("output length = %d, want 1", len(output))
	}
	item, ok := output[0].(map[string]any)
	if !ok {
		t.Fatalf("output[0] wrong type: %T", output[0])
	}
	if item["type"] != "message" {
		t.Errorf("output[0].type = %v, want message", item["type"])
	}
	if item["role"] != "assistant" {
		t.Errorf("output[0].role = %v, want assistant", item["role"])
	}
	if item["status"] != "completed" {
		t.Errorf("output[0].status = %v, want completed", item["status"])
	}

	content, ok := item["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("output[0].content wrong: %v", item["content"])
	}
	part := content[0].(map[string]any)
	if part["type"] != "output_text" {
		t.Errorf("content[0].type = %v, want output_text", part["type"])
	}
	if part["text"] != "Hello world" {
		t.Errorf("content[0].text = %v, want Hello world", part["text"])
	}
}

func TestSerializeResponsesResponse_WithReasoning(t *testing.T) {
	ir := &InternalResponse{
		ID:               "resp_r1",
		Model:            "claude-opus-4-8",
		Role:             "assistant",
		ReasoningContent: "Let me think carefully...",
		Content:          []ResponseContentBlock{{Type: "text", Text: "42"}},
		FinishReason:     "stop",
	}

	body, err := SerializeResponsesResponse(ir, "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}

	output := parsed["output"].([]any)
	if len(output) != 2 {
		t.Fatalf("output length = %d, want 2 (reasoning + message)", len(output))
	}
	// First item must be reasoning (matches pre-fix ordering in responses.go).
	reasoning, ok := output[0].(map[string]any)
	if !ok || reasoning["type"] != "reasoning" {
		t.Errorf("output[0] should be reasoning, got %+v", output[0])
	}
	summary := reasoning["summary"].([]any)
	if len(summary) != 1 {
		t.Fatalf("summary length = %d, want 1", len(summary))
	}
	sumText := summary[0].(map[string]any)
	if sumText["type"] != "summary_text" {
		t.Errorf("summary[0].type = %v, want summary_text", sumText["type"])
	}
	if sumText["text"] != "Let me think carefully..." {
		t.Errorf("summary[0].text = %v", sumText["text"])
	}

	// Second item must be the message.
	msg, ok := output[1].(map[string]any)
	if !ok || msg["type"] != "message" {
		t.Errorf("output[1] should be message, got %+v", output[1])
	}
}

func TestSerializeResponsesResponse_WithToolCalls(t *testing.T) {
	ir := &InternalResponse{
		ID:    "resp_t1",
		Model: "gpt-4o",
		Role:  "assistant",
		ToolCalls: []ResponseToolCall{
			{ID: "call_abc", Name: "get_weather", Arguments: `{"city":"SF"}`},
		},
		FinishReason: "tool_calls",
		Usage:        ResponseUsage{PromptTokens: 10, CompletionTokens: 8, TotalTokens: 18},
	}

	body, err := SerializeResponsesResponse(ir, "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}

	output := parsed["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("output length = %d, want 1 (single function_call item)", len(output))
	}
	item := output[0].(map[string]any)
	if item["type"] != "function_call" {
		t.Errorf("output[0].type = %v, want function_call", item["type"])
	}
	if item["status"] != "completed" {
		t.Errorf("output[0].status = %v, want completed", item["status"])
	}
	if item["name"] != "get_weather" {
		t.Errorf("output[0].name = %v, want get_weather", item["name"])
	}
	if item["arguments"] != `{"city":"SF"}` {
		t.Errorf("output[0].arguments = %v", item["arguments"])
	}
	if item["call_id"] != "call_abc" {
		t.Errorf("output[0].call_id = %v, want call_abc", item["call_id"])
	}
	if !strings.HasPrefix(item["id"].(string), "msg_") {
		t.Errorf("output[0].id should start with msg_, got %v", item["id"])
	}
	if !strings.Contains(item["id"].(string), "_fc_0") {
		t.Errorf("output[0].id should contain _fc_0 (first tool call), got %v", item["id"])
	}

	// Status should be "completed" since tool_calls finish_reason maps to completed.
	if parsed["status"] != "completed" {
		t.Errorf("status = %v, want completed", parsed["status"])
	}
}

func TestSerializeResponsesResponse_MultipleToolCalls(t *testing.T) {
	ir := &InternalResponse{
		ID:    "resp_t2",
		Model: "gpt-4o",
		Role:  "assistant",
		ToolCalls: []ResponseToolCall{
			{ID: "call_a", Name: "get_weather", Arguments: `{"city":"SF"}`},
			{ID: "call_b", Name: "get_time", Arguments: `{"tz":"UTC"}`},
		},
		FinishReason: "tool_calls",
	}

	body, err := SerializeResponsesResponse(ir, "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)

	output := parsed["output"].([]any)
	if len(output) != 2 {
		t.Fatalf("output length = %d, want 2 (two function_call items)", len(output))
	}

	ids := []string{
		output[0].(map[string]any)["id"].(string),
		output[1].(map[string]any)["id"].(string),
	}
	if ids[0] == ids[1] {
		t.Errorf("two tool calls should have distinct IDs, both got %q", ids[0])
	}
	if !strings.Contains(ids[0], "_fc_0") {
		t.Errorf("first tool id should contain _fc_0, got %q", ids[0])
	}
	if !strings.Contains(ids[1], "_fc_1") {
		t.Errorf("second tool id should contain _fc_1, got %q", ids[1])
	}
}

func TestSerializeResponsesResponse_LengthMapsToIncomplete(t *testing.T) {
	ir := &InternalResponse{
		ID:           "resp_l1",
		Model:        "gpt-4o",
		Role:         "assistant",
		Content:      []ResponseContentBlock{{Type: "text", Text: "truncated..."}},
		FinishReason: "length",
	}

	body, _ := SerializeResponsesResponse(ir, "")
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)

	if parsed["status"] != "incomplete" {
		t.Errorf("status = %v, want incomplete (length → incomplete)", parsed["status"])
	}
	// Message item should also be marked incomplete.
	item := parsed["output"].([]any)[0].(map[string]any)
	if item["status"] != "incomplete" {
		t.Errorf("message.status = %v, want incomplete", item["status"])
	}
}

func TestSerializeResponsesResponse_ContentFilterMapsToIncomplete(t *testing.T) {
	ir := &InternalResponse{
		ID:           "resp_cf1",
		Model:        "gpt-4o",
		Role:         "assistant",
		Content:      []ResponseContentBlock{{Type: "text", Text: ""}},
		FinishReason: "content_filter",
	}

	body, _ := SerializeResponsesResponse(ir, "")
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)

	if parsed["status"] != "incomplete" {
		t.Errorf("status = %v, want incomplete", parsed["status"])
	}
}

func TestSerializeResponsesResponse_ClientModelOverride(t *testing.T) {
	ir := &InternalResponse{
		ID:    "resp_m1",
		Model: "upstream-model",
		Role:  "assistant",
		Content: []ResponseContentBlock{
			{Type: "text", Text: "x"},
		},
		FinishReason: "stop",
	}

	body, _ := SerializeResponsesResponse(ir, "client-model-v3")
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)
	if parsed["model"] != "client-model-v3" {
		t.Errorf("model override: got %v, want client-model-v3", parsed["model"])
	}
}

func TestSerializeResponsesResponse_EmptyIRIDGeneratesDeterministicIDs(t *testing.T) {
	ir := &InternalResponse{
		Model:        "gpt-4o",
		Role:         "assistant",
		Content:      []ResponseContentBlock{{Type: "text", Text: "x"}},
		FinishReason: "stop",
	}

	body, _ := SerializeResponsesResponse(ir, "")
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)

	respID := parsed["id"].(string)
	if !strings.HasPrefix(respID, "resp_") {
		t.Errorf("auto-generated respID should start with resp_, got %q", respID)
	}
	// Length sanity: "resp_" (5) + 24 chars = 29
	if len(respID) != 5+24 {
		t.Errorf("respID length = %d, want 29, got %q", len(respID), respID)
	}

	item := parsed["output"].([]any)[0].(map[string]any)
	msgID := item["id"].(string)
	if !strings.HasPrefix(msgID, "msg_") {
		t.Errorf("auto-generated msgID should start with msg_, got %q", msgID)
	}
	if len(msgID) != 4+24 {
		t.Errorf("msgID length = %d, want 28, got %q", len(msgID), msgID)
	}
}

func TestSerializeResponsesResponse_EmptyContentNoToolCalls(t *testing.T) {
	// Empty IR (no text, no tool calls) — must still produce a valid
	// Responses object with a single message item containing empty text.
	ir := &InternalResponse{
		ID:           "resp_empty",
		Model:        "gpt-4o",
		Role:         "assistant",
		FinishReason: "stop",
	}

	body, err := SerializeResponsesResponse(ir, "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)

	output := parsed["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("output length = %d, want 1", len(output))
	}
	item := output[0].(map[string]any)
	if item["type"] != "message" {
		t.Errorf("type = %v, want message", item["type"])
	}
	content := item["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content length = %d, want 1", len(content))
	}
	part := content[0].(map[string]any)
	if part["text"] != "" {
		t.Errorf("text = %v, want empty string", part["text"])
	}
}

func TestSerializeResponsesResponse_AggregatesMultipleTextBlocks(t *testing.T) {
	// Multiple text blocks (rare, but possible from concatenated streams)
	// should be joined into a single output_text part.
	ir := &InternalResponse{
		ID:    "resp_multi",
		Model: "gpt-4o",
		Role:  "assistant",
		Content: []ResponseContentBlock{
			{Type: "text", Text: "Hello "},
			{Type: "text", Text: "world"},
		},
		FinishReason: "stop",
	}

	body, _ := SerializeResponsesResponse(ir, "")
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)

	item := parsed["output"].([]any)[0].(map[string]any)
	content := item["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content length = %d, want 1 (texts joined)", len(content))
	}
	part := content[0].(map[string]any)
	if part["text"] != "Hello world" {
		t.Errorf("joined text = %v, want 'Hello world'", part["text"])
	}
}

func TestSerializeResponsesResponse_DefaultCreatedAt(t *testing.T) {
	// When IR doesn't provide Created, the serializer stamps time.Now().
	ir := &InternalResponse{
		ID:    "resp_ts",
		Model: "gpt-4o",
		Role:  "assistant",
		Content: []ResponseContentBlock{
			{Type: "text", Text: "x"},
		},
		FinishReason: "stop",
	}

	body, _ := SerializeResponsesResponse(ir, "")
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)

	created, ok := parsed["created_at"].(float64)
	if !ok {
		t.Fatalf("created_at missing or wrong type: %v", parsed["created_at"])
	}
	if created <= 0 {
		t.Errorf("created_at = %v, want positive unix timestamp", created)
	}
	now := float64(time.Now().Unix())
	if created > now+1 || created < now-5 {
		t.Errorf("created_at = %v, want ~%v (within 5s of now)", created, now)
	}
}
