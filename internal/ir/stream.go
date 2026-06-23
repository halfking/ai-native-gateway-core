package ir

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// StreamChunk is the unified intermediate representation for streaming chunks.
// Its field set is the superset of OpenAI SSE chunks and Anthropic SSE events.
//
// Architecture (streaming direction):
//
//	OpenAI SSE chunk     ──Parse──→
//	                                 StreamChunk (superset) ──Serialize──→ OpenAI SSE
//	Anthropic SSE event  ──Parse──→                                        Anthropic SSE
//
// Complexity reduced from O(N²) to O(N): adding a new protocol only requires
// one Parser + one Serializer.
type StreamChunk struct {
	Type ChunkType // "delta" | "usage" | "done" | "error"

	// Delta content (Type="delta")
	Delta *StreamDelta

	// Usage info (Type="usage")
	Usage *StreamUsage

	// Error info (Type="error")
	Error *StreamError

	// Metadata (present in all chunk types)
	ID           string // Chunk ID (OpenAI: chatcmpl-xxx, Anthropic: msg_xxx)
	Model        string // Model name
	Created      int64  // Unix timestamp (OpenAI style; 0 if not available)
	FinishReason string // When stream ends: "stop" | "length" | "tool_calls" | etc.

	// Source protocol tracking (used by Serializer to determine output format)
	SourceProtocol string // "openai-chat" | "anthropic-messages"
}

// ChunkType discriminates the chunk purpose.
type ChunkType string

const (
	ChunkTypeDelta ChunkType = "delta" // Incremental content
	ChunkTypeUsage ChunkType = "usage" // Token usage statistics
	ChunkTypeDone  ChunkType = "done"  // Stream termination
	ChunkTypeError ChunkType = "error" // Error occurred
)

// StreamDelta represents incremental content in a chunk.
type StreamDelta struct {
	Role             string                // "assistant" (first chunk only)
	Content          string                // Text content delta
	ReasoningContent string                // Thinking/reasoning delta (OpenAI: reasoning_content, Anthropic: thinking)
	ToolCalls        []StreamToolCallDelta // Incremental tool calls
}

// StreamToolCallDelta represents incremental tool call data.
type StreamToolCallDelta struct {
	Index     int    // Tool call array index (OpenAI convention)
	ID        string // Tool call ID (first chunk only)
	Type      string // "function" (OpenAI), "tool_use" (Anthropic maps to function)
	Name      string // Function name (first chunk only)
	Arguments string // Incremental JSON arguments
}

// StreamUsage holds token usage statistics for a stream chunk.
type StreamUsage struct {
	PromptTokens     int // Input tokens (Anthropic: input_tokens)
	CompletionTokens int // Output tokens (Anthropic: output_tokens)
	TotalTokens      int // Sum of prompt + completion
}

// StreamError represents an error in the stream.
type StreamError struct {
	Type    string // "timeout" | "upstream_error" | "invalid_chunk" | etc.
	Message string // Human-readable error message
	Code    string // Error code for programmatic handling
}

// ─── Parsers ────────────────────────────────────────────────────────────────

// ParseOpenAIStreamChunk parses an OpenAI SSE line "data: {...}\n\n" into StreamChunk IR.
//
// OpenAI streaming format:
//
//	data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":123,
//	       "model":"gpt-4","choices":[{"index":0,"delta":{"role":"assistant","content":"Hi"},
//	       "finish_reason":null}]}
//
// Handles:
//   - choices[0].delta.role → Delta.Role
//   - choices[0].delta.content → Delta.Content
//   - choices[0].delta.reasoning_content → Delta.ReasoningContent
//   - choices[0].delta.tool_calls → Delta.ToolCalls
//   - choices[0].finish_reason → FinishReason
//   - usage → Usage
//   - "data: [DONE]" → ChunkTypeDone
func ParseOpenAIStreamChunk(line string) (*StreamChunk, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, fmt.Errorf("empty line")
	}

	// Extract "data: " prefix
	if !strings.HasPrefix(line, "data: ") {
		return nil, fmt.Errorf("not a data line: %s", line)
	}
	payload := strings.TrimPrefix(line, "data: ")
	payload = strings.TrimSpace(payload)

	// Handle [DONE] sentinel
	if payload == "[DONE]" {
		return &StreamChunk{
			Type:           ChunkTypeDone,
			SourceProtocol: ProtocolOpenAIChat,
		}, nil
	}

	// Parse JSON
	var raw struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		Model   string `json:"model"`
		Choices []struct {
			Index int `json:"index"`
			Delta struct {
				Role             string          `json:"role"`
				Content          string          `json:"content"`
				ReasoningContent string          `json:"reasoning_content"`
				ToolCalls        json.RawMessage `json:"tool_calls"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return nil, fmt.Errorf("unmarshal openai chunk: %w", err)
	}

	chunk := &StreamChunk{
		ID:             raw.ID,
		Model:          raw.Model,
		Created:        raw.Created,
		SourceProtocol: ProtocolOpenAIChat,
	}

	// Parse usage (if present)
	if raw.Usage != nil {
		chunk.Type = ChunkTypeUsage
		chunk.Usage = &StreamUsage{
			PromptTokens:     raw.Usage.PromptTokens,
			CompletionTokens: raw.Usage.CompletionTokens,
			TotalTokens:      raw.Usage.TotalTokens,
		}
		// Usage chunks can also have finish_reason
		if len(raw.Choices) > 0 && raw.Choices[0].FinishReason != nil {
			chunk.FinishReason = *raw.Choices[0].FinishReason
		}
		return chunk, nil
	}

	// Parse delta content
	if len(raw.Choices) > 0 {
		choice := raw.Choices[0]
		delta := choice.Delta

		chunk.Type = ChunkTypeDelta
		chunk.Delta = &StreamDelta{
			Role:             delta.Role,
			Content:          delta.Content,
			ReasoningContent: delta.ReasoningContent,
		}

		// Parse tool_calls if present
		if len(delta.ToolCalls) > 0 {
			var toolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function *struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			}
			if err := json.Unmarshal(delta.ToolCalls, &toolCalls); err == nil {
				for _, tc := range toolCalls {
					streamTC := StreamToolCallDelta{
						Index: tc.Index,
						ID:    tc.ID,
						Type:  tc.Type,
					}
					if tc.Function != nil {
						streamTC.Name = tc.Function.Name
						streamTC.Arguments = tc.Function.Arguments
					}
					chunk.Delta.ToolCalls = append(chunk.Delta.ToolCalls, streamTC)
				}
			}
		}

		// Parse finish_reason
		if choice.FinishReason != nil {
			chunk.FinishReason = *choice.FinishReason
		}

		return chunk, nil
	}

	// Empty chunk (no delta, no usage)
	chunk.Type = ChunkTypeDelta
	chunk.Delta = &StreamDelta{}
	return chunk, nil
}

// ParseAnthropicStreamEvent parses an Anthropic SSE event into StreamChunk IR.
//
// Anthropic streaming format:
//
//	event: message_start
//	data: {"type":"message_start","message":{"id":"msg_xxx","model":"claude-3",
//	       "usage":{"input_tokens":10,"output_tokens":0}}}
//
//	event: content_block_delta
//	data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}
//
// Handles:
//   - message_start → ChunkTypeUsage (with input_tokens)
//   - content_block_delta (text_delta) → ChunkTypeDelta with Content
//   - content_block_delta (thinking_delta) → ChunkTypeDelta with ReasoningContent
//   - content_block_delta (input_json_delta) → ChunkTypeDelta with ToolCalls
//   - message_delta → ChunkTypeDelta with FinishReason + usage
//   - message_stop → ChunkTypeDone
//   - error → ChunkTypeError
func ParseAnthropicStreamEvent(eventType string, data []byte) (*StreamChunk, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data")
	}

	// Parse base event structure
	var base struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &base); err != nil {
		return nil, fmt.Errorf("unmarshal base: %w", err)
	}

	chunk := &StreamChunk{
		SourceProtocol: ProtocolAnthropicMessages,
	}

	switch base.Type {
	case "message_start":
		var evt struct {
			Message struct {
				ID    string `json:"id"`
				Model string `json:"model"`
				Usage struct {
					InputTokens int `json:"input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal(data, &evt); err != nil {
			return nil, fmt.Errorf("unmarshal message_start: %w", err)
		}

		chunk.Type = ChunkTypeUsage
		chunk.ID = evt.Message.ID
		chunk.Model = evt.Message.Model
		chunk.Usage = &StreamUsage{
			PromptTokens: evt.Message.Usage.InputTokens,
		}
		return chunk, nil

	case "content_block_start":
		// Tool use block start
		var evt struct {
			Index        int `json:"index"`
			ContentBlock struct {
				Type  string          `json:"type"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"content_block"`
		}
		if err := json.Unmarshal(data, &evt); err != nil {
			return nil, fmt.Errorf("unmarshal content_block_start: %w", err)
		}

		if evt.ContentBlock.Type == "tool_use" {
			chunk.Type = ChunkTypeDelta
			chunk.Delta = &StreamDelta{
				ToolCalls: []StreamToolCallDelta{{
					Index:     evt.Index,
					ID:        evt.ContentBlock.ID,
					Type:      "function",
					Name:      evt.ContentBlock.Name,
					Arguments: string(evt.ContentBlock.Input),
				}},
			}
			return chunk, nil
		}

		// Text or thinking block start (no content yet)
		chunk.Type = ChunkTypeDelta
		chunk.Delta = &StreamDelta{}
		return chunk, nil

	case "content_block_delta":
		var evt struct {
			Index int `json:"index"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		if err := json.Unmarshal(data, &evt); err != nil {
			return nil, fmt.Errorf("unmarshal content_block_delta: %w", err)
		}

		chunk.Type = ChunkTypeDelta
		chunk.Delta = &StreamDelta{}

		switch evt.Delta.Type {
		case "text_delta", "text":
			chunk.Delta.Content = evt.Delta.Text
		case "thinking_delta", "thinking":
			chunk.Delta.ReasoningContent = evt.Delta.Thinking
		case "input_json_delta":
			chunk.Delta.ToolCalls = []StreamToolCallDelta{{
				Index:     evt.Index,
				Arguments: evt.Delta.PartialJSON,
			}}
		}

		return chunk, nil

	case "content_block_stop":
		// Block boundary marker (no new content)
		chunk.Type = ChunkTypeDelta
		chunk.Delta = &StreamDelta{}
		return chunk, nil

	case "message_delta":
		var evt struct {
			Delta struct {
				StopReason *string `json:"stop_reason"`
			} `json:"delta"`
			Usage struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(data, &evt); err != nil {
			return nil, fmt.Errorf("unmarshal message_delta: %w", err)
		}

		if evt.Usage.OutputTokens > 0 {
			chunk.Type = ChunkTypeUsage
			chunk.Usage = &StreamUsage{
				CompletionTokens: evt.Usage.OutputTokens,
			}
		} else {
			chunk.Type = ChunkTypeDelta
			chunk.Delta = &StreamDelta{}
		}

		if evt.Delta.StopReason != nil {
			// Map Anthropic stop_reason to OpenAI finish_reason
			chunk.FinishReason = mapAnthropicFinishReasonToOpenAI(*evt.Delta.StopReason)
		}

		return chunk, nil

	case "message_stop":
		chunk.Type = ChunkTypeDone
		return chunk, nil

	case "ping":
		// Keep-alive (no content)
		chunk.Type = ChunkTypeDelta
		chunk.Delta = &StreamDelta{}
		return chunk, nil

	case "error":
		var evt struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &evt); err != nil {
			return nil, fmt.Errorf("unmarshal error: %w", err)
		}

		chunk.Type = ChunkTypeError
		chunk.Error = &StreamError{
			Type:    evt.Error.Type,
			Message: evt.Error.Message,
			Code:    evt.Error.Type,
		}
		return chunk, nil

	default:
		return nil, fmt.Errorf("unknown event type: %s", base.Type)
	}
}

// ─── Serializers ────────────────────────────────────────────────────────────

// SerializeOpenAI serializes StreamChunk IR to OpenAI SSE format: "data: {...}\n\n"
func (c *StreamChunk) SerializeOpenAI(chatID string, model string, created int64) string {
	if c == nil {
		return ""
	}

	// Use chunk's metadata if provided
	if c.ID != "" {
		chatID = c.ID
	}
	if c.Model != "" {
		model = c.Model
	}
	if c.Created > 0 {
		created = c.Created
	}

	// Default values
	if chatID == "" {
		chatID = "chatcmpl-stream"
	}
	if created == 0 {
		created = time.Now().Unix()
	}

	switch c.Type {
	case ChunkTypeDone:
		return "data: [DONE]\n\n"

	case ChunkTypeError:
		if c.Error == nil {
			return ""
		}
		errBody := map[string]any{
			"error": map[string]any{
				"type":    c.Error.Type,
				"message": c.Error.Message,
				"code":    c.Error.Code,
			},
		}
		body, _ := json.Marshal(errBody)
		return fmt.Sprintf("data: %s\n\n", body)

	case ChunkTypeUsage, ChunkTypeDelta:
		obj := map[string]any{
			"id":      chatID,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
		}

		// Build choices array
		choice := map[string]any{"index": 0}

		if c.Delta != nil {
			delta := map[string]any{}
			if c.Delta.Role != "" {
				delta["role"] = c.Delta.Role
			}
			if c.Delta.Content != "" {
				delta["content"] = c.Delta.Content
			}
			if c.Delta.ReasoningContent != "" {
				delta["reasoning_content"] = c.Delta.ReasoningContent
			}
			if len(c.Delta.ToolCalls) > 0 {
				var toolCalls []map[string]any
				for _, tc := range c.Delta.ToolCalls {
					tcMap := map[string]any{"index": tc.Index}
					if tc.ID != "" {
						tcMap["id"] = tc.ID
					}
					if tc.Type != "" {
						tcMap["type"] = tc.Type
					}
					// OpenAI streaming spec requires the function.arguments field
					// to be present (even as empty string) when function.name is
					// set. Without it, clients throw "Expected 'function.name' to
					// be a string" validation errors.
					if tc.Name != "" || tc.Arguments != "" {
						fn := map[string]any{}
						if tc.Name != "" {
							fn["name"] = tc.Name
							// When name is present, arguments MUST be present too
							// (even if empty string).
							fn["arguments"] = tc.Arguments
						} else if tc.Arguments != "" {
							fn["arguments"] = tc.Arguments
						}
						tcMap["function"] = fn
					}
					toolCalls = append(toolCalls, tcMap)
				}
				delta["tool_calls"] = toolCalls
			}
			choice["delta"] = delta
		} else {
			choice["delta"] = map[string]any{}
		}

		if c.FinishReason != "" {
			choice["finish_reason"] = c.FinishReason
		} else {
			choice["finish_reason"] = nil
		}

		obj["choices"] = []any{choice}

		// Add usage if present
		if c.Usage != nil {
			obj["usage"] = map[string]any{
				"prompt_tokens":     c.Usage.PromptTokens,
				"completion_tokens": c.Usage.CompletionTokens,
				"total_tokens":      c.Usage.TotalTokens,
			}
		}

		body, _ := json.Marshal(obj)
		return fmt.Sprintf("data: %s\n\n", body)

	default:
		return ""
	}
}

// SerializeAnthropic serializes StreamChunk IR to Anthropic SSE format: "event: X\ndata: {...}\n\n"
func (c *StreamChunk) SerializeAnthropic(msgID string, model string) string {
	if c == nil {
		return ""
	}

	// Use chunk's metadata if provided
	if c.ID != "" {
		msgID = c.ID
	}
	if c.Model != "" {
		model = c.Model
	}

	// Default values
	if msgID == "" {
		msgID = "msg_stream"
	}

	switch c.Type {
	case ChunkTypeDone:
		body := map[string]any{"type": "message_stop"}
		data, _ := json.Marshal(body)
		return fmt.Sprintf("event: message_stop\ndata: %s\n\n", data)

	case ChunkTypeError:
		if c.Error == nil {
			return ""
		}
		body := map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    c.Error.Type,
				"message": c.Error.Message,
			},
		}
		data, _ := json.Marshal(body)
		return fmt.Sprintf("event: error\ndata: %s\n\n", data)

	case ChunkTypeUsage:
		// Anthropic emits usage in message_start and message_delta
		if c.Usage != nil && c.Usage.PromptTokens > 0 {
			// message_start (input tokens)
			body := map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id":    msgID,
					"model": model,
					"usage": map[string]any{
						"input_tokens": c.Usage.PromptTokens,
					},
				},
			}
			data, _ := json.Marshal(body)
			return fmt.Sprintf("event: message_start\ndata: %s\n\n", data)
		} else if c.Usage != nil && c.Usage.CompletionTokens > 0 {
			// message_delta (output tokens)
			delta := map[string]any{}
			if c.FinishReason != "" {
				stopReason := mapOpenAIFinishReasonToAnthropic(c.FinishReason)
				delta["stop_reason"] = stopReason
			}
			body := map[string]any{
				"type":  "message_delta",
				"delta": delta,
				"usage": map[string]any{
					"output_tokens": c.Usage.CompletionTokens,
				},
			}
			data, _ := json.Marshal(body)
			return fmt.Sprintf("event: message_delta\ndata: %s\n\n", data)
		}
		return ""

	case ChunkTypeDelta:
		if c.Delta == nil {
			return ""
		}

		var output strings.Builder

		// Text content
		if c.Delta.Content != "" {
			body := map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{
					"type": "text_delta",
					"text": c.Delta.Content,
				},
			}
			data, _ := json.Marshal(body)
			output.WriteString(fmt.Sprintf("event: content_block_delta\ndata: %s\n\n", data))
		}

		// Reasoning content
		if c.Delta.ReasoningContent != "" {
			body := map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{
					"type":     "thinking_delta",
					"thinking": c.Delta.ReasoningContent,
				},
			}
			data, _ := json.Marshal(body)
			output.WriteString(fmt.Sprintf("event: content_block_delta\ndata: %s\n\n", data))
		}

		// Tool calls
		for _, tc := range c.Delta.ToolCalls {
			if tc.Name != "" {
				// content_block_start (tool_use)
				body := map[string]any{
					"type":  "content_block_start",
					"index": tc.Index,
					"content_block": map[string]any{
						"type":  "tool_use",
						"id":    tc.ID,
						"name":  tc.Name,
						"input": map[string]any{},
					},
				}
				data, _ := json.Marshal(body)
				output.WriteString(fmt.Sprintf("event: content_block_start\ndata: %s\n\n", data))
			}
			if tc.Arguments != "" {
				// content_block_delta (input_json_delta)
				body := map[string]any{
					"type":  "content_block_delta",
					"index": tc.Index,
					"delta": map[string]any{
						"type":         "input_json_delta",
						"partial_json": tc.Arguments,
					},
				}
				data, _ := json.Marshal(body)
				output.WriteString(fmt.Sprintf("event: content_block_delta\ndata: %s\n\n", data))
			}
		}

		return output.String()

	default:
		return ""
	}
}

// ─── Finish reason helpers ──────────────────────────────────────────────────

// mapAnthropicFinishReasonToOpenAI converts Anthropic stop reasons to OpenAI form.
func mapAnthropicFinishReasonToOpenAI(reason string) string {
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

// mapOpenAIFinishReasonToAnthropic converts OpenAI finish reasons to Anthropic form.
func mapOpenAIFinishReasonToAnthropic(reason string) string {
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
