package relay

import "encoding/json"

// NormalizeOpenAIToolDefinitions coerces tool definitions into OpenAI-Chat
// nested shape: {"type":"function","function":{"name",...}}.
//
// Accepts Anthropic shape (name/input_schema), OpenAI-Responses flat shape
// (type+name+parameters at top level), and already-nested OpenAI-Chat shape.
// Mirrors services/llm-gateway/app/core/proxy/proxy.py _normalize_tools_for_litellm.
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
				out = append(out, map[string]any{
					"type":     "function",
					"function": fn,
				})
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
				out = append(out, map[string]any{"type": "function", "function": fn})
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
				out = append(out, map[string]any{"type": "function", "function": fn})
				continue
			}
		}
		out = append(out, tool)
	}
	return out
}

// OpenAIToolToAnthropic converts one OpenAI-Chat tool definition to Anthropic
// Messages shape (name/description/input_schema). Returns false when name is
// missing so callers can skip invalid entries instead of forwarding broken tools.
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

// SanitizeAnthropicToolDefinitions strips OpenAI/custom type wrappers from tool
// definitions before forwarding to anthropic-messages upstreams (MiniMax rejects
// tools carrying type:function or type:custom with error 2013 invalid tool type).
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
			out = append(out, anth)
		}
	}
	return out
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
	var tools []any
	if json.Unmarshal(raw, &tools) != nil || len(tools) == 0 {
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

// NormalizeToolsInChatBody normalizes tools[] to nested OpenAI-Chat shape.
func NormalizeToolsInChatBody(body []byte) []byte {
	return normalizeToolsInBody(body, NormalizeOpenAIToolDefinitions)
}

// SanitizeAnthropicToolsInBody strips invalid tool type fields for Anthropic upstream.
func SanitizeAnthropicToolsInBody(body []byte) []byte {
	return normalizeToolsInBody(body, SanitizeAnthropicToolDefinitions)
}
