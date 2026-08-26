package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ConvertChatRequestToAnthropic converts an OpenAI Chat Completions request
// body into Anthropic Messages format. Migrated from legacy relay so the
// new transformation package no longer depends on `_to-be-deprecated/relay`
// for Q3 request conversion.
func ConvertChatRequestToAnthropic(in []byte) ([]byte, error) {
	var src map[string]any
	if err := json.Unmarshal(in, &src); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	if err := validateChatMediaForAnthropic(src); err != nil {
		return nil, err
	}
	out := map[string]any{
		"model": src["model"],
	}
	if mt, ok := src["max_tokens"]; ok && mt != nil {
		out["max_tokens"] = mt
	} else {
		out["max_tokens"] = 4096
	}
	if s, ok := src["stream"]; ok {
		out["stream"] = s
	}
	if t, ok := src["temperature"]; ok {
		out["temperature"] = t
	}
	if tp, ok := src["top_p"]; ok {
		out["top_p"] = tp
	}
	if tk, ok := src["top_k"]; ok {
		out["top_k"] = tk
	}
	if stops, ok := src["stop"]; ok {
		out["stop_sequences"] = stops
	}

	if user, ok := src["user"].(string); ok && user != "" {
		out["metadata"] = map[string]any{
			"user_id": user,
		}
	}

	var systemContent string
	var anthropicMsgs []any
	if msgs, ok := src["messages"].([]any); ok {
		for _, msg := range msgs {
			msgMap, _ := msg.(map[string]any)
			role, _ := msgMap["role"].(string)
			if role == "system" {
				content, err := chatSystemContent(msgMap["content"])
				if err != nil {
					return nil, err
				}
				systemContent = joinNonEmpty(systemContent, content)
				continue
			}
			converted, err := convertChatMessageToAnthropic(msgMap)
			if err != nil {
				return nil, err
			}
			anthropicMsgs = append(anthropicMsgs, converted)
		}
	}
	if systemContent != "" {
		out["system"] = systemContent
	}
	out["messages"] = anthropicMsgs

	if tools, ok := src["tools"].([]any); ok {
		anthTools := make([]any, 0, len(tools))
		for _, tool := range tools {
			toolMap, _ := tool.(map[string]any)
			if anthropicTool, ok := openAIToolToAnthropic(toolMap); ok {
				anthTools = append(anthTools, anthropicTool)
				continue
			}
			// 2026-07-27 (F-1): openAIToolToAnthropic rejects non-function
			// tools (web_search, code_interpreter, file_search, ...). The
			// prior behavior silently dropped them, which is data loss for
			// agentic clients. Pass them through verbatim instead — Anthropic
			// will reject ones it doesn't understand, but that surfaces the
			// mismatch as an explicit upstream error rather than a silent
			// disappearance, and Anthropic-native built-ins (computer_use,
			// bash) routed via an OpenAI-protocol client survive.
			if toolType, _ := toolMap["type"].(string); toolType != "" && toolType != "function" {
				anthTools = append(anthTools, toolMap)
			}
		}
		if len(anthTools) > 0 {
			out["tools"] = anthTools
		}
	}
	if toolChoice, ok := src["tool_choice"]; ok {
		out["tool_choice"] = convertChatToolChoiceToAnthropic(toolChoice)
	}
	return json.Marshal(out)
}

func convertChatMessageToAnthropic(msg map[string]any) (map[string]any, error) {
	role, _ := msg["role"].(string)
	out := map[string]any{"role": role}
	content := msg["content"]
	switch typed := content.(type) {
	case string:
		out["content"] = typed
	case []any:
		blocks := make([]any, 0, len(typed))
		for _, block := range typed {
			blockMap, ok := block.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid OpenAI content block: expected object")
			}
			switch blockMap["type"] {
			case "text":
				if _, ok := blockMap["text"].(string); !ok {
					return nil, fmt.Errorf("invalid text content block: missing text")
				}
				blocks = append(blocks, map[string]any{"type": "text", "text": blockMap["text"]})
			case "image_url":
				if imageURL, ok := blockMap["image_url"].(map[string]any); ok {
					if url, ok := imageURL["url"].(string); ok {
						source := map[string]any{"type": "url", "url": url}
						if mediaType, data, ok := parseImageDataURI(url); ok {
							source = map[string]any{
								"type":       "base64",
								"media_type": mediaType,
								"data":       data,
							}
						}
						blocks = append(blocks, map[string]any{
							"type":   "image",
							"source": source,
						})
						continue
					}
				}
				return nil, fmt.Errorf("invalid image_url content block: missing url")
			default:
				return nil, fmt.Errorf("unsupported OpenAI content block: %v", blockMap["type"])
			}
		}
		out["content"] = blocks
	}
	if role == "tool" {
		if toolCallID, ok := msg["tool_call_id"].(string); ok {
			toolContent, _ := msg["content"].(string)
			out = map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": toolCallID,
						"content":     toolContent,
					},
				},
			}
		}
		return out, nil
	}
	if toolCalls, ok := msg["tool_calls"].([]any); ok {
		var existing []any
		switch current := out["content"].(type) {
		case []any:
			existing = current
		default:
			existing = []any{}
		}
		for _, toolCall := range toolCalls {
			toolCallMap, _ := toolCall.(map[string]any)
			function, _ := toolCallMap["function"].(map[string]any)
			if function == nil {
				return nil, fmt.Errorf("invalid tool call: missing function (tool_call_id=%v)", toolCallMap["id"])
			}
			argsStr, _ := function["arguments"].(string)
			var args any
			if !json.Valid([]byte(argsStr)) {
				return nil, fmt.Errorf("invalid tool arguments: tool_call_id=%v", toolCallMap["id"])
			}
			if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
				return nil, fmt.Errorf("unmarshal tool arguments failed: %w (tool_call_id=%v)", err, toolCallMap["id"])
			}
			if _, ok := args.(map[string]any); !ok {
				return nil, fmt.Errorf("invalid tool arguments: expected JSON object (tool_call_id=%v)", toolCallMap["id"])
			}
			existing = append(existing, map[string]any{
				"type":  "tool_use",
				"id":    toolCallMap["id"],
				"name":  function["name"],
				"input": args,
			})
		}
		out["content"] = existing
	}
	return out, nil
}

func chatSystemContent(content any) (string, error) {
	switch typed := content.(type) {
	case string:
		return typed, nil
	case []any:
		parts := make([]string, 0, len(typed))
		for _, raw := range typed {
			block, ok := raw.(map[string]any)
			if !ok {
				return "", fmt.Errorf("invalid system content block: expected object")
			}
			if block["type"] != "text" {
				return "", fmt.Errorf("unsupported OpenAI content block: %v", block["type"])
			}
			text, ok := block["text"].(string)
			if !ok {
				return "", fmt.Errorf("invalid system text content block: missing text")
			}
			parts = append(parts, text)
		}
		return strings.Join(parts, "\n"), nil
	default:
		return "", fmt.Errorf("invalid system content: expected string or text blocks")
	}
}

func joinNonEmpty(left, right string) string {
	if left == "" {
		return right
	}
	if right == "" {
		return left
	}
	return left + "\n" + right
}

func parseImageDataURI(value string) (mediaType, data string, ok bool) {
	if !strings.HasPrefix(value, "data:") {
		return "", "", false
	}
	comma := strings.IndexByte(value, ',')
	if comma <= len("data:") {
		return "", "", false
	}
	meta := strings.TrimPrefix(value[:comma], "data:")
	parts := strings.Split(meta, ";")
	if len(parts) < 2 || parts[len(parts)-1] != "base64" || parts[0] == "" {
		return "", "", false
	}
	return parts[0], value[comma+1:], true
}

func convertChatToolChoiceToAnthropic(toolChoice any) any {
	switch typed := toolChoice.(type) {
	case string:
		switch typed {
		case "auto":
			return map[string]any{"type": "auto"}
		case "none":
			return map[string]any{"type": "none"}
		case "required":
			return map[string]any{"type": "any"}
		}
	case map[string]any:
		if typed["type"] == "function" {
			if function, ok := typed["function"].(map[string]any); ok {
				if name, ok := function["name"].(string); ok {
					return map[string]any{"type": "tool", "name": name}
				}
			}
		}
	}
	return nil
}

func normalizeOpenAIToolDefinitions(tools []any) []any {
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
		if function, ok := tool["function"].(map[string]any); ok {
			if name, _ := function["name"].(string); name != "" {
				out = append(out, map[string]any{
					"type":     "function",
					"function": function,
				})
				continue
			}
		}
		if name, _ := tool["name"].(string); name != "" {
			if schema, hasSchema := tool["input_schema"]; hasSchema {
				function := map[string]any{"name": name}
				if description, ok := tool["description"].(string); ok && description != "" {
					function["description"] = description
				}
				if schema != nil {
					function["parameters"] = schema
				}
				out = append(out, map[string]any{"type": "function", "function": function})
				continue
			}
			if _, hasParams := tool["parameters"]; hasParams || tool["type"] == "function" {
				function := map[string]any{"name": name}
				if description, ok := tool["description"].(string); ok && description != "" {
					function["description"] = description
				}
				if parameters, ok := tool["parameters"]; ok {
					function["parameters"] = parameters
				} else {
					function["parameters"] = map[string]any{}
				}
				out = append(out, map[string]any{"type": "function", "function": function})
				continue
			}
		}
		out = append(out, tool)
	}
	return out
}

func openAIToolToAnthropic(tool map[string]any) (map[string]any, bool) {
	normalized := normalizeOpenAIToolDefinitions([]any{tool})
	if len(normalized) != 1 {
		return nil, false
	}
	toolMap, ok := normalized[0].(map[string]any)
	if !ok {
		return nil, false
	}
	function, _ := toolMap["function"].(map[string]any)
	if function == nil {
		return nil, false
	}
	name, _ := function["name"].(string)
	if name == "" {
		return nil, false
	}
	anthropicTool := map[string]any{"name": name}
	if description, ok := function["description"].(string); ok && description != "" {
		anthropicTool["description"] = description
	}
	if parameters, ok := function["parameters"]; ok {
		anthropicTool["input_schema"] = sanitizeInputSchema(parameters)
	} else {
		anthropicTool["input_schema"] = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return anthropicTool, true
}

// sanitizeInputSchema 修正 JSON Schema 中的常见格式错误，确保符合 Anthropic API 要求。
// 主要修正：
//  1. required 字段必须是字符串数组，不能是单个字符串
//  2. 递归处理嵌套的 properties 和 items
//
// 背景：claude-opus-4-8 对 input_schema.required 进行严格验证，要求必须是数组格式。
// 某些客户端或 SDK 可能发送错误格式（单个字符串或混合类型数组），导致 400 错误。
func sanitizeInputSchema(schema any) any {
	schemaMap, ok := schema.(map[string]any)
	if !ok {
		return schema
	}

	// 修正 required 字段
	if required, exists := schemaMap["required"]; exists && required != nil {
		switch r := required.(type) {
		case string:
			// 单个字符串 → 数组
			schemaMap["required"] = []string{r}
		case []any:
			// 确保所有元素都是字符串
			strArray := make([]string, 0, len(r))
			for _, v := range r {
				if s, ok := v.(string); ok {
					strArray = append(strArray, s)
				}
			}
			schemaMap["required"] = strArray
		case []string:
			// 已经是正确格式，保持不变
		default:
			// 其他类型（如数字、布尔），删除该字段
			delete(schemaMap, "required")
		}
	}

	// 递归处理 properties
	if properties, ok := schemaMap["properties"].(map[string]any); ok {
		for key, prop := range properties {
			properties[key] = sanitizeInputSchema(prop)
		}
	}

	// 递归处理 items (数组类型)
	if items, ok := schemaMap["items"]; ok {
		schemaMap["items"] = sanitizeInputSchema(items)
	}

	// 递归处理 additionalProperties
	if additionalProps, ok := schemaMap["additionalProperties"]; ok {
		if additionalPropsMap, isMap := additionalProps.(map[string]any); isMap {
			schemaMap["additionalProperties"] = sanitizeInputSchema(additionalPropsMap)
		}
	}

	return schemaMap
}

func validateChatMediaForAnthropic(src map[string]any) error {
	messages, _ := src["messages"].([]any)
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		blocks, _ := message["content"].([]any)
		for _, rawBlock := range blocks {
			block, _ := rawBlock.(map[string]any)
			typ, _ := block["type"].(string)
			switch typ {
			case "input_audio", "audio", "video", "video_url":
				return fmt.Errorf("unsupported_modality: Anthropic Messages cannot represent %s content", typ)
			case "file", "input_file":
				return fmt.Errorf("unsupported_modality: Anthropic Messages cannot represent %s content", typ)
			}
		}
	}
	return nil
}
