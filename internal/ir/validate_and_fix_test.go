package ir

import (
	"os"
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

// TestEnforceRoleAlternation_WarnOnly tests E3 default behavior (warn, no merge).
func TestEnforceRoleAlternation_WarnOnly(t *testing.T) {
	// Ensure fix is disabled (default)
	originalEnv := os.Getenv("LLM_GATEWAY_FIX_ROLE_ALTERNATION")
	os.Setenv("LLM_GATEWAY_FIX_ROLE_ALTERNATION", "false")
	defer os.Setenv("LLM_GATEWAY_FIX_ROLE_ALTERNATION", originalEnv)

	req := &InternalRequest{
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "A"}}},
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "B"}}},
			{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "C"}}},
		},
	}

	fixed := ValidateAndFixRequest(req, "test_warn")

	// Should NOT merge — 3 messages remain
	if len(fixed.Messages) != 3 {
		t.Errorf("expected 3 messages (warn-only mode), got %d", len(fixed.Messages))
	}

	// First two should still be separate user messages
	if fixed.Messages[0].Role != "user" || fixed.Messages[1].Role != "user" {
		t.Errorf("expected two consecutive user messages to remain unmerged")
	}
}

// TestEnforceRoleAlternation_MergeEnabled tests E3 merge behavior (fix enabled).
func TestEnforceRoleAlternation_MergeEnabled(t *testing.T) {
	// Enable fix
	originalEnv := os.Getenv("LLM_GATEWAY_FIX_ROLE_ALTERNATION")
	os.Setenv("LLM_GATEWAY_FIX_ROLE_ALTERNATION", "true")
	defer os.Setenv("LLM_GATEWAY_FIX_ROLE_ALTERNATION", originalEnv)

	req := &InternalRequest{
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "A"}}},
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "B"}}},
			{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "C"}}},
		},
	}

	fixed := ValidateAndFixRequest(req, "test_merge")

	// Should merge the two consecutive user messages → 2 messages total
	if len(fixed.Messages) != 2 {
		t.Errorf("expected 2 messages after merge, got %d", len(fixed.Messages))
	}

	// First message should be user with 2 content blocks
	if fixed.Messages[0].Role != "user" {
		t.Errorf("expected first message to be user, got %s", fixed.Messages[0].Role)
	}
	if len(fixed.Messages[0].Content) != 2 {
		t.Errorf("expected merged user message to have 2 content blocks, got %d", len(fixed.Messages[0].Content))
	}

	// Content should be concatenated
	if fixed.Messages[0].Content[0].Text != "A" || fixed.Messages[0].Content[1].Text != "B" {
		t.Errorf("expected content blocks [A, B], got [%s, %s]",
			fixed.Messages[0].Content[0].Text, fixed.Messages[0].Content[1].Text)
	}

	// Second message should be assistant
	if fixed.Messages[1].Role != "assistant" || fixed.Messages[1].Content[0].Text != "C" {
		t.Errorf("expected second message to be assistant:C")
	}
}

// TestEnforceRoleAlternation_SystemToolPreserved tests that system and tool
// messages are never merged (E3).
func TestEnforceRoleAlternation_SystemToolPreserved(t *testing.T) {
	originalEnv := os.Getenv("LLM_GATEWAY_FIX_ROLE_ALTERNATION")
	os.Setenv("LLM_GATEWAY_FIX_ROLE_ALTERNATION", "true")
	defer os.Setenv("LLM_GATEWAY_FIX_ROLE_ALTERNATION", originalEnv)

	req := &InternalRequest{
		Messages: []Message{
			{Role: "system", Content: []ContentBlock{{Type: "text", Text: "S1"}}},
			{Role: "system", Content: []ContentBlock{{Type: "text", Text: "S2"}}},
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "U"}}},
			{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "A"}}, ToolCalls: []ToolCall{
				{ID: "call_1", Type: "function", Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "search", Arguments: "{}"}},
				{ID: "call_2", Type: "function", Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "fetch", Arguments: "{}"}},
			}},
			{Role: "tool", Content: []ContentBlock{{Type: "text", Text: "T1"}}, ToolCallID: "call_1"},
			{Role: "tool", Content: []ContentBlock{{Type: "text", Text: "T2"}}, ToolCallID: "call_2"},
		},
	}

	fixed := ValidateAndFixRequest(req, "test_preserve")

	// System messages should NOT be merged (even consecutive)
	systemCount := 0
	for _, msg := range fixed.Messages {
		if msg.Role == "system" {
			systemCount++
		}
	}
	if systemCount != 2 {
		t.Errorf("expected 2 separate system messages, got %d", systemCount)
	}

	// Tool messages should NOT be merged
	toolCount := 0
	for _, msg := range fixed.Messages {
		if msg.Role == "tool" {
			toolCount++
		}
	}
	if toolCount != 2 {
		t.Errorf("expected 2 separate tool messages, got %d", toolCount)
	}
}
