package ir

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 综合往返矩阵测试（2026-11-11 审计轮 · 测试计划阶段2 第 6 项）。
//
// ── 为什么需要它（既有覆盖的缺口）────────────────────────────────────
// `production_replay_test.go` 已覆盖「真实会话 → parse → validate → serialize
// 到 5 个目标协议」，但**没有**覆盖：
//   ① **回读**（把序列化结果再 parse 回 IR）⇒ 往返的**幂等性**无人验证；
//   ② **语义等价**（IR1 与 IR2 的语义不变量是否保持）；
//   ③ **扩展保留**（往返后非标顶层字段是否逐字还原）。
// ⇒ 本文件补的正是这三项，形态是 **源协议 × 目标协议 全矩阵**。
//
// ── 断言层级 ──────────────────────────────────────────────────────────
// 全部打在 **wire**（线上字节）。理由（2026-11-11 踩坑记录）：打中间态会把
// 「协议归一化」（system → req.System、responses 并行 function_call 合并）
// 误报成缺陷。
//
// ── 三处「协议中立」的坑（本文件第一版踩到并已修正，勿回退）──────────
// ★ ① **工具调用在 IR 里有两份**：`Message.ToolCalls`（OpenAI 约定）与
//    内容块 `tool_use`（Anthropic 约定）。Anthropic parser **两者都写**
//    （parse_anthropic.go:276 + :291）⇒ 只扫其中一处会得出「工具调用不见了」
//    的**假红**。
// ★ ② **Gemini 的工具 ID 是协议合成的**：`gemini_call_<name>_<partIdx>`
//    （parse_gemini.go:278）⇒ Gemini 协议**根本没有 ID** ⇒ 跨 Gemini 的
//    往返**不可能**保持 ID ⇒ ID 等价断言只对**非 Gemini** 目标成立。
// ★ ③ **Gemini 的工具结果存在 `ToolResult.GeminiResponse` 里**（原始 JSON）
//    而不是 Content 文本块 ⇒ 只扫 Content 会得出「工具结果文本丢了」的**假红**。
//
// ── 矩阵范围与「诚实边界」─────────────────────────────────────────────
//   · 源协议 4 个：openai-chat / anthropic-messages / openai-responses /
//     gemini-generate。
//     ★ **ollama-chat 不能当源**：仓内只有 `ParseOllamaResponse`（响应侧），
//     **没有请求侧 parser** ⇒ 它的往返无法成立。
//   · 目标协议 5 个：上述 4 个 + ollama-chat（**只有序列化器**）。
//     ★ ollama-chat 目标**跳过幂等性**（同上：没有请求侧 parser 可回读）。
//   · **扩展保留**只在 src == dst 且 dst != ollama-chat 时断言：
//     `restoreExtensions` 的语义是「方言私有字段 → **仅目标方言认识时**还原」
//     （internal/ir/extensions_restore.go 文件头），跨协议会被 paramreg 裁剪；
//     ollama 则是**有意**不还原外域键（serialize_ollama.go 函数头契约）。

// ─────────────────────────────────────────────────────────────────────────────
// 夹具：一个源协议一份「富请求」
// ─────────────────────────────────────────────────────────────────────────────
//
// 每个夹具都刻意带上：system、纯文本 user、多模态 user（图片 base64）、
// 带 tool_calls 的 assistant、tool 结果、末尾 user、以及一个**非标顶层字段**
//（用于扩展保留断言）。⇒ 一份夹具能同时压到 5 类断言。

const rtImageB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==" // 1x1 PNG

const rtOpenAIBody = `{
  "model": "gpt-4o",
  "messages": [
    {"role": "system", "content": "You are a careful assistant."},
    {"role": "user", "content": [
      {"type": "text", "text": "What is in this picture?"},
      {"type": "image_url", "image_url": {"url": "data:image/png;base64,` + rtImageB64 + `"}}
    ]},
    {"role": "assistant", "content": null, "tool_calls": [
      {"id": "call_rt_1", "type": "function", "function": {"name": "lookup", "arguments": "{\"q\":\"x\"}"}}
    ]},
    {"role": "tool", "tool_call_id": "call_rt_1", "content": "42"},
    {"role": "user", "content": "thanks"}
  ],
  "temperature": 0.5,
  "max_tokens": 128,
  "x_vendor_custom": {"foo": "bar"}
}`

const rtAnthropicBody = `{
  "model": "claude-sonnet-5",
  "system": "You are a careful assistant.",
  "max_tokens": 128,
  "messages": [
    {"role": "user", "content": [
      {"type": "text", "text": "What is in this picture?"},
      {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "` + rtImageB64 + `"}}
    ]},
    {"role": "assistant", "content": [
      {"type": "tool_use", "id": "toolu_rt_1", "name": "lookup", "input": {"q": "x"}}
    ]},
    {"role": "user", "content": [
      {"type": "tool_result", "tool_use_id": "toolu_rt_1", "content": "42"}
    ]},
    {"role": "user", "content": "thanks"}
  ],
  "x_vendor_custom": {"foo": "bar"}
}`

// rtGeminiBody 用 **camelCase**（Gemini REST API 的 protobuf JSON 映射就是 camelCase：
// `inlineData`/`mimeType`）★ 仓内既有实现与既有测试也全部用 camelCase。
// ⚠ 用 snake_case（inline_data）会被 Go 的 json 解码**当成未知 part 丢弃**
//
//	（字段名匹配区分下划线）⇒ 夹具会失真且图片凭空消失。
const rtGeminiBody = `{
  "systemInstruction": {"parts": [{"text": "You are a careful assistant."}]},
  "contents": [
    {"role": "user", "parts": [
      {"text": "What is in this picture?"},
      {"inlineData": {"mimeType": "image/png", "data": "` + rtImageB64 + `"}}
    ]},
    {"role": "model", "parts": [
      {"functionCall": {"name": "lookup", "args": {"q": "x"}}}
    ]},
    {"role": "user", "parts": [
      {"functionResponse": {"name": "lookup", "response": {"result": "42"}}}
    ]},
    {"role": "user", "parts": [{"text": "thanks"}]}
  ],
  "generationConfig": {"temperature": 0.5, "maxOutputTokens": 128},
  "x_vendor_custom": {"foo": "bar"}
}`

const rtResponsesBody = `{
  "model": "gpt-4.1",
  "instructions": "You are a careful assistant.",
  "input": [
    {"role": "user", "content": [
      {"type": "input_text", "text": "What is in this picture?"},
      {"type": "input_image", "image_url": "data:image/png;base64,` + rtImageB64 + `"}
    ]},
    {"type": "function_call", "call_id": "call_rt_1", "name": "lookup", "arguments": "{\"q\":\"x\"}"},
    {"type": "function_call_output", "call_id": "call_rt_1", "output": "42"},
    {"role": "user", "content": [{"type": "input_text", "text": "thanks"}]}
  ],
  "temperature": 0.5,
  "max_output_tokens": 128,
  "x_vendor_custom": {"foo": "bar"}
}`

// rtSource 表示一个可作为往返**起点**的协议。
type rtSource struct {
	protocol string
	body     string
	// model 是**线上模型名**：Gemini 的 model 在 URL 路径上、body 里没有
	// ⇒ parser 拿不到 ⇒ 生产上由 executor 补到 IR
	// （executor_chat.go:3029 `irReq.Model = resolveOutboundModel(...)`）
	// ⇒ 矩阵测试必须照此补，否则「gemini→ollama」会因为 ollama 线上要求
	// model 必填而报错（那是**目标协议的要求**，不是转换缺陷）。
	model string
}

func rtSources() []rtSource {
	return []rtSource{
		{ProtocolOpenAIChat, rtOpenAIBody, ""},
		{ProtocolAnthropicMessages, rtAnthropicBody, ""},
		{ProtocolGeminiGenerate, rtGeminiBody, "gemini-2.5-pro"},
		{ProtocolOpenAIResponses, rtResponsesBody, ""},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 辅助
// ─────────────────────────────────────────────────────────────────────────────

// rtParse 按协议解析（**只覆盖有请求侧 parser 的 4 个协议**）。
func rtParse(t *testing.T, protocol string, body []byte) *InternalRequest {
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
		t.Fatalf("该协议没有请求侧 parser（不能作为往返起点/回读目标）: %s", protocol)
	}
	require.NoError(t, err, "%s 解析失败", protocol)
	return req
}

// rtSerialize 按协议序列化（5 个协议）。
func rtSerialize(t *testing.T, protocol string, req *InternalRequest) []byte {
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
	case ProtocolOllamaChat:
		data, err = SerializeOllama(req)
	default:
		t.Fatalf("不支持的目标协议: %s", protocol)
	}
	require.NoError(t, err, "序列化到 %s 失败", protocol)
	require.NotEmpty(t, data)
	return data
}

// rtHasRequestParser 报告该协议是否有请求侧 parser（决定能否回读）。
func rtHasRequestParser(protocol string) bool {
	switch protocol {
	case ProtocolOpenAIChat, ProtocolAnthropicMessages, ProtocolGeminiGenerate, ProtocolOpenAIResponses:
		return true
	}
	return false
}

// rtToolUseCount 数 IR 里的**不同**工具调用。
//
// ★ 必须按「不同调用」计数，不能按「出现次数」计数：IR 里同一个调用可能
//
//	**同时**出现在内容块 tool_use 与 Message.ToolCalls 两处（Anthropic parser
//	双写，parse_anthropic.go:276 + :291）⇒ 按出现次数会把 1 个调用数成 2。
//
// ★ 去重键：优先用调用 ID；没有 ID 时用 (工具名, 参数)。
func rtToolUseCount(msgs []Message) int {
	seen := map[string]bool{}
	add := func(id, name, args string) {
		key := id
		if key == "" {
			key = name + "\x00" + args
		}
		seen[key] = true
	}
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == "tool_use" && b.ToolUse != nil {
				add(b.ToolUse.ID, b.ToolUse.Name, string(b.ToolUse.Input))
			}
		}
		for _, tc := range m.ToolCalls {
			add(tc.ID, tc.Function.Name, tc.Function.Arguments)
		}
	}
	return len(seen)
}

// rtToolResultCount 数 IR 里的工具结果。
//
// ★ 必须同时数两种协议形态：① 内容块 tool_result（Anthropic/Gemini 形态）
//
//	② role=tool 的消息（OpenAI 形态：结果在消息上，不在内容块里）
//	⇒ 只数一种会得出「工具结果不见了」的**假红**。
func rtToolResultCount(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		blocks := 0
		for _, b := range m.Content {
			if b.Type == "tool_result" && b.ToolResult != nil {
				blocks++
			}
		}
		if (m.Role == "tool" || m.Role == "function") && m.ToolCallID != "" {
			// ★ 这条消息**本身**就是一个工具结果（内容块是它的载荷）
			//   ⇒ 计 1 且**不再**计它的块（Responses parser 就是这个形态：
			//   parse_responses.go:406-416 产出 role=tool + tool_call_id +
			//   内容块 tool_result ⇒ 同一结果被两种形态各表示一次）。
			n++
			continue
		}
		n += blocks
	}
	return n
}

// rtToolUseNames 收集 IR 里的工具名（**去重**）。
//
// ★ 必须去重：同一个调用可能同时出现在内容块 tool_use 与 Message.ToolCalls
//
//	两处（Anthropic parser 双写）⇒ 不去重会把 1 个调用数成 2 个名字。
func rtToolUseNames(msgs []Message) []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == "tool_use" && b.ToolUse != nil {
				add(b.ToolUse.Name)
			}
		}
		for _, tc := range m.ToolCalls {
			add(tc.Function.Name)
		}
	}
	sort.Strings(out)
	return out
}

// rtToolUseIDs 收集 IR 里的工具调用 ID（**去重**，理由同 rtToolUseNames）。
func rtToolUseIDs(msgs []Message) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == "tool_use" && b.ToolUse != nil {
				add(b.ToolUse.ID)
			}
		}
		for _, tc := range m.ToolCalls {
			add(tc.ID)
		}
	}
	sort.Strings(out)
	return out
}

// rtImageCount 数 IR 里的图片块。
func rtImageCount(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == "image" && b.Image != nil {
				n++
			}
		}
	}
	return n
}

// rtToolResultTexts 取一个工具结果里的文本。
//
// ★ Gemini 把 functionResponse 的**原始 JSON 整块**存成文本
//
//	（parse_gemini.go:301：`Text: string(fr.Response)`）⇒ 文本是 `{"result":"42"}`
//	而不是 `42` ⇒ ★ 必须归一（若它是 JSON 对象且含 result/output/content 字符串，
//	取那个值）否则跨协议比较会得出「工具结果文本丢了」的**假红**。
func rtToolResultTexts(tr *ToolResult) []string {
	var out []string
	for _, b := range tr.Content {
		if b.Type == "text" && b.Text != "" {
			out = append(out, b.Text)
		}
	}
	if len(out) == 0 && len(tr.GeminiResponse) > 0 {
		var m map[string]any
		if err := json.Unmarshal(tr.GeminiResponse, &m); err == nil {
			for _, k := range []string{"result", "output", "content", "response"} {
				if s, ok := m[k].(string); ok && s != "" {
					out = append(out, s)
					break
				}
			}
		}
	}
	// 归一：文本本身是 JSON 对象时，取其 result/output/content 字符串值。
	normalized := make([]string, 0, len(out))
	for _, s := range out {
		normalized = append(normalized, rtNormalizeResultText(s))
	}
	return normalized
}

// rtNormalizeResultText 把「JSON 形态的结果文本」归一为它的结果字符串。
func rtNormalizeResultText(s string) string {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "{") {
		return s
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(trimmed), &m); err != nil {
		return s
	}
	for _, k := range []string{"result", "output", "content"} {
		if v, ok := m[k].(string); ok {
			return v
		}
	}
	return s
}

// rtTexts 收集 IR 里全部**非空**文本（文本块 + 工具结果文本），排序后返回。
//
// ★ 用**集合**而不是拼接串：不同协议对「空内容」的表示不同
//
//	（OpenAI 的 `content:""` vs `content:[]`）⇒ 拼接串会被表示差异污染。
func rtTexts(msgs []Message) []string {
	var out []string
	for _, m := range msgs {
		isToolCarrier := m.Role == "tool" || m.Role == "function"
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				if b.Text == "" {
					continue
				}
				// ★ 工具结果消息的文本：Gemini 侧存的是**原始 JSON**
				//   （`{"result": "42"}`）⇒ 归一后与其它协议的 `42` 可比。
				if isToolCarrier {
					out = append(out, rtNormalizeResultText(b.Text))
					continue
				}
				out = append(out, b.Text)
			case "tool_result":
				if b.ToolResult != nil {
					out = append(out, rtToolResultTexts(b.ToolResult)...)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// rtCanonicalize 把「空内容」的多种线上表示归一："" / null / [] 三者语义相同
// （都表示「这条消息没有文本内容」）⇒ 归一后才比较幂等性。
//
// ★ 归一只针对**带 role 的消息对象**的 content 字段 ⇒ 不会误伤其它同名字段。
func rtCanonicalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if k == "content" {
				if _, hasRole := t["role"]; hasRole {
					switch cv := val.(type) {
					case string:
						if cv == "" {
							out[k] = []any{}
							continue
						}
					case nil:
						out[k] = []any{}
						continue
					}
				}
			}
			out[k] = rtCanonicalize(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = rtCanonicalize(val)
		}
		return out
	default:
		return v
	}
}

// rtCanonicalJSON 取一份 JSON 的**归一后**结构（用于幂等性比较）。
func rtCanonicalJSON(t *testing.T, data []byte) any {
	t.Helper()
	var v any
	require.NoError(t, json.Unmarshal(data, &v))
	return rtCanonicalize(v)
}

// ir2Messages 回读目标协议（复刻生产管线的 parse）。
func ir2Messages(t *testing.T, dst string, dst1 []byte) *InternalRequest {
	t.Helper()
	return rtParse(t, dst, dst1)
}

// rtJSONEqual 比较两份 JSON 的**结构**是否相同（不看键序）。
func rtJSONEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var av, bv any
	require.NoError(t, json.Unmarshal(a, &av))
	require.NoError(t, json.Unmarshal(b, &bv))
	return assert.ObjectsAreEqual(av, bv)
}

// mustMarshal 把任意值转成 JSON 字节。
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// ─────────────────────────────────────────────────────────────────────────────
// 主测试：源 × 目标 全矩阵
// ─────────────────────────────────────────────────────────────────────────────

// TestComprehensiveRoundtrip_Matrix 是「全协议组合往返」主测试。
//
// 对每个 (源协议, 目标协议) 组合依次验证：
//
//	① parse 成功 → validate/fix 成功 → serialize 成功且是合法 JSON；
//	② **语义等价**：回读后的 IR 与源 IR 在协议无关不变量上保持一致
//	   （工具调用条数/工具名/工具结果条数/图片条数/全部文本）；
//	③ **幂等性**：把目标协议的输出再回读、再序列化，结果必须与第一次产出
//	   **同一份 JSON**（★ 空内容表示归一后比较）；
//	④ **扩展保留**（仅 src == dst 且 dst != ollama-chat）：非标顶层字段必须
//	   逐字还原到线上。
func TestComprehensiveRoundtrip_Matrix(t *testing.T) {
	sources := rtSources()
	targets := []string{
		ProtocolOpenAIChat,
		ProtocolAnthropicMessages,
		ProtocolGeminiGenerate,
		ProtocolOpenAIResponses,
		ProtocolOllamaChat,
	}

	combos := 0
	for _, src := range sources {
		src := src
		for _, dst := range targets {
			dst := dst
			combos++
			t.Run(src.protocol+"->"+dst, func(t *testing.T) {
				// ── ① parse → validate → serialize ──────────────────────
				ir1 := rtParse(t, src.protocol, []byte(src.body))
				// ★ 生产上模型由 executor 补（Gemini 的 model 在 URL 路径上）
				if ir1.Model == "" && src.model != "" {
					ir1.Model = src.model
				}
				ir1 = ValidateAndFixRequest(ir1, "rt-"+src.protocol+"-"+dst)
				require.NotNil(t, ir1)

				dst1 := rtSerialize(t, dst, ir1)
				var wire map[string]any
				require.NoError(t, json.Unmarshal(dst1, &wire),
					"%s 的输出必须是合法 JSON", dst)

				// 目标协议的必填字段不得缺失（线上语义）。
				switch dst {
				case ProtocolAnthropicMessages:
					mt, ok := wire["max_tokens"].(float64)
					require.True(t, ok, "Anthropic 线上必须有 max_tokens")
					assert.Greater(t, mt, 0.0)
				case ProtocolOpenAIChat:
					assert.Contains(t, wire, "messages")
				case ProtocolOpenAIResponses:
					assert.Contains(t, wire, "input")
				case ProtocolGeminiGenerate:
					assert.Contains(t, wire, "contents")
				case ProtocolOllamaChat:
					assert.Contains(t, wire, "messages")
				}

				// ★ ollama-chat **没有请求侧 parser** ⇒ 回读（语义等价 + 幂等性）
				//   在它身上**不适用** ⇒ 只做线级检查后返回（诚实边界）。
				if !rtHasRequestParser(dst) {
					t.Logf("目标协议 %s 没有请求侧 parser ⇒ 回读类断言不适用（诚实边界）", dst)
					return
				}

				// ── ② 语义等价（协议无关不变量）────────────────────────
				// ★ 放在幂等性**之前**断言：语义丢失比表示差异更严重，
				//   先报出来才能定位到根因而不是被幂等性 diff 掩盖。
				ir2 := ir2Messages(t, dst, dst1)
				assert.Equal(t, rtToolUseCount(ir1.Messages), rtToolUseCount(ir2.Messages),
					"%s→%s：工具调用条数必须保持（★ 同一调用被发两次会在这里被抓到）", src.protocol, dst)
				assert.Equal(t, rtToolUseNames(ir1.Messages), rtToolUseNames(ir2.Messages),
					"%s→%s：工具名集合必须保持", src.protocol, dst)
				assert.Equal(t, rtToolResultCount(ir1.Messages), rtToolResultCount(ir2.Messages),
					"%s→%s：工具结果条数必须保持", src.protocol, dst)
				assert.Equal(t, rtImageCount(ir1.Messages), rtImageCount(ir2.Messages),
					"%s→%s：图片条数必须保持", src.protocol, dst)
				assert.Equal(t, rtTexts(ir1.Messages), rtTexts(ir2.Messages),
					"%s→%s：全部文本内容必须保持", src.protocol, dst)

				// ★ 工具调用 ID：Gemini 协议**没有 ID**（由 parser 按
				//   gemini_call_<name>_<partIdx> 合成）⇒ 跨 Gemini 的往返
				//   **不可能**保持 ID ⇒ 该断言只对非 Gemini 目标成立。
				if dst != ProtocolGeminiGenerate {
					assert.Equal(t, rtToolUseIDs(ir1.Messages), rtToolUseIDs(ir2.Messages),
						"%s→%s：工具调用 ID 必须保持", src.protocol, dst)
				}

				// ── ③ 幂等性（回读再序列化，必须产出同一份 JSON）───────
				ir2 = ValidateAndFixRequest(ir2, "rt2-"+src.protocol+"-"+dst)
				require.NotNil(t, ir2)
				dst2 := rtSerialize(t, dst, ir2)

				assert.Equal(t, rtCanonicalJSON(t, dst1), rtCanonicalJSON(t, dst2),
					"%s→%s 的二次往返必须产出同一份 JSON（幂等）\n第一次: %s\n第二次: %s",
					src.protocol, dst, string(dst1), string(dst2))

				// ── ④ 扩展保留（仅同协议往返）─────────────────────────
				if src.protocol != dst {
					return
				}
				for key, raw := range ir1.Extensions {
					got, ok := wire[key]
					require.True(t, ok,
						"%s 同协议往返后扩展字段 %q 必须回到线上", src.protocol, key)
					assert.True(t, rtJSONEqual(t, raw, mustMarshal(t, got)),
						"%s 同协议往返后扩展字段 %q 必须逐字还原\n期望: %s\n实际: %s",
						src.protocol, key, string(raw), string(mustMarshal(t, got)))
				}
			})
		}
	}
	assert.Equal(t, 20, combos, "矩阵组合数应是 4 源 × 5 目标 = 20")
}
