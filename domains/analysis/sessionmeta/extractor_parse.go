package sessionmeta

import (
	"encoding/json"
	"strings"
)

func contentText(raw json.RawMessage) string {
	var text string
	if decodeSingleJSON(raw, &text) == nil {
		return cleanText(text)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if decodeSingleJSON(raw, &blocks) != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Type == "text" || b.Type == "" {
			if v := cleanText(b.Text); v != "" {
				parts = append(parts, v)
			}
		}
	}
	return strings.Join(parts, " ")
}

func corpusParts(messages []Message, systemPrompt, userText string) (corpus, system, user string) {
	parts := make([]string, 0, len(messages)+2)
	for _, m := range messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role == "developer" {
			role = "system"
		}
		if role == "tool" || role == "function" {
			continue
		}
		text := cleanText(m.Content)
		if text == "" {
			continue
		}
		if role == "system" {
			text = truncateRunes(text, MaxSystemRunes)
		}
		parts = append(parts, role+": "+text)
		if role == "system" && system == "" {
			system = text
		}
		if role == "user" {
			user = text
		}
	}
	if systemPrompt = truncateRunes(cleanText(systemPrompt), MaxSystemRunes); systemPrompt != "" {
		system = systemPrompt
		parts = append(parts, "system: "+system)
	}
	if userText = cleanText(userText); userText != "" {
		user = userText
		parts = append(parts, "user: "+user)
	}
	return strings.Join(parts, "\n"), system, user
}

func firstSystem(messages []Message) string {
	for _, m := range messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role == "developer" {
			role = "system"
		}
		if role == "system" {
			return truncateRunes(cleanText(m.Content), MaxSystemRunes)
		}
	}
	return ""
}

func lastUser(messages []Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.ToLower(strings.TrimSpace(messages[i].Role)) == "user" {
			return cleanText(messages[i].Content)
		}
	}
	return ""
}
