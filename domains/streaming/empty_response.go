package streaming

import "encoding/json"

type nonStreamResponseFormat uint8

const (
	nonStreamResponseUnknown nonStreamResponseFormat = iota
	nonStreamResponseChat
	nonStreamResponseAnthropic
	nonStreamResponseResponses
)

func classifyNonStreamUpstreamResponse(body []byte) (nonStreamResponseFormat, bool) {
	if len(body) == 0 || !json.Valid(body) {
		return nonStreamResponseUnknown, false
	}

	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil {
		return nonStreamResponseUnknown, false
	}
	if raw, ok := envelope["choices"]; ok {
		return nonStreamResponseChat, isEmptyChatChoices(raw)
	}
	if raw, ok := envelope["content"]; ok {
		return nonStreamResponseAnthropic, isEmptyAnthropicContent(raw)
	}
	if raw, ok := envelope["output"]; ok {
		return nonStreamResponseResponses, isEmptyResponsesOutput(raw)
	}
	return nonStreamResponseUnknown, false
}

func isEmptyUpstreamChatResponse(body []byte) bool {
	if len(body) == 0 || !json.Valid(body) {
		return false
	}
	format, empty := classifyNonStreamUpstreamResponse(body)
	return format == nonStreamResponseUnknown || (format == nonStreamResponseChat && empty)
}

func isEmptyChatChoices(raw json.RawMessage) bool {
	var choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []any  `json:"tool_calls"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &choices) != nil || len(choices) == 0 {
		return true
	}
	for _, choice := range choices {
		if choice.Message.Content != "" || choice.Message.ReasoningContent != "" || len(choice.Message.ToolCalls) > 0 {
			return false
		}
	}
	return true
}

func isEmptyAnthropicContent(raw json.RawMessage) bool {
	var blocks []struct {
		Type      string `json:"type"`
		Text      string `json:"text"`
		Thinking  string `json:"thinking"`
		Signature string `json:"signature"`
		ID        string `json:"id"`
		Name      string `json:"name"`
	}
	if json.Unmarshal(raw, &blocks) != nil || len(blocks) == 0 {
		return true
	}
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				return false
			}
		case "thinking":
			if block.Thinking != "" || block.Signature != "" {
				return false
			}
		case "redacted_thinking", "server_tool_use", "web_search_tool_result":
			return false
		case "tool_use":
			return false
		}
	}
	return true
}

func isEmptyResponsesOutput(raw json.RawMessage) bool {
	var output []struct {
		Type      string `json:"type"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		Content   []struct {
			Text string `json:"text"`
		} `json:"content"`
		Summary []struct {
			Text string `json:"text"`
		} `json:"summary"`
	}
	if json.Unmarshal(raw, &output) != nil || len(output) == 0 {
		return true
	}
	for _, item := range output {
		if item.Type == "function_call" && (item.CallID != "" || item.Name != "" || item.Arguments != "") {
			return false
		}
		for _, content := range item.Content {
			if content.Text != "" {
				return false
			}
		}
		for _, summary := range item.Summary {
			if summary.Text != "" {
				return false
			}
		}
	}
	return true
}
