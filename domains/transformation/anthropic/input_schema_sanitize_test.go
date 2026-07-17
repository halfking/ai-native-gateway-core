package anthropic

import "testing"

func TestSanitizeInputSchema_RequiredString(t *testing.T) {
	// 测试单个字符串转数组
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"city": map[string]any{"type": "string"},
		},
		"required": "city", // 错误格式
	}

	result := sanitizeInputSchema(schema)
	resultMap := result.(map[string]any)
	required, ok := resultMap["required"].([]string)
	if !ok {
		t.Fatalf("required should be []string, got %T", resultMap["required"])
	}
	if len(required) != 1 || required[0] != "city" {
		t.Errorf("required = %v, want [\"city\"]", required)
	}
}

func TestSanitizeInputSchema_RequiredMixedArray(t *testing.T) {
	// 测试混合类型数组
	schema := map[string]any{
		"type":     "object",
		"required": []any{"city", 123, "unit", true}, // 包含非字符串
	}

	result := sanitizeInputSchema(schema)
	resultMap := result.(map[string]any)
	required := resultMap["required"].([]string)
	if len(required) != 2 {
		t.Fatalf("required should have 2 elements, got %d", len(required))
	}
	if required[0] != "city" || required[1] != "unit" {
		t.Errorf("required = %v, want [\"city\", \"unit\"]", required)
	}
}

func TestSanitizeInputSchema_RequiredStringArray(t *testing.T) {
	// 测试正确格式保持不变
	schema := map[string]any{
		"type":     "object",
		"required": []string{"city", "unit"},
	}

	result := sanitizeInputSchema(schema)
	resultMap := result.(map[string]any)
	required := resultMap["required"].([]string)
	if len(required) != 2 {
		t.Fatalf("required should have 2 elements, got %d", len(required))
	}
	if required[0] != "city" || required[1] != "unit" {
		t.Errorf("required = %v, want [\"city\", \"unit\"]", required)
	}
}

func TestSanitizeInputSchema_RequiredInvalidType(t *testing.T) {
	// 测试无效类型被删除
	schema := map[string]any{
		"type":     "object",
		"required": 123, // 数字类型
	}

	result := sanitizeInputSchema(schema)
	resultMap := result.(map[string]any)
	if _, exists := resultMap["required"]; exists {
		t.Errorf("invalid required field should be removed, got %v", resultMap["required"])
	}
}

func TestSanitizeInputSchema_Nested(t *testing.T) {
	// 测试嵌套 schema
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"address": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"city": map[string]any{"type": "string"},
				},
				"required": "city", // 嵌套的错误格式
			},
		},
	}

	result := sanitizeInputSchema(schema)
	resultMap := result.(map[string]any)
	props := resultMap["properties"].(map[string]any)
	address := props["address"].(map[string]any)
	required := address["required"].([]string)
	if len(required) != 1 || required[0] != "city" {
		t.Errorf("nested required = %v, want [\"city\"]", required)
	}
}

func TestSanitizeInputSchema_ArrayItems(t *testing.T) {
	// 测试数组 items 递归处理
	schema := map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
			"required": "name", // items 中的错误格式
		},
	}

	result := sanitizeInputSchema(schema)
	resultMap := result.(map[string]any)
	items := resultMap["items"].(map[string]any)
	required := items["required"].([]string)
	if len(required) != 1 || required[0] != "name" {
		t.Errorf("items.required = %v, want [\"name\"]", required)
	}
}

func TestSanitizeInputSchema_AdditionalProperties(t *testing.T) {
	// 测试 additionalProperties 递归处理
	schema := map[string]any{
		"type": "object",
		"additionalProperties": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"value": map[string]any{"type": "string"},
			},
			"required": "value",
		},
	}

	result := sanitizeInputSchema(schema)
	resultMap := result.(map[string]any)
	additionalProps := resultMap["additionalProperties"].(map[string]any)
	required := additionalProps["required"].([]string)
	if len(required) != 1 || required[0] != "value" {
		t.Errorf("additionalProperties.required = %v, want [\"value\"]", required)
	}
}

func TestSanitizeInputSchema_NonMapInput(t *testing.T) {
	// 测试非 map 输入直接返回
	schema := "invalid"
	result := sanitizeInputSchema(schema)
	if result != "invalid" {
		t.Errorf("non-map input should be returned as-is, got %v", result)
	}
}

func TestOpenAIToolToAnthropic_WithInvalidRequired(t *testing.T) {
	// 端到端测试：OpenAI tool 转 Anthropic，包含错误的 required 格式
	tool := map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "get_weather",
			"description": "Get weather",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"city": map[string]any{"type": "string"},
				},
				"required": "city", // 单个字符串
			},
		},
	}

	result, ok := openAIToolToAnthropic(tool)
	if !ok {
		t.Fatal("conversion failed")
	}

	inputSchema := result["input_schema"].(map[string]any)
	required, ok := inputSchema["required"].([]string)
	if !ok {
		t.Fatalf("input_schema.required should be []string after sanitize, got %T", inputSchema["required"])
	}
	if len(required) != 1 || required[0] != "city" {
		t.Errorf("input_schema.required = %v, want [\"city\"]", required)
	}
}

func TestOpenAIToolToAnthropic_WithValidRequired(t *testing.T) {
	// 测试正确格式保持不变
	tool := map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "get_weather",
			"description": "Get weather",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"city": map[string]any{"type": "string"},
					"unit": map[string]any{"type": "string"},
				},
				"required": []string{"city", "unit"},
			},
		},
	}

	result, ok := openAIToolToAnthropic(tool)
	if !ok {
		t.Fatal("conversion failed")
	}

	inputSchema := result["input_schema"].(map[string]any)
	required := inputSchema["required"].([]string)
	if len(required) != 2 || required[0] != "city" || required[1] != "unit" {
		t.Errorf("input_schema.required = %v, want [\"city\", \"unit\"]", required)
	}
}
