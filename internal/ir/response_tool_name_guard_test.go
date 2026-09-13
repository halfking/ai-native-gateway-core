package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

// 2026-09-13 unified empty-name rejection regression suite.
//
// Invariant: a COMPLETE-call wire format (chat.completions tool_calls
// entry, Anthropic tool_use block, Responses function_call item) must
// never carry an empty function name — SDK-side schema validation
// hard-errors on it. Streaming fragments without a name remain legal
// (continuation semantics); this file pins the terminal serializers.
// The Gemini serializer has carried this guard since inception; these
// tests pin the same invariant for the OpenAI, Anthropic, and Responses
// serializers.

// TestSerializeOpenAIResponse_RejectsNamelessToolCall pins the OpenAI
// chat.completions serializer: a tool call without a name must be
// dropped, and a named sibling must still round-trip.
func TestSerializeOpenAIResponse_RejectsNamelessToolCall(t *testing.T) {
	irResp := &InternalResponse{
		ID:    "resp_guard_1",
		Model: "gpt-4o",
		Role:  "assistant",
		ToolCalls: []ResponseToolCall{
			{ID: "call_bad", Name: "", Arguments: `{"x":1}`},
			{ID: "call_ok", Name: "get_weather", Arguments: `{"city":"SF"}`},
		},
		FinishReason: "tool_calls",
		Usage:        ResponseUsage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
	}

	body, err := SerializeOpenAIResponse(irResp, "")
	if err != nil {
		t.Fatalf("SerializeOpenAIResponse: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}

	choices := parsed["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("choices length = %d, want 1", len(choices))
	}
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	tcs, ok := msg["tool_calls"].([]any)
	if !ok || len(tcs) != 1 {
		t.Fatalf("tool_calls length = %d, want 1 (nameless call dropped)", len(tcs))
	}
	fn := tcs[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Errorf("surviving call name = %v, want get_weather", fn["name"])
	}
	if got := string(body); strings.Contains(got, `"call_bad"`) {
		t.Errorf("dropped call id leaked to wire: %s", got)
	}
}

// TestSerializeAnthropicResponse_RejectsNamelessToolCall pins both
// Anthropic response paths (compact message-content form and the
// full-JSON content form): a tool call without a name must not produce
// a tool_use block.
func TestSerializeAnthropicResponse_RejectsNamelessToolCall(t *testing.T) {
	irResp := &InternalResponse{
		ID:    "resp_guard_2",
		Model: "claude-3-5-sonnet",
		Role:  "assistant",
		Content: []ResponseContentBlock{
			{Type: "text", Text: "let me check"},
		},
		ToolCalls: []ResponseToolCall{
			{ID: "call_bad", Name: "", Arguments: `{"x":1}`},
		},
		FinishReason: "tool_calls",
	}

	body, err := SerializeAnthropicResponse(irResp, "")
	if err != nil {
		t.Fatalf("SerializeAnthropicResponse: %v", err)
	}
	got := string(body)
	if strings.Contains(got, `"type":"tool_use"`) || strings.Contains(got, `"type": "tool_use"`) {
		t.Errorf("nameless tool_use block leaked to wire: %s", got)
	}
	if strings.Contains(got, "call_bad") {
		t.Errorf("dropped call id leaked to wire: %s", got)
	}
	// The narration text must survive the drop.
	if !strings.Contains(got, "let me check") {
		t.Errorf("assistant text lost alongside dropped tool call: %s", got)
	}
}

// TestSerializeResponsesResponse_RejectsNamelessToolCall pins the
// Responses serializer: function_call output items without a name are
// dropped while text and named calls survive.
func TestSerializeResponsesResponse_AllowsNamedToolCallWithoutID(t *testing.T) {
	irResp := &InternalResponse{
		ID:    "resp_guard_idless",
		Model: "gpt-4o",
		Role:  "assistant",
		ToolCalls: []ResponseToolCall{{
			Name:      "get_weather",
			Arguments: `{"city":"SF"}`,
		}},
		FinishReason: "tool_calls",
	}
	body, err := SerializeResponsesResponse(irResp, "")
	if err != nil {
		t.Fatalf("SerializeResponsesResponse: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	output := parsed["output"].([]any)
	if len(output) != 1 || output[0].(map[string]any)["name"] != "get_weather" {
		t.Fatalf("named idless function call was dropped: %s", string(body))
	}
}

func TestSerializeResponsesResponse_RejectsNamelessToolCall(t *testing.T) {
	irResp := &InternalResponse{
		ID:    "resp_guard_3",
		Model: "gpt-4o",
		Role:  "assistant",
		Content: []ResponseContentBlock{
			{Type: "text", Text: "checking"},
		},
		ToolCalls: []ResponseToolCall{
			{ID: "call_bad", Name: "", Arguments: `{"x":1}`},
			{ID: "call_ok", Name: "get_weather", Arguments: `{"city":"SF"}`},
		},
		FinishReason: "tool_calls",
	}

	body, err := SerializeResponsesResponse(irResp, "")
	if err != nil {
		t.Fatalf("SerializeResponsesResponse: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}

	output := parsed["output"].([]any)
	var sawMessage, sawFunctionCall bool
	for _, o := range output {
		item := o.(map[string]any)
		switch item["type"] {
		case "message":
			sawMessage = true
		case "function_call":
			sawFunctionCall = true
			if item["name"] != "get_weather" {
				t.Errorf("function_call name = %v, want get_weather", item["name"])
			}
		}
	}
	if !sawMessage {
		t.Errorf("message item lost: %s", string(body))
	}
	if !sawFunctionCall {
		t.Errorf("named function_call lost: %s", string(body))
	}
	if got := string(body); strings.Contains(got, "call_bad") {
		t.Errorf("dropped call id leaked to wire: %s", got)
	}
}
