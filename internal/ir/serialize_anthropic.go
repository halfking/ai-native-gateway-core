package ir

import (
	"encoding/json"
	"fmt"
)

// SerializeAnthropic serializes an InternalRequest into an Anthropic Messages request body.
func SerializeAnthropic(req *InternalRequest) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("request is nil")
	}

	out := map[string]any{
		"model":      req.Model,
		"max_tokens": req.MaxTokens,
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
	if req.TopK != nil {
		out["top_k"] = *req.TopK
	}

	// Stop sequences
	if len(req.Stop) > 0 {
		out["stop_sequences"] = req.Stop
	}

	// System prompt
	if req.System != nil {
		system := serializeAnthropicSystem(req.System)
		out["system"] = system
	}

	// Messages
	messages := serializeAnthropicMessages(req)
	if len(messages) > 0 {
		out["messages"] = messages
	}

	// Tools
	if len(req.Tools) > 0 {
		tools := serializeAnthropicTools(req.Tools)
		out["tools"] = tools
	}

	// Tool choice
	if req.ToolChoice != nil {
		out["tool_choice"] = serializeAnthropicToolChoice(req.ToolChoice)
	}

	// Metadata
	if req.User != "" {
		out["metadata"] = map[string]any{
			"user_id": req.User,
		}
	}

	// Thinking config
	if req.Thinking != nil {
		out["thinking"] = map[string]any{
			"type":          req.Thinking.Type,
			"budget_tokens": req.Thinking.BudgetTokens,
		}
	}

	// Cache control (top-level)
	if len(req.CacheControl) > 0 {
		out["cache_control"] = serializeAnthropicCacheControl(req.CacheControl)
	}

	// Documents
	if len(req.Documents) > 0 {
		docs := serializeAnthropicDocuments(req.Documents)
		out["documents"] = docs
	}

	return json.Marshal(out)
}

// serializeAnthropicSystem serializes the system prompt.
func serializeAnthropicSystem(system *SystemPrompt) any {
	if system == nil {
		return nil
	}

	// If system has parts (content blocks), use array format
	if len(system.Parts) > 0 {
		parts := make([]map[string]any, 0, len(system.Parts))
		for _, part := range system.Parts {
			parts = append(parts, serializeAnthropicContentBlock(part))
		}
		return parts
	}

	// If system has PDFs, use array with document blocks
	if len(system.PDFs) > 0 {
		parts := make([]map[string]any, 0, len(system.PDFs))
		for _, pdf := range system.PDFs {
			pdfMap := map[string]any{
				"type": "document",
				"source": map[string]any{
					"type": pdf.Source.Type,
				},
			}
			if pdf.Source.MimeType != "" {
				pdfMap["source"].(map[string]any)["mime_type"] = pdf.Source.MimeType
			}
			if pdf.Source.Data != "" {
				pdfMap["source"].(map[string]any)["data"] = pdf.Source.Data
			}
			if pdf.Source.URL != "" {
				pdfMap["source"].(map[string]any)["url"] = pdf.Source.URL
			}
			if pdf.Title != "" {
				pdfMap["title"] = pdf.Title
			}
			if pdf.CacheCtrl != nil {
				pdfMap["cache_control"] = map[string]any{"type": pdf.CacheCtrl.Type}
			}
			parts = append(parts, pdfMap)
		}
		return parts
	}

	// Plain text system prompt
	return system.Content
}

// serializeAnthropicMessages converts IR messages to Anthropic format.
func serializeAnthropicMessages(req *InternalRequest) []map[string]any {
	messages := make([]map[string]any, 0, len(req.Messages))

	for _, msg := range req.Messages {
		messages = append(messages, serializeAnthropicMessage(msg))
	}

	return messages
}

// serializeAnthropicMessage converts a single IR Message to Anthropic format.
func serializeAnthropicMessage(msg Message) map[string]any {
	out := map[string]any{
		"role": msg.Role,
	}

	// Convert content
	if len(msg.Content) == 0 && len(msg.ToolCalls) == 0 {
		// Empty message
		out["content"] = ""
	} else if len(msg.Content) == 1 && msg.Content[0].Type == "text" && len(msg.ToolCalls) == 0 {
		// Simple text content - use string format
		out["content"] = msg.Content[0].Text
	} else {
		// Content blocks (may include tool_use blocks)
		content := serializeAnthropicMessageContent(msg)
		out["content"] = content
	}

	// Tool role fields
	if msg.Role == "tool" {
		if msg.ToolCallID != "" {
			out["tool_use_id"] = msg.ToolCallID
		}
		if msg.Name != "" {
			out["name"] = msg.Name
		}
	}

	return out
}

// serializeAnthropicMessageContent converts IR message content to Anthropic content blocks.
func serializeAnthropicMessageContent(msg Message) []map[string]any {
	result := make([]map[string]any, 0)

	// First, add text and other content blocks
	for _, block := range msg.Content {
		result = append(result, serializeAnthropicContentBlock(block))
	}

	// Then, add tool_use blocks from ToolCalls
	for _, tc := range msg.ToolCalls {
		toolUse := map[string]any{
			"type": "tool_use",
			"id":   tc.ID,
			"name": tc.Function.Name,
		}
		// Parse arguments JSON
		var args any
		if tc.Function.Arguments != "" {
			json.Unmarshal([]byte(tc.Function.Arguments), &args)
		}
		toolUse["input"] = args
		result = append(result, toolUse)
	}

	return result
}

// serializeAnthropicContentBlock converts an IR ContentBlock to Anthropic format.
func serializeAnthropicContentBlock(block ContentBlock) map[string]any {
	out := map[string]any{
		"type": block.Type,
	}

	switch block.Type {
	case "text":
		out["text"] = block.Text

	case "image":
		source := map[string]any{
			"type": block.Image.Type,
		}
		if block.Image.MediaType != "" {
			source["media_type"] = block.Image.MediaType
		}
		if block.Image.URL != "" {
			source["url"] = block.Image.URL
		}
		if block.Image.Data != "" {
			source["data"] = block.Image.Data
		}
		out["source"] = source

	case "tool_use":
		if block.ToolUse != nil {
			out["id"] = block.ToolUse.ID
			out["name"] = block.ToolUse.Name
			// Parse input JSON
			var input any
			if block.ToolUse.Input != nil {
				json.Unmarshal(block.ToolUse.Input, &input)
			}
			out["input"] = input
		}

	case "tool_result":
		if block.ToolResult != nil {
			out["tool_use_id"] = block.ToolResult.ToolUseID
			out["is_error"] = block.ToolResult.IsError

			// Serialize content - can be text blocks
			if len(block.ToolResult.Content) > 0 {
				content := make([]map[string]any, 0, len(block.ToolResult.Content))
				for _, cb := range block.ToolResult.Content {
					if cb.Type == "text" {
						content = append(content, map[string]any{
							"type": "text",
							"text": cb.Text,
						})
					}
				}
				if len(content) == 1 {
					out["content"] = content[0]["text"]
				} else {
					out["content"] = content
				}
			}
		}

	case "thinking":
		if block.Thinking != nil {
			out["thinking"] = block.Thinking.Thinking
		}

	case "redacted_thinking":
		out["thinking"] = block.RedactedThinking
	}

	// Add cache_control if present
	if block.CacheControl != nil {
		out["cache_control"] = map[string]any{"type": block.CacheControl.Type}
	}

	// Add index if present
	if block.Index != nil {
		out["index"] = *block.Index
	}

	return out
}

// serializeAnthropicTools converts IR ToolDefinitions to Anthropic format.
func serializeAnthropicTools(tools []ToolDefinition) []map[string]any {
	result := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		toolMap := map[string]any{
			"name": tool.Name,
		}
		if tool.Description != "" {
			toolMap["description"] = tool.Description
		}
		if tool.Parameters != nil {
			toolMap["input_schema"] = tool.Parameters
		}
		result = append(result, toolMap)
	}
	return result
}

// serializeAnthropicToolChoice converts IR ToolChoice to Anthropic format.
func serializeAnthropicToolChoice(tc *ToolChoice) any {
	if tc == nil {
		return nil
	}

	switch tc.Type {
	case "auto", "none", "any":
		return tc.Type
	case "required":
		return "any" // Anthropic uses "any" for required
	case "tool":
		return map[string]any{
			"type": "tool",
			"name": tc.Name,
		}
	}

	return tc.Type
}

// serializeAnthropicCacheControl serializes cache control.
func serializeAnthropicCacheControl(cc []CacheControl) any {
	if len(cc) == 0 {
		return nil
	}
	if len(cc) == 1 {
		return map[string]any{"type": cc[0].Type}
	}
	result := make([]map[string]any, len(cc))
	for i, c := range cc {
		result[i] = map[string]any{"type": c.Type}
	}
	return result
}

// serializeAnthropicDocuments serializes documents.
func serializeAnthropicDocuments(docs []Document) []map[string]any {
	result := make([]map[string]any, 0, len(docs))
	for _, doc := range docs {
		docMap := map[string]any{
			"type": doc.Type,
			"source": map[string]any{
				"type": doc.Source.Type,
			},
		}
		if doc.Source.MediaType != "" {
			docMap["source"].(map[string]any)["media_type"] = doc.Source.MediaType
		}
		if doc.Source.Data != "" {
			docMap["source"].(map[string]any)["data"] = doc.Source.Data
		}
		if doc.Source.URL != "" {
			docMap["source"].(map[string]any)["url"] = doc.Source.URL
		}
		if doc.Title != "" {
			docMap["title"] = doc.Title
		}
		if doc.Context != "" {
			docMap["context"] = doc.Context
		}
		if doc.CacheCtrl != nil {
			docMap["cache_control"] = map[string]any{"type": doc.CacheCtrl.Type}
		}
		result = append(result, docMap)
	}
	return result
}
