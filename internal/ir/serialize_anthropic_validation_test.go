package ir

import (
	"strings"
	"testing"
)

// TestSerializeAnthropic_OrphanedToolResult tests orphaned tool_result handling.
//
// 2026-11-13 改判（211 事故 request 75777ea4…，claude-sonnet-5）：原契约是
// 「孤儿必须拒绝」（清晰错误信息）——但 ZCode 会话压缩删 assistant tool_use
// 留下 80 个孤儿 tool_result 时，拒绝 ⇒ 客户端重放同样失败 ⇒ 会话砖化
// （r1048「trim 吞 user」同族灾难）。现契约：孤儿降级为 text 块（内容保留、
// 带原 id 标注）放行 + anomaly 留痕（tool_result_orphan_downgrade）。
func TestSerializeAnthropic_OrphanedToolResult(t *testing.T) {
	req := &InternalRequest{
		Model:     "claude-3-5-sonnet-20241022",
		MaxTokens: 1024,
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

	body, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("orphaned tool_result must be downgraded and serialized, got error: %v", err)
	}
	// 孤儿内容必须以降级文本保留到达上游（不丢信息），且带原 id 标注。
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "[orphaned tool result toolu_orphaned_123]") {
		t.Errorf("downgraded text should carry the orphaned ID annotation, got: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "Result") {
		t.Errorf("orphaned tool_result content must survive the downgrade, got: %s", bodyStr)
	}
}

// TestSerializeAnthropic_ValidToolUse tests that valid tool_use → tool_result chains pass.
func TestSerializeAnthropic_ValidToolUse(t *testing.T) {
	req := &InternalRequest{
		Model:     "claude-3-5-sonnet-20241022",
		MaxTokens: 1024,
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
		Model:     "claude-3-5-sonnet-20241022",
		MaxTokens: 1024,
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

	// 2026-11-13 改判（同 TestSerializeAnthropic_OrphanedToolResult）：混排孤儿
	// 降级放行——合法配对保持 tool_result，孤儿转 text。
	body, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("partial orphans must be downgraded and serialized, got error: %v", err)
	}
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "[orphaned tool result toolu_orphaned]") {
		t.Errorf("downgraded text should carry the orphaned ID, got: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "Lost") {
		t.Errorf("orphaned content must survive, got: %s", bodyStr)
	}
}

// TestSerializeAnthropic_ShortMessageList tests that validation is skipped for short lists.
func TestSerializeAnthropic_ShortMessageList(t *testing.T) {
	// Single message - should not trigger validation
	req := &InternalRequest{
		Model:     "claude-3-5-sonnet-20241022",
		MaxTokens: 1024,
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
		Model:     "claude-3-5-sonnet-20241022",
		MaxTokens: 1024,
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

	// 2026-11-13 改判（同 TestSerializeAnthropic_OrphanedToolResult）：多孤儿
	// 降级放行——每个孤儿都保留 id 标注文本（原「错误信息截断前 3」契约随
	// 拒绝路径一起退役）。
	body, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("multiple orphans must be downgraded and serialized, got error: %v", err)
	}
	bodyStr := string(body)
	for _, id := range []string{"toolu_orphan_1", "toolu_orphan_2", "toolu_orphan_3", "toolu_orphan_4", "toolu_orphan_5"} {
		if !strings.Contains(bodyStr, "[orphaned tool result "+id+"]") {
			t.Errorf("downgraded text should carry annotation for %s, got: %s", id, bodyStr)
		}
	}
}
