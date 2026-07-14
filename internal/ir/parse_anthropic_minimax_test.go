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

func TestParseAnthropic_MiniMaxMessageLevelToolCallID(t *testing.T) {
	body := []byte(`{
		"model":"MiniMax-M3",
		"messages":[{
			"role":"tool",
			"tool_call_id":"call_message_level",
			"content":"ok"
		}]
	}`)

	req, err := ParseAnthropic(body)
	if err != nil {
		t.Fatalf("ParseAnthropic failed: %v", err)
	}
	if got := req.Messages[0].ToolCallID; got != "call_message_level" {
		t.Fatalf("ToolCallID = %q, want call_message_level", got)
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

// TestParseAnthropicContentBlock_MiniMaxToolCallID 验证 parseAnthropicContentBlock
// 单块 helper（系统提示中 tool_result 块的解析路径）也支持 MiniMax 的
// tool_call_id 回退，与 parseAnthropicContentBlocks 批量路径保持一致。
//
// 修复背景：parseAnthropicContentBlocks 在 335-338 行已支持 tool_call_id 回退，
// 但 parseAnthropicContentBlock（单块 helper）此前只读 tool_use_id。该 helper
// 由 parseAnthropicSystem 用于系统提示中嵌入的工具结果块；两个 helper 行为
// 不一致属于解析阶段跨协议契约 bug。
func TestParseAnthropicContentBlock_MiniMaxToolCallID(t *testing.T) {
	// system prompt 的 content 可以是数组；这里放入一个 tool_result 块来触发
	// parseAnthropicContentBlock 单块 helper 的执行路径。
	body := []byte(`{
		"model": "MiniMax-M3",
		"max_tokens": 1024,
		"system": [{
			"type": "tool_result",
			"tool_call_id": "call_system_block",
			"content": "embedded tool result"
		}],
		"messages": [{
			"role": "user",
			"content": "hello"
		}]
	}`)

	req, err := ParseAnthropic(body)
	if err != nil {
		t.Fatalf("ParseAnthropic failed: %v", err)
	}

	if req.System == nil || len(req.System.Parts) == 0 {
		t.Fatalf("expected system prompt with parsed parts")
	}

	var found bool
	for _, part := range req.System.Parts {
		if part.Type == "tool_result" && part.ToolResult != nil {
			found = true
			if part.ToolResult.ToolUseID != "call_system_block" {
				t.Errorf("ToolUseID = %q, want call_system_block (MiniMax tool_call_id fallback)", part.ToolResult.ToolUseID)
			}
		}
	}
	if !found {
		t.Fatalf("expected tool_result block in system parts, got %+v", req.System.Parts)
	}
}

// TestSerializeAnthropic_ToolRoleNestedToolResultContent 验证当 IR Message
// Role=tool 但 Content 内含 tool_result 块（而不是纯 text）时，序列化器
// 仍能正确提取嵌套的 text 内容写入 Anthropic tool_result.content 字段。
//
// 修复背景：serializeAnthropicMessage 的 Role=tool 分支此前只遍历
// msg.Content 中 Type==text 的块，遗漏 Type==tool_result 块内部嵌套的文本。
// 当 OpenAI Chat role=tool 请求经过 IR 时也可能产生此形状。
func TestSerializeAnthropic_ToolRoleNestedToolResultContent(t *testing.T) {
	req := &InternalRequest{
		Model:     "MiniMax-M3",
		MaxTokens: 256,
		Messages: []Message{
			{
				Role:       "tool",
				ToolCallID: "call_nested_1",
				Name:       "lookup",
				Content: []ContentBlock{
					{
						Type: "tool_result",
						ToolResult: &ToolResult{
							ToolUseID: "call_nested_1",
							Content: []ContentBlock{
								{Type: "text", Text: "first line"},
								{Type: "text", Text: "second line"},
							},
						},
					},
				},
			},
		},
	}
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

	if id, _ := toolResult["tool_call_id"].(string); id != "call_nested_1" {
		t.Errorf("tool_call_id = %q, want call_nested_1", id)
	}

	gotContent, ok := toolResult["content"].(string)
	if !ok {
		t.Fatalf("tool_result.content is not a string: %T (%v)", toolResult["content"], toolResult["content"])
	}
	want := "first line\nsecond line"
	if gotContent != want {
		t.Errorf("tool_result.content = %q, want %q", gotContent, want)
	}
}
