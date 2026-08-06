package ir

import (
	"testing"
)

func TestSanitizeToolMessages_OrphanedToolMessage(t *testing.T) {
	// Scenario: tool message referencing a closed tool_call_id
	// (user message closed the scope)
	messages := []Message{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}},
		{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "Let me check."}}, ToolCalls: []ToolCall{
			{ID: "call_123", Type: "function", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "search", Arguments: "{}"}},
		}},
		{Role: "tool", ToolCallID: "call_123", Content: []ContentBlock{{Type: "text", Text: "result"}}},
		{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "Here's the result"}}},
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "continue"}}},
		// Orphaned: references call_123 but scope was closed by user message
		{Role: "tool", ToolCallID: "call_123", Content: []ContentBlock{{Type: "text", Text: "stale"}}},
	}

	result := SanitizeToolMessages(messages, "")

	if len(result) != 5 {
		t.Errorf("expected 5 messages after sanitize, got %d", len(result))
	}
	// Last message should be the user message, not the orphaned tool
	if result[len(result)-1].Role != "user" {
		t.Errorf("expected last message to be user, got %s", result[len(result)-1].Role)
	}
}

func TestSanitizeToolMessages_ValidSequence(t *testing.T) {
	// Scenario: valid tool calling sequence
	messages := []Message{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}},
		{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "calling tool"}}, ToolCalls: []ToolCall{
			{ID: "call_abc", Type: "function", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "search", Arguments: "{}"}},
		}},
		{Role: "tool", ToolCallID: "call_abc", Content: []ContentBlock{{Type: "text", Text: "result"}}},
		{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "Here's the answer"}}},
	}

	result := SanitizeToolMessages(messages, "")

	if len(result) != len(messages) {
		t.Errorf("expected %d messages, got %d (valid sequence should not be modified)", len(messages), len(result))
	}
}

func TestSanitizeToolMessages_MismatchedToolCallID(t *testing.T) {
	// Scenario: tool message with tool_call_id that doesn't match any assistant.tool_calls[].id
	messages := []Message{
		{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "calling"}}, ToolCalls: []ToolCall{
			{ID: "call_valid", Type: "function", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "search", Arguments: "{}"}},
		}},
		{Role: "tool", ToolCallID: "call_invalid", Content: []ContentBlock{{Type: "text", Text: "wrong id"}}},
		{Role: "tool", ToolCallID: "call_valid", Content: []ContentBlock{{Type: "text", Text: "correct id"}}},
	}

	result := SanitizeToolMessages(messages, "")

	if len(result) != 2 {
		t.Errorf("expected 2 messages (assistant + 1 valid tool), got %d", len(result))
	}
	// Second message should be the valid tool message
	if result[1].Role != "tool" || result[1].ToolCallID != "call_valid" {
		t.Errorf("expected second message to be valid tool with call_valid, got %s with %s",
			result[1].Role, result[1].ToolCallID)
	}
}

func TestSanitizeToolMessages_EmptyToolCallID(t *testing.T) {
	// Scenario: tool message without tool_call_id
	messages := []Message{
		{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "calling"}}, ToolCalls: []ToolCall{
			{ID: "call_123", Type: "function", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "search", Arguments: "{}"}},
		}},
		{Role: "tool", ToolCallID: "", Content: []ContentBlock{{Type: "text", Text: "no id"}}},
	}

	result := SanitizeToolMessages(messages, "")

	if len(result) != 1 {
		t.Errorf("expected 1 message (only assistant), got %d", len(result))
	}
}

func TestSanitizeToolMessages_MultipleToolCalls(t *testing.T) {
	// Scenario: assistant calls multiple tools, all responses valid
	messages := []Message{
		{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "calling 3 tools"}}, ToolCalls: []ToolCall{
			{ID: "call_1", Type: "function", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "search", Arguments: "{}"}},
			{ID: "call_2", Type: "function", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "calculate", Arguments: "{}"}},
			{ID: "call_3", Type: "function", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "lookup", Arguments: "{}"}},
		}},
		{Role: "tool", ToolCallID: "call_1", Content: []ContentBlock{{Type: "text", Text: "result 1"}}},
		{Role: "tool", ToolCallID: "call_2", Content: []ContentBlock{{Type: "text", Text: "result 2"}}},
		{Role: "tool", ToolCallID: "call_3", Content: []ContentBlock{{Type: "text", Text: "result 3"}}},
	}

	result := SanitizeToolMessages(messages, "")

	if len(result) != 4 {
		t.Errorf("expected 4 messages, got %d", len(result))
	}
}

func TestSanitizeToolMessages_RealWorldMiniMaxBug(t *testing.T) {
	// Scenario: reproduces the MiniMax "tool id not found (2013)" bug
	// - assistant with tool_calls
	// - valid tool response
	// - assistant without tool_calls (closes scope)
	// - user message
	// - orphaned tool message (references closed call_id)
	messages := []Message{
		{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "call_function_0ksgl64qo8yj_1", Type: "function"},
		}},
		{Role: "tool", ToolCallID: "call_function_0ksgl64qo8yj_1", Content: []ContentBlock{{Type: "text", Text: "result"}}},
		{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "Here's the result"}}}, // closes scope
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "continue"}}},
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "next step"}}},
		// This orphaned tool message causes MiniMax 2013 error
		{Role: "tool", ToolCallID: "call_function_0ksgl64qo8yj_1", Content: []ContentBlock{{Type: "text", Text: "stale"}}},
	}

	result := SanitizeToolMessages(messages, "")

	// Should remove the orphaned tool message
	if len(result) != 5 {
		t.Errorf("expected 5 messages (removing orphaned tool), got %d", len(result))
	}
	// Last message should be user, not tool
	if result[len(result)-1].Role != "user" {
		t.Errorf("expected last message to be user, got %s", result[len(result)-1].Role)
	}
}

// TestValidateAndFix_EmptyUserBetweenToolResults 是 2026-08-06 审计回归测试。
//
// 旧 validateAndFixMessages 先跑 SanitizeToolMessages 再跑 removeEmptyMessages。
// 当空 user 消息夹在 tool results 之间时（某些 SDK 如 Vercel AI SDK 会产生
// 这种序列），sanitize 先看到 user 消息并关闭 tool-call scope，导致后续合法
// tool result 被误判为孤儿删除。修复：先 removeEmptyMessages 清除噪音，
// 再 SanitizeToolMessages 分析清理后的序列。
func TestValidateAndFix_EmptyUserBetweenToolResults(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "run the tool"}}},
		{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "calling"}}, ToolCalls: []ToolCall{
			{ID: "call_1", Type: "function", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "do_work", Arguments: "{}"}},
		}},
		{Role: "tool", ToolCallID: "call_1", Content: []ContentBlock{{Type: "text", Text: "result_a"}}},
		// 空 user 消息 — SDK 噪音
		{Role: "user", Content: []ContentBlock{}},
		{Role: "tool", ToolCallID: "call_1", Content: []ContentBlock{{Type: "text", Text: "result_b"}}},
	}

	result := ValidateAndFixRequest(&InternalRequest{Messages: messages}, "test_req")

	// 两条 tool result 都应存活（call_1 合法）
	toolCount := 0
	for _, m := range result.Messages {
		if m.Role == "tool" {
			toolCount++
		}
	}
	if toolCount != 2 {
		t.Errorf("expected 2 tool results preserved, got %d", toolCount)
		for i, m := range result.Messages {
			t.Logf("  [%d] role=%s tool_call_id=%s", i, m.Role, m.ToolCallID)
		}
	}

	// 空 user 消息应被删除
	for i, m := range result.Messages {
		if m.Role == "user" && len(m.Content) == 0 {
			t.Errorf("empty user message at index %d should have been removed", i)
		}
	}
}
