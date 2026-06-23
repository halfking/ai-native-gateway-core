package ir

import (
	"encoding/json"
	"fmt"
)

// SerializeOpenAI serializes an InternalRequest into an OpenAI Chat Completions request body.
func SerializeOpenAI(req *InternalRequest) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("request is nil")
	}

	out := map[string]any{
		"model": req.Model,
	}

	// MaxTokens (prefer the explicit field, note: OpenAI uses max_tokens not max_completion_tokens for Chat)
	if req.MaxTokens > 0 {
		out["max_tokens"] = req.MaxTokens
	}

	// Streaming
	if req.Stream {
		out["stream"] = true
	}

	// Sampling parameters
	if req.Temperature != nil {
		out["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		out["top_p"] = *req.TopP
	}

	// Stop sequences
	if len(req.Stop) > 0 {
		out["stop"] = req.Stop
	}

	// OpenAI-only fields
	if req.FrequencyPenalty != nil {
		out["frequency_penalty"] = *req.FrequencyPenalty
	}
	if req.PresencePenalty != nil {
		out["presence_penalty"] = *req.PresencePenalty
	}
	if req.Logprobs != nil {
		out["logprobs"] = *req.Logprobs
	}
	if req.TopLogprobs != nil {
		out["top_logprobs"] = *req.TopLogprobs
	}
	if req.Seed != nil {
		out["seed"] = *req.Seed
	}
	if req.N > 0 {
		out["n"] = req.N
	}
	if req.User != "" {
		out["user"] = req.User
	}

	// Response format
	if req.ResponseFormat != nil {
		rf := map[string]any{"type": req.ResponseFormat.Type}
		if req.ResponseFormat.Schema != nil {
			rf["json_schema"] = req.ResponseFormat.Schema
		}
		out["response_format"] = rf
	}

	// Messages (system prompt becomes first message)
	messages := serializeOpenAIMessages(req)
	if len(messages) > 0 {
		out["messages"] = messages
	}

	// Tools
	if len(req.Tools) > 0 {
		tools := serializeOpenAITools(req.Tools)
		out["tools"] = tools
	}

	// Tool choice
	if req.ToolChoice != nil {
		out["tool_choice"] = serializeOpenAIToolChoice(req.ToolChoice)
	}

	return json.Marshal(out)
}

// serializeOpenAIMessages converts IR messages to OpenAI format.
func serializeOpenAIMessages(req *InternalRequest) []map[string]any {
	messages := make([]map[string]any, 0, len(req.Messages)+1)

	// Prepend system message if present
	if req.System != nil && req.System.Content != "" {
		messages = append(messages, map[string]any{
			"role":    "system",
			"content": req.System.Content,
		})
	}

	// Convert each message
	for _, msg := range req.Messages {
		messages = append(messages, serializeOpenAIMessage(msg))
	}

	return messages
}

// serializeOpenAIMessage converts a single IR Message to OpenAI format.
func serializeOpenAIMessage(msg Message) map[string]any {
	out := map[string]any{
		"role": msg.Role,
	}

	// Special handling for tool role messages
	// Anthropic sends tool results as user messages with tool_result content blocks
	// OpenAI expects tool role messages with tool_call_id and content
	if msg.Role == "user" && len(msg.Content) > 0 {
		// Check if this is actually a tool result (Anthropic format)
		for _, block := range msg.Content {
			if block.Type == "tool_result" && block.ToolResult != nil {
				// Convert to OpenAI tool message
				out["role"] = "tool"
				out["tool_call_id"] = block.ToolResult.ToolUseID

				// Extract text content from tool result
				var textParts []string
				for _, cb := range block.ToolResult.Content {
					if cb.Type == "text" {
						textParts = append(textParts, cb.Text)
					}
				}

				if len(textParts) > 0 {
					out["content"] = joinTextParts(textParts)
				} else {
					out["content"] = ""
				}

				// Optional name field
				if msg.Name != "" {
					out["name"] = msg.Name
				}

				return out
			}
		}
	}

	// Handle tool role with explicit ToolCallID
	if msg.Role == "tool" {
		if msg.ToolCallID != "" {
			out["tool_call_id"] = msg.ToolCallID
		}
		if msg.Name != "" {
			out["name"] = msg.Name
		}

		// Extract content from blocks
		if len(msg.Content) > 0 {
			if len(msg.Content) == 1 && msg.Content[0].Type == "text" {
				out["content"] = msg.Content[0].Text
			} else {
				// Multiple blocks or tool_result - extract text
				var textParts []string
				for _, block := range msg.Content {
					if block.Type == "text" {
						textParts = append(textParts, block.Text)
					} else if block.Type == "tool_result" && block.ToolResult != nil {
						for _, cb := range block.ToolResult.Content {
							if cb.Type == "text" {
								textParts = append(textParts, cb.Text)
							}
						}
					}
				}
				out["content"] = joinTextParts(textParts)
			}
		}

		return out
	}

	// Handle content for non-tool roles
	if len(msg.Content) == 0 {
		// Empty content - may need tool_calls
	} else if len(msg.Content) == 1 && msg.Content[0].Type == "text" && msg.ToolCalls == nil {
		// Simple text content - use string format
		out["content"] = msg.Content[0].Text
	} else {
		// Multimodal content or tool_calls
		content := serializeOpenAIMessageContent(msg.Content)
		out["content"] = content

		// Add tool_calls for assistant messages
		if len(msg.ToolCalls) > 0 {
			out["tool_calls"] = serializeOpenAIToolCalls(msg.ToolCalls)
		}
	}

	// Handle name for other roles
	if msg.Name != "" && msg.Role != "tool" {
		out["name"] = msg.Name
	}

	return out
}

// joinTextParts joins text parts with newlines.
func joinTextParts(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		return parts[0]
	}
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += "\n"
		}
		result += p
	}
	return result
}

// serializeOpenAIMessageContent converts IR content blocks to OpenAI content array.
func serializeOpenAIMessageContent(blocks []ContentBlock) []map[string]any {
	result := make([]map[string]any, 0, len(blocks))

	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				result = append(result, map[string]any{
					"type": "text",
					"text": block.Text,
				})
			}
		case "image":
			if block.Image != nil {
				result = append(result, map[string]any{
					"type": "image_url",
					"image_url": map[string]any{
						"url": block.Image.URL,
					},
				})
			}
		case "tool_use":
			// tool_use blocks are converted to OpenAI tool_calls format and
			// stored in message.ToolCalls (handled by the caller), not in the
			// content array.
		case "tool_result":
			// For tool results in content blocks, serialize as text
			if block.ToolResult != nil {
				var text string
				for _, cb := range block.ToolResult.Content {
					if cb.Type == "text" {
						text += cb.Text + "\n"
					}
				}
				if text != "" {
					result = append(result, map[string]any{
						"type": "text",
						"text": text,
					})
				}
			}
		}
	}

	return result
}

// serializeOpenAIToolCalls converts IR ToolCalls to OpenAI format.
func serializeOpenAIToolCalls(calls []ToolCall) []map[string]any {
	result := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		result = append(result, map[string]any{
			"id":   call.ID,
			"type": "function",
			"function": map[string]any{
				"name":      call.Function.Name,
				"arguments": call.Function.Arguments,
			},
		})
	}
	return result
}

// serializeOpenAITools converts IR ToolDefinitions to OpenAI format.
func serializeOpenAITools(tools []ToolDefinition) []map[string]any {
	result := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		result = append(result, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  tool.Parameters,
			},
		})
	}
	return result
}

// serializeOpenAIToolChoice converts IR ToolChoice to OpenAI format.
func serializeOpenAIToolChoice(tc *ToolChoice) any {
	if tc == nil {
		return nil
	}

	switch tc.Type {
	case "auto", "none":
		return tc.Type
	case "any", "required":
		return tc.Type
	case "tool":
		return map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": tc.Name,
			},
		}
	}

	return tc.Type
}
