package ir

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 工具调用的规范视图守卫（2026-11-11 协议转换审计轮）。
//
// ── 背景 ──────────────────────────────────────────────────────────────
// IR 里同一个工具调用有**两种表示**：内容块 `tool_use`（Anthropic/Gemini 约定）
// 与消息级 `Message.ToolCalls`（OpenAI/Responses 约定）。生产方认的不一致
// （Anthropic parser 两者都写；OpenAI parser 只写消息级；Gemini parser 只写
// 内容块），消费方认的也不一致 ⇒ 长出下面 5 条**生产路径**缺陷。
//
// 本文件是它们的**具名钉测**：每条都打在 **wire**（线上字节），
// 因为「客户端/上游看得见的东西」才是判据。
//
// 全部缺陷均由 `internal/ir/comprehensive_roundtrip_test.go` 的全矩阵往返测试
// 实测撞出（不是推断，也不是读代码猜的）。

// ─────────────────────────────────────────────────────────────────────────────
// 辅助
// ─────────────────────────────────────────────────────────────────────────────

// pinWire 解析线上 JSON。
func pinWire(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	return m
}

// pinGeminiFunctionCalls 取出 Gemini body 里全部 functionCall 的 (name, args)。
func pinGeminiFunctionCalls(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	wire := pinWire(t, body)
	contents, ok := wire["contents"].([]any)
	require.True(t, ok, "Gemini body 必须有 contents")
	var out []map[string]any
	for _, c := range contents {
		cm, ok := c.(map[string]any)
		require.True(t, ok)
		parts, ok := cm["parts"].([]any)
		require.True(t, ok)
		for _, p := range parts {
			pm, ok := p.(map[string]any)
			require.True(t, ok)
			if fc, ok := pm["functionCall"].(map[string]any); ok {
				out = append(out, fc)
			}
		}
	}
	return out
}

// pinGeminiFunctionResponses 取出 Gemini body 里全部 functionResponse 的 name。
func pinGeminiFunctionResponses(t *testing.T, body []byte) []string {
	t.Helper()
	wire := pinWire(t, body)
	contents, ok := wire["contents"].([]any)
	require.True(t, ok, "Gemini body 必须有 contents")
	var out []string
	for _, c := range contents {
		cm, ok := c.(map[string]any)
		require.True(t, ok)
		parts, ok := cm["parts"].([]any)
		require.True(t, ok)
		for _, p := range parts {
			pm, ok := p.(map[string]any)
			require.True(t, ok)
			if fr, ok := pm["functionResponse"].(map[string]any); ok {
				name, _ := fr["name"].(string)
				out = append(out, name)
			}
		}
	}
	return out
}

// pinAnthropicToolUseIDs 取出 Anthropic body 里全部 tool_use 的 id。
func pinAnthropicToolUseIDs(t *testing.T, body []byte) []string {
	t.Helper()
	wire := pinWire(t, body)
	msgs, ok := wire["messages"].([]any)
	require.True(t, ok, "Anthropic body 必须有 messages")
	var out []string
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		require.True(t, ok)
		content, ok := mm["content"].([]any)
		if !ok {
			continue
		}
		for _, b := range content {
			bm, ok := b.(map[string]any)
			if !ok {
				continue
			}
			if bm["type"] == "tool_use" {
				id, _ := bm["id"].(string)
				out = append(out, id)
			}
		}
	}
	return out
}

// pinOpenAIToolCalls 取出 OpenAI body 里所有消息上的 tool_calls。
func pinOpenAIToolCalls(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	wire := pinWire(t, body)
	msgs, ok := wire["messages"].([]any)
	require.True(t, ok, "OpenAI body 必须有 messages")
	var out []map[string]any
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		require.True(t, ok)
		calls, ok := mm["tool_calls"].([]any)
		if !ok {
			continue
		}
		for _, c := range calls {
			cm, ok := c.(map[string]any)
			require.True(t, ok)
			out = append(out, cm)
		}
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// 钉测 ①（D1）：同一个工具调用**只发一次**
// ─────────────────────────────────────────────────────────────────────────────

// TestToolCallCanonical_GeminiNoDuplicateFunctionCall 钉住 D1。
//
// 缺陷：Anthropic parser 把 tool_use **同时**写进内容块与 Message.ToolCalls
// （parse_anthropic.go:276 + :291）⇒ 而 Gemini 序列化器**两处都发**
// ⇒ Anthropic 客户端到 Gemini 上游时，同一个 functionCall 被发**两次**
// ⇒ 上游模型会看到重复的工具调用。
func TestToolCallCanonical_GeminiNoDuplicateFunctionCall(t *testing.T) {
	req, err := ParseAnthropic([]byte(rtAnthropicBody))
	require.NoError(t, err)
	req = ValidateAndFixRequest(req, "pin-d1")

	body, err := SerializeGemini(req)
	require.NoError(t, err)

	calls := pinGeminiFunctionCalls(t, body)
	require.Len(t, calls, 1,
		"同一个工具调用必须只发一个 functionCall（实际 %d 个）", len(calls))
	assert.Equal(t, "lookup", calls[0]["name"])
}

// TestToolCallCanonical_OpenAINoDuplicateToolCalls 钉住 D1 在 OpenAI 侧的同款。
//
// Anthropic 源在 OpenAI 目标上：内容块 tool_use 与镜像的 Message.ToolCalls
// 指向同一个调用 ⇒ 规范视图必须去重 ⇒ tool_calls 只能有一个。
func TestToolCallCanonical_OpenAINoDuplicateToolCalls(t *testing.T) {
	req, err := ParseAnthropic([]byte(rtAnthropicBody))
	require.NoError(t, err)
	req = ValidateAndFixRequest(req, "pin-d1-openai")

	body, err := SerializeOpenAI(req)
	require.NoError(t, err)

	calls := pinOpenAIToolCalls(t, body)
	require.Len(t, calls, 1,
		"同一个工具调用必须只发一个 tool_calls（实际 %d 个）", len(calls))
}

// ─────────────────────────────────────────────────────────────────────────────
// 钉测 ②（D5）：内容块形态的工具调用**不得在转换中消失**
// ─────────────────────────────────────────────────────────────────────────────

// TestToolCallCanonical_GeminiToolCallSurvivesToOpenAI 钉住 D5（最重的一条）。
//
// 缺陷：Gemini parser 把 functionCall 存成**内容块 tool_use**（不写
// Message.ToolCalls）⇒ 而 serialize_openai **只读** msg.ToolCalls
// ⇒ Gemini 客户端的函数调用到 OpenAI 上游时**整个消失**
// ⇒ 模型根本不知道该调哪个工具。
func TestToolCallCanonical_GeminiToolCallSurvivesToOpenAI(t *testing.T) {
	req, err := ParseGemini([]byte(rtGeminiBody))
	require.NoError(t, err)
	req = ValidateAndFixRequest(req, "pin-d5-openai")

	body, err := SerializeOpenAI(req)
	// ★ 失败信息要指向**机制**：★ 回退修复（退回只读消息级 ToolCalls）时，
	//   Gemini 源里的函数调用在 IR 里只是内容块 ⇒ 序列化器看不到 assistant
	//   的 tool_calls ⇒ **紧随其后的工具结果被判成孤儿** ⇒ 整条请求被拒
	//   ⇒ ★★ 失败点在 require.NoError，而不是下面的线上断言 ★★
	//   ⇒ ★★ 所以这里必须写清「不是孤儿」，否则定位会指向错误的地方 ★★。
	require.NoError(t, err,
		"Gemini 客户端的函数调用在 OpenAI 上游上不得被当成孤儿工具结果而拒绝整条请求")

	calls := pinOpenAIToolCalls(t, body)
	require.Len(t, calls, 1,
		"Gemini 客户端的函数调用在 OpenAI 上游上不得消失（实际 %d 个）", len(calls))
	fn, ok := calls[0]["function"].(map[string]any)
	require.True(t, ok, "tool_calls 必须带 function")
	assert.Equal(t, "lookup", fn["name"])
}

// TestToolCallCanonical_GeminiToolCallSurvivesToResponses 钉住 D5 的 Responses 同款。
func TestToolCallCanonical_GeminiToolCallSurvivesToResponses(t *testing.T) {
	req, err := ParseGemini([]byte(rtGeminiBody))
	require.NoError(t, err)
	req = ValidateAndFixRequest(req, "pin-d5-responses")

	body, err := SerializeResponsesRequest(req)
	require.NoError(t, err)

	wire := pinWire(t, body)
	input, ok := wire["input"].([]any)
	require.True(t, ok, "Responses body 必须有 input")
	found := 0
	for _, item := range input {
		im, ok := item.(map[string]any)
		require.True(t, ok)
		if im["type"] == "function_call" {
			found++
			assert.Equal(t, "lookup", im["name"])
		}
	}
	assert.Equal(t, 1, found,
		"Gemini 客户端的函数调用在 Responses 上游上不得消失（实际 %d 个）", found)
}

// TestToolCallCanonical_GeminiToolCallSurvivesToAnthropic 钉住 D5 的 Anthropic 同款。
func TestToolCallCanonical_GeminiToolCallSurvivesToAnthropic(t *testing.T) {
	req, err := ParseGemini([]byte(rtGeminiBody))
	require.NoError(t, err)
	req = ValidateAndFixRequest(req, "pin-d5-anthropic")

	body, err := SerializeAnthropic(req)
	require.NoError(t, err)

	ids := pinAnthropicToolUseIDs(t, body)
	require.Len(t, ids, 1,
		"Gemini 客户端的函数调用在 Anthropic 上游上不得消失（实际 %d 个）", len(ids))
}

// ─────────────────────────────────────────────────────────────────────────────
// 钉测 ③（D2）：functionResponse 的 name 必须是**函数名**
// ─────────────────────────────────────────────────────────────────────────────

// TestToolCallCanonical_GeminiFunctionResponseNameMatchesCall 钉住 D2。
//
// 缺陷：Gemini 协议**没有工具调用 ID**，functionResponse 只能靠**函数名**与
// functionCall 配对。而旧实现用 `toolUseNameFromID(ToolUseID)` 猜名字，
// 它只认 `gemini_call_` 前缀 ⇒ Anthropic 的 `toolu_xxx` / OpenAI 的
// `call_xxx` 原样返回 ⇒ functionResponse.name ≠ functionCall.name
// ⇒ 工具结果成**孤儿**（上游配不上这次调用）。
func TestToolCallCanonical_GeminiFunctionResponseNameMatchesCall(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		body     string
	}{
		{"anthropic", ProtocolAnthropicMessages, rtAnthropicBody},
		{"openai", ProtocolOpenAIChat, rtOpenAIBody},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := rtParse(t, tt.protocol, []byte(tt.body))
			req = ValidateAndFixRequest(req, "pin-d2-"+tt.name)

			body, err := SerializeGemini(req)
			require.NoError(t, err)

			calls := pinGeminiFunctionCalls(t, body)
			require.Len(t, calls, 1)
			callName, _ := calls[0]["name"].(string)

			responses := pinGeminiFunctionResponses(t, body)
			require.Len(t, responses, 1,
				"工具结果必须被发成一个 functionResponse")
			assert.Equal(t, callName, responses[0],
				"functionResponse.name 必须等于 functionCall.name（否则结果成孤儿）")
			assert.NotEqual(t, "toolu_rt_1", responses[0],
				"functionResponse.name 不得是工具调用 ID")
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 钉测 ④（D4）：工具结果载荷**只发一次**
// ─────────────────────────────────────────────────────────────────────────────

// TestToolCallCanonical_GeminiToolResultNotDuplicated 钉住 D4。
//
// 缺陷：Chat Completions 的工具消息把结果放在消息内容里；Gemini 序列化器
// 一边把它发成 functionResponse（response.result），一边又把它当普通文本
// part 再发一遍 ⇒ **同一个载荷出现两次**。
func TestToolCallCanonical_GeminiToolResultNotDuplicated(t *testing.T) {
	req, err := ParseOpenAI([]byte(rtOpenAIBody))
	require.NoError(t, err)
	req = ValidateAndFixRequest(req, "pin-d4")

	body, err := SerializeGemini(req)
	require.NoError(t, err)

	wire := pinWire(t, body)
	contents, ok := wire["contents"].([]any)
	require.True(t, ok)

	// 结果文本 "42" 在**整个 body**里只能出现一次（functionResponse 内部那次
	// 算载荷本身，不算独立 part）⇒ 数独立的 text part。
	textParts := 0
	for _, c := range contents {
		cm := c.(map[string]any)
		for _, p := range cm["parts"].([]any) {
			pm := p.(map[string]any)
			if t, ok := pm["text"].(string); ok && t == "42" {
				textParts++
			}
		}
	}
	assert.Equal(t, 0, textParts,
		"工具结果已被 functionResponse 承载 ⇒ 不得再作为独立 text part 发一遍")
	assert.Len(t, pinGeminiFunctionResponses(t, body), 1)
}

// ─────────────────────────────────────────────────────────────────────────────
// 钉测 ⑤（D3）：Gemini 客户端的工具结果**不得被静默删除**
// ─────────────────────────────────────────────────────────────────────────────

// TestToolCallCanonical_GeminiClientToolResultSurvivesFix 钉住 D3。
//
// 缺陷（三处同源，缺一不可）：
//   ① validate_and_fix.go 的 removeEmptyMessages：工具消息没有消息级
//      tool_call_id 就当「空消息」删除（Gemini 形态的关联键在内容块里）
//   ② tool_result_messages.go 的 expandToolResultMessages：只展开 role=user
//   ③ sanitize_tool_messages.go 的活跃调用 ID 集：只收消息级 ToolCalls
// ⇒ 结果：Gemini 客户端发来的 functionResponse 在生产链路
// （executor_chat.go:3046/3138/3206 + inline_validation.go:52 会调
// ValidateAndFixRequest）上**凭空消失** ⇒ 上游只收到悬空的工具调用。
func TestToolCallCanonical_GeminiClientToolResultSurvivesFix(t *testing.T) {
	body := []byte(`{"contents":[
      {"role":"user","parts":[{"text":"q"}]},
      {"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"q":"x"}}}]},
      {"role":"function","parts":[{"functionResponse":{"name":"lookup","response":{"result":"42"}}}]}
    ]}`)

	req, err := ParseGemini(body)
	require.NoError(t, err)
	fixed := ValidateAndFixRequest(req, "pin-d3")

	// 工具结果必须还在（IR 层）
	found := false
	for _, m := range fixed.Messages {
		for _, b := range m.Content {
			if b.Type == "tool_result" && b.ToolResult != nil {
				found = true
			}
		}
	}
	assert.True(t, found, "Gemini 客户端的工具结果不得被 ValidateAndFixRequest 删除")

	// 且必须真的回到线上（wire 层）
	out, err := SerializeGemini(fixed)
	require.NoError(t, err)
	responses := pinGeminiFunctionResponses(t, out)
	require.Len(t, responses, 1,
		"Gemini 客户端的工具结果必须回到线上（实际 %d 个 functionResponse）", len(responses))
	assert.Equal(t, "lookup", responses[0],
		"functionResponse.name 必须是函数名（Gemini 按名配对）")
}
