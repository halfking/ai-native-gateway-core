package ir

import (
	"testing"
)

func TestValidateAndFixRequest_EmptyMessages(t *testing.T) {
	req := &InternalRequest{
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}},
			{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: ""}}}, // Empty
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "continue"}}},
		},
	}

	fixed := ValidateAndFixRequest(req)

	if len(fixed.Messages) != 2 {
		t.Errorf("expected 2 messages after removing empty, got %d", len(fixed.Messages))
	}
}

func TestValidateAndFixRequest_MissingToolCallID(t *testing.T) {
	req := &InternalRequest{
		Messages: []Message{
			{Role: "assistant", ToolCalls: []ToolCall{
				{ID: "", Type: "function", Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "search", Arguments: "{}"}},
			}},
		},
	}

	fixed := ValidateAndFixRequest(req)

	if fixed.Messages[0].ToolCalls[0].ID == "" {
		t.Errorf("expected tool_call_id to be generated, but it's still empty")
	}
}

func TestValidateAndFixRequest_EmptyFunctionName(t *testing.T) {
	req := &InternalRequest{
		Messages: []Message{
			{Role: "assistant", ToolCalls: []ToolCall{
				{ID: "call_123", Type: "function", Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "", Arguments: "{}"}},
			}},
		},
	}

	fixed := ValidateAndFixRequest(req)

	if fixed.Messages[0].ToolCalls[0].Function.Name == "" {
		t.Errorf("expected function name to be fixed, but it's still empty")
	}
	if fixed.Messages[0].ToolCalls[0].Function.Name != "__unknown_tool__" {
		t.Errorf("expected function name to be '__unknown_tool__', got %s", fixed.Messages[0].ToolCalls[0].Function.Name)
	}
}

func TestValidateAndFixRequest_SystemMessagePlacement(t *testing.T) {
	req := &InternalRequest{
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}},
			{Role: "system", Content: []ContentBlock{{Type: "text", Text: "You are helpful"}}},
			{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "Hi"}}},
		},
	}

	fixed := ValidateAndFixRequest(req)

	if fixed.Messages[0].Role != "system" {
		t.Errorf("expected system message to be moved to front, got %s", fixed.Messages[0].Role)
	}
}

func TestValidateAndFixRequest_Comprehensive(t *testing.T) {
	// Test all fixes in one request
	req := &InternalRequest{
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}},
			{Role: "assistant", ToolCalls: []ToolCall{
				{ID: "call_valid", Type: "function", Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "search", Arguments: "{}"}},
			}},
			{Role: "tool", ToolCallID: "call_valid", Content: []ContentBlock{{Type: "text", Text: "result"}}},
			{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: ""}}}, // Empty - remove
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "continue"}}},
			{Role: "tool", ToolCallID: "call_valid", Content: []ContentBlock{{Type: "text", Text: "orphaned"}}}, // Orphaned - remove
			{Role: "system", Content: []ContentBlock{{Type: "text", Text: "System prompt"}}},                    // Move to front
			{Role: "assistant", ToolCalls: []ToolCall{
				{ID: "", Type: "function", Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "", Arguments: "{}"}}, // Fix ID and name
			}},
		},
	}

	fixed := ValidateAndFixRequest(req)

	// System message should be first
	if fixed.Messages[0].Role != "system" {
		t.Errorf("expected system message first, got %s", fixed.Messages[0].Role)
	}

	// Should have removed empty assistant and orphaned tool
	expectedCount := 6 // system, user, assistant(tc), tool, user, assistant(tc-fixed)
	if len(fixed.Messages) != expectedCount {
		t.Errorf("expected %d messages after fixes, got %d", expectedCount, len(fixed.Messages))
	}

	// Last assistant should have fixed tool_call
	lastMsg := fixed.Messages[len(fixed.Messages)-1]
	if lastMsg.Role != "assistant" || len(lastMsg.ToolCalls) == 0 {
		t.Errorf("expected last message to be assistant with tool_calls")
	}
	if lastMsg.ToolCalls[0].ID == "" {
		t.Errorf("expected tool_call_id to be generated")
	}
	if lastMsg.ToolCalls[0].Function.Name != "__unknown_tool__" {
		t.Errorf("expected function name to be fixed")
	}
}

func TestValidateAndFixRequest_NilRequest(t *testing.T) {
	fixed := ValidateAndFixRequest(nil)
	if fixed != nil {
		t.Errorf("expected nil request to return nil")
	}
}

func TestValidateAndFixRequest_EmptyRequest(t *testing.T) {
	req := &InternalRequest{
		Messages: []Message{},
	}

	fixed := ValidateAndFixRequest(req)

	if len(fixed.Messages) != 0 {
		t.Errorf("expected empty messages to stay empty")
	}
}
