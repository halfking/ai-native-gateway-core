package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSerializeAnthropic_MiniMaxToolCallID 测试 Bug #3 修复：
// 当 TargetProvider="minimax" 时，tool_result 块使用 tool_call_id
// 而非标准 Anthropic 的 tool_use_id。
func TestSerializeAnthropic_MiniMaxToolCallID(t *testing.T) {
	req := &InternalRequest{
		Model:          "MiniMax-M3",
		TargetProvider: "minimax",
		MaxTokens:      1024,
		Messages: []Message{
			{
				Role:    "user",
				Content: []ContentBlock{{Type: "text", Text: "调用 get_weather"}},
			},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{
					{
						ID:   "call_minimax_123",
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
				ToolCallID: "call_minimax_123",
				Content:    []ContentBlock{{Type: "text", Text: "晴天 25度"}},
			},
		},
	}

	body, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("SerializeAnthropic failed: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	messages, ok := result["messages"].([]any)
	if !ok || len(messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(messages))
	}

	// Check the tool result (converted to user message index 2)
	toolMsg, ok := messages[2].(map[string]any)
	if !ok {
		t.Fatalf("tool message is not a map")
	}

	content, ok := toolMsg["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("tool message content missing or empty")
	}

	toolResult, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("tool_result block is not a map")
	}

	// Must use tool_call_id, NOT tool_use_id
	if _, hasCallID := toolResult["tool_call_id"]; !hasCallID {
		t.Errorf("minimax: expected tool_call_id field, got %v", toolResult)
	}
	if _, hasUseID := toolResult["tool_use_id"]; hasUseID {
		t.Errorf("minimax: should NOT have tool_use_id field, got %v", toolResult)
	}

	if id, _ := toolResult["tool_call_id"].(string); id != "call_minimax_123" {
		t.Errorf("tool_call_id = %q, want call_minimax_123", id)
	}
}

// TestSerializeAnthropic_StandardAnthropicToolUseID 测试标准 Anthropic 协议
// （TargetProvider 为空或其他非 minimax 值）应继续使用 tool_use_id。
func TestSerializeAnthropic_StandardAnthropicToolUseID(t *testing.T) {
	req := &InternalRequest{
		Model:          "claude-opus-4",
		TargetProvider: "",
		MaxTokens:      1024,
		Messages: []Message{
			{
				Role:    "user",
				Content: []ContentBlock{{Type: "text", Text: "调用 get_weather"}},
			},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{
					{
						ID:   "call_anthropic_123",
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
				ToolCallID: "call_anthropic_123",
				Content:    []ContentBlock{{Type: "text", Text: "晴天 25度"}},
			},
		},
	}

	body, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("SerializeAnthropic failed: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	messages := result["messages"].([]any)
	toolMsg := messages[2].(map[string]any)
	content := toolMsg["content"].([]any)
	toolResult := content[0].(map[string]any)

	// Must use tool_use_id, NOT tool_call_id
	if _, hasUseID := toolResult["tool_use_id"]; !hasUseID {
		t.Errorf("standard anthropic: expected tool_use_id field, got %v", toolResult)
	}
	if _, hasCallID := toolResult["tool_call_id"]; hasCallID {
		t.Errorf("standard anthropic: should NOT have tool_call_id field, got %v", toolResult)
	}

	if id, _ := toolResult["tool_use_id"].(string); id != "call_anthropic_123" {
		t.Errorf("tool_use_id = %q, want call_anthropic_123", id)
	}
}

// TestSerializeAnthropic_OtherProviderUsesStandardToolUseID 测试其他 provider
// （如 openai）也使用标准 tool_use_id（非 minimax 时兜底）。
func TestSerializeAnthropic_OtherProviderUsesStandardToolUseID(t *testing.T) {
	req := &InternalRequest{
		Model:          "claude-sonnet",
		TargetProvider: "openai",
		MaxTokens:      1024,
		Messages: []Message{
			{
				Role:       "tool",
				ToolCallID: "call_test",
				Content:    []ContentBlock{{Type: "text", Text: "Result"}},
			},
		},
	}

	body, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("SerializeAnthropic failed: %v", err)
	}

	// Should NOT contain "tool_call_id" for non-minimax providers
	if strings.Contains(string(body), `"tool_call_id"`) {
		t.Errorf("non-minimax: should not contain tool_call_id, got: %s", string(body))
	}
}
