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
			Content          json.RawMessage `json:"content"`
			ReasoningContent string          `json:"reasoning_content"`
			ToolCalls        []any           `json:"tool_calls"`
		} `json:"message"`
	}
	// R16: malformed choices → not empty (aligned with the executors copy);
	// the converter/quality layers own semantics, the empty gate must not
	// fail over on a gateway-side parse gap.
	if json.Unmarshal(raw, &choices) != nil {
		return false
	}
	if len(choices) == 0 {
		return true
	}
	for _, choice := range choices {
		if choice.Message.ReasoningContent != "" || len(choice.Message.ToolCalls) > 0 {
			return false
		}
		if chatContentHasOutput(choice.Message.Content) {
			return false
		}
	}
	return true
}

// chatContentHasOutput judges message.content in both wire shapes: a plain
// string and a content-part array (multimodal / server-tool parts / refusal).
// Unknown part types count as output — semantic attribution belongs to the
// converter / IR layer (b2639182b), the empty gate must not reclassify
// payload-carrying bodies as empty because of its own parsing gaps.
func chatContentHasOutput(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return false
		}
		return s != ""
	}
	if raw[0] == '[' {
		var parts []struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Refusal string `json:"refusal"`
		}
		if json.Unmarshal(raw, &parts) != nil {
			return true // array shape we cannot interpret — assume payload
		}
		for _, p := range parts {
			if p.Text != "" || p.Refusal != "" {
				return true
			}
			switch p.Type {
			case "", "text", "output_text", "refusal":
				// text-shaped: only non-empty text/refusal counts
			default:
				return true // unknown part type: payload we cannot represent
			}
		}
		return false
	}
	return false
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
		default:
			// R16 (2026-09-12): unrecognized block types count as output so
			// unknown-only bodies reach the IR OnlyUnsupportedBlocks guard
			// (KindConversion, stage=gateway) instead of an empty-response
			// failover that demotes the provider for a gateway gap.
			if block.Type != "" {
				return false
			}
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
		// R16: named output item types this classifier doesn't model
		// (web_search_call, mcp_call, ...) count as output — same policy as
		// the Anthropic block loop above.
		switch item.Type {
		case "function_call":
			if item.CallID != "" || item.Name != "" || item.Arguments != "" {
				return false
			}
		case "message", "reasoning":
			// fall through to the content/summary checks below
		default:
			if item.Type != "" {
				return false
			}
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
