package ir

import (
	"encoding/json"
	"fmt"
)

// ParseOpenAI parses an OpenAI Chat Completions request body into InternalRequest.
func ParseOpenAI(body []byte) (*InternalRequest, error) {
	var src struct {
		Model             string          `json:"model"`
		Messages         json.RawMessage `json:"messages"`
		MaxTokens        *int            `json:"max_tokens"`
		MaxCompletionTokens *int          `json:"max_completion_tokens,omitempty"`
		Temperature      *float64        `json:"temperature,omitempty"`
		TopP             *float64        `json:"top_p,omitempty"`
		Stop             json.RawMessage `json:"stop,omitempty"`
		Stream           *bool           `json:"stream,omitempty"`
		Tools            json.RawMessage `json:"tools,omitempty"`
		ToolChoice       json.RawMessage `json:"tool_choice,omitempty"`
		FrequencyPenalty *float64        `json:"frequency_penalty,omitempty"`
		PresencePenalty  *float64        `json:"presence_penalty,omitempty"`
		LogProbs         *bool           `json:"logprobs,omitempty"`
		TopLogProbs      *int            `json:"top_logprobs,omitempty"`
		Seed             *int64          `json:"seed,omitempty"`
		ResponseFormat   json.RawMessage `json:"response_format,omitempty"`
		N                *int            `json:"n,omitempty"`
		User             string          `json:"user,omitempty"`
	}

	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("unmarshal openai body: %w", err)
	}

	ir := &InternalRequest{
		Model:            src.Model,
		SourceProtocol:   ProtocolOpenAIChat,
		FrequencyPenalty: src.FrequencyPenalty,
		PresencePenalty:  src.PresencePenalty,
		Logprobs:        src.LogProbs,
		TopLogprobs:     src.TopLogProbs,
		Seed:            src.Seed,
		N:               derefInt(src.N),
		User:            src.User,
	}

	if src.MaxTokens != nil {
		ir.MaxTokens = *src.MaxTokens
	} else if src.MaxCompletionTokens != nil {
		ir.MaxTokens = *src.MaxCompletionTokens
	}

	if src.Temperature != nil {
		ir.Temperature = src.Temperature
	}
	if src.TopP != nil {
		ir.TopP = src.TopP
	}

	// Parse stop sequences
	if src.Stop != nil && string(src.Stop) != "null" {
		ir.Stop = parseStringArray(src.Stop)
	}

	// Parse streaming
	if src.Stream != nil {
		ir.Stream = *src.Stream
	}

	// Parse messages
	if src.Messages != nil && string(src.Messages) != "null" {
		messages, err := parseOpenAIMessages(src.Messages)
		if err != nil {
			return nil, fmt.Errorf("parse messages: %w", err)
		}
		ir.Messages = messages
	}

	// Parse tools
	if src.Tools != nil && string(src.Tools) != "null" {
		tools, err := parseOpenAITools(src.Tools)
		if err != nil {
			return nil, fmt.Errorf("parse tools: %w", err)
		}
		ir.Tools = tools
	}

	// Parse tool_choice
	if src.ToolChoice != nil && string(src.ToolChoice) != "null" {
		tc, err := parseOpenAIToolChoice(src.ToolChoice)
		if err != nil {
			return nil, fmt.Errorf("parse tool_choice: %w", err)
		}
		ir.ToolChoice = tc
	}

	// Parse response_format
	if src.ResponseFormat != nil && string(src.ResponseFormat) != "null" {
		rf, err := parseOpenAIResponseFormat(src.ResponseFormat)
		if err != nil {
			return nil, fmt.Errorf("parse response_format: %w", err)
		}
		ir.ResponseFormat = rf
	}

	// Extract system prompt
	ir.System = extractSystemPrompt(&ir.Messages)

	return ir, nil
}

// parseOpenAIMessages parses OpenAI messages into IR Message format.
func parseOpenAIMessages(raw json.RawMessage) ([]Message, error) {
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

		irMsg, err := parseOpenAIMessage(msg)
		if err != nil {
			return nil, fmt.Errorf("parse message[%d]: %w", i, err)
		}
		messages = append(messages, *irMsg)
	}

	return messages, nil
}

// parseOpenAIMessage parses a single OpenAI message into IR Message.
func parseOpenAIMessage(msg map[string]any) (*Message, error) {
	role, _ := msg["role"].(string)

	irMsg := &Message{
		Role: role,
	}

	// Handle content
	switch content := msg["content"].(type) {
	case string:
		// Simple text content
		irMsg.Content = []ContentBlock{{Type: "text", Text: content}}
	case []any:
		// Multimodal content
		blocks, err := parseOpenAIContentBlocks(content)
		if err != nil {
			return nil, fmt.Errorf("parse content blocks: %w", err)
		}
		irMsg.Content = blocks
	case nil:
		irMsg.Content = []ContentBlock{}
	}

	// Handle tool_calls (assistant messages)
	if toolCalls, ok := msg["tool_calls"].([]any); ok {
		irMsg.ToolCalls = make([]ToolCall, 0, len(toolCalls))
		for _, tc := range toolCalls {
			if tcMap, ok := tc.(map[string]any); ok {
				irMsg.ToolCalls = append(irMsg.ToolCalls, parseOpenAIToolCall(tcMap))
			}
		}
	}

	// Handle tool_call_id (tool role messages)
	if toolCallID, ok := msg["tool_call_id"].(string); ok {
		irMsg.ToolCallID = toolCallID
	}

	// Handle name (usually for tool role)
	if name, ok := msg["name"].(string); ok {
		irMsg.Name = name
	}

	return irMsg, nil
}

// parseOpenAIContentBlocks parses OpenAI content blocks into IR ContentBlock.
func parseOpenAIContentBlocks(blocks []any) ([]ContentBlock, error) {
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
		case "image_url":
			img := parseOpenAIImageBlock(blockMap)
			irBlock.Image = img
			irBlock.Type = "image" // Normalize to our type
		}
		result = append(result, irBlock)
	}
	return result, nil
}

// parseOpenAIImageBlock parses an OpenAI image_url content block.
func parseOpenAIImageBlock(block map[string]any) *ImageSource {
	img := &ImageSource{Type: "url"}

	if urlObj, ok := block["image_url"].(map[string]any); ok {
		if url, ok := urlObj["url"].(string); ok {
			img.URL = url
		}
		if mt, ok := urlObj["detail"].(string); ok {
			// detail can be "low", "high", "auto"
			_ = mt
		}
	}

	return img
}

// parseOpenAIToolCall parses an OpenAI tool_call.
func parseOpenAIToolCall(tc map[string]any) ToolCall {
	tcResult := ToolCall{
		Type: "function",
	}

	if id, ok := tc["id"].(string); ok {
		tcResult.ID = id
	}

	if fn, ok := tc["function"].(map[string]any); ok {
		if name, ok := fn["name"].(string); ok {
			tcResult.Function.Name = name
		}
		if args, ok := fn["arguments"].(string); ok {
			tcResult.Function.Arguments = args
		}
	}

	return tcResult
}

// parseOpenAITools parses OpenAI tool definitions into IR ToolDefinition.
func parseOpenAITools(raw json.RawMessage) ([]ToolDefinition, error) {
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

		// Handle nested function object (OpenAI standard format)
		if fn, ok := tool["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok {
				td.Name = name
			}
			if desc, ok := fn["description"].(string); ok {
				td.Description = desc
			}
			if params, ok := fn["parameters"].(json.RawMessage); ok {
				td.Parameters = params
			}
		} else {
			// Flat format or Anthropic-style
			if name, ok := tool["name"].(string); ok {
				td.Name = name
			}
			if desc, ok := tool["description"].(string); ok {
				td.Description = desc
			}
			// Try input_schema (Anthropic style) or parameters
			if params, ok := tool["input_schema"].(json.RawMessage); ok {
				td.Parameters = params
			} else if params, ok := tool["parameters"].(json.RawMessage); ok {
				td.Parameters = params
			}
		}

		result = append(result, td)
	}
	return result, nil
}

// parseOpenAIToolChoice parses OpenAI tool_choice.
func parseOpenAIToolChoice(raw json.RawMessage) (*ToolChoice, error) {
	// Can be string ("auto", "none") or object
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
		if fn, ok := c["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok {
				tc.Name = name
			}
		}
	}

	return tc, nil
}

// parseOpenAIResponseFormat parses OpenAI response_format.
func parseOpenAIResponseFormat(raw json.RawMessage) (*ResponseFormat, error) {
	var rf map[string]any
	if err := json.Unmarshal(raw, &rf); err != nil {
		return nil, fmt.Errorf("unmarshal response_format: %w", err)
	}

	result := &ResponseFormat{}
	if t, ok := rf["type"].(string); ok {
		result.Type = t
	}
	if schema, ok := rf["json_schema"].(json.RawMessage); ok {
		result.Schema = schema
	}

	return result, nil
}

// extractSystemPrompt extracts the system message from messages and normalizes it.
func extractSystemPrompt(messages *[]Message) *SystemPrompt {
	if messages == nil || len(*messages) == 0 {
		return nil
	}

	// Find the first system message
	for i, msg := range *messages {
		if msg.Role == "system" {
			system := &SystemPrompt{}
			if len(msg.Content) > 0 && msg.Content[0].Type == "text" {
				system.Content = msg.Content[0].Text
			}
			// Remove system message from the list
			// We need to reconstruct without the system message
			newMessages := make([]Message, 0, len(*messages)-1)
			newMessages = append(newMessages, (*messages)[:i]...)
			newMessages = append(newMessages, (*messages)[i+1:]...)
			*messages = newMessages
			return system
		}
	}

	return nil
}

// parseStringArray parses a JSON array of strings.
func parseStringArray(raw json.RawMessage) []string {
	var result []string
	if raw == nil {
		return result
	}

	// Try []string first
	if err := json.Unmarshal(raw, &result); err == nil {
		return result
	}

	// Try single string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []string{s}
	}

	return result
}

// derefInt safely dereferences an int pointer.
func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
