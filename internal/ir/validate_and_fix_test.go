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

	fixed := ValidateAndFixRequest(req, "")

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

	fixed := ValidateAndFixRequest(req, "")

	if fixed.Messages[0].ToolCalls[0].ID == "" {
		t.Errorf("expected tool_call_id to be generated, but it's still empty")
	}
}

// TestValidateAndFixRequest_ToolCallIDUniqueness guards against the old
// generateToolCallID(index) which reduced index%10 and therefore collided
// once a request carried more than 10 missing-id tool_calls, or two assistant
// messages whose first tool_call both lacked an id. The ids must be unique
// across the whole request.
func TestValidateAndFixRequest_ToolCallIDUniqueness(t *testing.T) {
	// 12 tool_calls on one assistant message — all without ids.
	const n = 12
	calls := make([]ToolCall, n)
	for i := range calls {
		calls[i] = ToolCall{
			ID: "", Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "search", Arguments: "{}"},
		}
	}
	// A second assistant message whose first call also lacks an id; with the old
	// impl both first-calls would map to "call_generated_0".
	msgs := []Message{
		{Role: "assistant", ToolCalls: calls},
		{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "", Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "search", Arguments: "{}"},
		}}},
	}

	fixed := ValidateAndFixRequest(&InternalRequest{Messages: msgs}, "")

	seen := make(map[string]int)
	for _, m := range fixed.Messages {
		for _, tc := range m.ToolCalls {
			if tc.ID == "" {
				t.Fatal("found a tool_call whose id is still empty after fix")
			}
			if prev, ok := seen[tc.ID]; ok {
				t.Fatalf("duplicate tool_call_id %q (first at msg %d)", tc.ID, prev)
			}
			seen[tc.ID] = -1
		}
	}

	if want := n + 1; len(seen) != want {
		t.Errorf("expected %d unique generated ids, got %d", want, len(seen))
	}
}

// TestGenerateToolCallID_Deterministic verifies the id is stable for a given
// (message, call) pair, which log correlation and replay rely on.
func TestGenerateToolCallID_Deterministic(t *testing.T) {
	if got, want := generateToolCallID(0, 0), "call_generated_0_0"; got != want {
		t.Errorf("generateToolCallID(0,0) = %q, want %q", got, want)
	}
	if got, want := generateToolCallID(3, 11), "call_generated_3_11"; got != want {
		t.Errorf("generateToolCallID(3,11) = %q, want %q", got, want)
	}
	// Beyond the old 10-bucket limit, ids must still differ.
	if generateToolCallID(0, 9) == generateToolCallID(0, 10) {
		t.Error("ids collide at the old index%10 boundary (9 vs 10)")
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

	fixed := ValidateAndFixRequest(req, "")

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

	fixed := ValidateAndFixRequest(req, "")

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

	fixed := ValidateAndFixRequest(req, "")

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
	fixed := ValidateAndFixRequest(nil, "")
	if fixed != nil {
		t.Errorf("expected nil request to return nil")
	}
}

func TestValidateAndFixRequest_EmptyRequest(t *testing.T) {
	req := &InternalRequest{
		Messages: []Message{},
	}

	fixed := ValidateAndFixRequest(req, "")

	if len(fixed.Messages) != 0 {
		t.Errorf("expected empty messages to stay empty")
	}
}
