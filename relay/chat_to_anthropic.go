package relay

import (
	"encoding/json"
	"fmt"
)

// ConvertChatRequestToAnthropic converts an OpenAI Chat Completions
// request body into Anthropic Messages format. Used for Q3 (openai
// client -> anthropic upstream).
//
// Supported fields:
//   - model (passthrough)
//   - max_tokens (passthrough)
//   - stream (passthrough)
//   - temperature, top_p, top_k (passthrough)
//   - stop -> stop_sequences
//   - messages: system -> top-level system; user/assistant/text/image_url
//   - tools: function type with name/description/parameters -> name/description/input_schema
//   - tool_choice: string "auto"/"none" or function-name object
//
// NOT supported (YAGNI):
//   - logprobs, top_logprobs
//   - n>1 (multiple completions)
//   - response_format
//   - seed
//   - user
func ConvertChatRequestToAnthropic(in []byte) ([]byte, error) {
	var src map[string]any
	if err := json.Unmarshal(in, &src); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	out := map[string]any{
		"model":      src["model"],
		"max_tokens": src["max_tokens"],
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

	var systemContent string
	var anthropicMsgs []any
	if msgs, ok := src["messages"].([]any); ok {
		for _, m := range msgs {
			mm, _ := m.(map[string]any)
			role, _ := mm["role"].(string)
			if role == "system" {
				if s, ok := mm["content"].(string); ok {
					systemContent = s
				}
				continue
			}
			am := convertChatMessageToAnthropic(mm)
			anthropicMsgs = append(anthropicMsgs, am)
		}
	}
	if systemContent != "" {
		out["system"] = systemContent
	}
	out["messages"] = anthropicMsgs

	if tools, ok := src["tools"].([]any); ok {
		anthTools := make([]any, 0, len(tools))
		for _, t := range tools {
			tm, _ := t.(map[string]any)
			if tm["type"] == "function" {
				fn, _ := tm["function"].(map[string]any)
				anthTool := map[string]any{"name": fn["name"]}
				if d, ok := fn["description"].(string); ok && d != "" {
					anthTool["description"] = d
				}
				if p, ok := fn["parameters"]; ok {
					anthTool["input_schema"] = p
				}
				anthTools = append(anthTools, anthTool)
			}
		}
		if len(anthTools) > 0 {
			out["tools"] = anthTools
		}
	}
	if tc, ok := src["tool_choice"]; ok {
		out["tool_choice"] = convertChatToolChoiceToAnthropic(tc)
	}
	return json.Marshal(out)
}

func convertChatMessageToAnthropic(m map[string]any) map[string]any {
	role, _ := m["role"].(string)
	out := map[string]any{"role": role}
	content := m["content"]
	switch c := content.(type) {
	case string:
		out["content"] = c
	case []any:
		blocks := make([]any, 0, len(c))
		for _, b := range c {
			bb, _ := b.(map[string]any)
			switch bb["type"] {
			case "text":
				blocks = append(blocks, map[string]any{"type": "text", "text": bb["text"]})
			case "image_url":
				if iu, ok := bb["image_url"].(map[string]any); ok {
					if u, ok := iu["url"].(string); ok {
						blocks = append(blocks, map[string]any{
							"type":   "image",
							"source": map[string]any{"type": "url", "url": u},
						})
					}
				}
			}
		}
		out["content"] = blocks
	}
	if role == "tool" {
		if tcID, ok := m["tool_call_id"].(string); ok {
			tcContent, _ := m["content"].(string)
			out = map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": tcID,
						"content":     tcContent,
					},
				},
			}
		}
		return out
	}
	if tcs, ok := m["tool_calls"].([]any); ok {
		var existing []any
		switch e := out["content"].(type) {
		case []any:
			existing = e
		default:
			existing = []any{}
		}
		for _, tc := range tcs {
			tcm, _ := tc.(map[string]any)
			fn, _ := tcm["function"].(map[string]any)
			argsStr, _ := fn["arguments"].(string)
			var args any
			if json.Unmarshal([]byte(argsStr), &args) != nil {
				args = map[string]any{}
			}
			existing = append(existing, map[string]any{
				"type":  "tool_use",
				"id":    tcm["id"],
				"name":  fn["name"],
				"input": args,
			})
		}
		out["content"] = existing
	}
	return out
}

func convertChatToolChoiceToAnthropic(tc any) any {
	switch v := tc.(type) {
	case string:
		switch v {
		case "auto":
			return map[string]any{"type": "auto"}
		case "none":
			return map[string]any{"type": "none"}
		case "required":
			return map[string]any{"type": "any"}
		}
	case map[string]any:
		if v["type"] == "function" {
			if fn, ok := v["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok {
					return map[string]any{"type": "tool", "name": name}
				}
			}
		}
	}
	return nil
}
