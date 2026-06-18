package admin

import (
	"encoding/json"
	"fmt"
	"strings"
)

// TurnDisplay is the normalized per-request view: latest user input plus
// assistant plain text and a compact tool-call summary.
type TurnDisplay struct {
	UserTurn      string
	AssistantText string
	ToolSummary   string
}

func extractTurnDisplay(requestBody, responseBody, requestPreview, responsePreview *string) TurnDisplay {
	var out TurnDisplay
	if requestBody != nil && strings.TrimSpace(*requestBody) != "" {
		out.UserTurn = extractLatestUserFromRequestJSON([]byte(*requestBody))
	}
	if out.UserTurn == "" && requestPreview != nil {
		out.UserTurn = latestUserFromPreview(*requestPreview)
	}
	if out.UserTurn == "" && requestPreview != nil {
		out.UserTurn = strings.TrimSpace(*requestPreview)
	}

	if responseBody != nil && strings.TrimSpace(*responseBody) != "" {
		out.AssistantText, out.ToolSummary = extractAssistantFromResponseJSON([]byte(*responseBody))
	}
	if out.AssistantText == "" && out.ToolSummary == "" && responsePreview != nil {
		out.AssistantText, out.ToolSummary = assistantFromPreview(*responsePreview)
	}
	if out.AssistantText == "" && responsePreview != nil {
		out.AssistantText = strings.TrimSpace(*responsePreview)
	}
	return out
}

func enrichSessionMessageFields(msg map[string]any, requestBody, responseBody, requestPreview, responsePreview *string) {
	d := extractTurnDisplay(requestBody, responseBody, requestPreview, responsePreview)
	if d.UserTurn != "" {
		msg["user_turn"] = d.UserTurn
	}
	if d.AssistantText != "" {
		msg["assistant_text"] = d.AssistantText
	}
	if d.ToolSummary != "" {
		msg["tool_summary"] = d.ToolSummary
	}
}

func buildTurnCorpusLine(ts string, d TurnDisplay, status string, errKind *string) string {
	var b strings.Builder
	if d.UserTurn != "" {
		fmt.Fprintf(&b, "[%s][user] %s\n", ts, sanitizeSummaryText(d.UserTurn))
	}
	asst := strings.TrimSpace(d.AssistantText)
	if d.ToolSummary != "" {
		if asst != "" {
			asst += " | " + d.ToolSummary
		} else {
			asst = d.ToolSummary
		}
	}
	if asst != "" {
		fmt.Fprintf(&b, "[%s][assistant] %s\n", ts, sanitizeSummaryText(asst))
	}
	if status == "failure" && errKind != nil && strings.TrimSpace(*errKind) != "" {
		fmt.Fprintf(&b, "[%s][status] failure:%s\n", ts, strings.TrimSpace(*errKind))
	}
	return b.String()
}

func extractLatestUserFromRequestJSON(body []byte) string {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return ""
	}
	messages, ok := root["messages"].([]any)
	if !ok || len(messages) == 0 {
		if prompt, ok := root["prompt"].(string); ok && strings.TrimSpace(prompt) != "" {
			return normalizeDisplayWhitespace(prompt)
		}
		if input, ok := root["input"].(string); ok && strings.TrimSpace(input) != "" {
			return normalizeDisplayWhitespace(input)
		}
		return ""
	}
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if strings.EqualFold(strings.TrimSpace(role), "user") {
			text := extractMessageContentText(msg["content"])
			if text != "" {
				return text
			}
		}
	}
	return ""
}

func extractAssistantFromResponseJSON(body []byte) (text string, toolSummary string) {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return "", ""
	}
	if choices, ok := root["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if msg, ok := choice["message"].(map[string]any); ok {
				text = extractMessageContentText(msg["content"])
				toolSummary = summarizeToolCalls(msg["tool_calls"])
			}
			if delta, ok := choice["delta"].(map[string]any); ok && text == "" {
				text = extractMessageContentText(delta["content"])
				if toolSummary == "" {
					toolSummary = summarizeToolCalls(delta["tool_calls"])
				}
			}
		}
	}
	if text == "" {
		if output, ok := root["output"].([]any); ok {
			parts := make([]string, 0, len(output))
			for _, item := range output {
				block, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if t := extractMessageContentText(block["content"]); t != "" {
					parts = append(parts, t)
				} else if t, ok := block["text"].(string); ok && t != "" {
					parts = append(parts, normalizeDisplayWhitespace(t))
				}
			}
			text = strings.Join(parts, " ")
		}
	}
	return normalizeDisplayWhitespace(text), toolSummary
}

func extractMessageContentText(content any) string {
	switch v := content.(type) {
	case string:
		return normalizeDisplayWhitespace(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			switch block := item.(type) {
			case map[string]any:
				if text, ok := block["text"].(string); ok && strings.TrimSpace(text) != "" {
					parts = append(parts, normalizeDisplayWhitespace(text))
					continue
				}
				if out, ok := block["output_text"].(string); ok && strings.TrimSpace(out) != "" {
					parts = append(parts, normalizeDisplayWhitespace(out))
					continue
				}
				if typ, _ := block["type"].(string); typ == "tool_use" || typ == "tool_result" {
					name, _ := block["name"].(string)
					if name != "" {
						parts = append(parts, "tool="+name)
					}
				}
			default:
				s := strings.TrimSpace(fmt.Sprint(item))
				if s != "" && s != "<nil>" {
					parts = append(parts, s)
				}
			}
		}
		return strings.Join(parts, " ")
	default:
		s := strings.TrimSpace(fmt.Sprint(v))
		if s == "" || s == "<nil>" {
			return ""
		}
		return normalizeDisplayWhitespace(s)
	}
}

func summarizeToolCalls(raw any) string {
	calls, ok := raw.([]any)
	if !ok || len(calls) == 0 {
		return ""
	}
	names := make([]string, 0, len(calls))
	for _, item := range calls {
		call, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := ""
		if fn, ok := call["function"].(map[string]any); ok {
			name, _ = fn["name"].(string)
		}
		if name == "" {
			name, _ = call["name"].(string)
		}
		if name == "" {
			name, _ = call["type"].(string)
		}
		if name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return fmt.Sprintf("工具调用 %d 次", len(calls))
	}
	if len(names) == 1 {
		return "工具: " + names[0]
	}
	return fmt.Sprintf("工具: %s 等 %d 个", strings.Join(names[:minInt(3, len(names))], ", "), len(names))
}

func latestUserFromPreview(preview string) string {
	preview = strings.TrimSpace(preview)
	if preview == "" {
		return ""
	}
	parts := strings.Split(preview, " | ")
	for i := len(parts) - 1; i >= 0; i-- {
		p := strings.TrimSpace(parts[i])
		lower := strings.ToLower(p)
		for _, prefix := range []string{"user:", "human:"} {
			if strings.HasPrefix(lower, prefix) {
				return normalizeDisplayWhitespace(strings.TrimSpace(p[len(prefix):]))
			}
		}
	}
	return ""
}

func assistantFromPreview(preview string) (text string, toolSummary string) {
	preview = strings.TrimSpace(preview)
	if preview == "" {
		return "", ""
	}
	parts := strings.Split(preview, " | ")
	for i := len(parts) - 1; i >= 0; i-- {
		p := strings.TrimSpace(parts[i])
		lower := strings.ToLower(p)
		if strings.HasPrefix(lower, "assistant:") {
			text = normalizeDisplayWhitespace(strings.TrimSpace(p[len("assistant:"):]))
			break
		}
	}
	if strings.Contains(strings.ToLower(preview), "tools=") {
		toolSummary = "含工具调用"
	}
	return text, toolSummary
}

func normalizeDisplayWhitespace(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func parseNoTopicVirtualTaskID(taskID string) (hourStart string, ok bool) {
	taskID = strings.TrimSpace(taskID)
	if !strings.HasPrefix(taskID, "notopic:") {
		return "", false
	}
	parts := strings.SplitN(taskID, ":", 3)
	if len(parts) < 3 || strings.TrimSpace(parts[2]) == "" {
		return "", false
	}
	return strings.TrimSpace(parts[2]), true
}
