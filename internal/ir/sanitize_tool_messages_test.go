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

	if len(result) != 2 || result[1].ToolCallID != "call_123" || result[1].Content[0].ToolResult == nil || !result[1].Content[0].ToolResult.IsError {
		t.Errorf("expected the unanswered actual call to receive an interrupted error, got %#v", result)
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

// TestSanitizeToolMessages_MergesConsecutiveAssistantToolCallSplits is the
// 2026-10-13 production incident regression (request
// d717f89a8c79123c7ca7cd69ff2cbf9d, MiniMax 400 "tool call result does not
// follow tool call (2013)").
//
// Agent clients (ZCode) emit parallel tool calls as consecutive assistant
// messages each carrying ONE tool_call, followed by the tool results in call
// order. The old scope rule closed the previous scope on every assistant
// message, so only the LAST call's result survived sanitization; MiniMax then
// rejected the request because the earlier calls had no results — the exact
// error this sanitizer was built to prevent.
func TestSanitizeToolMessages_MergesConsecutiveAssistantToolCallSplits(t *testing.T) {
	toolCall := func(id, name string) ToolCall {
		var tc ToolCall
		tc.ID = id
		tc.Type = "function"
		tc.Function.Name = name
		tc.Function.Arguments = "{}"
		return tc
	}
	messages := []Message{
		{Role: "system", Content: []ContentBlock{{Type: "text", Text: "You are ZCode"}}},
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "check the repo"}}},
		{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "let me run three commands"}}},
		{Role: "assistant", ToolCalls: []ToolCall{toolCall("call_f7", "Bash")}},
		{Role: "assistant", ToolCalls: []ToolCall{toolCall("call_f8", "Bash")}},
		{Role: "assistant", ToolCalls: []ToolCall{toolCall("call_f9", "Bash")}},
		{Role: "tool", ToolCallID: "call_f7", Content: []ContentBlock{{Type: "text", Text: "git status output"}}},
		{Role: "tool", ToolCallID: "call_f8", Content: []ContentBlock{{Type: "text", Text: "git branch output"}}},
		{Role: "tool", ToolCallID: "call_f9", Content: []ContentBlock{{Type: "text", Text: "git log output"}}},
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "continue"}}},
	}

	result := SanitizeToolMessages(messages, "")

	// Shape: system, user, assistant(text), assistant(3 merged tool_calls),
	// tool f7, tool f8, tool f9, user → 8 messages, nothing dropped.
	if len(result) != 8 {
		t.Fatalf("expected 8 messages after merge, got %d:", len(result))
	}
	for i, m := range result {
		t.Logf("  [%d] role=%s tool_calls=%d tool_call_id=%s", i, m.Role, len(m.ToolCalls), m.ToolCallID)
	}

	// The three split assistant messages must be ONE canonical message whose
	// tool_calls array carries all three ids (premise-still-present guard:
	// passing the result-count check alone doesn't prove the calls survived).
	var mergedIdx = -1
	for i, m := range result {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			if mergedIdx != -1 {
				t.Fatalf("found a second assistant message with tool_calls at index %d; split messages were not merged", i)
			}
			mergedIdx = i
		}
	}
	if mergedIdx == -1 {
		t.Fatal("no assistant message with tool_calls survived sanitization")
	}
	gotIDs := make([]string, 0, 3)
	for _, tc := range result[mergedIdx].ToolCalls {
		gotIDs = append(gotIDs, tc.ID)
	}
	wantIDs := []string{"call_f7", "call_f8", "call_f9"}
	if len(gotIDs) != len(wantIDs) {
		t.Fatalf("expected merged assistant to carry %d tool_calls, got %v", len(wantIDs), gotIDs)
	}
	for i := range wantIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Errorf("merged tool_calls[%d] = %s, want %s", i, gotIDs[i], wantIDs[i])
		}
	}

	// All three tool results must survive (the incident dropped f7/f8).
	kept := map[string]bool{}
	for _, m := range result {
		if m.Role == "tool" {
			kept[m.ToolCallID] = true
		}
	}
	for _, id := range wantIDs {
		if !kept[id] {
			t.Errorf("tool result %s was removed; MiniMax would reject its call as result-less (2013)", id)
		}
	}

	// The merged assistant message must sit immediately before its results.
	if mergedIdx+3 >= len(result) || result[mergedIdx+1].ToolCallID != "call_f7" ||
		result[mergedIdx+2].ToolCallID != "call_f8" || result[mergedIdx+3].ToolCallID != "call_f9" {
		t.Error("merged assistant message is not immediately followed by its three tool results")
	}
}

// TestValidateAndFix_ParallelCallSplitSurvives runs the incident shape through
// the full ValidateAndFixRequest pipeline (the actual entry point the
// executor calls), so a future reordering of pipeline steps can't reintroduce
// the drop by another route.
func TestValidateAndFix_ParallelCallSplitSurvives(t *testing.T) {
	toolCall := func(id string) ToolCall {
		var tc ToolCall
		tc.ID = id
		tc.Type = "function"
		tc.Function.Name = "Bash"
		tc.Function.Arguments = "{\"command\":\"pwd\"}"
		return tc
	}
	messages := []Message{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "go"}}},
		{Role: "assistant", ToolCalls: []ToolCall{toolCall("call_f7")}},
		{Role: "assistant", ToolCalls: []ToolCall{toolCall("call_f8")}},
		{Role: "assistant", ToolCalls: []ToolCall{toolCall("call_f9")}},
		{Role: "tool", ToolCallID: "call_f7", Content: []ContentBlock{{Type: "text", Text: "r7"}}},
		{Role: "tool", ToolCallID: "call_f8", Content: []ContentBlock{{Type: "text", Text: "r8"}}},
		{Role: "tool", ToolCallID: "call_f9", Content: []ContentBlock{{Type: "text", Text: "r9"}}},
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "next"}}},
	}

	result := ValidateAndFixRequest(&InternalRequest{Messages: messages}, "test_parallel_split")

	for _, id := range []string{"call_f7", "call_f8", "call_f9"} {
		found := false
		for _, m := range result.Messages {
			if m.Role == "tool" && m.ToolCallID == id {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("full pipeline dropped tool result %s", id)
		}
	}
	assistantWithCalls := 0
	for _, m := range result.Messages {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			assistantWithCalls++
		}
	}
	if assistantWithCalls != 1 {
		t.Errorf("expected exactly 1 assistant message with tool_calls after full pipeline, got %d", assistantWithCalls)
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
