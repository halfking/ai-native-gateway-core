package caveman

// message.go 翻译自 messageContent.ts。处理 chat message 的 string/array content。

// extractTextContent 把 message content 抽成纯文本。对齐 messageContent.ts:25-36。
// string → 原样；非 array → ""；array → 所有 text part 的 text 用 \n 连接。
func extractTextContent(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, p := range c {
			if tb, ok := p.(map[string]any); ok {
				if t, ok := tb["text"].(string); ok {
					// isTextBlock: type 未定义或 "text" 或 "input_text"（对齐 messageContent.ts:13-23）。
					tp, _ := tb["type"].(string)
					if tp == "" || tp == "text" || tp == "input_text" {
						parts = append(parts, t)
					}
				}
			}
		}
		return joinStrings(parts, "\n")
	}
	return ""
}

// mapTextContent 对 message 的 text 部分应用 transform，返回 (新message, 是否改动)。
// 对齐 messageContent.ts:38-59。
//   - string content → transform(content)
//   - 非 array content → 原样返回 (false)
//   - array content → 对每个 text part 调 transform，非 text part 原样保留；
//     若无改动则原样返回 (false)，避免无谓分配。
func mapTextContent(msg map[string]any, transform func(text string, index int) string) (map[string]any, bool) {
	switch content := msg["content"].(type) {
	case string:
		newContent := transform(content, 0)
		if newContent == content {
			return msg, false
		}
		out := copyMsgMap(msg)
		out["content"] = newContent
		return out, true
	case []any:
		textIndex := 0
		changed := false
		newContent := make([]any, len(content))
		for i, p := range content {
			tb, ok := p.(map[string]any)
			if !ok {
				newContent[i] = p
				continue
			}
			t, hasText := tb["text"].(string)
			tp, _ := tb["type"].(string)
			isText := hasText && (tp == "" || tp == "text" || tp == "input_text")
			if !isText {
				newContent[i] = p
				continue
			}
			newText := transform(t, textIndex)
			textIndex++
			if newText == t {
				newContent[i] = p
			} else {
				changed = true
				nb := copyMsgMap(tb)
				nb["text"] = newText
				newContent[i] = nb
			}
		}
		if !changed {
			return msg, false
		}
		out := copyMsgMap(msg)
		out["content"] = newContent
		return out, true
	}
	return msg, false
}

// copyMsgMap 浅拷贝 map。
func copyMsgMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// joinStrings 用 sep 连接，避免引入 strings（本文件保持独立）。
func joinStrings(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		return parts[0]
	}
	total := 0
	for _, p := range parts {
		total += len(p)
	}
	total += len(sep) * (len(parts) - 1)
	b := make([]byte, 0, total)
	b = append(b, parts[0]...)
	for _, p := range parts[1:] {
		b = append(b, sep...)
		b = append(b, p...)
	}
	return string(b)
}
