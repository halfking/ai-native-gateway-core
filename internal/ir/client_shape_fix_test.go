package ir

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 客户端错误格式修正器钉测（2026-11-11 审计第二轮 · 批 2）。
//
// 覆盖 client_shape_fix.go 的五族修正：
//   ① role 归一化（缺失/空/非字符串 → tool|user 推断 + anomaly）
//   ② tool_calls[].arguments（空串→"{}" 修正；非法 JSON 披露保留）
//   ③ 非对象元素跳过披露（tool_calls / content 块）
//   ④ 复合字段形状错披露（modalities/stop 等）
//   ⑤ Gemini 四类畸形 part 披露
//
// 断言纪律：修正打在 IR 结构上、披露打在 anomaly 事件上（reason=
// client_shape_fix）。

func shapeFixKinds(evs []AnomalyEvent) map[string]int {
	out := map[string]int{}
	for _, ev := range evs {
		if fix, ok := ev.Metadata["client_shape_fix"].(string); ok {
			out[fix]++
		}
	}
	return out
}

// ── ① role 归一化 ────────────────────────────────────────────────────────────

func TestClientShape_RoleNormalization(t *testing.T) {
	t.Run("openai_missing_role_defaults", func(t *testing.T) {
		body := []byte(`{"model":"gpt-x","messages":[
			{"role":"user","content":"hi"},
			{"tool_call_id":"call_1","content":"result"},
			{"content":"no role at all"}]}`)
		var evs []AnomalyEvent
		var req *InternalRequest
		var err error
		prev := SetAnomalyReporter(func(ev AnomalyEvent) {
			if strings.HasPrefix(ev.Reason, "client_shape_fix") {
				evs = append(evs, ev)
			}
		})
		req, err = ParseOpenAI(body)
		SetAnomalyReporter(prev)
		require.NoError(t, err)
		require.Len(t, req.Messages, 3)
		// 带 tool_call_id 的消息 → tool
		assert.Equal(t, "tool", req.Messages[1].Role)
		// 无任何特征的消息 → user
		assert.Equal(t, "user", req.Messages[2].Role)
		// 两条推断都留痕
		kinds := shapeFixKinds(evs)
		assert.Equal(t, 1, kinds["inferred_tool_from_tool_call_id"])
		assert.Equal(t, 1, kinds["inferred_user"])
	})

	t.Run("anthropic_role_inferred_from_tool_result_blocks", func(t *testing.T) {
		body := []byte(`{"model":"claude-x","max_tokens":100,"messages":[
			{"role":"user","content":"hi"},
			{"content":[{"type":"tool_result","tool_use_id":"tu_1","content":"ok"}]}]}`)
		var evs []AnomalyEvent
		prev := SetAnomalyReporter(func(ev AnomalyEvent) {
			if strings.HasPrefix(ev.Reason, "client_shape_fix") {
				evs = append(evs, ev)
			}
		})
		req, err := ParseAnthropic(body)
		SetAnomalyReporter(prev)
		require.NoError(t, err)
		require.Len(t, req.Messages, 2)
		assert.Equal(t, "tool", req.Messages[1].Role, "含 tool_result 块的无 role 消息应推断为 tool")
		assert.Equal(t, 1, shapeFixKinds(evs)["inferred_tool_from_blocks"])
	})

	t.Run("valid_roles_untouched", func(t *testing.T) {
		body := []byte(`{"model":"gpt-x","messages":[
			{"role":"system","content":"s"},{"role":"user","content":"u"},
			{"role":"assistant","content":"a"}]}`)
		var evs []AnomalyEvent
		prev := SetAnomalyReporter(func(ev AnomalyEvent) {
			if strings.HasPrefix(ev.Reason, "client_shape_fix") {
				evs = append(evs, ev)
			}
		})
		req, err := ParseOpenAI(body)
		SetAnomalyReporter(prev)
		require.NoError(t, err)
		assert.Empty(t, evs, "合法 role 不得产生任何事件")
		// 注：system 消息被规范化提升进 req.System（types.go 既有约定），
		// 不在 Messages 里。
		require.NotNil(t, req.System)
		assert.Equal(t, "s", req.System.Content)
		assert.Equal(t, []string{"user", "assistant"},
			[]string{req.Messages[0].Role, req.Messages[1].Role})
	})
}

// ── ② tool_calls[].arguments ────────────────────────────────────────────────

func TestClientShape_ToolCallArguments(t *testing.T) {
	t.Run("empty_arguments_normalized_to_object", func(t *testing.T) {
		body := []byte(`{"model":"gpt-x","messages":[{"role":"assistant","tool_calls":[
			{"id":"call_1","type":"function","function":{"name":"ping","arguments":""}}]}]}`)
		var evs []AnomalyEvent
		prev := SetAnomalyReporter(func(ev AnomalyEvent) {
			if strings.HasPrefix(ev.Reason, "client_shape_fix") {
				evs = append(evs, ev)
			}
		})
		req, err := ParseOpenAI(body)
		SetAnomalyReporter(prev)
		require.NoError(t, err)
		require.Len(t, req.Messages[0].ToolCalls, 1)
		assert.Equal(t, "{}", req.Messages[0].ToolCalls[0].Function.Arguments,
			"空参数串应修正为 {}（上游普遍要求合法 JSON）")
		assert.Equal(t, 1, shapeFixKinds(evs)["empty_arguments_normalized"])
	})

	t.Run("invalid_json_preserved_and_disclosed", func(t *testing.T) {
		body := []byte(`{"model":"gpt-x","messages":[{"role":"assistant","tool_calls":[
			{"id":"call_1","type":"function","function":{"name":"f","arguments":"{broken"}}]}]}`)
		var evs []AnomalyEvent
		prev := SetAnomalyReporter(func(ev AnomalyEvent) {
			if strings.HasPrefix(ev.Reason, "client_shape_fix") {
				evs = append(evs, ev)
			}
		})
		req, err := ParseOpenAI(body)
		SetAnomalyReporter(prev)
		require.NoError(t, err)
		assert.Equal(t, "{broken", req.Messages[0].ToolCalls[0].Function.Arguments,
			"非法 JSON 不猜测修复，原样保留")
		assert.Equal(t, 1, shapeFixKinds(evs)["invalid_json_disclosed"])
	})

	t.Run("valid_arguments_untouched", func(t *testing.T) {
		body := []byte(`{"model":"gpt-x","messages":[{"role":"assistant","tool_calls":[
			{"id":"call_1","type":"function","function":{"name":"f","arguments":"{\"x\":1}"}}]}]}`)
		var evs []AnomalyEvent
		prev := SetAnomalyReporter(func(ev AnomalyEvent) {
			if strings.HasPrefix(ev.Reason, "client_shape_fix") {
				evs = append(evs, ev)
			}
		})
		req, err := ParseOpenAI(body)
		SetAnomalyReporter(prev)
		require.NoError(t, err)
		assert.Equal(t, `{"x":1}`, req.Messages[0].ToolCalls[0].Function.Arguments)
		assert.Empty(t, evs)
	})

	t.Run("responses_function_call_empty_args", func(t *testing.T) {
		body := []byte(`{"model":"gpt-x","input":[
			{"type":"function_call","name":"ping","arguments":""}]}`)
		var evs []AnomalyEvent
		prev := SetAnomalyReporter(func(ev AnomalyEvent) {
			if strings.HasPrefix(ev.Reason, "client_shape_fix") {
				evs = append(evs, ev)
			}
		})
		req, err := ParseResponses(body)
		SetAnomalyReporter(prev)
		require.NoError(t, err)
		require.Len(t, req.Messages, 1)
		require.Len(t, req.Messages[0].ToolCalls, 1)
		assert.Equal(t, "{}", req.Messages[0].ToolCalls[0].Function.Arguments)
		assert.Equal(t, 1, shapeFixKinds(evs)["empty_arguments_normalized"])
	})
}

// ── ③ 非对象元素跳过披露 ─────────────────────────────────────────────────────

func TestClientShape_NonObjectElementDisclosed(t *testing.T) {
	t.Run("tool_calls_array_string_element", func(t *testing.T) {
		body := []byte(`{"model":"gpt-x","messages":[{"role":"assistant","tool_calls":[
			"not-an-object",
			{"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`)
		var evs []AnomalyEvent
		prev := SetAnomalyReporter(func(ev AnomalyEvent) {
			if strings.HasPrefix(ev.Reason, "client_shape_fix") {
				evs = append(evs, ev)
			}
		})
		req, err := ParseOpenAI(body)
		SetAnomalyReporter(prev)
		require.NoError(t, err)
		require.Len(t, req.Messages[0].ToolCalls, 1, "合法 tool_call 仍保留")
		kinds := shapeFixKinds(evs)
		assert.Equal(t, 1, kinds["non_object_element_skipped"], "跳过必须披露")
	})

	t.Run("content_blocks_null_element", func(t *testing.T) {
		body := []byte(`{"model":"gpt-x","messages":[{"role":"user","content":[
			null,
			{"type":"text","text":"hi"}]}]}`)
		var evs []AnomalyEvent
		prev := SetAnomalyReporter(func(ev AnomalyEvent) {
			if strings.HasPrefix(ev.Reason, "client_shape_fix") {
				evs = append(evs, ev)
			}
		})
		req, err := ParseOpenAI(body)
		SetAnomalyReporter(prev)
		require.NoError(t, err)
		require.Len(t, req.Messages[0].Content, 1)
		assert.Equal(t, 1, shapeFixKinds(evs)["non_object_element_skipped"])
	})

	t.Run("anthropic_content_blocks_string_element", func(t *testing.T) {
		body := []byte(`{"model":"claude-x","max_tokens":10,"messages":[
			{"role":"user","content":["bare string block",{"type":"text","text":"hi"}]}]}`)
		var evs []AnomalyEvent
		prev := SetAnomalyReporter(func(ev AnomalyEvent) {
			if strings.HasPrefix(ev.Reason, "client_shape_fix") {
				evs = append(evs, ev)
			}
		})
		_, err := ParseAnthropic(body)
		SetAnomalyReporter(prev)
		require.NoError(t, err)
		assert.Equal(t, 1, shapeFixKinds(evs)["non_object_element_skipped"])
	})
}

// ── ④ 复合字段形状错披露 ─────────────────────────────────────────────────────

func TestClientShape_CompoundFieldMalformed(t *testing.T) {
	t.Run("modalities_string_not_array", func(t *testing.T) {
		body := []byte(`{"model":"gpt-x","messages":[{"role":"user","content":"hi"}],
			"modalities":"text"}`)
		var evs []AnomalyEvent
		prev := SetAnomalyReporter(func(ev AnomalyEvent) {
			if strings.HasPrefix(ev.Reason, "client_shape_fix") {
				evs = append(evs, ev)
			}
		})
		req, err := ParseOpenAI(body)
		SetAnomalyReporter(prev)
		require.NoError(t, err)
		assert.Nil(t, req.Modalities, "畸形 modalities 丢弃（既有行为）")
		assert.Equal(t, 1, shapeFixKinds(evs)["unmarshal_failed_dropped"], "丢弃必须披露")
	})

	t.Run("stop_object_unparseable", func(t *testing.T) {
		body := []byte(`{"model":"gpt-x","messages":[{"role":"user","content":"hi"}],
			"stop":{"bad":"shape"}}`)
		var evs []AnomalyEvent
		prev := SetAnomalyReporter(func(ev AnomalyEvent) {
			if strings.HasPrefix(ev.Reason, "client_shape_fix") {
				evs = append(evs, ev)
			}
		})
		req, err := ParseOpenAI(body)
		SetAnomalyReporter(prev)
		require.NoError(t, err)
		assert.Empty(t, req.Stop)
		assert.Equal(t, 1, shapeFixKinds(evs)["unparseable_dropped"])
	})

	// 2026-11-12 批判审计 P3 订正：合法空数组 `[]` parse 成功（语义=无停止
	// 序列），旧判据误报 "unparseable_dropped"——披露不得撒谎。
	t.Run("stop_empty_array_not_disclosed", func(t *testing.T) {
		body := []byte(`{"model":"gpt-x","messages":[{"role":"user","content":"hi"}],
			"stop":[]}`)
		var evs []AnomalyEvent
		prev := SetAnomalyReporter(func(ev AnomalyEvent) {
			if strings.HasPrefix(ev.Reason, "client_shape_fix") {
				evs = append(evs, ev)
			}
		})
		req, err := ParseOpenAI(body)
		SetAnomalyReporter(prev)
		require.NoError(t, err)
		assert.Empty(t, req.Stop)
		assert.Equal(t, 0, shapeFixKinds(evs)["unparseable_dropped"], "合法空数组不得披露为 unparseable")
	})

	// 登记不修（2026-11-12 批判审计）：`"stop":[1,2]` 走 []string 部分填充
	// 得 ["",""]（垃圾空串停止序列）——parseStringArray 既有语义，非本次
	// 订正范围；len>0 ⇒ 披露判据不触发 ⇒ 不撒谎但值是垃圾。真修需
	// parseStringArray 对类型不匹配元素 fail-visible（登记为后续候选）。
	t.Run("stop_array_numeric_elements_current_semantics", func(t *testing.T) {
		body := []byte(`{"model":"gpt-x","messages":[{"role":"user","content":"hi"}],
			"stop":[1,2]}`)
		req, err := ParseOpenAI(body)
		require.NoError(t, err)
		assert.Equal(t, []string{"", ""}, req.Stop, "既有部分填充语义（登记不修）")
	})

	t.Run("valid_compound_fields_untouched", func(t *testing.T) {
		body := []byte(`{"model":"gpt-x","messages":[{"role":"user","content":"hi"}],
			"modalities":["text"],"stop":["END"]}`)
		var evs []AnomalyEvent
		prev := SetAnomalyReporter(func(ev AnomalyEvent) {
			if strings.HasPrefix(ev.Reason, "client_shape_fix") {
				evs = append(evs, ev)
			}
		})
		req, err := ParseOpenAI(body)
		SetAnomalyReporter(prev)
		require.NoError(t, err)
		assert.Equal(t, []string{"text"}, req.Modalities)
		assert.Equal(t, []string{"END"}, req.Stop)
		assert.Empty(t, evs)
	})
}

// ── ⑤ Gemini 畸形 part 披露 ─────────────────────────────────────────────────

func TestClientShape_GeminiMalformedParts(t *testing.T) {
	t.Run("inline_data_empty_payload", func(t *testing.T) {
		body := []byte(`{"contents":[{"role":"user","parts":[
			{"inlineData":{"mimeType":"image/png"}},
			{"text":"hi"}]}]}`)
		var evs []AnomalyEvent
		prev := SetAnomalyReporter(func(ev AnomalyEvent) {
			if strings.HasPrefix(ev.Reason, "client_shape_fix") {
				evs = append(evs, ev)
			}
		})
		req, err := ParseGemini(body)
		SetAnomalyReporter(prev)
		require.NoError(t, err)
		require.Len(t, req.Messages, 1)
		// 合法 text part 保留
		found := false
		for _, b := range req.Messages[0].Content {
			if b.Type == "text" {
				found = true
			}
		}
		assert.True(t, found, "合法 part 保留")
		assert.Equal(t, 1, shapeFixKinds(evs)["malformed_part_dropped"], "畸形 inlineData 丢弃必须披露")
	})

	t.Run("function_call_missing_name", func(t *testing.T) {
		body := []byte(`{"contents":[{"role":"model","parts":[
			{"functionCall":{"args":{"x":1}}}]}]}`)
		var evs []AnomalyEvent
		prev := SetAnomalyReporter(func(ev AnomalyEvent) {
			if strings.HasPrefix(ev.Reason, "client_shape_fix") {
				evs = append(evs, ev)
			}
		})
		_, err := ParseGemini(body)
		SetAnomalyReporter(prev)
		require.NoError(t, err)
		assert.Equal(t, 1, shapeFixKinds(evs)["malformed_part_dropped"])
	})
}

// ── ⑥ role alternation 默认翻转 ─────────────────────────────────────────────

func TestClientShape_RoleAlternationDefaultMerged(t *testing.T) {
	t.Run("default_merges_consecutive_users", func(t *testing.T) {
		// 2026-11-11 用户裁决：默认自动修复。unset env ⇒ 合并。
		req := &InternalRequest{
			Messages: []Message{
				{Role: "user", Content: []ContentBlock{{Type: "text", Text: "a"}}},
				{Role: "user", Content: []ContentBlock{{Type: "text", Text: "b"}}},
				{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "c"}}},
			},
		}
		fixed := ValidateAndFixRequest(req, "")
		require.Len(t, fixed.Messages, 2, "连续 user 应合并为 1 条")
		assert.Equal(t, "user", fixed.Messages[0].Role)
		var text string
		for _, b := range fixed.Messages[0].Content {
			text += b.Text
		}
		assert.Contains(t, text, "a")
		assert.Contains(t, text, "b")
	})

	t.Run("env_false_restores_warn_only", func(t *testing.T) {
		t.Setenv("LLM_GATEWAY_FIX_ROLE_ALTERNATION", "false")
		req := &InternalRequest{
			Messages: []Message{
				{Role: "user", Content: []ContentBlock{{Type: "text", Text: "a"}}},
				{Role: "user", Content: []ContentBlock{{Type: "text", Text: "b"}}},
			},
		}
		fixed := ValidateAndFixRequest(req, "")
		assert.Len(t, fixed.Messages, 2, "env=false 回退 warn-only（不合并）")
	})

	t.Run("tool_metadata_never_merged", func(t *testing.T) {
		req := &InternalRequest{
			Messages: []Message{
				{Role: "user", Content: []ContentBlock{{Type: "text", Text: "a"}}},
				{Role: "user", Content: []ContentBlock{{Type: "text", Text: "b"}}},
				{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Type: "function", Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "f", Arguments: "{}"}}}},
				{Role: "tool", ToolCallID: "call_1", Content: []ContentBlock{{Type: "tool_result", ToolResult: &ToolResult{ToolUseID: "call_1", Content: []ContentBlock{{Type: "text", Text: "ok"}}}}}},
			},
		}
		fixed := ValidateAndFixRequest(req, "")
		require.Len(t, fixed.Messages, 3, "连续 user 合并成 1 + assistant + tool")
		assert.Equal(t, "assistant", fixed.Messages[1].Role)
		assert.Equal(t, "tool", fixed.Messages[2].Role, "tool 消息永不参与合并")
	})
}
