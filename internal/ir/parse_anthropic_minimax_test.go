package ir

import (
	"encoding/json"
	"testing"
)

// TestParseAnthropic_ToolUseID 测试标准 Anthropic 协议 tool_use_id 解析。
func TestParseAnthropic_ToolUseID(t *testing.T) {
	body := []byte(`{
		"model": "claude-opus-4",
		"max_tokens": 1024,
		"messages": [{
			"role": "user",
			"content": [{
				"type": "tool_result",
				"tool_use_id": "toolu_test_123",
				"content": "weather is sunny"
			}]
		}]
	}`)

	req, err := ParseAnthropic(body)
	if err != nil {
		t.Fatalf("ParseAnthropic failed: %v", err)
	}

	if len(req.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(req.Messages))
	}

	msg := req.Messages[0]
	if len(msg.Content) == 0 {
		t.Fatalf("message has no content blocks")
	}

	block := msg.Content[0]
	if block.Type != "tool_result" || block.ToolResult == nil {
		t.Fatalf("expected tool_result block")
	}

	if block.ToolResult.ToolUseID != "toolu_test_123" {
		t.Errorf("ToolUseID = %q, want toolu_test_123", block.ToolResult.ToolUseID)
	}
}

// TestParseAnthropic_MiniMaxToolCallID 测试 MiniMax 协议 tool_call_id 解析。
// MiniMax（Anthropic 兼容协议）使用 tool_call_id 字段名而非标准的 tool_use_id。
func TestParseAnthropic_MiniMaxToolCallID(t *testing.T) {
	body := []byte(`{
		"model": "MiniMax-M3",
		"max_tokens": 1024,
		"messages": [{
			"role": "user",
			"content": [{
				"type": "tool_result",
				"tool_call_id": "call_minimax_456",
				"content": "weather is sunny"
			}]
		}]
	}`)

	req, err := ParseAnthropic(body)
	if err != nil {
		t.Fatalf("ParseAnthropic failed: %v", err)
	}

	if len(req.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(req.Messages))
	}

	msg := req.Messages[0]
	if len(msg.Content) == 0 {
		t.Fatalf("message has no content blocks")
	}

	block := msg.Content[0]
	if block.Type != "tool_result" || block.ToolResult == nil {
		t.Fatalf("expected tool_result block")
	}

	if block.ToolResult.ToolUseID != "call_minimax_456" {
		t.Errorf("ToolUseID = %q, want call_minimax_456", block.ToolResult.ToolUseID)
	}
}

// TestParseAnthropic_RoundTripWithTargetProvider 测试 Parse → Serialize 往返保留
// MiniMax 的 tool_call_id。
func TestParseAnthropic_RoundTripWithTargetProvider(t *testing.T) {
	body := []byte(`{
		"model": "MiniMax-M3",
		"max_tokens": 1024,
		"messages": [{
			"role": "user",
			"content": [{
				"type": "tool_result",
				"tool_call_id": "call_round_trip",
				"content": "ok"
			}]
		}]
	}`)

	req, err := ParseAnthropic(body)
	if err != nil {
		t.Fatalf("ParseAnthropic failed: %v", err)
	}

	// Set TargetProvider for round-trip serialization
	req.TargetProvider = "minimax"

	out, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("SerializeAnthropic failed: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	messages := result["messages"].([]any)
	userMsg := messages[0].(map[string]any)
	content := userMsg["content"].([]any)
	toolResult := content[0].(map[string]any)

	if id, _ := toolResult["tool_call_id"].(string); id != "call_round_trip" {
		t.Errorf("round-trip lost tool_call_id, got %q", id)
	}
	if _, ok := toolResult["tool_use_id"]; ok {
		t.Errorf("round-trip should not emit tool_use_id for minimax, got %v", toolResult)
	}
}
