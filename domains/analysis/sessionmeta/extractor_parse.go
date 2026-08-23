package sessionmeta

import (
	"encoding/json"
	"strings"
)

func contentText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return cleanText(text)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
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
		if m.Role == "tool" || m.Role == "function" {
			continue
		}
		text := cleanText(m.Content)
		if text == "" {
			continue
		}
		parts = append(parts, m.Role+": "+text)
		if m.Role == "system" && system == "" {
			system = text
		}
		if m.Role == "user" {
			user = text
		}
	}
	if cleanText(systemPrompt) != "" {
		system = cleanText(systemPrompt)
		parts = append(parts, "system: "+system)
	}
	if cleanText(userText) != "" {
		user = cleanText(userText)
		parts = append(parts, "user: "+user)
	}
	return strings.Join(parts, "\n"), system, user
}

func firstSystem(messages []Message) string {
	for _, m := range messages {
		if m.Role == "system" {
			return cleanText(m.Content)
		}
	}
	return ""
}

func lastUser(messages []Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return cleanText(messages[i].Content)
		}
	}
	return ""
}
