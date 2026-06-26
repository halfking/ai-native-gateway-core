package memory

import (
	"encoding/json"
	"fmt"
	"strings"
)

const compactionSnippetPrefix = "[Gateway injected Memora context — earlier task-relevant facts, retrieved by L1 search. " +
	"These are stable, verified facts the upstream may not have in its own context window. " +
	"Treat as ground truth and do not paraphrase unless asked.]\n"

func RebuildBodyWithMemoraSnippets(origBody []byte, snippets []Memory, keepRecentPairs int) ([]byte, bool) {
	if len(snippets) == 0 {
		return nil, false
	}
	if keepRecentPairs <= 0 {
		keepRecentPairs = 2
	}

	var probe struct {
		Model    string          `json:"model"`
		Messages json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(origBody, &probe); err != nil {
		return nil, false
	}
	if len(probe.Messages) == 0 {
		return nil, false
	}

	allMsgs := decodeMessages(probe.Messages)
	if len(allMsgs) == 0 {
		return nil, false
	}
	headMsgs, tailMsgs := splitSystemAndTail(allMsgs, keepRecentPairs*2)
	if len(headMsgs) == 0 && len(tailMsgs) == 0 {
		return nil, false
	}

	plainText := buildPlainText(snippets)
	if strings.TrimSpace(plainText) == "" {
		return nil, false
	}
	dynCtx, err := json.Marshal(map[string]any{
		"role":    "user",
		"content": compactionSnippetPrefix + plainText,
	})
	if err != nil {
		return nil, false
	}

	merged := make([]json.RawMessage, 0, 2+len(tailMsgs))
	merged = append(merged, headMsgs...)
	merged = append(merged, dynCtx)
	merged = append(merged, tailMsgs...)
	newMsgs, err := json.Marshal(merged)
	if err != nil {
		return nil, false
	}

	spliced, ok := spliceMessagesRaw(origBody, newMsgs)
	if !ok {
		return nil, false
	}
	return spliced, true
}

func buildPlainText(snippets []Memory) string {
	var b strings.Builder
	for i, s := range snippets {
		if strings.TrimSpace(s.Text) == "" {
			continue
		}
		if i > 0 {
			b.WriteString("\n---\n")
		}
		fmt.Fprintf(&b, "fact %d: %s", i+1, s.Text)
	}
	return b.String()
}

func decodeMessages(raw json.RawMessage) []json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var out []json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func splitSystemAndTail(msgs []json.RawMessage, tailMax int) (head, tail []json.RawMessage) {
	i := 0
	for i < len(msgs) && isSystemMessage(msgs[i]) {
		head = append(head, msgs[i])
		i++
	}
	rest := msgs[i:]
	if len(rest) <= tailMax {
		return head, rest
	}
	return head, rest[len(rest)-tailMax:]
}

func isSystemMessage(raw json.RawMessage) bool {
	var probe struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return probe.Role == "system"
}

func spliceMessagesRaw(origBody, newMsgs []byte) ([]byte, bool) {
	key := []byte(`"messages"`)
	idx := indexOfKey(origBody, key)
	if idx < 0 {
		return nil, false
	}
	i := idx + len(key)
	for i < len(origBody) && (origBody[i] == ' ' || origBody[i] == '\t' || origBody[i] == '\n' || origBody[i] == '\r') {
		i++
	}
	if i >= len(origBody) || origBody[i] != ':' {
		return nil, false
	}
	i++
	for i < len(origBody) && (origBody[i] == ' ' || origBody[i] == '\t' || origBody[i] == '\n' || origBody[i] == '\r') {
		i++
	}
	if i >= len(origBody) || origBody[i] != '[' {
		return nil, false
	}
	end := i + 1
	depth := 1
	inStr := false
	escape := false
	for end < len(origBody) {
		c := origBody[end]
		if inStr {
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == '"' {
				inStr = false
			}
		} else {
			switch c {
			case '"':
				inStr = true
			case '[':
				depth++
			case ']':
				depth--
				if depth == 0 {
					goto found
				}
			}
		}
		end++
	}
	return nil, false
found:
	var out []byte
	out = append(out, origBody[:i]...)
	out = append(out, newMsgs...)
	out = append(out, origBody[end+1:]...)
	return out, true
}

func indexOfKey(body, key []byte) int {
	for i := 0; i+len(key) <= len(body); {
		if body[i] == '"' && i+len(key) < len(body) && string(body[i:i+len(key)]) == string(key) {
			if i > 0 {
				k := i - 1
				for k > 0 && (body[k] == ' ' || body[k] == '\t' || body[k] == '\n' || body[k] == '\r') {
					k--
				}
				if body[k] != '{' && body[k] != ',' {
					i++
					continue
				}
			}
			j := i + len(key)
			for j < len(body) && (body[j] == ' ' || body[j] == '\t' || body[j] == '\n' || body[j] == '\r') {
				j++
			}
			if j < len(body) && body[j] == ':' {
				return i
			}
		}
		i++
	}
	return -1
}
