package ir

import (
	"encoding/json"
	"testing"
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
