package executors

import "encoding/json"

// isNonStreamEmptyResponse is a local copy of streaming.isEmptyUpstreamChatResponse,
// duplicated here because the executors sub-package cannot import its parent
// (streaming) package — and we need to detect empty non-stream responses BEFORE
// WriteHeader so the outer candidate loop can transparently failover to the
// next credential instead of returning a terminal 502 to the client.
//
// Behaviour matches streaming.isEmptyUpstreamChatResponse exactly:
//   - Empty if choices is missing/empty
//   - Empty if choices[0].message has no content AND no reasoning_content AND
//     no tool_calls
//
// 2026-07-15: kept in sync with streaming/empty_response.go (the handler-side
// 502 fallback uses the parent-package version).
func isNonStreamEmptyResponse(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var chatResp struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []any  `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return false
	}
	if len(chatResp.Choices) == 0 {
		return true
	}
	msg := chatResp.Choices[0].Message
	hasContent := msg.Content != "" ||
		msg.ReasoningContent != "" ||
		len(msg.ToolCalls) > 0
	return !hasContent
}

// isEmptyAnthropicMessagesResponse identifies a syntactically valid native
// Messages response that carries no semantic assistant output. It deliberately
// accepts tool_use blocks, including an empty input object, because those are
// actionable model output rather than an empty completion.
func isEmptyAnthropicMessagesResponse(body []byte) bool {
	if len(body) == 0 || !json.Valid(body) {
		return false
	}

	var envelope struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Type != "message" {
		return false
	}
	if envelope.Content == nil {
		return true
	}

	var blocks []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
		ID       string `json:"id"`
		Name     string `json:"name"`
		Input    any    `json:"input"`
	}
	if err := json.Unmarshal(envelope.Content, &blocks); err != nil || len(blocks) == 0 {
		return true
	}
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				return false
			}
		case "thinking":
			if block.Thinking != "" {
				return false
			}
		case "tool_use", "server_tool_use", "web_search_tool_result":
			return false
		}
	}
	return true
}
