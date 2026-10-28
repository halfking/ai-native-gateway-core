package streaming

import (
	"bytes"
	"encoding/json"
)

func NormalizeOpenAIToolDefinitions(tools []any) []any {
	if len(tools) == 0 {
		return tools
	}
	out := make([]any, 0, len(tools))
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)
			continue
		}
		if fn, ok := tool["function"].(map[string]any); ok {
			if name, _ := fn["name"].(string); name != "" {
				// RISK-2（2026-10-29）：重建时保留顶层非标准键（不覆盖
				// type/function）—— 对所有客户端一致，网关不做静默裁剪。
				rebuilt := map[string]any{
					"type":     "function",
					"function": fn,
				}
				copyToolExtras(rebuilt, tool, "type", "function")
				out = append(out, rebuilt)
				continue
			}
		}
		if name, _ := tool["name"].(string); name != "" {
			if schema, hasSchema := tool["input_schema"]; hasSchema {
				fn := map[string]any{"name": name}
				if d, ok := tool["description"].(string); ok && d != "" {
					fn["description"] = d
				}
				if schema != nil {
					fn["parameters"] = schema
				}
				rebuilt := map[string]any{"type": "function", "function": fn}
				copyToolExtras(rebuilt, tool, "type", "function", "name", "description", "input_schema", "parameters")
				out = append(out, rebuilt)
				continue
			}
			if _, hasParams := tool["parameters"]; hasParams || tool["type"] == "function" {
				fn := map[string]any{"name": name}
				if d, ok := tool["description"].(string); ok && d != "" {
					fn["description"] = d
				}
				if p, ok := tool["parameters"]; ok {
					fn["parameters"] = p
				} else {
					fn["parameters"] = map[string]any{}
				}
				rebuilt := map[string]any{"type": "function", "function": fn}
				copyToolExtras(rebuilt, tool, "type", "function", "name", "description", "input_schema", "parameters")
				out = append(out, rebuilt)
				continue
			}
		}
		out = append(out, tool)
	}
	return out
}

func OpenAIToolToAnthropic(tool map[string]any) (map[string]any, bool) {
	normalized := NormalizeOpenAIToolDefinitions([]any{tool})
	if len(normalized) != 1 {
		return nil, false
	}
	tm, ok := normalized[0].(map[string]any)
	if !ok {
		return nil, false
	}
	fn, _ := tm["function"].(map[string]any)
	if fn == nil {
		return nil, false
	}
	name, _ := fn["name"].(string)
	if name == "" {
		return nil, false
	}
	anth := map[string]any{"name": name}
	if d, ok := fn["description"].(string); ok && d != "" {
		anth["description"] = d
	}
	if p, ok := fn["parameters"]; ok {
		anth["input_schema"] = p
	} else {
		anth["input_schema"] = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return anth, true
}

func SanitizeAnthropicToolDefinitions(tools []any) []any {
	if len(tools) == 0 {
		return tools
	}
	out := make([]any, 0, len(tools))
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)
			continue
		}
		if anth, ok := OpenAIToolToAnthropic(tool); ok {
			// RISK-2（2026-10-29）：包裹形工具的顶层非标准键原样保留
			//（type/function 已被消费展开）。
			copyToolExtras(anth, tool, "type", "function")
			out = append(out, anth)
			continue
		}
		if name, _ := tool["name"].(string); name != "" {
			anth := map[string]any{"name": name}
			if d, ok := tool["description"].(string); ok && d != "" {
				anth["description"] = d
			}
			if s, ok := tool["input_schema"]; ok {
				anth["input_schema"] = s
			} else if p, ok := tool["parameters"]; ok {
				anth["input_schema"] = p
			}
			// RISK-2（2026-10-29）：平铺形工具的顶层非标准键原样保留
			//（name/description/input_schema/parameters 已被消费翻译）。
			copyToolExtras(anth, tool, "name", "description", "input_schema", "parameters")
			out = append(out, anth)
		}
	}
	return out
}

// copyToolExtras RISK-2（2026-10-29）：把消费集之外的顶层键原样拷进
// 重建后的工具对象（不覆盖已写入键）。与 paramreg「未知字段 fail-open」
// 同一哲学：网关不做静默裁剪。
func copyToolExtras(dst, tool map[string]any, consumed ...string) {
	skip := make(map[string]bool, len(consumed))
	for _, k := range consumed {
		skip[k] = true
	}
	for k, v := range tool {
		if skip[k] {
			continue
		}
		if _, exists := dst[k]; exists {
			continue
		}
		dst[k] = v
	}
}

func normalizeToolsInBody(body []byte, normalize func([]any) []any) []byte {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return body
	}
	raw, ok := obj["tools"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return body
	}
	// UseNumber：工具定义里的数字（参数约束、extra 键的值）按原始字面量
	// 保真（anthropic_message_fix.go 同款纪律 —— 工具输入不过 float64）。
	var tools []any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&tools) != nil || len(tools) == 0 {
		return body
	}
	normalized := normalize(tools)
	if len(normalized) == 0 {
		delete(obj, "tools")
	} else {
		b, err := json.Marshal(normalized)
		if err != nil {
			return body
		}
		obj["tools"] = b
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}

func NormalizeToolsInChatBody(body []byte) []byte {
	return normalizeToolsInBody(body, NormalizeOpenAIToolDefinitions)
}

func SanitizeAnthropicToolsInBody(body []byte) []byte {
	return normalizeToolsInBody(body, SanitizeAnthropicToolDefinitions)
}
