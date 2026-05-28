package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kaixuan/llm-gateway-go/transform"
)

func requestPreview(body []byte) string {
	return previewJSON(body, 320)
}

func responsePreview(body []byte) string {
	return previewJSON(body, 320)
}

func transformSummary(tx *transform.TransformResult, outboundModel string) string {
	if tx == nil {
		if outboundModel == "" {
			return ""
		}
		return "outbound=" + outboundModel
	}
	parts := make([]string, 0, 6)
	if tx.MatchedRule != "" {
		parts = append(parts, "rule="+tx.MatchedRule)
	}
	if outboundModel != "" {
		parts = append(parts, "outbound="+outboundModel)
	} else if tx.OutboundModel != "" {
		parts = append(parts, "outbound="+tx.OutboundModel)
	}
	if len(tx.PassthroughFields) > 0 {
		parts = append(parts, "keep="+strings.Join(tx.PassthroughFields, ","))
	}
	if len(tx.StripRequestFields) > 0 {
		parts = append(parts, "strip="+strings.Join(tx.StripRequestFields, ","))
	}
	if len(tx.EgressPreference) > 0 {
		parts = append(parts, "egress="+strings.Join(tx.EgressPreference, ","))
	}
	if tx.DisguiseProfileID != "" {
		parts = append(parts, "disguise="+tx.DisguiseProfileID)
	}
	if len(tx.InjectHeaders) > 0 {
		headers := make([]string, 0, len(tx.InjectHeaders))
		for k := range tx.InjectHeaders {
			headers = append(headers, k)
		}
		parts = append(parts, "headers="+strings.Join(headers, ","))
	}
	return strings.Join(parts, " | ")
}

func previewJSON(body []byte, limit int) string {
	if len(body) == 0 {
		return ""
	}
	if summary := summarizeJSON(body); summary != "" {
		return truncateText(summary, limit)
	}
	return truncateText(compactJSON(body), limit)
}

func summarizeJSON(body []byte) string {
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	if summary := summarizeMessagesContainer(parsed); summary != "" {
		return summary
	}
	if m, ok := parsed.(map[string]any); ok {
		if choices, ok := m["choices"].([]any); ok && len(choices) > 0 {
			if choice, ok := choices[0].(map[string]any); ok {
				if msg, ok := choice["message"].(map[string]any); ok {
					return summarizeMessagesContainer(map[string]any{"messages": []any{msg}})
				}
			}
		}
		if output, ok := m["output"].([]any); ok {
			parts := make([]string, 0, 2)
			for _, item := range output {
				msg, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if role, ok := msg["role"].(string); ok {
					parts = append(parts, role+": "+extractResponseOutputText(msg))
				}
				if len(parts) >= 2 {
					break
				}
			}
			return strings.Join(parts, " | ")
		}
	}
	return ""
}

func summarizeMessagesContainer(parsed any) string {
	var messages []any
	switch v := parsed.(type) {
	case []any:
		messages = v
	case map[string]any:
		if raw, ok := v["messages"].([]any); ok {
			messages = raw
		}
	}
	if len(messages) == 0 {
		return ""
	}
	parts := make([]string, 0, 3)
	for _, item := range messages {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		content := extractMessageText(msg["content"])
		if toolCalls, ok := msg["tool_calls"].([]any); ok && len(toolCalls) > 0 {
			content = strings.TrimSpace(content + " tools=" + fmt.Sprintf("%d", len(toolCalls)))
		}
		part := strings.TrimSpace(strings.TrimSpace(role) + ": " + content)
		part = strings.Trim(part, ": ")
		if part != "" {
			parts = append(parts, truncateText(part, 120))
		}
		if len(parts) >= 3 {
			break
		}
	}
	return strings.Join(parts, " | ")
}

func extractMessageText(content any) string {
	switch v := content.(type) {
	case string:
		return normalizeWhitespace(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if block, ok := item.(map[string]any); ok {
				if text, ok := block["text"].(string); ok && text != "" {
					parts = append(parts, normalizeWhitespace(text))
				}
				if typ, _ := block["type"].(string); typ == "tool_use" {
					name, _ := block["name"].(string)
					parts = append(parts, "tool="+name)
				}
				continue
			}
			parts = append(parts, normalizeWhitespace(fmt.Sprint(item)))
		}
		return strings.Join(parts, " ")
	default:
		return normalizeWhitespace(fmt.Sprint(v))
	}
}

func extractResponseOutputText(msg map[string]any) string {
	content, ok := msg["content"].([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(content))
	for _, item := range content {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if text, ok := block["text"].(string); ok && text != "" {
			parts = append(parts, normalizeWhitespace(text))
			continue
		}
		if text, ok := block["output_text"].(string); ok && text != "" {
			parts = append(parts, normalizeWhitespace(text))
		}
	}
	return strings.Join(parts, " ")
}

func compactJSON(body []byte) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, body); err == nil {
		return buf.String()
	}
	return normalizeWhitespace(string(body))
}

func truncateText(s string, limit int) string {
	s = normalizeWhitespace(s)
	if len(s) <= limit {
		return s
	}
	if limit <= 1 {
		return s[:limit]
	}
	return s[:limit-1] + "…"
}

func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}