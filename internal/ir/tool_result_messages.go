package ir

// expandToolResultMessages maps Anthropic's mixed user turn into the separate
// tool outputs and following user content required by both OpenAI dialects.
// Keeping the whole turn as one tool output silently discards follow-up text
// and every tool result after the first one.
func expandToolResultMessages(messages []Message) []Message {
	out := make([]Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Role != "user" || !hasToolResultBlock(msg.Content) {
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
