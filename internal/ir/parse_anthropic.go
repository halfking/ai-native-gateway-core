package ir

import (
	"encoding/json"
	"fmt"
)

// ParseAnthropic parses an Anthropic Messages API request body into InternalRequest.
func ParseAnthropic(body []byte) (*InternalRequest, error) {
	var src struct {
		Model         string             `json:"model"`
		Messages      json.RawMessage    `json:"messages"`
		MaxTokens     int                `json:"max_tokens"`
		System        json.RawMessage    `json:"system,omitempty"`
		Stream        *bool              `json:"stream,omitempty"`
		Temperature   *float64           `json:"temperature,omitempty"`
		TopP          *float64           `json:"top_p,omitempty"`
		TopK          *int               `json:"top_k,omitempty"`
		StopSequences json.RawMessage    `json:"stop_sequences,omitempty"`
		Tools         json.RawMessage    `json:"tools,omitempty"`
		ToolChoice    json.RawMessage    `json:"tool_choice,omitempty"`
		Metadata      *anthropicMeta     `json:"metadata,omitempty"`
		Thinking      *anthropicThinking `json:"thinking,omitempty"`
		CacheControl  json.RawMessage    `json:"cache_control,omitempty"`
		Documents     json.RawMessage    `json:"documents,omitempty"`
	}

	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("unmarshal anthropic body: %w", err)
	}

	ir := &InternalRequest{
		Model:          src.Model,
		MaxTokens:      src.MaxTokens,
		SourceProtocol: ProtocolAnthropicMessages,
	}

	if src.Temperature != nil {
		ir.Temperature = src.Temperature
	}
	if src.TopP != nil {
		ir.TopP = src.TopP
	}
	if src.TopK != nil {
		ir.TopK = src.TopK
	}

	// Parse streaming
	if src.Stream != nil {
		ir.Stream = *src.Stream
	}

	// Parse stop sequences
	if src.StopSequences != nil && string(src.StopSequences) != "null" {
		ir.Stop = parseStringArray(src.StopSequences)
	}

	// Parse system prompt
	if src.System != nil && string(src.System) != "null" {
		ir.System = parseAnthropicSystem(src.System)
	}

	// Parse messages
	if src.Messages != nil && string(src.Messages) != "null" {
		messages, err := parseAnthropicMessages(src.Messages)
		if err != nil {
			return nil, fmt.Errorf("parse messages: %w", err)
		}
		ir.Messages = messages
	}

	// Parse tools
	if src.Tools != nil && string(src.Tools) != "null" {
		tools, err := parseAnthropicTools(src.Tools)
		if err != nil {
			return nil, fmt.Errorf("parse tools: %w", err)
		}
		ir.Tools = tools
	}

	// Parse tool_choice
	if src.ToolChoice != nil && string(src.ToolChoice) != "null" {
		tc, err := parseAnthropicToolChoice(src.ToolChoice)
		if err != nil {
			return nil, fmt.Errorf("parse tool_choice: %w", err)
		}
		ir.ToolChoice = tc
	}

	// Parse metadata
	if src.Metadata != nil {
		ir.Metadata = &Metadata{}
		if src.Metadata.UserID != "" {
			ir.Metadata.UserID = src.Metadata.UserID
			ir.User = src.Metadata.UserID // Normalize to OpenAI-style User
		}
	}

	// Parse thinking config
	if src.Thinking != nil {
		ir.Thinking = &ThinkingConfig{
			Type:         src.Thinking.Type,
			BudgetTokens: src.Thinking.BudgetTokens,
		}
	}

	// Parse cache_control
	if src.CacheControl != nil && string(src.CacheControl) != "null" {
		ir.CacheControl = parseAnthropicCacheControl(src.CacheControl)
	}

	// Parse documents
	if src.Documents != nil && string(src.Documents) != "null" {
		docs, err := parseAnthropicDocuments(src.Documents)
		if err != nil {
			return nil, fmt.Errorf("parse documents: %w", err)
		}
		ir.Documents = docs
	}

	return ir, nil
}

type anthropicMeta struct {
	UserID string `json:"user_id,omitempty"`
}

type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

// parseAnthropicSystem parses the Anthropic system field.
func parseAnthropicSystem(raw json.RawMessage) *SystemPrompt {
	// Can be string or array of blocks
	var content any
	if err := json.Unmarshal(raw, &content); err != nil {
		return nil
	}

	system := &SystemPrompt{}

	switch c := content.(type) {
	case string:
		system.Content = c
	case []any:
		blocks := make([]ContentBlock, 0, len(c))
		for _, item := range c {
			if blockMap, ok := item.(map[string]any); ok {
				block := parseAnthropicContentBlock(blockMap)
				if block != nil {
					blocks = append(blocks, *block)
				}
			}
		}
		system.Parts = blocks
	}

	return system
}

// parseAnthropicMessages parses Anthropic messages into IR format.
func parseAnthropicMessages(raw json.RawMessage) ([]Message, error) {
	var rawMessages []json.RawMessage
	if err := json.Unmarshal(raw, &rawMessages); err != nil {
		return nil, fmt.Errorf("unmarshal messages array: %w", err)
	}

	messages := make([]Message, 0, len(rawMessages))
	for i, rawMsg := range rawMessages {
		var msg map[string]any
		if err := json.Unmarshal(rawMsg, &msg); err != nil {
			return nil, fmt.Errorf("unmarshal message[%d]: %w", i, err)
		}

		irMsg, err := parseAnthropicMessage(msg)
		if err != nil {
			return nil, fmt.Errorf("parse message[%d]: %w", i, err)
		}
		messages = append(messages, *irMsg)
	}

	return messages, nil
}

// parseAnthropicMessage parses a single Anthropic message into IR Message.
func parseAnthropicMessage(msg map[string]any) (*Message, error) {
	role, _ := msg["role"].(string)

	irMsg := &Message{
		Role: role,
	}

	// Handle content
	content := msg["content"]
	if content == nil {
		irMsg.Content = []ContentBlock{}
		return irMsg, nil
	}

	switch c := content.(type) {
	case string:
		// Simple text content
		irMsg.Content = []ContentBlock{{Type: "text", Text: c}}
	case []any:
		// Content blocks
		blocks, err := parseAnthropicContentBlocks(c)
		if err != nil {
			return nil, fmt.Errorf("parse content blocks: %w", err)
		}
		irMsg.Content = blocks

		// P0 fix (2026-06-23): Extract tool_use blocks into ToolCalls
		// (OpenAI convention). Without this, SerializeOpenAI produces
		// `content: []` for assistant messages containing only tool_use
		// blocks. Mirrors the `role: tool` handling in serialize_openai.go.
		for _, block := range blocks {
			if block.Type == "tool_use" && block.ToolUse != nil {
				tc := ToolCall{
					ID:   block.ToolUse.ID,
					Type: "function",
				}
				tc.Function.Name = block.ToolUse.Name
				// ToolUse.Input is a pre-serialized JSON object
				tc.Function.Arguments = string(block.ToolUse.Input)
				irMsg.ToolCalls = append(irMsg.ToolCalls, tc)
			}
		}
	}

	// Handle name (usually for tool role)
	if name, ok := msg["name"].(string); ok {
		irMsg.Name = name
	}

	// Handle tool_use_id (for tool role)
	if toolUseID, ok := msg["tool_use_id"].(string); ok {
		irMsg.ToolCallID = toolUseID
	}

	// Handle source (for tool role results that aren't in content blocks)
	if source, ok := msg["source"].(string); ok {
		// This is a special case for some Anthropic responses
		_ = source
	}

	return irMsg, nil
}

// parseAnthropicContentBlocks parses Anthropic content blocks into IR ContentBlock.
func parseAnthropicContentBlocks(blocks []any) ([]ContentBlock, error) {
	result := make([]ContentBlock, 0, len(blocks))
	for _, block := range blocks {
		blockMap, ok := block.(map[string]any)
		if !ok {
			continue
		}

		blockType, _ := blockMap["type"].(string)
		irBlock := ContentBlock{Type: blockType}

		switch blockType {
		case "text":
			if text, ok := blockMap["text"].(string); ok {
				irBlock.Text = text
			}

		case "tool_use":
			id, _ := blockMap["id"].(string)
			name, _ := blockMap["name"].(string)
			inputRaw := blockMap["input"]
			inputJSON, _ := json.Marshal(inputRaw)

			irBlock.ToolUse = &ToolUse{
				ID:    id,
				Name:  name,
				Input: inputJSON,
			}

		case "tool_result":
			toolUseID, _ := blockMap["tool_use_id"].(string)
			content := blockMap["content"]
			isError, _ := blockMap["is_error"].(bool)

			// Parse content (can be string or array of blocks)
			var contentBlocks []ContentBlock
			switch c := content.(type) {
			case string:
				contentBlocks = []ContentBlock{{Type: "text", Text: c}}
			case []any:
				for _, item := range c {
					if itemMap, ok := item.(map[string]any); ok {
						cb := parseAnthropicContentBlock(itemMap)
						if cb != nil {
							contentBlocks = append(contentBlocks, *cb)
						}
					}
				}
			}

			irBlock.ToolResult = &ToolResult{
				ToolUseID: toolUseID,
				Content:   contentBlocks,
				IsError:   isError,
			}

		case "image":
			img := parseAnthropicImageBlock(blockMap)
			irBlock.Image = img

		case "thinking":
			thinking, _ := blockMap["thinking"].(string)
			sig, _ := blockMap["signature"].(string)
			irBlock.Thinking = &ThinkingBlock{Thinking: thinking, Signature: sig}

		case "redacted_thinking":
			if rt, ok := blockMap["thinking"].(string); ok {
				irBlock.RedactedThinking = rt
			}

		default:
			// Store unknown block types as raw JSON for round-trip
			raw, _ := json.Marshal(blockMap)
			irBlock.RawContent = string(raw)
		}

		// Parse cache_control if present
		if cc, ok := blockMap["cache_control"].(map[string]any); ok {
			if ccType, ok := cc["type"].(string); ok {
				irBlock.CacheControl = &CacheControl{Type: ccType}
			}
		}

		// Parse index if present
		if idx, ok := blockMap["index"].(float64); ok {
			i := int(idx)
			irBlock.Index = &i
		}

		result = append(result, irBlock)
	}
	return result, nil
}

// parseAnthropicContentBlock parses a single Anthropic content block.
func parseAnthropicContentBlock(blockMap map[string]any) *ContentBlock {
	blockType, _ := blockMap["type"].(string)
	if blockType == "" {
		return nil
	}

	irBlock := &ContentBlock{Type: blockType}

	switch blockType {
	case "text":
		if text, ok := blockMap["text"].(string); ok {
			irBlock.Text = text
		}
	case "tool_use":
		id, _ := blockMap["id"].(string)
		name, _ := blockMap["name"].(string)
		inputRaw := blockMap["input"]
		inputJSON, _ := json.Marshal(inputRaw)
		irBlock.ToolUse = &ToolUse{ID: id, Name: name, Input: inputJSON}
	case "tool_result":
		toolUseID, _ := blockMap["tool_use_id"].(string)
		isError, _ := blockMap["is_error"].(bool)
		content := blockMap["content"]
		var contentBlocks []ContentBlock
		switch c := content.(type) {
		case string:
			contentBlocks = []ContentBlock{{Type: "text", Text: c}}
		case []any:
			for _, item := range c {
				if itemMap, ok := item.(map[string]any); ok {
					cb := parseAnthropicContentBlock(itemMap)
					if cb != nil {
						contentBlocks = append(contentBlocks, *cb)
					}
				}
			}
		}
		irBlock.ToolResult = &ToolResult{ToolUseID: toolUseID, Content: contentBlocks, IsError: isError}
	case "image":
		irBlock.Image = parseAnthropicImageBlock(blockMap)
	case "thinking":
		if thinking, ok := blockMap["thinking"].(string); ok {
			sig, _ := blockMap["signature"].(string)
			irBlock.Thinking = &ThinkingBlock{Thinking: thinking, Signature: sig}
		}
	case "redacted_thinking":
		if rt, ok := blockMap["thinking"].(string); ok {
			irBlock.RedactedThinking = rt
		}
	}

	return irBlock
}

// parseAnthropicImageBlock parses an Anthropic image content block.
func parseAnthropicImageBlock(block map[string]any) *ImageSource {
	img := &ImageSource{}

	if source, ok := block["source"].(map[string]any); ok {
		img.Type, _ = source["type"].(string)
		img.MediaType, _ = source["media_type"].(string)
		img.URL, _ = source["url"].(string)
		img.Data, _ = source["data"].(string)
	}

	return img
}

// parseAnthropicTools parses Anthropic tool definitions into IR ToolDefinition.
func parseAnthropicTools(raw json.RawMessage) ([]ToolDefinition, error) {
	var tools []any
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil, fmt.Errorf("unmarshal tools: %w", err)
	}

	result := make([]ToolDefinition, 0, len(tools))
	for _, t := range tools {
		tool, ok := t.(map[string]any)
		if !ok {
			continue
		}

		td := ToolDefinition{}

		if name, ok := tool["name"].(string); ok {
			td.Name = name
		}
		if desc, ok := tool["description"].(string); ok {
			td.Description = desc
		}
		if schema, ok := tool["input_schema"].(json.RawMessage); ok {
			td.Parameters = schema
		} else if schema, ok := tool["parameters"].(json.RawMessage); ok {
			td.Parameters = schema
		}

		result = append(result, td)
	}
	return result, nil
}

// parseAnthropicToolChoice parses Anthropic tool_choice.
func parseAnthropicToolChoice(raw json.RawMessage) (*ToolChoice, error) {
	var choice any
	if err := json.Unmarshal(raw, &choice); err != nil {
		return nil, fmt.Errorf("unmarshal tool_choice: %w", err)
	}

	tc := &ToolChoice{}

	switch c := choice.(type) {
	case string:
		tc.Type = c
	case map[string]any:
		if t, ok := c["type"].(string); ok {
			tc.Type = t
		}
		if name, ok := c["name"].(string); ok {
			tc.Name = name
		}
	}

	return tc, nil
}

// parseAnthropicCacheControl parses cache_control field.
func parseAnthropicCacheControl(raw json.RawMessage) []CacheControl {
	var cc any
	if err := json.Unmarshal(raw, &cc); err != nil {
		return nil
	}

	var result []CacheControl

	switch c := cc.(type) {
	case map[string]any:
		if ccType, ok := c["type"].(string); ok {
			result = append(result, CacheControl{Type: ccType})
		}
	case []any:
		for _, item := range c {
			if itemMap, ok := item.(map[string]any); ok {
				if ccType, ok := itemMap["type"].(string); ok {
					result = append(result, CacheControl{Type: ccType})
				}
			}
		}
	}

	return result
}

// parseAnthropicDocuments parses Anthropic documents field.
func parseAnthropicDocuments(raw json.RawMessage) ([]Document, error) {
	var docs []any
	if err := json.Unmarshal(raw, &docs); err != nil {
		return nil, fmt.Errorf("unmarshal documents: %w", err)
	}

	result := make([]Document, 0, len(docs))
	for _, d := range docs {
		if docMap, ok := d.(map[string]any); ok {
			doc := Document{
				Type: "document",
			}

			if title, ok := docMap["title"].(string); ok {
				doc.Title = title
			}
			if context, ok := docMap["context"].(string); ok {
				doc.Context = context
			}

			if source, ok := docMap["source"].(map[string]any); ok {
				doc.Source.Type, _ = source["type"].(string)
				doc.Source.MediaType, _ = source["media_type"].(string)
				doc.Source.Data, _ = source["data"].(string)
				doc.Source.URL, _ = source["url"].(string)
			}

			if cc, ok := docMap["cache_control"].(map[string]any); ok {
				if ccType, ok := cc["type"].(string); ok {
					doc.CacheCtrl = &CacheControl{Type: ccType}
				}
			}

			result = append(result, doc)
		}
	}

	return result, nil
}
