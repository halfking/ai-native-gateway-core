package ir

// expandToolResultMessages maps Anthropic's mixed user turn into the separate
// tool outputs and following user content required by both OpenAI dialects.
// Keeping the whole turn as one tool output silently discards follow-up text
// and every tool result after the first one.
func expandToolResultMessages(messages []Message) []Message {
	out := make([]Message, 0, len(messages))
	for _, msg := range messages {
		// ★ 2026-11-11（协议转换审计轮 · D3）：**role=="tool"** 的消息也可能
		//   承载工具结果（Gemini 形态：parse_gemini.go 把 functionResponse 的
		//   role 映射成 "tool"，关联键在**内容块**的 ToolUseID 上）。
		//   ⇒ 此前只展开 role=="user" ⇒ Gemini 解析出的工具结果**不会被展开**
		//   ⇒ Message.ToolCallID 恒空 ⇒ SanitizeToolMessages 当「无 tool_call_id
		//   的非法工具消息」**静默删除** ⇒ ★ Gemini 客户端的工具结果凭空消失。
		//   ⇒ 判据：role=="user"（Anthropic 形态）或 role=="tool" 且消息级
		//   ToolCallID 为空（Gemini 形态；消息级 ID 有值时以它为准，不展开）。
		isUserShape := msg.Role == "user"
		isToolShape := (msg.Role == "tool" || msg.Role == "function") && msg.ToolCallID == ""
		if (!isUserShape && !isToolShape) || !hasToolResultBlock(msg.Content) {
			out = append(out, msg)
			continue
		}
		pending := msg
		pending.Content = nil
		flush := func() {
			if len(pending.Content) > 0 {
				out = append(out, pending)
				pending.Content = nil
			}
		}
		for _, block := range msg.Content {
			if block.Type == "tool_result" && block.ToolResult != nil {
				flush()
				out = append(out, Message{Role: "tool", ToolCallID: block.ToolResult.ToolUseID, Content: []ContentBlock{block}})
			} else {
				pending.Content = append(pending.Content, block)
			}
		}
		flush()
	}
	return out
}
