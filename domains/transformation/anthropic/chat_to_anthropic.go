package anthropic

import (
	"encoding/json"
	"fmt"
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
				if system, ok := msgMap["content"].(string); ok {
					systemContent = system
				}
				continue
			}
			anthropicMsgs = append(anthropicMsgs, convertChatMessageToAnthropic(msgMap))
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

func convertChatMessageToAnthropic(msg map[string]any) map[string]any {
	role, _ := msg["role"].(string)
	out := map[string]any{"role": role}
	content := msg["content"]
	switch typed := content.(type) {
	case string:
		out["content"] = typed
	case []any:
		blocks := make([]any, 0, len(typed))
		for _, block := range typed {
			blockMap, _ := block.(map[string]any)
			switch blockMap["type"] {
			case "text":
				blocks = append(blocks, map[string]any{"type": "text", "text": blockMap["text"]})
			case "image_url":
				if imageURL, ok := blockMap["image_url"].(map[string]any); ok {
					if url, ok := imageURL["url"].(string); ok {
						blocks = append(blocks, map[string]any{
							"type":   "image",
							"source": map[string]any{"type": "url", "url": url},
						})
					}
				}
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
		return out
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
			argsStr, _ := function["arguments"].(string)
			var args any
			if json.Unmarshal([]byte(argsStr), &args) != nil {
				args = map[string]any{}
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
	return out
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
		anthropicTool["input_schema"] = parameters
	} else {
		anthropicTool["input_schema"] = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return anthropicTool, true
}
