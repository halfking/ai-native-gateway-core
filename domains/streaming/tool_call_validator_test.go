package streaming

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToolCallValidator_Complete(t *testing.T) {
	v := NewToolCallValidator()

	// Register a tool_use
	v.OnToolUse("toolu_123", "get_weather", 0)

	// Verify it's pending
	assert.True(t, v.HasPendingToolUses())
	assert.Equal(t, 1, v.PendingCount())

	// Complete it with tool_result
	v.OnToolResult("toolu_123")

	// Verify completion
	assert.False(t, v.HasPendingToolUses())
	assert.Equal(t, 0, v.PendingCount())

	// Validate should succeed
	err := v.ValidateComplete()
	assert.NoError(t, err)
}

func TestToolCallValidator_MissingResult(t *testing.T) {
	v := NewToolCallValidator()

	// Register a tool_use
	v.OnToolUse("toolu_123", "get_weather", 0)

	// Stream ends without tool_result
	// In Anthropic's protocol, this is valid - tool_use without tool_result
	// means the assistant is requesting tool execution, and the client will
	// provide tool_result in the next turn.
	err := v.ValidateComplete()
	assert.NoError(t, err, "tool_use without tool_result is valid (assistant requesting execution)")
}

func TestToolCallValidator_MissingResultWhenResultExpected(t *testing.T) {
	v := NewToolCallValidator()

	// Register a tool_use
	v.OnToolUse("toolu_123", "get_weather", 0)
	
	// Register another tool_use
	v.OnToolUse("toolu_456", "calculate", 1)

	// Only provide result for the second one
	v.OnToolResult("toolu_456")

	// Stream ends - now we have a mix: one tool_use has a result, one doesn't
	// This is incomplete because we started providing results but didn't finish
	err := v.ValidateComplete()
	require.Error(t, err)

	// Check error details
	incompleteErr, ok := err.(*IncompleteToolCallError)
	require.True(t, ok, "expected IncompleteToolCallError")
	assert.Equal(t, 1, len(incompleteErr.MissingResults))
	assert.Equal(t, "toolu_123", incompleteErr.MissingResults[0])
	assert.Equal(t, 2, incompleteErr.TotalToolUses)
}

func TestToolCallValidator_MultipleMissingResults(t *testing.T) {
	v := NewToolCallValidator()

	// Register multiple tool_uses
	v.OnToolUse("toolu_1", "get_weather", 0)
	v.OnToolUse("toolu_2", "calculate", 1)
	v.OnToolUse("toolu_3", "search", 2)

	// Complete only the middle one
	v.OnToolResult("toolu_2")

	// Validate should fail with 2 missing
	err := v.ValidateComplete()
	require.Error(t, err)

	incompleteErr, ok := err.(*IncompleteToolCallError)
	require.True(t, ok)
	assert.Equal(t, 2, len(incompleteErr.MissingResults))
	assert.Equal(t, 3, incompleteErr.TotalToolUses)

	// Check that the missing IDs are toolu_1 and toolu_3
	missingSet := make(map[string]bool)
	for _, id := range incompleteErr.MissingResults {
		missingSet[id] = true
	}
	assert.True(t, missingSet["toolu_1"])
	assert.True(t, missingSet["toolu_3"])
	assert.False(t, missingSet["toolu_2"])
}

func TestToolCallValidator_EmptyStream(t *testing.T) {
	v := NewToolCallValidator()

	// No tool_use blocks registered
	assert.False(t, v.HasPendingToolUses())
	assert.Equal(t, 0, v.PendingCount())

	// Validation should succeed (no tool calls to validate)
	err := v.ValidateComplete()
	assert.NoError(t, err)
}

func TestToolCallValidator_NilValidator(t *testing.T) {
	var v *ToolCallValidator // nil validator

	// All operations should be no-ops
	v.OnToolUse("toolu_123", "get_weather", 0)
	v.OnToolResult("toolu_123")
	assert.False(t, v.HasPendingToolUses())
	assert.Equal(t, 0, v.PendingCount())
	err := v.ValidateComplete()
	assert.NoError(t, err)
}

func TestToolCallValidator_InvalidIDs(t *testing.T) {
	v := NewToolCallValidator()

	// Empty IDs should be ignored
	v.OnToolUse("", "get_weather", 0)
	assert.False(t, v.HasPendingToolUses())

	v.OnToolResult("")
	assert.False(t, v.HasPendingToolUses())

	err := v.ValidateComplete()
	assert.NoError(t, err)
}

func TestToolCallValidator_DuplicateToolUse(t *testing.T) {
	v := NewToolCallValidator()

	// Register same tool_use twice (protocol violation, but we handle it)
	v.OnToolUse("toolu_123", "get_weather", 0)
	v.OnToolUse("toolu_123", "get_weather", 0)

	// Should still track as one
	assert.Equal(t, 1, v.PendingCount())

	// Complete it once
	v.OnToolResult("toolu_123")

	// Should be complete
	assert.False(t, v.HasPendingToolUses())
	err := v.ValidateComplete()
	assert.NoError(t, err)
}

func TestToolCallValidator_ResultWithoutUse(t *testing.T) {
	v := NewToolCallValidator()

	// tool_result arrives without a preceding tool_use
	// (protocol violation, but shouldn't crash)
	v.OnToolResult("toolu_orphan")

	// Should be tracked as seen but not cause validation failure
	assert.False(t, v.HasPendingToolUses())
	err := v.ValidateComplete()
	assert.NoError(t, err)
}

func TestToolCallValidator_OutOfOrderCompletion(t *testing.T) {
	v := NewToolCallValidator()

	// Register tools in order
	v.OnToolUse("toolu_1", "first", 0)
	v.OnToolUse("toolu_2", "second", 1)
	v.OnToolUse("toolu_3", "third", 2)

	// Complete out of order: 3, 1, 2
	v.OnToolResult("toolu_3")
	v.OnToolResult("toolu_1")
	v.OnToolResult("toolu_2")

	// All should be complete
	assert.False(t, v.HasPendingToolUses())
	err := v.ValidateComplete()
	assert.NoError(t, err)
}

func TestToolCallValidator_StreamEndedNoNewRegistrations(t *testing.T) {
	v := NewToolCallValidator()

	// Register and complete a tool_use
	v.OnToolUse("toolu_1", "get_weather", 0)
	v.OnToolResult("toolu_1")

	// End the stream
	err := v.ValidateComplete()
	assert.NoError(t, err)

	// Try to register a new tool_use after validation
	// (should be ignored since stream ended)
	v.OnToolUse("toolu_2", "late_tool", 1)

	// Should not affect the validated state
	assert.False(t, v.HasPendingToolUses())
}

func TestToolCallValidator_PartialCompletion(t *testing.T) {
	v := NewToolCallValidator()

	// Register 5 tool_uses
	for i := 1; i <= 5; i++ {
		v.OnToolUse("toolu_"+string('0'+rune(i)), "tool", i-1)
	}

	assert.Equal(t, 5, v.PendingCount())

	// Complete 3 of them
	v.OnToolResult("toolu_1")
	v.OnToolResult("toolu_3")
	v.OnToolResult("toolu_5")

	assert.Equal(t, 2, v.PendingCount())
	assert.True(t, v.HasPendingToolUses())

	// Validate should fail
	err := v.ValidateComplete()
	require.Error(t, err)

	incompleteErr, ok := err.(*IncompleteToolCallError)
	require.True(t, ok)
	assert.Equal(t, 2, len(incompleteErr.MissingResults))
}

func TestClassifyIncompleteToolCall(t *testing.T) {
	tests := []struct {
		name             string
		streamGotDone    bool
		expectedReason   string
		expectedResumable bool
	}{
		{
			name:             "interrupted before done",
			streamGotDone:    false,
			expectedReason:   "incomplete_tool_call_interrupted",
			expectedResumable: true,
		},
		{
			name:             "completed with done but missing result",
			streamGotDone:    true,
			expectedReason:   "incomplete_tool_call_after_done",
			expectedResumable: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, resumable, reason := classifyIncompleteToolCall(tt.streamGotDone)
			assert.Equal(t, tt.expectedReason, reason)
			assert.Equal(t, tt.expectedResumable, resumable)
		})
	}
}

func TestIncompleteToolCallError_Message(t *testing.T) {
	// Single missing result
	err1 := &IncompleteToolCallError{
		MissingResults: []string{"toolu_123"},
		TotalToolUses:  1,
	}
	assert.Contains(t, err1.Error(), "toolu_123")

	// Multiple missing results
	err2 := &IncompleteToolCallError{
		MissingResults: []string{"toolu_1", "toolu_2"},
		TotalToolUses:  3,
	}
	assert.Contains(t, err2.Error(), "multiple")
}
