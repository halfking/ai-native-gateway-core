package ir

import (
	"encoding/json"
	"fmt"
	"time"
)

// InternalResponse is the unified intermediate representation for upstream
// responses. Its field set is the superset of OpenAI Chat Completions and
// Anthropic Messages response fields.
//
// Architecture (response direction):
//
//	Upstream Response (Anthropic/OpenAI) → Parse → IR → Serialize → Client Response
//
// Complexity reduced from O(N²) to O(N): adding a new protocol only requires
// one Parser + one Serializer.
type InternalResponse struct {
	ID        string
	Model     string
	Created   int64  // Unix timestamp (OpenAI style); 0 if not available
	Role      string // "assistant" (Anthropic uses top-level role field)
	SourceProtocol string // "openai-chat" | "anthropic-messages" — which upstream we parsed

	// Content is the normalized message content. Both OpenAI messages[] and
	// Anthropic content[] are normalized into this structure.
	Content []ResponseContentBlock

	// ToolCalls is the normalized tool call list. OpenAI's message.tool_calls
	// and Anthropic's content[].tool_use are both normalized here.
	ToolCalls []ResponseToolCall

	// ReasoningContent holds extended thinking (Claude) from OpenAI's
	// reasoning_content or Anthropic's content[].thinking blocks.
	ReasoningContent string

	// FinishReason is the unified stop reason.
	// OpenAI: "stop" | "length" | "content_filter" | "tool_calls"
	// Anthropic: "end_turn" | "stop_sequence" | "max_tokens" | "tool_use"
	// We store the OpenAI form; Anthropic values are mapped via mapFinishReason.
	FinishReason string

	// Usage statistics (both protocols have compatible usage fields)
	Usage ResponseUsage
}

// ResponseContentBlock represents a single content element in a response.
// Type values: "text" | "tool_use" | "thinking" | "redacted_thinking"
type ResponseContentBlock struct {
	Type string // Discriminant

	// type=text
	Text string

	// type=tool_use
	ID    string
	Name  string
	Input json.RawMessage // Already-serialized JSON object

	// type=thinking / redacted_thinking
	Thinking string
}

// ResponseToolCall represents a tool call from the assistant.
type ResponseToolCall struct {
	ID   string
	Name string
	// Arguments is the JSON-stringified tool input.
	Arguments string
}

// ResponseUsage holds token usage statistics.
type ResponseUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// ─── Parse ─────────────────────────────────────────────────────────────────

// ParseAnthropicResponse parses an Anthropic Messages API response body into IR.
func ParseAnthropicResponse(body []byte) (*InternalResponse, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("empty response body")
	}
	var src struct {
		ID     string `json:"id"`
		Type   string `json:"type"`
		Role   string `json:"role"`
		Model  string `json:"model"`
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ID       string `json:"id"`
			Name     string `json:"name"`
			Input    json.RawMessage `json:"input"`
			Thinking string `json:"thinking"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("unmarshal anthropic response: %w", err)
	}

	ir := &InternalResponse{
		ID:             src.ID,
		Model:          src.Model,
		Role:           src.Role,
		SourceProtocol: ProtocolAnthropicMessages,
		FinishReason:   mapAnthropicFinishReason(src.StopReason),
		Usage: ResponseUsage{
			PromptTokens:     src.Usage.InputTokens,
			CompletionTokens: src.Usage.OutputTokens,
			TotalTokens:      src.Usage.InputTokens + src.Usage.OutputTokens,
		},
	}

	for _, c := range src.Content {
		switch c.Type {
		case "text":
			ir.Content = append(ir.Content, ResponseContentBlock{Type: "text", Text: c.Text})
		case "tool_use":
			ir.Content = append(ir.Content, ResponseContentBlock{
				Type:  "tool_use",
				ID:    c.ID,
				Name:  c.Name,
				Input: c.Input,
			})
			ir.ToolCalls = append(ir.ToolCalls, ResponseToolCall{
				ID:   c.ID,
				Name: c.Name,
			})
		case "thinking":
			if c.Thinking != "" {
				ir.ReasoningContent += c.Thinking
				ir.Content = append(ir.Content, ResponseContentBlock{Type: "thinking", Thinking: c.Thinking})
			}
		}
	}

	return ir, nil
}

// ParseOpenAIResponse parses an OpenAI Chat Completions response body into IR.
func ParseOpenAIResponse(body []byte) (*InternalResponse, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("empty response body")
	}
	var src struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Role              string `json:"role"`
				Content           any    `json:"content"`
				ToolCalls         []struct {
					ID      string `json:"id"`
					Type    string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("unmarshal openai response: %w", err)
	}

	ir := &InternalResponse{
		ID:             src.ID,
		Model:          src.Model,
		Created:        src.Created,
		SourceProtocol: ProtocolOpenAIChat,
		Usage: ResponseUsage{
			PromptTokens:     src.Usage.PromptTokens,
			CompletionTokens: src.Usage.CompletionTokens,
			TotalTokens:      src.Usage.TotalTokens,
		},
	}

	if len(src.Choices) > 0 {
		choice := src.Choices[0]
		ir.Role = choice.Message.Role
		ir.FinishReason = choice.FinishReason
		if choice.Message.ReasoningContent != "" {
			ir.ReasoningContent = choice.Message.ReasoningContent
		}

		// Parse content
		switch c := choice.Message.Content.(type) {
		case string:
			if c != "" {
				ir.Content = append(ir.Content, ResponseContentBlock{Type: "text", Text: c})
			}
		case []any:
			for _, item := range c {
				if m, ok := item.(map[string]any); ok {
					ir.Content = append(ir.Content, parseOpenAIResponseContentBlock(m))
				}
			}
		}

		// Tool calls
		for _, tc := range choice.Message.ToolCalls {
			ir.ToolCalls = append(ir.ToolCalls, ResponseToolCall{
				ID:   tc.ID,
				Name: tc.Function.Name,
			})
		}
	}

	return ir, nil
}

// parseOpenAIResponseContentBlock parses a single OpenAI content block into IR format.
func parseOpenAIResponseContentBlock(m map[string]any) ResponseContentBlock {
	typ, _ := m["type"].(string)
	switch typ {
	case "text":
		text, _ := m["text"].(string)
		return ResponseContentBlock{Type: "text", Text: text}
	case "tool_use":
		id, _ := m["id"].(string)
		name, _ := m["name"].(string)
		inputRaw, _ := json.Marshal(m["input"])
		return ResponseContentBlock{Type: "tool_use", ID: id, Name: name, Input: inputRaw}
	}
	return ResponseContentBlock{Type: typ}
}

// ─── Serialize ──────────────────────────────────────────────────────────────

// SerializeOpenAIResponse serializes an InternalResponse into an OpenAI
// Chat Completions response body. Used for Q3 (openai client ← anthropic upstream).
func SerializeOpenAIResponse(ir *InternalResponse, clientModel string) ([]byte, error) {
	if ir == nil {
		return nil, fmt.Errorf("response is nil")
	}

	model := ir.Model
	if clientModel != "" {
		model = clientModel
	}

	// Build message content
	messageContent := buildOpenAIResponseContent(ir)

	msg := map[string]any{"role": ir.Role}
	if messageContent != nil {
		msg["content"] = messageContent
	}
	if ir.ReasoningContent != "" {
		msg["reasoning_content"] = ir.ReasoningContent
	}

	// Tool calls
	var toolCalls []map[string]any
	for _, tc := range ir.ToolCalls {
		toolCalls = append(toolCalls, map[string]any{
			"id":   tc.ID,
			"type": "function",
			"function": map[string]any{
				"name":      tc.Name,
				"arguments": tc.Arguments,
			},
		})
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}

	created := ir.Created
	if created == 0 {
		created = time.Now().Unix()
	}

	finishReason := ir.FinishReason
	if finishReason == "" {
		finishReason = "stop"
	}

	out := map[string]any{
		"id":      ir.ID,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []map[string]any{{
			"index": 0,
			"message": msg,
			"finish_reason": finishReason,
		}},
		"usage": map[string]any{
			"prompt_tokens":     ir.Usage.PromptTokens,
			"completion_tokens": ir.Usage.CompletionTokens,
			"total_tokens":     ir.Usage.TotalTokens,
		},
	}

	return json.Marshal(out)
}

// buildOpenAIResponseContent builds the OpenAI response content from IR.
func buildOpenAIResponseContent(ir *InternalResponse) any {
	if len(ir.Content) == 0 && len(ir.ToolCalls) == 0 {
		return nil
	}
	if len(ir.Content) == 1 && ir.Content[0].Type == "text" && len(ir.ToolCalls) == 0 {
		return ir.Content[0].Text
	}

	blocks := make([]map[string]any, 0, len(ir.Content))
	for _, c := range ir.Content {
		switch c.Type {
		case "text":
			blocks = append(blocks, map[string]any{"type": "text", "text": c.Text})
		case "tool_use":
			blocks = append(blocks, map[string]any{
				"type": "tool_use",
				"id":   c.ID,
				"name": c.Name,
			})
		}
	}

	// Add tool_use blocks for tool calls that aren't already in content
	existingIDs := make(map[string]bool)
	for _, c := range ir.Content {
		if c.Type == "tool_use" {
			existingIDs[c.ID] = true
		}
	}
	for _, tc := range ir.ToolCalls {
		if !existingIDs[tc.ID] {
			blocks = append(blocks, map[string]any{
				"type": "tool_use",
				"id":   tc.ID,
				"name": tc.Name,
			})
		}
	}

	return blocks
}

// SerializeAnthropicResponse serializes an InternalResponse into an Anthropic
// Messages API response body. Used for Q2 (anthropic client ← openai upstream).
func SerializeAnthropicResponse(ir *InternalResponse, clientModel string) ([]byte, error) {
	if ir == nil {
		return nil, fmt.Errorf("response is nil")
	}

	model := ir.Model
	if clientModel != "" {
		model = clientModel
	}

	// Build content blocks
	content := buildAnthropicResponseContent(ir)

	// Build stop_reason (Anthropic form)
	stopReason := mapFinishReasonToAnthropic(ir.FinishReason)

	out := map[string]any{
		"id":          ir.ID,
		"type":        "message",
		"role":         ir.Role,
		"model":        model,
		"content":      content,
		"stop_reason":  stopReason,
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  ir.Usage.PromptTokens,
			"output_tokens": ir.Usage.CompletionTokens,
		},
	}

	return json.Marshal(out)
}

// buildAnthropicResponseContent builds Anthropic content blocks from IR.
func buildAnthropicResponseContent(ir *InternalResponse) []map[string]any {
	content := make([]map[string]any, 0)

	for _, c := range ir.Content {
		switch c.Type {
		case "text":
			content = append(content, map[string]any{"type": "text", "text": c.Text})
		case "tool_use":
			var input any
			if c.Input != nil {
				_ = json.Unmarshal(c.Input, &input)
			}
			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    c.ID,
				"name":  c.Name,
				"input": input,
			})
		case "thinking":
			content = append(content, map[string]any{
				"type":     "thinking",
				"thinking": c.Thinking,
			})
		}
	}

	// Add tool_use blocks for tool calls not already in content
	existingIDs := make(map[string]bool)
	for _, c := range ir.Content {
		if c.Type == "tool_use" {
			existingIDs[c.ID] = true
		}
	}
	for _, tc := range ir.ToolCalls {
		if !existingIDs[tc.ID] {
			content = append(content, map[string]any{
				"type": "tool_use",
				"id":   tc.ID,
				"name": tc.Name,
			})
		}
	}

	if len(content) == 0 {
		content = append(content, map[string]any{"type": "text", "text": ""})
	}
	return content
}

// ─── Finish reason helpers ───────────────────────────────────────────────────

// mapAnthropicFinishReason converts Anthropic stop reasons to OpenAI form.
func mapAnthropicFinishReason(reason string) string {
	switch reason {
	case "end_turn":
		return "stop"
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	case "refusal":
		return "content_filter"
	default:
		return "stop"
	}
}

// mapFinishReasonToAnthropic converts OpenAI finish reasons to Anthropic form.
func mapFinishReasonToAnthropic(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	case "content_filter":
		return "refusal"
	default:
		return "end_turn"
	}
}
