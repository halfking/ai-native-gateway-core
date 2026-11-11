package ir

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 客户端错误格式修正测试（2026-11-11 审计轮）。
//
// ⚠️ 断言层级纪律（本轮踩坑后确立）：
// 本文件第一版把断言打在 **parse 层**（req.MaxTokens / req.Tools[0].Name），
// 于是把两个**已实现**的能力误报成缺陷：
//   - max_tokens 默认值：序列化层 anthropicMaxTokens() 已填 4096
//     （internal/ir/serialize_anthropic.go:1314），parse 层保持 0 是**正确**的
//     ——IR 记客户端真实意图，默认值属于线级语义。
//   - 孤立 tool_result：ValidateAndFixRequest → SanitizeToolMessages 已清理
//     （internal/ir/validate_and_fix.go:74），且已接进生产管线
//     （domains/streaming/executors/executor_chat.go:3046/3138/3206）。
//
// ⇒ 修正后的断言层级：**Parse → ValidateAndFixRequest → Serialize → 断 wire 字节**。
// 这与 internal/ir/cross_format_extension_matrix_test.go 的既有范式一致。
// 纪律：**验收「客户端可见行为」不看中间态，看 wire**。

// ─────────────────────────────────────────────────────────────────────────────
// wire 级断言辅助
// ─────────────────────────────────────────────────────────────────────────────

// wireOf 把任意值解码成 map，便于对线上 JSON 断字段。
func wireOf(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m), "wire 必须是合法 JSON")
	return m
}

// wireTools 取 wire 里的 tools 数组。
func wireTools(t *testing.T, wire map[string]any) []any {
	t.Helper()
	raw, ok := wire["tools"]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	require.True(t, ok, "tools 必须是数组，实际 %T", raw)
	return list
}

// ─────────────────────────────────────────────────────────────────────────────
// 形制 A：客户端把 OpenAI 工具定义发到 Anthropic 端点
// ─────────────────────────────────────────────────────────────────────────────

// TestClientError_OpenAIToolFormatToAnthropic 覆盖 2026-11-11 实测撞出的
// 严重缺陷：parseAnthropicTools 把 type="function" 当成 Anthropic 的
// provider-specific builtin（旧判据只排除 "custom_tool"），于是走 Raw 兜底
// 分支 → Name/Description/Parameters 全丢 → 线上输出 {"name":""}。
//
// 修法：parseAnthropicTools 归一化 OpenAI 嵌套 function 形态（与
// parseOpenAITools 同构），目标侧序列化器再按目标协议输出原生形态。
func TestClientError_OpenAIToolFormatToAnthropic(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantName string
		wantDesc string
		// wantSchemaPresence=false 表示该形态不应带 input_schema（内建工具直通）
		wantSchemaPresence bool
	}{
		{
			name: "openai_nested_function",
			input: `{
				"model": "claude-sonnet-4-20250514",
				"max_tokens": 100,
				"tools": [{
					"type": "function",
					"function": {
						"name": "get_weather",
						"description": "Get weather",
						"parameters": {"type":"object","properties":{"city":{"type":"string"}}}
					}
				}],
				"messages": [{"role":"user","content":"weather?"}]
			}`,
			wantName:           "get_weather",
			wantDesc:           "Get weather",
			wantSchemaPresence: true,
		},
		{
			name: "openai_nested_function_without_type",
			input: `{
				"model": "claude-sonnet-4-20250514",
				"max_tokens": 100,
				"tools": [{
					"function": {
						"name": "search_docs",
						"parameters": {"type":"object"}
					}
				}],
				"messages": [{"role":"user","content":"search?"}]
			}`,
			wantName:           "search_docs",
			wantSchemaPresence: true,
		},
		{
			name: "openai_flat_function_with_type",
			input: `{
				"model": "claude-sonnet-4-20250514",
				"max_tokens": 100,
				"tools": [{
					"type": "function",
					"name": "flat_tool",
					"parameters": {"type":"object"}
				}],
				"messages": [{"role":"user","content":"go"}]
			}`,
			wantName:           "flat_tool",
			wantSchemaPresence: true,
		},
		{
			name: "anthropic_native_unchanged",
			input: `{
				"model": "claude-sonnet-4-20250514",
				"max_tokens": 100,
				"tools": [{
					"name": "native_tool",
					"description": "native",
					"input_schema": {"type":"object"}
				}],
				"messages": [{"role":"user","content":"go"}]
			}`,
			wantName:           "native_tool",
			wantDesc:           "native",
			wantSchemaPresence: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := ParseAnthropic([]byte(tt.input))
			require.NoError(t, err)
			require.NotNil(t, req)

			fixed := ValidateAndFixRequest(req, "client-fix-tools")
			require.NotNil(t, fixed)

			data, err := SerializeAnthropic(fixed)
			require.NoError(t, err)

			wire := wireOf(t, data)
			tools := wireTools(t, wire)
			require.Len(t, tools, 1, "应产出 1 个工具")

			tool := tools[0].(map[string]any)

			// 核心断言：工具名不能为空（这正是缺陷现场）
			name, _ := tool["name"].(string)
			assert.Equal(t, tt.wantName, name, "工具名必须被正确归一化")

			// 不得同时残留 OpenAI 的 function 嵌套形态
			_, hasFunction := tool["function"]
			assert.False(t, hasFunction, "Anthropic 线上形态不得残留 OpenAI 的 function 键")

			if tt.wantDesc != "" {
				desc, _ := tool["description"].(string)
				assert.Equal(t, tt.wantDesc, desc)
			}

			if tt.wantSchemaPresence {
				assert.Contains(t, tool, "input_schema", "应输出 Anthropic 原生的 input_schema")
			}
		})
	}
}

// TestClientError_OpenAIToolFormatCrossProtocol 覆盖跨协议方向：
// Anthropic 端点收到 OpenAI 工具 → 归一化 → 再序列化到 OpenAI 端点时
// 仍是合法的 OpenAI function 形态（不能变成 Anthropic 形态）。
func TestClientError_OpenAIToolFormatCrossProtocol(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 100,
		"tools": [{
			"type": "function",
			"function": {
				"name": "get_weather",
				"description": "Get weather",
				"parameters": {"type":"object"}
			}
		}],
		"messages": [{"role":"user","content":"weather?"}]
	}`

	req, err := ParseAnthropic([]byte(input))
	require.NoError(t, err)

	fixed := ValidateAndFixRequest(req, "client-fix-cross")
	data, err := SerializeOpenAI(fixed)
	require.NoError(t, err)

	wire := wireOf(t, data)
	tools := wireTools(t, wire)
	require.Len(t, tools, 1)

	tool := tools[0].(map[string]any)
	fn, ok := tool["function"].(map[string]any)
	require.True(t, ok, "OpenAI 线上形态必须有 function 对象，实际 %v", tool)

	name, _ := fn["name"].(string)
	assert.Equal(t, "get_weather", name, "跨协议后工具名必须保持")

	// OpenAI 形态不得残留 Anthropic 的 input_schema
	_, hasSchema := tool["input_schema"]
	assert.False(t, hasSchema, "OpenAI 线上形态不得残留 input_schema")
}

// ─────────────────────────────────────────────────────────────────────────────
// 形制 B：Anthropic 请求缺 max_tokens（Anthropic 硬性必填）
// ─────────────────────────────────────────────────────────────────────────────

// TestClientError_MissingMaxTokensAnthropic 断 **wire** 层：客户端省略
// max_tokens 时，线上必须带 >0 的 max_tokens（Anthropic 硬要求，否则 400）。
//
// 注意：断言打在 wire，不打 req.MaxTokens —— IR 记客户端意图，默认值是
// 线级语义（serialize_anthropic.go:anthropicMaxTokens → 4096）。
func TestClientError_MissingMaxTokensAnthropic(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"messages": [{"role":"user","content":"hi"}]
	}`

	req, err := ParseAnthropic([]byte(input))
	require.NoError(t, err)

	// IR 层：不应被改写（保留客户端真实意图）
	assert.Equal(t, 0, req.MaxTokens, "IR 层应保留客户端省略的事实")

	fixed := ValidateAndFixRequest(req, "client-fix-maxtokens")
	data, err := SerializeAnthropic(fixed)
	require.NoError(t, err)

	wire := wireOf(t, data)
	maxTokens, ok := wire["max_tokens"].(float64)
	require.True(t, ok, "wire 必须含 max_tokens")
	assert.Greater(t, maxTokens, 0.0, "max_tokens 必须 >0（Anthropic 硬要求）")
	assert.Equal(t, float64(DefaultAnthropicMaxTokens), maxTokens,
		"应落到约定的默认 4096")

	// 客户端显式给值时不得被覆盖
	req2, err := ParseAnthropic([]byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 777,
		"messages": [{"role":"user","content":"hi"}]
	}`))
	require.NoError(t, err)
	data2, err := SerializeAnthropic(ValidateAndFixRequest(req2, "client-fix-maxtokens"))
	require.NoError(t, err)
	assert.Equal(t, 777.0, wireOf(t, data2)["max_tokens"], "客户端显式值不得被覆盖")
}

// ─────────────────────────────────────────────────────────────────────────────
// 形制 C：孤立 tool_result（没有对应 tool_use）
// ─────────────────────────────────────────────────────────────────────────────

// TestClientError_OrphanToolResult 断 **validate 层**（这是该规则的归属层）：
// SanitizeToolMessages 应移除没有对应 tool_calls 的 tool 消息。
func TestClientError_OrphanToolResult(t *testing.T) {
	req := &InternalRequest{
		Model: "gpt-4",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "get weather"}}},
			// 缺少 assistant 的 tool_use → 下面这条是孤儿
			{Role: "tool", ToolCallID: "call_missing123", Name: "get_weather",
				Content: []ContentBlock{{Type: "text", Text: "sunny"}}},
		},
	}

	fixed := ValidateAndFixRequest(req, "client-fix-orphan")
	require.NotNil(t, fixed)

	for _, m := range fixed.Messages {
		assert.NotEqual(t, "tool", m.Role,
			"孤立的 tool 消息应被移除，实际仍存在: %+v", m)
	}
}

// TestClientError_ValidToolResultPreserved 对照组：有对应 tool_use 的
// tool 消息**必须**保留（防止"修复"变成"删光"）。
func TestClientError_ValidToolResultPreserved(t *testing.T) {
	req := &InternalRequest{
		Model: "gpt-4",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "get weather"}}},
			{Role: "assistant", ToolCalls: []ToolCall{
				{ID: "call_ok123", Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "get_weather", Arguments: "{}"}},
			}},
			{Role: "tool", ToolCallID: "call_ok123", Name: "get_weather",
				Content: []ContentBlock{{Type: "text", Text: "sunny"}}},
		},
	}

	fixed := ValidateAndFixRequest(req, "client-fix-valid")
	require.NotNil(t, fixed)

	var found bool
	for _, m := range fixed.Messages {
		if m.Role == "tool" && m.ToolCallID == "call_ok123" {
			found = true
		}
	}
	assert.True(t, found, "有对应 tool_use 的 tool 消息必须保留")
}

// ─────────────────────────────────────────────────────────────────────────────
// 形制 D：空 tool_calls / 空 content
// ─────────────────────────────────────────────────────────────────────────────

// TestClientError_EmptyToolCallsRemoved 断 wire：空 tool_calls 不得上线。
func TestClientError_EmptyToolCallsRemoved(t *testing.T) {
	req := &InternalRequest{
		Model: "gpt-4",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
			{Role: "assistant", Content: []ContentBlock{}, ToolCalls: []ToolCall{}},
		},
	}

	data, err := SerializeOpenAI(ValidateAndFixRequest(req, "client-fix-empty"))
	require.NoError(t, err)

	wire := wireOf(t, data)
	messages := wire["messages"].([]any)
	assistant := messages[len(messages)-1].(map[string]any)

	_, hasToolCalls := assistant["tool_calls"]
	assert.False(t, hasToolCalls, "空 tool_calls 不得上线")
}

// ─────────────────────────────────────────────────────────────────────────────
// 形制 E：协议混用字段的保留
// ─────────────────────────────────────────────────────────────────────────────

// TestClientError_ProtocolMixedFieldsPreserved 断 wire：客户端混用协议的
// 专有字段不得被静默丢弃（Extensions 机制承载 → 序列化时还原）。
func TestClientError_ProtocolMixedFieldsPreserved(t *testing.T) {
	t.Run("openai_request_with_anthropic_thinking", func(t *testing.T) {
		input := `{
			"model": "gpt-4",
			"messages": [{"role":"user","content":"hi"}],
			"max_tokens": 100,
			"thinking": {"type":"enabled","budget_tokens":1000}
		}`

		req, err := ParseOpenAI([]byte(input))
		require.NoError(t, err)

		// thinking 要么进 IR 结构化字段，要么进 Extensions —— 不许消失
		if req.Thinking == nil {
			_, has := req.Extensions["thinking"]
			assert.True(t, has, "thinking 既未结构化也未进 Extensions ⇒ 静默丢失")
		}
	})

	t.Run("anthropic_request_with_openai_penalty", func(t *testing.T) {
		input := `{
			"model": "claude-sonnet-4-20250514",
			"max_tokens": 100,
			"messages": [{"role":"user","content":"hi"}],
			"frequency_penalty": 0.5
		}`

		req, err := ParseAnthropic([]byte(input))
		require.NoError(t, err)

		if req.FrequencyPenalty == nil {
			_, has := req.Extensions["frequency_penalty"]
			assert.True(t, has, "frequency_penalty 既未结构化也未进 Extensions ⇒ 静默丢失")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// 形制 F：自定义扩展字段的无损往返
// ─────────────────────────────────────────────────────────────────────────────

// TestClientError_CustomExtensionRoundtrip 断 wire：未知字段往返必须逐字保留。
func TestClientError_CustomExtensionRoundtrip(t *testing.T) {
	input := `{
		"model": "gpt-4",
		"messages": [{"role":"user","content":"hi"}],
		"custom_field_1": "value1",
		"internal_tracking_id": "track_123"
	}`

	req, err := ParseOpenAI([]byte(input))
	require.NoError(t, err)

	data, err := SerializeOpenAI(ValidateAndFixRequest(req, "client-fix-ext"))
	require.NoError(t, err)

	wire := wireOf(t, data)
	assert.Equal(t, "value1", wire["custom_field_1"])
	assert.Equal(t, "track_123", wire["internal_tracking_id"])
}

// ─────────────────────────────────────────────────────────────────────────────
// 形制 G：客户端把数字参数写成字符串
// ─────────────────────────────────────────────────────────────────────────────

// TestClientError_StringNumericParameter 字符串型数值参数的**当前口径**。
//
// 2026-11-11 起：网关会做**无歧义**的容错修正（"0.7" → 0.7）——
// 详见 coerce_numeric.go 与 coerce_numeric_test.go。
// 这里断言两件事：
//   ① 能无损解析的数值字符串 ⇒ **修正后成功**（不再是硬失败）；
//   ② 不能解析的（"abc"）⇒ **仍然失败**（不猜客户端意图）。
func TestClientError_StringNumericParameter(t *testing.T) {
	t.Run("coercible_numeric_string_accepted", func(t *testing.T) {
		req, err := ParseOpenAI([]byte(`{
			"model": "gpt-4",
			"messages": [{"role":"user","content":"hi"}],
			"temperature": "0.7"
		}`))
		require.NoError(t, err, "可无损解析的数值字符串应被容错修正")
		require.NotNil(t, req.Temperature)
		assert.InDelta(t, 0.7, *req.Temperature, 1e-9)
	})

	t.Run("incoercible_string_still_fails", func(t *testing.T) {
		_, err := ParseOpenAI([]byte(`{
			"model": "gpt-4",
			"messages": [{"role":"user","content":"hi"}],
			"temperature": "abc"
		}`))
		require.Error(t, err, "无法解析的字符串必须仍然失败（不许猜客户端意图）")
	})
}
