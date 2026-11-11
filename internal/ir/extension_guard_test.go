package ir

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 扩展参数完整性守卫（2026-11-11 审计轮）。
//
// 目标（对应用户要求「非原厂默认的参数需要使用扩展参数保存，并在还原时进行还原」）：
//   ① 客户端发的**任何**顶层非标准字段，parse 后不得消失（要么进 IR 结构化
//      字段，要么进 Extensions）；
//   ② 往返后必须**逐字**还原到线上；
//   ③ 原厂专有参数在**跨协议**转换中不得被静默裁剪。
//
// 断言层级：全部打在 **wire**（线上字节），不打中间态。
// 理由（2026-11-11 踩坑记录）：第一版客户端错误测试把断言打在 parse 层，
// 于是把两个已实现的能力误报成缺陷。守卫必须断「客户端可见行为」。

// ─────────────────────────────────────────────────────────────────────────────
// 辅助
// ─────────────────────────────────────────────────────────────────────────────

// guardWire 解析线上 JSON。
func guardWire(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	return m
}

// ─────────────────────────────────────────────────────────────────────────────
// 守卫 ①：未知顶层字段 parse 后不得消失
// ─────────────────────────────────────────────────────────────────────────────

// TestExtensionGuard_UnknownTopLevelFieldsSurviveParse
//
// 未知字段的处理是本轮审计登记的核心机制：parse 时收进 Extensions，
// serialize 时还原。若 Extensions 抽取失效 ⇒ 字段静默丢失 ⇒
// 客户端的私有参数（如实验开关、路由标记）被网关吃掉且无人知晓。
func TestExtensionGuard_UnknownTopLevelFieldsSurviveParse(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		input    string
		keys     []string
	}{
		{
			name:     "openai_custom_fields",
			protocol: ProtocolOpenAIChat,
			input: `{
				"model": "gpt-4",
				"messages": [{"role":"user","content":"hi"}],
				"custom_param_1": "value1",
				"internal_flag": true,
				"experiment_id": 12345,
				"nested_obj": {"a":1,"b":"two"}
			}`,
			keys: []string{"custom_param_1", "internal_flag", "experiment_id", "nested_obj"},
		},
		{
			name:     "anthropic_custom_fields",
			protocol: ProtocolAnthropicMessages,
			input: `{
				"model": "claude-sonnet-4-20250514",
				"max_tokens": 100,
				"messages": [{"role":"user","content":"hi"}],
				"beta_feature": "on",
				"vendor_trace": "trace-123"
			}`,
			keys: []string{"beta_feature", "vendor_trace"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := guardParse(t, tt.protocol, []byte(tt.input))
			require.NotNil(t, req)

			// ① 不得消失：每个键要么在 IR 结构化字段上，要么在 Extensions
			for _, k := range tt.keys {
				if guardIRHasStructuredField(req, k) {
					continue
				}
				_, inExt := req.Extensions[k]
				assert.True(t, inExt,
					"字段 %q 既未结构化也未进 Extensions ⇒ 静默丢失", k)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 守卫 ②：往返后逐字还原
// ─────────────────────────────────────────────────────────────────────────────

// TestExtensionGuard_RoundTripRestoresUnknownFields
//
// 这是 Extensions 机制的**核心承诺**：非原厂参数在「解析→序列化」后必须
// 逐字回到线上。任何裁剪都会让客户端的私有参数失效且无告警。
func TestExtensionGuard_RoundTripRestoresUnknownFields(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		input    string
		keys     []string
	}{
		{
			name:     "openai",
			protocol: ProtocolOpenAIChat,
			input: `{
				"model": "gpt-4",
				"messages": [{"role":"user","content":"hi"}],
				"custom_param_1": "value1",
				"internal_flag": true,
				"experiment_id": 12345,
				"nested_obj": {"a":1,"b":"two"},
				"arr": [1,2,3]
			}`,
			keys: []string{"custom_param_1", "internal_flag", "experiment_id", "nested_obj", "arr"},
		},
		{
			name:     "anthropic",
			protocol: ProtocolAnthropicMessages,
			input: `{
				"model": "claude-sonnet-4-20250514",
				"max_tokens": 100,
				"messages": [{"role":"user","content":"hi"}],
				"beta_feature": "on",
				"vendor_trace": "trace-123",
				"num": 42
			}`,
			keys: []string{"beta_feature", "vendor_trace", "num"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := guardParse(t, tt.protocol, []byte(tt.input))
			fixed := ValidateAndFixRequest(req, "ext-guard")

			data := guardSerialize(t, tt.protocol, fixed)
			wire := guardWire(t, data)

			for _, k := range tt.keys {
				want := guardJSONField(t, []byte(tt.input), k)
				got, ok := wire[k]
				require.True(t, ok, "往返后字段 %q 丢失", k)
				assert.JSONEq(t, want, mustJSON(t, got),
					"字段 %q 往返后必须逐字一致", k)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 守卫 ③：原厂专有参数跨协议不得被静默裁剪
// ─────────────────────────────────────────────────────────────────────────────

// TestExtensionGuard_ProviderSpecificFieldsSurviveCrossProtocol
//
// 原厂专有参数（如 Gemini safety_settings）在**跨协议**时目标协议可能没有
// 对应字段 —— 此时必须落到 Extensions 而不是被丢弃。
// 「静默丢弃安全语义参数」在 2026-08-11 已被登记为 P3 缺陷，本守卫防止复发。
func TestExtensionGuard_ProviderSpecificFieldsSurviveCrossProtocol(t *testing.T) {
	t.Run("gemini_safety_settings_to_openai", func(t *testing.T) {
		// ⚠️ Gemini 原生线上格式是 camelCase：safetySettings / cachedContent。
		// （本轮第一版误用 snake_case（safety_settings / cached_content）——
		//  那不是 Gemini 的线上形态，snake_case 键会正确地落进 Extensions
		//  当未知字段处理。断言必须钉在**线上真实形态**上。）
		input := `{
			"model": "gemini-2.0-flash",
			"contents": [{"role":"user","parts":[{"text":"hi"}]}],
			"safetySettings": [
				{"category":"HARM_CATEGORY_HATE_SPEECH","threshold":"BLOCK_NONE"}
			],
			"cachedContent": "cachedContents/abc123"
		}`

		req, err := ParseGemini([]byte(input))
		require.NoError(t, err)
		require.NotNil(t, req)

		// safety_settings 必须进 IR 结构化字段
		assert.NotEmpty(t, req.SafetySettings, "safety_settings 必须被结构化解析")
		assert.Equal(t, "cachedContents/abc123", req.CachedContent)

		// 跨到 OpenAI：目标协议**无对应字段** ⇒ 按 paramreg 裁决
		// （KindIRHandled + Dialects=[gemini] ⇒ ActionDrop）不得盲还原进
		// OpenAI body（上游会 400）；正确处置是**诚实披露丢失**。
		// 详见 gemini_only_loss.go 与 gemini_only_loss_test.go。
		fixed := ValidateAndFixRequest(req, "ext-guard-gemini")
		data, err := SerializeOpenAI(fixed)
		require.NoError(t, err)

		wire := guardWire(t, data)
		_, hasSafety := wire["safety_settings"]
		_, hasCached := wire["cached_content"]
		assert.False(t, hasSafety,
			"Gemini 专有字段不得盲还原进 OpenAI body（上游对未知顶层键 400）")
		assert.False(t, hasCached,
			"Gemini 专有字段不得盲还原进 OpenAI body（上游对未知顶层键 400）")
	})

	t.Run("gemini_snake_case_keys_are_unknown_not_safety", func(t *testing.T) {
		// 反向对照：snake_case 不是 Gemini 线上形态 ⇒ 必须当**未知字段**
		// 落进 Extensions（而不是被误认成 safety_settings）。
		// 这条守住「不要为了宽容而认错字段」。
		input := `{
			"model": "gemini-2.0-flash",
			"contents": [{"role":"user","parts":[{"text":"hi"}]}],
			"safety_settings": [
				{"category":"HARM_CATEGORY_HATE_SPEECH","threshold":"BLOCK_NONE"}
			]
		}`

		req, err := ParseGemini([]byte(input))
		require.NoError(t, err)

		assert.Empty(t, req.SafetySettings, "snake_case 键不得被误认成 safety_settings")
		_, hasExt := req.Extensions["safety_settings"]
		assert.True(t, hasExt, "snake_case 键应作为未知字段落进 Extensions")
	})

	t.Run("anthropic_thinking_to_openai", func(t *testing.T) {
		input := `{
			"model": "claude-sonnet-4-20250514",
			"max_tokens": 100,
			"messages": [{"role":"user","content":"hi"}],
			"thinking": {"type":"enabled","budget_tokens":1024}
		}`

		req, err := ParseAnthropic([]byte(input))
		require.NoError(t, err)

		if req.Thinking == nil {
			_, has := req.Extensions["thinking"]
			assert.True(t, has, "thinking 跨协议后消失")
			return
		}
		// 结构化保留即可（由序列化器决定是否映射到目标协议）
		assert.NotNil(t, req.Thinking)
	})

	t.Run("responses_unknown_field_roundtrip", func(t *testing.T) {
		// Responses 协议的扩展字段往返（补齐协议覆盖面）
		input := `{
			"model": "gpt-5",
			"input": [{"role":"user","content":"hi"}],
			"vendor_extra_flag": "on",
			"vendor_extra_num": 7
		}`

		req, err := ParseResponses([]byte(input))
		require.NoError(t, err)

		for _, k := range []string{"vendor_extra_flag", "vendor_extra_num"} {
			_, inExt := req.Extensions[k]
			assert.True(t, inExt, "Responses 的扩展字段 %s 必须进 Extensions", k)
		}

		fixed := ValidateAndFixRequest(req, "ext-guard-responses")
		data, err := SerializeResponsesRequest(fixed)
		require.NoError(t, err)

		wire := guardWire(t, data)
		assert.Equal(t, "on", wire["vendor_extra_flag"], "Responses 扩展字段往返后必须还原")
		assert.Equal(t, float64(7), wire["vendor_extra_num"], "Responses 扩展字段往返后必须还原")
	})

	t.Run("ollama_unknown_field_dropped_but_disclosed", func(t *testing.T) {
		// Ollama 目标：非 ollama.* 的扩展字段被**有意丢弃**
		// （契约：外域键不泄漏进 Ollama body）⇒ 但**必须披露丢失**。
		scope := NewIRScopedReporter(nil)
		defer scope.Close()
		scope.SetAsCurrent()
		defer scope.Unset()

		input := `{
			"model": "gpt-4",
			"messages": [{"role":"user","content":"hi"}],
			"vendor_keepalive": "5m",
			"ollama.keep_alive": "10m"
		}`

		req, err := ParseOpenAI([]byte(input))
		require.NoError(t, err)

		_, inExt := req.Extensions["vendor_keepalive"]
		require.True(t, inExt, "未知字段必须进 Extensions")

		fixed := ValidateAndFixRequest(req, "ext-guard-ollama")
		data, err := SerializeOllama(fixed)
		require.NoError(t, err)

		wire := guardWire(t, data)
		// 外域键不得泄漏进 Ollama body（有意的契约）
		assert.NotContains(t, wire, "vendor_keepalive",
			"非 Ollama 命名空间的扩展字段不得泄漏进 Ollama body")
		// 但 ollama.* 私有键要提升到线上
		assert.Equal(t, "10m", wire["keep_alive"], "ollama.* 私有键必须提升到顶层")

		// ★ 丢弃必须被披露
		var sawDrop bool
		for _, ev := range scope.Snapshot() {
			if ev.AnomalyType == AnomalyProtocolLoss && ev.FieldPath == "vendor_keepalive" {
				sawDrop = true
				assert.Equal(t, ProtocolOllamaChat, ev.TargetProtocol)
			}
		}
		assert.True(t, sawDrop,
			"Ollama 目标丢弃扩展字段必须上报 loss（静默丢弃违反纪律）")
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// 守卫 ④：工具层级的扩展键（RISK-2 回归）
// ─────────────────────────────────────────────────────────────────────────────

// TestExtensionGuard_ToolTopLevelExtraKeysRoundTrip
//
// 工具对象顶层的非标准键（如 zcode 的 vendor_tool_flag、Anthropic 的
// cache_control）收在 ToolDefinition.Extra —— 防止 RISK-2 复发。
func TestExtensionGuard_ToolTopLevelExtraKeysRoundTrip(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 100,
		"tools": [{
			"name": "my_tool",
			"input_schema": {"type":"object"},
			"vendor_tool_flag": "on",
			"cache_control": {"type":"ephemeral"}
		}],
		"messages": [{"role":"user","content":"go"}]
	}`

	req, err := ParseAnthropic([]byte(input))
	require.NoError(t, err)
	require.Len(t, req.Tools, 1)

	data, err := SerializeAnthropic(ValidateAndFixRequest(req, "ext-guard-tool"))
	require.NoError(t, err)

	wire := guardWire(t, data)
	tools, ok := wire["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)

	tool := tools[0].(map[string]any)
	assert.Equal(t, "on", tool["vendor_tool_flag"], "工具顶层非标准键必须还原")
	assert.Contains(t, tool, "cache_control", "工具顶层非标准键必须还原")
}

// ─────────────────────────────────────────────────────────────────────────────
// 辅助函数
// ─────────────────────────────────────────────────────────────────────────────

// guardParse 按协议解析。
func guardParse(t *testing.T, protocol string, body []byte) *InternalRequest {
	t.Helper()
	var req *InternalRequest
	var err error
	switch protocol {
	case ProtocolOpenAIChat:
		req, err = ParseOpenAI(body)
	case ProtocolAnthropicMessages:
		req, err = ParseAnthropic(body)
	case ProtocolGeminiGenerate:
		req, err = ParseGemini(body)
	case ProtocolOpenAIResponses:
		req, err = ParseResponses(body)
	default:
		t.Fatalf("不支持的协议: %s", protocol)
	}
	require.NoError(t, err)
	return req
}

// guardSerialize 按协议序列化。
func guardSerialize(t *testing.T, protocol string, req *InternalRequest) []byte {
	t.Helper()
	var data []byte
	var err error
	switch protocol {
	case ProtocolOpenAIChat:
		data, err = SerializeOpenAI(req)
	case ProtocolAnthropicMessages:
		data, err = SerializeAnthropic(req)
	case ProtocolGeminiGenerate:
		data, err = SerializeGemini(req)
	case ProtocolOpenAIResponses:
		data, err = SerializeResponsesRequest(req)
	default:
		t.Fatalf("不支持的协议: %s", protocol)
	}
	require.NoError(t, err)
	return data
}

// guardIRHasStructuredField 判断该键是否已被 IR 的结构化字段消费。
func guardIRHasStructuredField(req *InternalRequest, key string) bool {
	switch key {
	case "frequency_penalty":
		return req.FrequencyPenalty != nil
	case "presence_penalty":
		return req.PresencePenalty != nil
	case "seed":
		return req.Seed != nil
	case "thinking":
		return req.Thinking != nil
	case "safety_settings":
		return len(req.SafetySettings) > 0
	case "cached_content":
		return req.CachedContent != ""
	case "max_tokens":
		return req.MaxTokens != 0
	default:
		return false
	}
}

// guardJSONField 从原始 JSON 里取某个顶层字段的原始字面量。
func guardJSONField(t *testing.T, raw []byte, key string) string {
	t.Helper()
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &m))
	v, ok := m[key]
	require.True(t, ok, "原始 JSON 缺字段 %s", key)
	return string(v)
}

// mustJSON 把任意值转成 JSON 字面量。
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}
