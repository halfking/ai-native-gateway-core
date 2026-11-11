package ir

// tool_calls_canonical.go —— 工具调用的**规范视图**（2026-11-11 协议转换审计轮）。
//
// ── 为什么需要这个文件（根因）──────────────────────────────────────────
// IR 里同一个工具调用有**两种表示**：
//   ① 内容块 `tool_use`（Anthropic / Gemini 约定）
//   ② 消息级 `Message.ToolCalls`（OpenAI / Responses 约定）
//
// 而**各生产方/消费方认的不一致**，于是长出了一串真缺陷（全部由
// `internal/ir/comprehensive_roundtrip_test.go` 的全矩阵往返测试实测撞出）：
//
//   · 生产方：Anthropic parser **两者都写**（parse_anthropic.go:276 + :291）；
//     OpenAI parser 只写 ②；Gemini parser 只写 ①。
//   · 消费方：serialize_openai / serialize_responses **只读 ②**；
//     serialize_anthropic 两边都读（自己写了 `represented` 去重）；
//     serialize_gemini 两边都读（**没有去重** ⇒ 同一个调用被发两次）。
//
// ⇒ 后果（均为生产路径）：
//   · Gemini 客户端的 functionCall（IR 里是 ①）到 OpenAI/Responses 上游
//     ⇒ **工具调用整个消失**（模型看不到该调用）。
//   · Anthropic 客户端到 Gemini 上游 ⇒ **同一个 functionCall 发两次**。
//
// ── 本文件的修法（单一职责 + 开闭原则）───────────────────────────────
// 把「一条消息有哪些工具调用」这条**规则**收敛到**一个**实现里：
//   · `canonicalToolCalls` —— 规范视图：**内容块 tool_use 优先**（它带 ID），
//     `Message.ToolCalls` 只补齐**未被代表**的调用。
//   · `representedToolCallIndexes` —— 同一规则的判定部分，供 serialize_anthropic
//     复用（它需要「哪些 ToolCalls 已被内容块代表」这个信息）。
// ⇒ 以后新增协议序列化器只要用 `canonicalToolCalls`，就不会再踩这族坑
//   （开闭原则：对扩展开放、对修改关闭）。

// canonicalToolCalls 返回一条消息的**规范工具调用列表**。
//
// 规则（与 serialize_anthropic.go 自 2026-09-22 起使用的规则**逐字一致**）：
//   ① 内容块里的 `tool_use` 优先（它带 ID，Anthropic/Gemini 约定）；
//   ② `Message.ToolCalls` 里**未被代表**的调用补在后面（OpenAI 约定）。
//
// 去重判据（与 Anthropic 序列化器一致）：ID、工具名、参数三者全同 ⇒ 同一个调用。
// ★ ID 为空时退化为「工具名 + 参数」判重（部分客户端不发 ID）。
func canonicalToolCalls(msg Message) []ToolCall {
	out := make([]ToolCall, 0, len(msg.ToolCalls))
	for _, block := range msg.Content {
		if block.Type != "tool_use" || block.ToolUse == nil {
			continue
		}
		tc := ToolCall{ID: block.ToolUse.ID, Type: "function"}
		tc.Function.Name = block.ToolUse.Name
		tc.Function.Arguments = string(block.ToolUse.Input)
		out = append(out, tc)
	}
	for i, tc := range msg.ToolCalls {
		if representedToolCallIndexes(msg)[i] {
			continue
		}
		out = append(out, tc)
	}
	return out
}

// representedToolCallIndexes 返回「哪些 Message.ToolCalls 已被内容块代表」的
// 下标集合。
//
// ★ 单独抽出来的原因：serialize_anthropic.go 需要这个信息来**抑制**重复的
//   tool_use 块（它自己按「内容块在前、ToolCalls 补齐」的顺序发）⇒
//   规则必须与 canonicalToolCalls **同源**，否则两处会漂移。
func representedToolCallIndexes(msg Message) []bool {
	represented := make([]bool, len(msg.ToolCalls))
	for _, block := range msg.Content {
		if block.Type != "tool_use" || block.ToolUse == nil {
			continue
		}
		for i, call := range msg.ToolCalls {
			if represented[i] {
				continue
			}
			if call.ID == block.ToolUse.ID &&
				call.Function.Name == block.ToolUse.Name &&
				call.Function.Arguments == string(block.ToolUse.Input) {
				represented[i] = true
				break
			}
		}
	}
	return represented
}

// toolNameIndex 建立「工具调用 ID → 工具名」的跨消息索引。
//
// ★ 用途：Gemini 协议**没有工具调用 ID**，它的 functionResponse 只能靠
//   **函数名**与 functionCall 配对 ⇒ 出站时必须把「结果」关联回「调用名」。
//   而结果的关联键在 IR 里是 `ToolResult.ToolUseID`（可能是 Anthropic 的
//   `toolu_xxx`、OpenAI 的 `call_xxx`、或 Gemini 合成的 `gemini_call_*`）
//   ⇒ **不能靠解析 ID 字符串猜名字**（旧实现只认 `gemini_call_` 前缀，
//   遇到 `toolu_xxx` 就原样返回 ⇒ functionResponse.name 与 functionCall.name
//   对不上 ⇒ 工具结果成孤儿）。
func toolNameIndex(messages []Message) map[string]string {
	idx := make(map[string]string)
	for _, m := range messages {
		for _, block := range m.Content {
			if block.Type == "tool_use" && block.ToolUse != nil && block.ToolUse.ID != "" {
				idx[block.ToolUse.ID] = block.ToolUse.Name
			}
		}
		for _, tc := range m.ToolCalls {
			if tc.ID != "" && tc.Function.Name != "" {
				if _, ok := idx[tc.ID]; !ok {
					idx[tc.ID] = tc.Function.Name
				}
			}
		}
	}
	return idx
}
