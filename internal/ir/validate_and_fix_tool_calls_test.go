package ir

import "testing"

func TestMergeConsecutiveSameRoleMessages_PreservesAssistantToolCalls(t *testing.T) {
	messages := []Message{
		{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "first"}}, ToolCalls: []ToolCall{{ID: "call_1"}}},
		{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "second"}}, ToolCalls: []ToolCall{{ID: "call_2"}}},
	}

	merged := mergeConsecutiveSameRoleMessages(messages)
	if len(merged) != 2 {
		t.Fatalf("tool-call-bearing assistant messages were merged: %d messages", len(merged))
	}
	if merged[0].ToolCalls[0].ID != "call_1" || merged[1].ToolCalls[0].ID != "call_2" {
		t.Fatalf("tool calls changed during merge: %#v", merged)
	}
}

func TestMergeConsecutiveSameRoleMessages_LeavesUnknownRolesSeparate(t *testing.T) {
	messages := []Message{
		{Role: "developer", Content: []ContentBlock{{Type: "text", Text: "first"}}},
		{Role: "developer", Content: []ContentBlock{{Type: "text", Text: "second"}}},
	}

	merged := mergeConsecutiveSameRoleMessages(messages)
	if len(merged) != 2 {
		t.Fatalf("unknown-role messages were merged: %d messages", len(merged))
	}
}
