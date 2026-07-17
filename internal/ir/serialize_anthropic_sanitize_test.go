package ir

import (
	"encoding/json"
	"testing"
)

func TestSerializeAnthropic_Tools_SanitizeRequired(t *testing.T) {
	// 测试 IR → Anthropic 序列化时清理 required 字段（单个字符串）
	ir := &InternalRequest{
		Model:     "claude-opus-4-8",
		MaxTokens: 1024,
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "test"}}},
		},
		Tools: []ToolDefinition{
			{
				Name:        "get_weather",
				Description: "Get weather",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"city": {"type": "string"}
					},
					"required": "city"
				}`),
			},
		},
	}

	out, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatal(err)
	}

	var outMap map[string]any
	if err := json.Unmarshal(out, &outMap); err != nil {
		t.Fatal(err)
	}

	tools := outMap["tools"].([]any)
	tool0 := tools[0].(map[string]any)
	inputSchema := tool0["input_schema"].(map[string]any)
	required := inputSchema["required"].([]any) // JSON unmarshals as []any

	if len(required) != 1 {
		t.Fatalf("required should have 1 element, got %d", len(required))
	}
	if required[0] != "city" {
		t.Errorf("required[0] = %v, want \"city\"", required[0])
	}
}

func TestSerializeAnthropic_Tools_SanitizeMixedArray(t *testing.T) {
	// 测试混合类型数组过滤
	ir := &InternalRequest{
		Model:     "claude-opus-4-8",
		MaxTokens: 1024,
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "test"}}},
		},
		Tools: []ToolDefinition{
			{
				Name:        "get_weather",
				Description: "Get weather",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"city": {"type": "string"},
						"unit": {"type": "string"}
					},
					"required": ["city", 123, "unit", true]
				}`),
			},
		},
	}

	out, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatal(err)
	}

	var outMap map[string]any
	if err := json.Unmarshal(out, &outMap); err != nil {
		t.Fatal(err)
	}

	tools := outMap["tools"].([]any)
	tool0 := tools[0].(map[string]any)
	inputSchema := tool0["input_schema"].(map[string]any)
	required := inputSchema["required"].([]any)

	if len(required) != 2 {
		t.Fatalf("required should have 2 elements (filtered), got %d", len(required))
	}
	if required[0] != "city" || required[1] != "unit" {
		t.Errorf("required = %v, want [\"city\", \"unit\"]", required)
	}
}

func TestSerializeAnthropic_Tools_NestedRequired(t *testing.T) {
	// 测试嵌套 schema 的 required 字段清理
	ir := &InternalRequest{
		Model:     "claude-opus-4-8",
		MaxTokens: 1024,
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "test"}}},
		},
		Tools: []ToolDefinition{
			{
				Name:        "create_user",
				Description: "Create user",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"address": {
							"type": "object",
							"properties": {
								"city": {"type": "string"}
							},
							"required": "city"
						}
					}
				}`),
			},
		},
	}

	out, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatal(err)
	}

	var outMap map[string]any
	if err := json.Unmarshal(out, &outMap); err != nil {
		t.Fatal(err)
	}

	tools := outMap["tools"].([]any)
	tool0 := tools[0].(map[string]any)
	inputSchema := tool0["input_schema"].(map[string]any)
	properties := inputSchema["properties"].(map[string]any)
	address := properties["address"].(map[string]any)
	required := address["required"].([]any)

	if len(required) != 1 {
		t.Fatalf("nested required should have 1 element, got %d", len(required))
	}
	if required[0] != "city" {
		t.Errorf("nested required[0] = %v, want \"city\"", required[0])
	}
}

func TestSerializeAnthropic_Tools_ValidRequired(t *testing.T) {
	// 测试正确格式保持不变
	ir := &InternalRequest{
		Model:     "claude-opus-4-8",
		MaxTokens: 1024,
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "test"}}},
		},
		Tools: []ToolDefinition{
			{
				Name:        "get_weather",
				Description: "Get weather",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"city": {"type": "string"},
						"unit": {"type": "string"}
					},
					"required": ["city", "unit"]
				}`),
			},
		},
	}

	out, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatal(err)
	}

	var outMap map[string]any
	if err := json.Unmarshal(out, &outMap); err != nil {
		t.Fatal(err)
	}

	tools := outMap["tools"].([]any)
	tool0 := tools[0].(map[string]any)
	inputSchema := tool0["input_schema"].(map[string]any)
	required := inputSchema["required"].([]any)

	if len(required) != 2 {
		t.Fatalf("required should have 2 elements, got %d", len(required))
	}
	if required[0] != "city" || required[1] != "unit" {
		t.Errorf("required = %v, want [\"city\", \"unit\"]", required)
	}
}

func TestSerializeAnthropic_Tools_InvalidJSON(t *testing.T) {
	// 测试无效 JSON 的回退逻辑
	// Note: json.RawMessage validates at marshal time, so invalid JSON will cause
	// SerializeAnthropic to fail. This test verifies the error is propagated correctly.
	ir := &InternalRequest{
		Model:     "claude-opus-4-8",
		MaxTokens: 1024,
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "test"}}},
		},
		Tools: []ToolDefinition{
			{
				Name:        "get_weather",
				Description: "Get weather",
				Parameters:  json.RawMessage(`{invalid json`),
			},
		},
	}

	// Invalid JSON should cause serialization to fail
	_, err := SerializeAnthropic(ir)
	if err == nil {
		t.Error("SerializeAnthropic should fail with invalid JSON in Parameters")
	}
}
