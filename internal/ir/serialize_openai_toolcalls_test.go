package ir

import (
	"encoding/json"
	"testing"
)

// TestSerializeOpenAI_EmptyContentWithToolCalls tests Bug #1 fix:
// When msg.Content is empty but msg.ToolCalls is not empty,
// the serializer must output both "content": "" and "tool_calls": [...].
func TestSerializeOpenAI_EmptyContentWithToolCalls(t *testing.T) {
	req := &InternalRequest{
		Model: "gpt-4",
		Messages: []Message{
			{
				Role:    "user",
				Content: []ContentBlock{{Type: "text", Text: "Use the weather tool"}},
			},
			{
				Role:    "assistant",
				Content: []ContentBlock{}, // Empty content
				ToolCalls: []ToolCall{
					{
						ID:   "call_abc123",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{
							Name:      "get_weather",
							Arguments: `{"city":"Beijing"}`,
						},
					},
				},
			},
			{
				Role:       "tool",
				ToolCallID: "call_abc123",
				Content:    []ContentBlock{{Type: "text", Text: "Sunny, 25°C"}},
			},
		},
	}

	body, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI failed: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	messages, ok := result["messages"].([]any)
	if !ok || len(messages) < 2 {
		t.Fatalf("messages not found or too short")
	}

	// Check the assistant message (index 1)
	assistantMsg, ok := messages[1].(map[string]any)
	if !ok {
		t.Fatalf("assistant message is not a map")
	}

	// Must have "content" field (even if empty)
	if _, ok := assistantMsg["content"]; !ok {
		t.Errorf("assistant message missing 'content' field")
	}

	// Must have "tool_calls" field
	toolCalls, ok := assistantMsg["tool_calls"].([]any)
	if !ok {
		t.Fatalf("assistant message missing 'tool_calls' field or wrong type")
	}

	if len(toolCalls) != 1 {
		t.Errorf("expected 1 tool_call, got %d", len(toolCalls))
	}

	// Verify tool_call structure
	tc, ok := toolCalls[0].(map[string]any)
	if !ok {
		t.Fatalf("tool_call is not a map")
	}

	if id, _ := tc["id"].(string); id != "call_abc123" {
		t.Errorf("tool_call.id = %q, want call_abc123", id)
	}
}

// TestSerializeOpenAI_MultipleToolCallsWithEmptyContent tests Bug #1 fix
// with multiple tool calls and empty content.
func TestSerializeOpenAI_MultipleToolCallsWithEmptyContent(t *testing.T) {
	req := &InternalRequest{
		Model: "gpt-4",
		Messages: []Message{
			{
				Role:    "user",
				Content: []ContentBlock{{Type: "text", Text: "Get weather for multiple cities"}},
			},
			{
				Role:    "assistant",
				Content: []ContentBlock{}, // Empty content
				ToolCalls: []ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{
							Name:      "get_weather",
							Arguments: `{"city":"Beijing"}`,
						},
					},
					{
						ID:   "call_2",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{
							Name:      "get_weather",
							Arguments: `{"city":"Shanghai"}`,
						},
					},
				},
			},
			{
				Role:       "tool",
				ToolCallID: "call_1",
				Content:    []ContentBlock{{Type: "text", Text: "Sunny"}},
			},
			{
				Role:       "tool",
				ToolCallID: "call_2",
				Content:    []ContentBlock{{Type: "text", Text: "Rainy"}},
			},
		},
	}

	body, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI failed: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	messages, ok := result["messages"].([]any)
	if !ok || len(messages) < 2 {
		t.Fatalf("messages not found or too short")
	}

	// Check the assistant message (index 1)
	assistantMsg, ok := messages[1].(map[string]any)
	if !ok {
		t.Fatalf("assistant message is not a map")
	}

	// Must have "tool_calls" field with 2 calls
	toolCalls, ok := assistantMsg["tool_calls"].([]any)
	if !ok {
		t.Fatalf("assistant message missing 'tool_calls' field")
	}

	if len(toolCalls) != 2 {
		t.Errorf("expected 2 tool_calls, got %d", len(toolCalls))
	}

	// Verify tool_call IDs
	tc1, _ := toolCalls[0].(map[string]any)
	if id, _ := tc1["id"].(string); id != "call_1" {
		t.Errorf("tool_call[0].id = %q, want call_1", id)
	}

	tc2, _ := toolCalls[1].(map[string]any)
	if id, _ := tc2["id"].(string); id != "call_2" {
		t.Errorf("tool_call[1].id = %q, want call_2", id)
	}
}
