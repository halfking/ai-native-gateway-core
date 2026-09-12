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
				Content          json.RawMessage `json:"content"`
				ReasoningContent string          `json:"reasoning_content"`
				ToolCalls        []any           `json:"tool_calls"`
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
	if msg.ReasoningContent != "" || len(msg.ToolCalls) > 0 {
		return false
	}
	return !chatContentHasOutput(msg.Content)
}

// chatContentHasOutput mirrors streaming.chatContentHasOutput (kept in sync —
// see the file-level note above): string vs content-part array shapes; unknown
// part types count as output so payload-carrying bodies are not failed over
// for a gateway-side parse gap (R16, 2026-09-12).
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

// isEmptyAnthropicMessagesResponse identifies a syntactically valid native
// Messages response that carries no semantic assistant output, mirroring the
// block semantics of streaming.isEmptyAnthropicContent (the handler-side
// authority) so the executor-level failover and the terminal 502 classifier
// cannot disagree about the same body:
//   - tool_use / server_tool_use / web_search_tool_result / redacted_thinking
//     blocks always count as output (a tool call is actionable even with an
//     empty input object);
//   - a thinking block counts as output when either thinking text or a
//     signature is present;
//   - any block with an unrecognized type counts as output (R16, 2026-09-12):
//     the gateway cannot know whether e.g. container_upload carries payload,
//     and b2639182b's OnlyUnsupportedBlocks guard in WriteNonStreamResponse
//     owns the attribution (KindConversion, stage=gateway) — an empty-response
//     failover here would demote the provider for a gateway-side gap and
//     starve the guard of bodies. Mixed unknown+text stays non-empty via the
//     text branch.
//
// Two deliberate refinements over the handler classifier, both in the safe
// direction for failover:
//   - the envelope must carry type=="message", so foreign JSON shapes are
//     never misjudged here;
//   - a message envelope with NO content key at all counts as empty — a 2xx
//     Messages response without content carries zero output by construction.
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
		Type      string `json:"type"`
		Text      string `json:"text"`
		Thinking  string `json:"thinking"`
		Signature string `json:"signature"`
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
			if block.Thinking != "" || block.Signature != "" {
				return false
			}
		case "tool_use", "server_tool_use", "web_search_tool_result", "redacted_thinking":
			return false
		default:
			if block.Type != "" {
				return false
			}
		}
	}
	return true
}
