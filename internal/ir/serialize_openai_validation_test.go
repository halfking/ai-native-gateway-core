package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSerializeOpenAI_OrphanedToolResults tests Bug #2 fix:
// When tool messages exist but their tool_call_ids don't match any
// assistant tool_calls, validation should fail with a clear error.
func TestSerializeOpenAI_OrphanedToolResults(t *testing.T) {
	req := &InternalRequest{
		Model: "gpt-4",
		Messages: []Message{
			{
				Role:    "user",
				Content: []ContentBlock{{Type: "text", Text: "Hello"}},
			},
			// Tool message with no matching assistant tool_call
			{
				Role:       "tool",
				ToolCallID: "call_orphaned_123",
				Content:    []ContentBlock{{Type: "text", Text: "Result"}},
			},
			{
				Role:    "user",
				Content: []ContentBlock{{Type: "text", Text: "Continue"}},
			},
		},
	}

	_, err := SerializeOpenAI(req)
	if err == nil {
		t.Fatalf("expected validation error for orphaned tool result, got nil")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "orphaned tool result") {
		t.Errorf("error message should mention 'orphaned tool result', got: %v", errMsg)
	}
	if !strings.Contains(errMsg, "call_orphaned_123") {
		t.Errorf("error message should include the orphaned ID, got: %v", errMsg)
	}
	if !strings.Contains(errMsg, "likely client bug") {
		t.Errorf("error message should mention 'likely client bug', got: %v", errMsg)
	}
}

// TestSerializeOpenAI_ValidToolCalls tests that valid tool call chains pass validation.
func TestSerializeOpenAI_ValidToolCalls(t *testing.T) {
	req := &InternalRequest{
		Model: "gpt-4",
		Messages: []Message{
			{
				Role:    "user",
				Content: []ContentBlock{{Type: "text", Text: "What's the weather?"}},
			},
			{
				Role:    "assistant",
				Content: []ContentBlock{{Type: "text", Text: "Let me check"}},
				ToolCalls: []ToolCall{
					{
						ID:   "call_valid_123",
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
				ToolCallID: "call_valid_123",
				Content:    []ContentBlock{{Type: "text", Text: "Sunny, 25°C"}},
			},
			{
				Role:    "assistant",
				Content: []ContentBlock{{Type: "text", Text: "It's sunny and 25°C"}},
			},
		},
	}

	body, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI failed on valid tool calls: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	// Verify messages were serialized correctly
	messages, ok := result["messages"].([]any)
	if !ok || len(messages) != 4 {
		t.Errorf("expected 4 messages, got %d", len(messages))
	}
}

// TestSerializeOpenAI_PartialOrphans tests mixed valid and orphaned tool results.
func TestSerializeOpenAI_PartialOrphans(t *testing.T) {
	req := &InternalRequest{
		Model: "gpt-4",
		Messages: []Message{
			{
				Role:    "user",
				Content: []ContentBlock{{Type: "text", Text: "Test"}},
			},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{
					{
						ID:   "call_valid",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{
							Name:      "tool1",
							Arguments: `{}`,
						},
					},
				},
			},
			{
				Role:       "tool",
				ToolCallID: "call_valid",
				Content:    []ContentBlock{{Type: "text", Text: "OK"}},
			},
			// Orphaned tool result
			{
				Role:       "tool",
				ToolCallID: "call_orphaned",
				Content:    []ContentBlock{{Type: "text", Text: "Lost"}},
			},
		},
	}

	_, err := SerializeOpenAI(req)
	if err == nil {
		t.Fatalf("expected validation error for partial orphans, got nil")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "call_orphaned") {
		t.Errorf("error should mention orphaned ID, got: %v", errMsg)
	}
}

// TestSerializeOpenAI_ShortMessageList tests that validation is skipped for short message lists.
// This ensures backward compatibility with unit tests that have minimal messages.
func TestSerializeOpenAI_ShortMessageList(t *testing.T) {
	// Single user message - should not trigger validation
	req := &InternalRequest{
		Model: "gpt-4",
		Messages: []Message{
			{
				Role:    "user",
				Content: []ContentBlock{{Type: "text", Text: "Hello"}},
			},
		},
	}

	_, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI failed on short message list: %v", err)
	}

	// Two messages - should not trigger validation
	req.Messages = append(req.Messages, Message{
		Role:    "assistant",
		Content: []ContentBlock{{Type: "text", Text: "Hi"}},
	})

	_, err = SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI failed on 2-message list: %v", err)
	}
}

// TestSerializeOpenAI_MultipleOrphanedToolResults tests that the error message
// limits displayed orphans to first 3 to avoid huge error messages.
func TestSerializeOpenAI_MultipleOrphanedToolResults(t *testing.T) {
	req := &InternalRequest{
		Model: "gpt-4",
		Messages: []Message{
			{
				Role:    "user",
				Content: []ContentBlock{{Type: "text", Text: "Test"}},
			},
		},
	}

	// Add 5 orphaned tool results
	for i := 1; i <= 5; i++ {
		req.Messages = append(req.Messages, Message{
			Role:       "tool",
			ToolCallID: "call_orphan_" + string(rune('0'+i)),
			Content:    []ContentBlock{{Type: "text", Text: "Result"}},
		})
	}

	_, err := SerializeOpenAI(req)
	if err == nil {
		t.Fatalf("expected validation error for multiple orphans, got nil")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "found 5 orphaned") {
		t.Errorf("error should mention total count of 5, got: %v", errMsg)
	}
	// The error should limit displayed IDs to first 3
	if !strings.Contains(errMsg, "call_orphan_1") {
		t.Errorf("error should show first orphan, got: %v", errMsg)
	}
}
