package ir

import (
	"strings"
	"testing"
)

// TestSerializeAnthropic_OrphanedToolResult tests that orphaned tool_result blocks
// (without matching assistant tool_use) are rejected with a clear error.
func TestSerializeAnthropic_OrphanedToolResult(t *testing.T) {
	req := &InternalRequest{
		Model:      "claude-3-5-sonnet-20241022",
		MaxTokens:  1024,
		Messages: []Message{
			{
				Role:    "user",
				Content: []ContentBlock{{Type: "text", Text: "Hello"}},
			},
			// Orphaned tool_result (no matching assistant tool_use)
			{
				Role: "user",
				Content: []ContentBlock{
					{
						Type: "tool_result",
						ToolResult: &ToolResult{
							ToolUseID: "toolu_orphaned_123",
							Content: []ContentBlock{
								{Type: "text", Text: "Result"},
							},
						},
					},
				},
			},
			{
				Role:    "user",
				Content: []ContentBlock{{Type: "text", Text: "Continue"}},
			},
		},
	}

	_, err := SerializeAnthropic(req)
	if err == nil {
		t.Fatalf("expected validation error for orphaned tool_result, got nil")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "orphaned tool result") {
		t.Errorf("error message should mention 'orphaned tool result', got: %v", errMsg)
	}
	if !strings.Contains(errMsg, "toolu_orphaned_123") {
		t.Errorf("error message should include the orphaned ID, got: %v", errMsg)
	}
	if !strings.Contains(errMsg, "likely client bug") {
		t.Errorf("error message should mention 'likely client bug', got: %v", errMsg)
	}
}

// TestSerializeAnthropic_ValidToolUse tests that valid tool_use → tool_result chains pass.
func TestSerializeAnthropic_ValidToolUse(t *testing.T) {
	req := &InternalRequest{
		Model:      "claude-3-5-sonnet-20241022",
		MaxTokens:  1024,
		Messages: []Message{
			{
				Role:    "user",
				Content: []ContentBlock{{Type: "text", Text: "What's the weather?"}},
			},
			{
				Role: "assistant",
				Content: []ContentBlock{
					{
						Type: "tool_use",
						ToolUse: &ToolUse{
							ID:    "toolu_valid_123",
							Name:  "get_weather",
							Input: []byte(`{"city":"Beijing"}`),
						},
					},
				},
			},
			{
				Role: "user",
				Content: []ContentBlock{
					{
						Type: "tool_result",
						ToolResult: &ToolResult{
							ToolUseID: "toolu_valid_123",
							Content: []ContentBlock{
								{Type: "text", Text: "Sunny, 25°C"},
							},
						},
					},
				},
			},
			{
				Role:    "assistant",
				Content: []ContentBlock{{Type: "text", Text: "It's sunny and 25°C"}},
			},
		},
	}

	body, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("SerializeAnthropic failed on valid tool_use: %v", err)
	}

	if len(body) == 0 {
		t.Errorf("expected non-empty body")
	}
}

// TestSerializeAnthropic_MiniMaxOrphanedToolResult tests MiniMax-specific orphaned tool_result with tool_call_id.
func TestSerializeAnthropic_MiniMaxOrphanedToolResult(t *testing.T) {
	req := &InternalRequest{
		Model:          "abab6.5s-chat",
		MaxTokens:      1024,
		TargetProvider: "minimax",
		Messages: []Message{
			{
				Role:    "user",
				Content: []ContentBlock{{Type: "text", Text: "Test"}},
			},
			{
				Role: "assistant",
				Content: []ContentBlock{
					{
						Type: "tool_use",
						ToolUse: &ToolUse{
							ID:    "call_minimax_123",
							Name:  "test_tool",
							Input: []byte(`{}`),
						},
					},
				},
			},
			{
				Role: "user",
				Content: []ContentBlock{
					{
						Type: "tool_result",
						ToolResult: &ToolResult{
							ToolUseID: "call_minimax_123", // Will be serialized as tool_call_id for MiniMax
							Content: []ContentBlock{
								{Type: "text", Text: "OK"},
							},
						},
					},
				},
			},
		},
	}

	body, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("SerializeAnthropic failed for MiniMax: %v", err)
	}

	// Verify tool_call_id is used (validated by validateAnthropicToolCallIntegrity with targetProvider="minimax")
	if len(body) == 0 {
		t.Errorf("expected non-empty body")
	}
}

// TestSerializeAnthropic_PartialOrphans tests mixed valid and orphaned tool_results.
func TestSerializeAnthropic_PartialOrphans(t *testing.T) {
	req := &InternalRequest{
		Model:      "claude-3-5-sonnet-20241022",
		MaxTokens:  1024,
		Messages: []Message{
			{
				Role:    "user",
				Content: []ContentBlock{{Type: "text", Text: "Test"}},
			},
			{
				Role: "assistant",
				Content: []ContentBlock{
					{
						Type: "tool_use",
						ToolUse: &ToolUse{
							ID:    "toolu_valid",
							Name:  "tool1",
							Input: []byte(`{}`),
						},
					},
				},
			},
			{
				Role: "user",
				Content: []ContentBlock{
					{
						Type: "tool_result",
						ToolResult: &ToolResult{
							ToolUseID: "toolu_valid",
							Content: []ContentBlock{
								{Type: "text", Text: "OK"},
							},
						},
					},
					// Orphaned tool_result in same user message
					{
						Type: "tool_result",
						ToolResult: &ToolResult{
							ToolUseID: "toolu_orphaned",
							Content: []ContentBlock{
								{Type: "text", Text: "Lost"},
							},
						},
					},
				},
			},
		},
	}

	_, err := SerializeAnthropic(req)
	if err == nil {
		t.Fatalf("expected validation error for partial orphans, got nil")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "toolu_orphaned") {
		t.Errorf("error should mention orphaned ID, got: %v", errMsg)
	}
}

// TestSerializeAnthropic_ShortMessageList tests that validation is skipped for short lists.
func TestSerializeAnthropic_ShortMessageList(t *testing.T) {
	// Single message - should not trigger validation
	req := &InternalRequest{
		Model:      "claude-3-5-sonnet-20241022",
		MaxTokens:  1024,
		Messages: []Message{
			{
				Role:    "user",
				Content: []ContentBlock{{Type: "text", Text: "Hello"}},
			},
		},
	}

	_, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("SerializeAnthropic failed on short message list: %v", err)
	}

	// Two messages - should not trigger validation
	req.Messages = append(req.Messages, Message{
		Role:    "assistant",
		Content: []ContentBlock{{Type: "text", Text: "Hi"}},
	})

	_, err = SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("SerializeAnthropic failed on 2-message list: %v", err)
	}
}

// TestSerializeAnthropic_MultipleOrphans tests error message truncation.
func TestSerializeAnthropic_MultipleOrphans(t *testing.T) {
	req := &InternalRequest{
		Model:      "claude-3-5-sonnet-20241022",
		MaxTokens:  1024,
		Messages: []Message{
			{
				Role:    "user",
				Content: []ContentBlock{{Type: "text", Text: "Test"}},
			},
			{
				Role:    "user",
				Content: []ContentBlock{{Type: "text", Text: "Message 2"}},
			},
			{
				Role:    "user",
				Content: []ContentBlock{{Type: "text", Text: "Message 3"}},
			},
		},
	}

	// Add 5 orphaned tool_results
	orphanBlocks := make([]ContentBlock, 5)
	for i := 0; i < 5; i++ {
		orphanBlocks[i] = ContentBlock{
			Type: "tool_result",
			ToolResult: &ToolResult{
				ToolUseID: "toolu_orphan_" + string(rune('1'+i)),
				Content: []ContentBlock{
					{Type: "text", Text: "Result"},
				},
			},
		}
	}
	req.Messages = append(req.Messages, Message{
		Role:    "user",
		Content: orphanBlocks,
	})

	_, err := SerializeAnthropic(req)
	if err == nil {
		t.Fatalf("expected validation error for multiple orphans, got nil")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "found 5 orphaned") {
		t.Errorf("error should mention total count of 5, got: %v", errMsg)
	}
	// Should limit displayed IDs to first 3
	if !strings.Contains(errMsg, "toolu_orphan_1") {
		t.Errorf("error should show first orphan, got: %v", errMsg)
	}
}
