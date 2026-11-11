package ir

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 生产会话回放测试（2026-11-11 审计轮）。
//
// ── 数据来源 ───────────────────────────────────────────────────────────
// 211 生产库（acc-pg17）的 session_bodies：
//   request_delta —— 客户端当轮消息；outbound_body —— 网关实际出站消息。
// 提取脚本：scripts/extract_211_session_fixtures.sh
// 夹具：internal/ir/testdata/production_sessions/sessions.jsonl
//
// ── 夹具的隐私处理 ─────────────────────────────────────────────────────
// 真实会话含用户内容（源码、对话、路径）⇒ 夹具**已脱敏**：载荷文本被占位
// 符替换并截断；**结构判别值原样保留**（role / type / tool 名 /
// tool_call_id / 消息顺序）—— 这些正是转换语义的载体。
//
// ── 断言层级 ───────────────────────────────────────────────────────────
// 全部打在 **wire**（线上字节）与 **IR 结构**上，不打中间态。
// 理由：2026-11-11 第一版把断言打在 parse 层，误报了两个已实现的能力。
//
// ── 覆盖 ───────────────────────────────────────────────────────────────
// ① 三种客户端协议（openai-chat / anthropic-messages / openai-responses）
// ② 多轮长上下文（最高 60 条消息、含大量 tool_calls / tool 结果）
// ③ **黄金对拍**：有 egress 的样本，IR 管线的输出必须与生产实际出站**语义一致**

// productionFixture 是一条真实会话样本。
type productionFixture struct {
	SessionID      string           `json:"session_id"`
	TurnNo         int              `json:"turn_no"`
	ClientProtocol string           `json:"client_protocol"`
	Model          string           `json:"model"`
	ClientMessages []map[string]any `json:"client_messages"`
	EgressMessages []map[string]any `json:"egress_messages"`
}

// loadProductionFixtures 读取夹具。
func loadProductionFixtures(t *testing.T) []productionFixture {
	t.Helper()
	path := filepath.Join("testdata", "production_sessions", "sessions.jsonl")
	fh, err := os.Open(path)
	if err != nil {
		t.Skipf("生产会话夹具不存在（先跑 scripts/extract_211_session_fixtures.sh）: %v", err)
	}
	defer fh.Close()

	var out []productionFixture
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20) // 单行可达数百 KB
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var f productionFixture
		if err := json.Unmarshal(line, &f); err != nil {
			t.Fatalf("夹具行解析失败: %v", err)
		}
		out = append(out, f)
	}
	require.NoError(t, sc.Err())
	require.NotEmpty(t, out, "夹具为空")
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// 请求信封构造：把存储形态的消息还原成各协议的**客户端请求体**
// ─────────────────────────────────────────────────────────────────────────────

// buildClientRequest 按客户端协议构造请求体。
//
// 注意：session_bodies 里的消息是**存储归一形态**（chat 形态的 role/content/
// tool_calls），不是各协议的线上原貌 ⇒ 要还原成协议形态才能回放：
//   · openai-chat      → {"model":…,"messages":[…]}
//   · anthropic-*      → {"model":…,"max_tokens":…,"messages":[…]}
//   · openai-responses → {"model":…,"input":[…]}（chat 形态需转成
//     function_call / function_call_output item）
func buildClientRequest(f productionFixture) ([]byte, error) {
	switch f.ClientProtocol {
	case ProtocolOpenAIChat:
		return json.Marshal(map[string]any{
			"model":    modelOrDefault(f.Model),
			"messages": f.ClientMessages,
		})

	case ProtocolAnthropicMessages:
		return json.Marshal(map[string]any{
			"model":      modelOrDefault(f.Model),
			"max_tokens": 4096,
			"messages":   f.ClientMessages,
		})

	case ProtocolOpenAIResponses:
		return json.Marshal(map[string]any{
			"model": modelOrDefault(f.Model),
			"input": chatMessagesToResponsesItems(f.ClientMessages),
		})

	default:
		return nil, assertErrf("不支持的客户端协议: " + f.ClientProtocol)
	}
}

// modelOrDefault 夹具里的模型名已脱敏，统一给一个可用的占位名。
func modelOrDefault(m string) string {
	if m == "" || m == "redacted-model" {
		return "test-model"
	}
	return m
}

// chatMessagesToResponsesItems 把 chat 形态消息转成 Responses input item。
//
// Responses 的 tool 语义用两种 item 表达：
//   · function_call         —— assistant 的工具调用（name/arguments/call_id）
//   · function_call_output  —— 工具结果（call_id/output）
// 存储形态是 chat 的 tool_calls / role=tool ⇒ 这里做等价映射。
func chatMessagesToResponsesItems(msgs []map[string]any) []map[string]any {
	items := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		role, _ := m["role"].(string)
		content, hasContent := m["content"]

		if role == "assistant" {
			if rawCalls, ok := m["tool_calls"].([]any); ok {
				for _, c := range rawCalls {
					call, ok := c.(map[string]any)
					if !ok {
						continue
					}
					fn, _ := call["function"].(map[string]any)
					name, _ := fn["name"].(string)
					args, _ := fn["arguments"].(string)
					callID, _ := call["id"].(string)
					items = append(items, map[string]any{
						"type":      "function_call",
						"call_id":   callID,
						"name":      name,
						"arguments": args,
					})
				}
				continue
			}
		}

		if role == "tool" {
			callID, _ := m["tool_call_id"].(string)
			items = append(items, map[string]any{
				"type":    "function_call_output",
				"call_id": callID,
				"output":  contentToText(content),
			})
			continue
		}

		item := map[string]any{"role": role}
		if hasContent {
			item["content"] = content
		}
		items = append(items, item)
	}
	return items
}

// contentToText 把 content（字符串或块数组）压成纯文本。
func contentToText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var sb []byte
		for _, blk := range c {
			b, ok := blk.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := b["text"].(string); ok {
				sb = append(sb, t...)
			}
		}
		return string(sb)
	default:
		return ""
	}
}

// assertErrf 造一个带格式的 error（避免为一个用途引入 fmt 依赖面）。
type assertErrf string

func (e assertErrf) Error() string { return string(e) }

// ─────────────────────────────────────────────────────────────────────────────
// 测试 ①：三协议回放 —— parse → validate → serialize（五个目标协议）
// ─────────────────────────────────────────────────────────────────────────────

// TestProductionReplay_AllProtocolsRoundTrip 用真实会话样本走完整管线。
//
// 断言（对每个样本 × 每个目标协议）：
//   · parse 成功
//   · validate+fix 成功
//   · serialize 成功且是合法 JSON
//   · 消息数与角色序列保持（tool 结果被清理的情形除外，见下）
//   · 工具调用 ID 集合保持
func TestProductionReplay_AllProtocolsRoundTrip(t *testing.T) {
	fixtures := loadProductionFixtures(t)

	targets := []struct {
		name      string
		protocol  string
		serialize func(*InternalRequest) ([]byte, error)
	}{
		{"openai-chat", ProtocolOpenAIChat, SerializeOpenAI},
		{"anthropic-messages", ProtocolAnthropicMessages, SerializeAnthropic},
		{"openai-responses", ProtocolOpenAIResponses, SerializeResponsesRequest},
		{"ollama-chat", ProtocolOllamaChat, SerializeOllama},
		{"gemini-generate", ProtocolGeminiGenerate, SerializeGemini},
	}

	stats := map[string]int{}
	for _, f := range fixtures {
		f := f
		t.Run(f.ClientProtocol+"/"+f.SessionID, func(t *testing.T) {
			body, err := buildClientRequest(f)
			require.NoError(t, err, "构造客户端请求失败")

			irReq, err := parseByClientProtocol(f.ClientProtocol, body)
			require.NoError(t, err, "真实会话样本解析失败（这是真实场景的回归）")

			irReq = ValidateAndFixRequest(irReq, "replay-"+f.SessionID)
			require.NotNil(t, irReq)

			// ── 协议无关的不变量（所有协议都必须成立）────────────────────
			// ① 工具调用 ID 集合必须保持
			wantIDs := toolCallIDsOf(f.ClientMessages)
			gotIDs := toolCallIDsOfIR(irReq.Messages)
			assert.ElementsMatch(t, wantIDs, gotIDs, "工具调用 ID 集合必须保持")

			// ② 工具名集合必须保持
			assert.ElementsMatch(t,
				toolNamesOf(f.ClientMessages),
				toolNamesOfIR(irReq.Messages),
				"工具名集合必须保持")

			// ③ tool 结果消息数：网关应**恰好**清掉客户端自身的孤儿，
			//    且不多删一条。
			//    孤儿定义（客户端侧）：tool 结果的 tool_call_id 在客户端
			//    自己的 tool_calls 里找不到对应调用。
			//    实测（211 真实样本）：5 个样本各含 1 条这样的孤儿，
			//    其中一条的 tool_call_id 是 UUID（`11341237-1aba-…`）
			//    而非 `call_` 前缀 ⇒ 真实客户端错误。
			//    ⇒ SanitizeToolMessages 删掉的条数必须**等于**孤儿数。
			wantTool := countRole(f.ClientMessages, "tool")
			orphanCount := countClientOrphanToolResults(f.ClientMessages)
			assert.Equal(t,
				wantTool-orphanCount,
				countRoleIR(irReq.Messages, "tool"),
				"tool 结果消息数应 = 客户端条数 − 客户端孤儿条数（孤儿 %d）",
				orphanCount)

			// ④ user 消息数必须保持
			assert.Equal(t,
				countRole(f.ClientMessages, "user"),
				countRoleIR(irReq.Messages, "user"),
				"user 消息数必须保持")

			// ── 协议特定：1:1 消息序列只在 openai-chat 成立 ──────────────
			// openai-responses 的 input[] 是**item 序列**不是消息序列：
			//   · system item 被提升到 req.System（不再是一条消息）
			//   · 连续 function_call item（并行调用）被**合并**成一条
			//     assistant 消息（parse_responses.go r1030 行为）
			//   ⇒ 客户端 60 条（assistant 37）→ IR 45 条（assistant 22）
			//     而 tool(22)/user(1) 与 37 个 tool_call 全部保持。
			// 这两条都是**产品行为**（有注释与既有测试钉住），不是缺陷 ⇒
			// 1:1 序列断言只对 openai-chat 施加。
			//
			// ⚠️ system 消息在**所有协议**都会被规范化进 req.System
			//（types.go: "In OpenAI it's part of messages with role=system;
			// in Anthropic it's a top-level system field. We normalize to
			// this field."）⇒ 比序列时必须**剔除 system**。
			if f.ClientProtocol == ProtocolOpenAIChat {
				clientRoles := expectedRolesAfterFix(f.ClientMessages)
				irRoles := rolesOfIR(irReq.Messages)
				assert.Equal(t, len(clientRoles), len(irRoles),
					"openai-chat 消息数应 = 客户端(剔除 system 与孤儿 tool 后) %d / IR %d",
					len(clientRoles), len(irRoles))
				for i := range clientRoles {
					if i >= len(irRoles) {
						break
					}
					assert.Equal(t, clientRoles[i], irRoles[i],
						"openai-chat 第 %d 条消息角色应保持", i)
				}
				// system 必须落在 req.System 里（不许消失）
				if countRole(f.ClientMessages, "system") > 0 {
					assert.NotNil(t, irReq.System,
						"system 消息必须被规范化进 req.System（不许消失）")
				}
			}

			// 五个目标协议逐一序列化
			for _, tgt := range targets {
				data, err := tgt.serialize(irReq)
				require.NoError(t, err, "序列化到 %s 失败", tgt.name)
				require.NotEmpty(t, data)

				var wire map[string]any
				require.NoError(t, json.Unmarshal(data, &wire),
					"%s 的输出必须是合法 JSON", tgt.name)

				// 目标协议的必填字段不得缺失
				switch tgt.protocol {
				case ProtocolAnthropicMessages:
					mt, ok := wire["max_tokens"].(float64)
					require.True(t, ok, "Anthropic 线上必须有 max_tokens")
					assert.Greater(t, mt, 0.0)
				case ProtocolOpenAIChat:
					assert.Contains(t, wire, "messages")
				}

				stats[tgt.name]++
			}
		})
	}

	t.Logf("回放统计: %v", stats)
	assert.Equal(t, len(fixtures)*len(targets), stats["openai-chat"]+stats["anthropic-messages"]+
		stats["openai-responses"]+stats["ollama-chat"]+stats["gemini-generate"])
}

// ─────────────────────────────────────────────────────────────────────────────
// 测试 ②：黄金对拍 —— IR 管线输出 vs 生产实际出站
// ─────────────────────────────────────────────────────────────────────────────

// TestProductionReplay_MatchesProductionEgress 黄金对拍。
//
// 口径（登记在案）：
//   · 生产出站（outbound_body）是网关**实际**发给上游的消息数组 ⇒ 它就是
//     「IR 管线 + 会话存储补齐 + 角色交替修正」的产物。
//   · 实测差异来源只有一个：客户端发了**两条连续 user 消息**，网关把它们
//     合并成一条（生产开了 LLM_GATEWAY_FIX_ROLE_ALTERNATION；默认是
//     warn-only）。⇒ 本测试用 t.Setenv 打开该开关以对齐生产口径。
//   · 消息数一致时，逐条比对：角色 / tool_call_id / 工具名 / 内容存在性。
//
// 消息数不一致的样本（本轮实测有 60 vs 7、9 vs 8 等）说明该轮客户端发的
// 与出站不是同一形态（会话补齐/压缩）⇒ 不做等值断言，只做结构检查。
func TestProductionReplay_MatchesProductionEgress(t *testing.T) {
	// 生产环境开了角色交替合并；默认是 warn-only ⇒ 这里显式对齐生产口径。
	t.Setenv("LLM_GATEWAY_FIX_ROLE_ALTERNATION", "true")

	fixtures := loadProductionFixtures(t)

	var compared, skipped int
	for _, f := range fixtures {
		if len(f.EgressMessages) == 0 {
			skipped++
			continue
		}
		if len(f.EgressMessages) != len(f.ClientMessages) {
			// 合并连续同角色消息后条数会减少 ⇒ 用「合并后」的条数比对
			if len(f.EgressMessages) != countAfterRoleMerge(f.ClientMessages) {
				skipped++
				continue
			}
		}

		f := f
		t.Run(f.ClientProtocol+"/"+f.SessionID+"/golden", func(t *testing.T) {
			body, err := buildClientRequest(f)
			require.NoError(t, err)

			irReq, err := parseByClientProtocol(f.ClientProtocol, body)
			require.NoError(t, err)
			irReq = ValidateAndFixRequest(irReq, "golden-"+f.SessionID)

			data, err := SerializeOpenAI(irReq)
			require.NoError(t, err)

			var wire struct {
				Messages []map[string]any `json:"messages"`
			}
			require.NoError(t, json.Unmarshal(data, &wire))

			// ① 消息数
			require.Equal(t, len(f.EgressMessages), len(wire.Messages),
				"出站消息数应与生产一致")

			// ② 逐条：角色
			for i := range f.EgressMessages {
				if i >= len(wire.Messages) {
					break
				}
				want, _ := f.EgressMessages[i]["role"].(string)
				got, _ := wire.Messages[i]["role"].(string)
				assert.Equal(t, want, got, "第 %d 条角色应与生产一致", i)

				// ③ tool_call_id
				wantID, _ := f.EgressMessages[i]["tool_call_id"].(string)
				gotID, _ := wire.Messages[i]["tool_call_id"].(string)
				assert.Equal(t, wantID, gotID, "第 %d 条 tool_call_id 应与生产一致", i)

				// ④ 内容存在性（不比字面：夹具已脱敏，生产原文不可得）
				assert.Equal(t,
					hasSubstantiveContent(f.EgressMessages[i]),
					hasSubstantiveContent(wire.Messages[i]),
					"第 %d 条内容存在性应与生产一致", i)
			}

			// ⑤ 工具调用 ID 集合
			assert.ElementsMatch(t,
				toolCallIDsOf(f.EgressMessages),
				toolCallIDsOf(wire.Messages),
				"工具调用 ID 集合应与生产一致")

			// ⑥ 工具名集合
			assert.ElementsMatch(t,
				toolNamesOf(f.EgressMessages),
				toolNamesOf(wire.Messages),
				"工具名集合应与生产一致")
		})
		compared++
	}

	t.Logf("黄金对拍: %d 条参与，%d 条跳过（无出站体或形态不可比）", compared, skipped)
	assert.Greater(t, compared, 0, "应有可对拍的样本")
}

// countAfterRoleMerge 给出会话在「合并连续同角色消息」后的条数。
//
// 合并规则（validate_and_fix.go mergeConsecutiveSameRoleMessages）：
//   · 只合并 user/assistant（system 与 tool 不参与交替判定）
//   · 连续同角色的消息合并成一条
func countAfterRoleMerge(msgs []map[string]any) int {
	n := 0
	var lastRole string
	for _, m := range msgs {
		r, _ := m["role"].(string)
		if r == "system" || r == "tool" {
			n++
			continue
		}
		if r == lastRole {
			continue // 并入上一条
		}
		n++
		lastRole = r
	}
	return n
}

// hasSubstantiveContent 判断消息是否有实质内容（用于与生产对拍内容存在性）。
func hasSubstantiveContent(m map[string]any) bool {
	if m == nil {
		return false
	}
	if c, ok := m["content"]; ok {
		switch v := c.(type) {
		case string:
			if v != "" {
				return true
			}
		case []any:
			if len(v) > 0 {
				return true
			}
		}
	}
	if raw, ok := m["tool_calls"].([]any); ok && len(raw) > 0 {
		return true
	}
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// 测试 ③：稳定性 —— 同一输入两次回放必须逐字节一致
// ─────────────────────────────────────────────────────────────────────────────

// TestProductionReplay_Deterministic 确定性：转换必须是纯函数。
//
// 理由：转换里若有 map 迭代顺序依赖 ⇒ 同一请求两次转换产出不同字节 ⇒
// 上游可能收到不同的请求（签名/缓存/幂等语义都会受影响）。
func TestProductionReplay_Deterministic(t *testing.T) {
	fixtures := loadProductionFixtures(t)

	for _, f := range fixtures {
		f := f
		t.Run(f.ClientProtocol+"/"+f.SessionID+"/determinism", func(t *testing.T) {
			body, err := buildClientRequest(f)
			require.NoError(t, err)

			var first string
			for i := 0; i < 3; i++ {
				irReq, err := parseByClientProtocol(f.ClientProtocol, body)
				require.NoError(t, err)
				irReq = ValidateAndFixRequest(irReq, "determinism")
				data, err := SerializeOpenAI(irReq)
				require.NoError(t, err)
				if i == 0 {
					first = string(data)
					continue
				}
				assert.Equal(t, first, string(data),
					"同一输入的多次转换必须逐字节一致（否则有 map 迭代顺序依赖）")
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 辅助
// ─────────────────────────────────────────────────────────────────────────────

// parseByClientProtocol 按客户端协议解析。
func parseByClientProtocol(protocol string, body []byte) (*InternalRequest, error) {
	switch protocol {
	case ProtocolOpenAIChat:
		return ParseOpenAI(body)
	case ProtocolAnthropicMessages:
		return ParseAnthropic(body)
	case ProtocolOpenAIResponses:
		return ParseResponses(body)
	case ProtocolGeminiGenerate:
		return ParseGemini(body)
	default:
		return nil, assertErrf("不支持的客户端协议: " + protocol)
	}
}

// rolesOf 取存储形态消息的角色序列。
func rolesOf(msgs []map[string]any) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		r, _ := m["role"].(string)
		out = append(out, r)
	}
	return out
}

// rolesOfIR 取 IR 消息的角色序列。
func rolesOfIR(msgs []Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Role)
	}
	return out
}

// nonSystemRoles 取剔除 system 后的角色序列（system 会被规范化进 req.System）。
func nonSystemRoles(msgs []map[string]any) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		r, _ := m["role"].(string)
		if r == "system" {
			continue
		}
		out = append(out, r)
	}
	return out
}

// expectedRolesAfterFix 给出「网关修正后」应有的角色序列：
// 剔除 system（被规范化进 req.System）+ 剔除客户端自身的孤儿 tool 结果
// （被 SanitizeToolMessages 清理）。
//
// 这条期望序列与 IR 的实际序列逐项相等 ⇒ 证明网关**恰好**修了该修的、
// **没有**动不该动的消息（顺序也保持）。
func expectedRolesAfterFix(msgs []map[string]any) []string {
	callIDs := map[string]bool{}
	for _, m := range msgs {
		if raw, ok := m["tool_calls"].([]any); ok {
			for _, c := range raw {
				call, ok := c.(map[string]any)
				if !ok {
					continue
				}
				if id, ok := call["id"].(string); ok {
					callIDs[id] = true
				}
			}
		}
	}
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		r, _ := m["role"].(string)
		if r == "system" {
			continue
		}
		if r == "tool" {
			tcid, _ := m["tool_call_id"].(string)
			if !callIDs[tcid] {
				continue // 客户端孤儿 ⇒ 网关会清掉
			}
		}
		out = append(out, r)
	}
	return out
}

// countClientOrphanToolResults 数客户端消息里「孤儿 tool 结果」的条数：
// tool 结果的 tool_call_id 在客户端自己的 tool_calls 里找不到对应调用。
//
// 这是**客户端侧**的错误形态（网关应当清掉它们，且只清掉这些）。
func countClientOrphanToolResults(msgs []map[string]any) int {
	ids := map[string]bool{}
	for _, m := range msgs {
		if raw, ok := m["tool_calls"].([]any); ok {
			for _, c := range raw {
				call, ok := c.(map[string]any)
				if !ok {
					continue
				}
				if id, ok := call["id"].(string); ok {
					ids[id] = true
				}
			}
		}
	}
	n := 0
	for _, m := range msgs {
		if r, _ := m["role"].(string); r != "tool" {
			continue
		}
		tcid, _ := m["tool_call_id"].(string)
		if !ids[tcid] {
			n++
		}
	}
	return n
}

// countRole 数存储形态消息里某角色的条数。
func countRole(msgs []map[string]any, role string) int {
	n := 0
	for _, m := range msgs {
		if r, _ := m["role"].(string); r == role {
			n++
		}
	}
	return n
}

// countRoleIR 数 IR 消息里某角色的条数。
func countRoleIR(msgs []Message, role string) int {
	n := 0
	for _, m := range msgs {
		if m.Role == role {
			n++
		}
	}
	return n
}

// toolCallIDsOf 取存储形态消息里的工具调用 ID。
func toolCallIDsOf(msgs []map[string]any) []string {
	var out []string
	for _, m := range msgs {
		if raw, ok := m["tool_calls"].([]any); ok {
			for _, c := range raw {
				call, ok := c.(map[string]any)
				if !ok {
					continue
				}
				if id, ok := call["id"].(string); ok {
					out = append(out, id)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// toolCallIDsOfIR 取 IR 消息里的工具调用 ID。
func toolCallIDsOfIR(msgs []Message) []string {
	var out []string
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			out = append(out, c.ID)
		}
	}
	sort.Strings(out)
	return out
}

// toolNamesOf 取存储形态消息里的工具名。
func toolNamesOf(msgs []map[string]any) []string {
	var out []string
	for _, m := range msgs {
		if raw, ok := m["tool_calls"].([]any); ok {
			for _, c := range raw {
				call, ok := c.(map[string]any)
				if !ok {
					continue
				}
				fn, _ := call["function"].(map[string]any)
				if n, ok := fn["name"].(string); ok {
					out = append(out, n)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// toolNamesOfIR 取 IR 消息里的工具名。
func toolNamesOfIR(msgs []Message) []string {
	var out []string
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			out = append(out, c.Function.Name)
		}
	}
	sort.Strings(out)
	return out
}
