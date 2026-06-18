// Package compressor - conversation_text.go (Round 47 / v7 T12)
//
// Migrated from routing/context_summarize.go on 2026-06-18. Extracts a
// plain-text representation of an OpenAI / Anthropic conversation body
// for the LLM-summary pass. Splits role / text / tool_call blocks into
// a flat "[role]\ntext\n\n" stream so the summarization prompt sees a
// consistent shape regardless of wire format.
//
// All helpers in this file are package-private (lowercase). The
// rebuilder_openai.go / rebuilder_anthropic.go rebuilder tests already
// exercise the equivalent internal logic; this file is the LLM-summary
// counterpart and is unit-tested by compaction_test.go.

package compressor

import (
	"encoding/json"
	"fmt"
	"strings"
)

// extractConversationText returns a flat-text rendering of the chat
// body, suitable for handing to a summarization LLM. Format:
//
//	[role1]
//	text1
//
//	[role2]
//	text2
//
// Tool calls and tool results are folded into the parent message's
// text via formatOpenAIToolCalls / formatAnthropicToolBlocks.
func extractConversationText(body []byte, clientProtocol string) (string, error) {
	if clientProtocol == "anthropic-messages" {
		return extractAnthropicConversationText(body)
	}
	return extractOpenAIConversationText(body)
}

func extractOpenAIConversationText(body []byte) (string, error) {
	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, raw := range req.Messages {
		role, text := messageRoleAndSummary(raw)
		if text == "" {
			continue
		}
		fmt.Fprintf(&b, "[%s]\n%s\n\n", role, text)
	}
	return b.String(), nil
}

func extractAnthropicConversationText(body []byte) (string, error) {
	var req struct {
		System   json.RawMessage   `json:"system"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return "", err
	}
	var b strings.Builder
	if len(req.System) > 0 {
		if text := rawJSONTextContent(req.System); text != "" {
			fmt.Fprintf(&b, "[system]\n%s\n\n", text)
		}
	}
	for _, raw := range req.Messages {
		role, text := messageRoleAndSummary(raw)
		if text == "" {
			continue
		}
		fmt.Fprintf(&b, "[%s]\n%s\n\n", role, text)
	}
	return b.String(), nil
}

func messageRoleAndSummary(raw json.RawMessage) (role, text string) {
	var probe struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCalls  json.RawMessage `json:"tool_calls"`
		ToolCallID string          `json:"tool_call_id"`
		Name       string          `json:"name"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "", ""
	}
	var parts []string
	if t := rawJSONTextContent(probe.Content); t != "" {
		parts = append(parts, t)
	}
	if toolText := formatOpenAIToolCalls(probe.ToolCalls); toolText != "" {
		parts = append(parts, toolText)
	}
	if blockText := formatAnthropicToolBlocks(probe.Content); blockText != "" {
		parts = append(parts, blockText)
	}
	if probe.Role == "tool" {
		label := probe.Name
		if label == "" {
			label = probe.ToolCallID
		}
		if label != "" {
			parts = append([]string{fmt.Sprintf("tool_result(%s)", label)}, parts...)
		} else {
			parts = append([]string{"tool_result"}, parts...)
		}
	}
	return probe.Role, strings.TrimSpace(strings.Join(parts, "\n"))
}

func formatOpenAIToolCalls(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var calls []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &calls); err != nil || len(calls) == 0 {
		return ""
	}
	var b strings.Builder
	for _, c := range calls {
		name := c.Function.Name
		if name == "" {
			name = c.ID
		}
		fmt.Fprintf(&b, "tool_call(%s): %s\n", name, truncateForLog([]byte(c.Function.Arguments), 512))
	}
	return strings.TrimSpace(b.String())
}

func formatAnthropicToolBlocks(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var parts []struct {
		Type      string          `json:"type"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"tool_use_id"`
		Text      string          `json:"text"`
		Content   json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(content, &parts); err != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		switch p.Type {
		case "tool_use":
			fmt.Fprintf(&b, "tool_use(%s): %s\n", p.Name, truncateForLog(p.Input, 512))
		case "tool_result":
			result := p.Text
			if result == "" {
				result = rawJSONTextContent(p.Content)
			}
			fmt.Fprintf(&b, "tool_result(%s): %s\n", p.ToolUseID, truncateForLog([]byte(result), 512))
		}
	}
	return strings.TrimSpace(b.String())
}

// rawJSONTextContent returns the plain text of a content field. Supports
// the three shapes Anthropic / OpenAI use:
//   - string: returned as-is
//   - array of {type:text,text:...} blocks: blocks concatenated with "\n"
//   - anything else: ""
func rawJSONTextContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return ""
		}
		return s
	}
	if trimmed[0] == '[' {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &blocks); err != nil {
			return ""
		}
		var b strings.Builder
		for _, bl := range blocks {
			if bl.Type == "text" || bl.Type == "" {
				if bl.Text != "" {
					if b.Len() > 0 {
						b.WriteString("\n")
					}
					b.WriteString(bl.Text)
				}
			}
		}
		return b.String()
	}
	return ""
}
