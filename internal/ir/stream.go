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

	// Internal stream quality annotation. These fields are not serialized and
	// preserve wire compatibility while exposing tool argument validation.
	Quality             string // verified | partial | rejected
	ArgumentsJSONReason string
}

// AnnotateArgumentsJSON validates completed streaming tool arguments without
// changing the serialized SSE payload.
func (c *StreamChunk) AnnotateArgumentsJSON(arguments string) {
	if c == nil {
		return
	}
	a := NewToolArgumentsAssembler()
	_ = a.Append(arguments)
	_, reason, err := a.Finalize()
	c.ArgumentsJSONReason = reason
	if err != nil {
		c.Quality = "rejected"
		return
	}
	if reason != "" {
		c.Quality = "partial"
		return
	}
	c.Quality = "verified"
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
//
// audit-stream-multimodal (2026-07-13): Extended with multimodal delta fields
// for audio output (OpenAI Realtime/gpt-4o-audio-preview) and thinking signature
// propagation (Anthropic signature_delta).
type StreamDelta struct {
	Role             string                // "assistant" (first chunk only)
	Content          string                // Text content delta
	ReasoningContent string                // Thinking/reasoning delta (OpenAI: reasoning_content, Anthropic: thinking)
	ToolCalls        []StreamToolCallDelta // Incremental tool calls

	// ThinkingSignature carries the Anthropic chain-of-thought verification
	// token emitted at the end of a thinking block (signature_delta event).
	// Critical for Anthropic multi-turn round-trip.
	ThinkingSignature string

	// AudioDelta is the OpenAI audio output chunk (gpt-4o-audio-preview realtime).
	AudioDelta *StreamAudioDelta

	// DeltaType is the explicit content type from the upstream provider:
	//   "text" | "reasoning" | "audio" | "tool_call" | "signature" | ""
	DeltaType string
}

// StreamAudioDelta carries incremental audio output (base64 PCM chunks +
// optional transcript text).
type StreamAudioDelta struct {
	Data       string `json:"data"`
	Transcript string `json:"transcript"`
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
//
// audit-stream-multimodal (2026-07-13): Extended with cache tokens, reasoning
// tokens, and multimodal token breakdowns for parity with non-stream Usage.
type StreamUsage struct {
	PromptTokens     int // Input tokens (Anthropic: input_tokens)
	CompletionTokens int // Output tokens (Anthropic: output_tokens)
	TotalTokens      int // Sum of prompt + completion

	// Cache tokens (Anthropic prompt caching, OpenAI prompt caching)
	CacheReadTokens  *int
	CacheWriteTokens *int

	// Reasoning tokens (DeepSeek R1, OpenAI o1/o3)
	ReasoningTokens *int

	// Multimodal token breakdowns
	ImageTokens *int
	AudioTokens *int
	VideoTokens *int
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

	// Extract "data: " prefix (with lenient parsing for malformed SSE)
	if !strings.HasPrefix(line, "data: ") {
		// Lenient mode: if line looks like JSON, auto-prefix it
		if strings.HasPrefix(line, "{") && strings.Contains(line, "\"id\"") {
			line = "data: " + line
		} else if strings.HasPrefix(line, "event:") || strings.HasPrefix(line, ":") {
			// SSE comment or event line without data - skip silently
			return nil, nil
		} else {
			return nil, fmt.Errorf("not a data line: %s", line)
		}
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
				Content          json.RawMessage `json:"content"`
				ReasoningContent string          `json:"reasoning_content"`
				ToolCalls        json.RawMessage `json:"tool_calls"`
				// audit-stream-multimodal (2026-07-13): OpenAI audio output delta
				Audio json.RawMessage `json:"audio"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
			// audit-stream-multimodal (2026-07-13): detailed usage
			PromptTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
				AudioTokens  int `json:"audio_tokens"`
				ImageTokens  int `json:"image_tokens"`
				VideoTokens  int `json:"video_tokens"`
			} `json:"prompt_tokens_details"`
			CompletionTokensDetails *struct {
				ReasoningTokens int `json:"reasoning_tokens"`
				AudioTokens     int `json:"audio_tokens"`
			} `json:"completion_tokens_details"`
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
		// audit-stream-multimodal (2026-07-13): detailed usage fields
		if raw.Usage.PromptTokensDetails != nil {
			d := raw.Usage.PromptTokensDetails
			if d.CachedTokens > 0 {
				v := d.CachedTokens
				chunk.Usage.CacheReadTokens = &v
			}
			if d.AudioTokens > 0 {
				v := d.AudioTokens
				chunk.Usage.AudioTokens = &v
			}
			if d.ImageTokens > 0 {
				v := d.ImageTokens
				chunk.Usage.ImageTokens = &v
			}
			if d.VideoTokens > 0 {
				v := d.VideoTokens
				chunk.Usage.VideoTokens = &v
			}
		}
		if raw.Usage.CompletionTokensDetails != nil {
			d := raw.Usage.CompletionTokensDetails
			if d.ReasoningTokens > 0 {
				v := d.ReasoningTokens
				chunk.Usage.ReasoningTokens = &v
			}
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

		content := normalizeOpenAIStreamContent(delta.Content)
		chunk.Type = ChunkTypeDelta
		chunk.Delta = &StreamDelta{
			Role:             delta.Role,
			Content:          content,
			ReasoningContent: delta.ReasoningContent,
		}
		// Determine delta type for cross-protocol routing
		switch {
		case delta.Audio != nil && string(delta.Audio) != "null":
			chunk.Delta.DeltaType = "audio"
		case delta.ReasoningContent != "":
			chunk.Delta.DeltaType = "reasoning"
		case content != "":
			chunk.Delta.DeltaType = "text"
		}

		// audit-stream-multimodal (2026-07-13): parse audio output delta
		if len(delta.Audio) > 0 && string(delta.Audio) != "null" {
			var ad struct {
				Data       string `json:"data"`
				Transcript string `json:"transcript"`
				ExpiresAt  int64  `json:"expires_at"`
			}
			if err := json.Unmarshal(delta.Audio, &ad); err == nil {
				chunk.Delta.AudioDelta = &StreamAudioDelta{
					Data:       ad.Data,
					Transcript: ad.Transcript,
				}
			}
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

// normalizeOpenAIStreamContent accepts both standard OpenAI string deltas and
// Qwen/DashScope's structured text delta array. Unknown block shapes do not
// represent text and are intentionally omitted from the text-only StreamDelta.
func normalizeOpenAIStreamContent(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}

	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}

	var builder strings.Builder
	for _, block := range blocks {
		if value, ok := block["text"].(string); ok {
			builder.WriteString(value)
		}
	}
	return builder.String()
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
					InputTokens              int `json:"input_tokens"`
					CacheCreationInputTokens int `json:"cache_creation_input_tokens"` // audit-stream-multimodal
					CacheReadInputTokens     int `json:"cache_read_input_tokens"`     // audit-stream-multimodal
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
		// audit-stream-multimodal (2026-07-13): extract Anthropic cache tokens
		if evt.Message.Usage.CacheCreationInputTokens > 0 {
			v := evt.Message.Usage.CacheCreationInputTokens
			chunk.Usage.CacheWriteTokens = &v
		}
		if evt.Message.Usage.CacheReadInputTokens > 0 {
			v := evt.Message.Usage.CacheReadInputTokens
			chunk.Usage.CacheReadTokens = &v
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

		// PR-2 (2026-06-24): thinking-block start. Emit a Delta with
		// Role pre-filled so downstream consumers can mark HasThinking
		// without scanning the raw payload. The actual thinking text
		// arrives in the following thinking_delta(s).
		if evt.ContentBlock.Type == "thinking" {
			chunk.Type = ChunkTypeDelta
			chunk.Delta = &StreamDelta{}
			return chunk, nil
		}

		// Text block start (no content yet)
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
				// PR-2 (2026-06-24): signature_delta closes a thinking
				// block. OpenAI has no equivalent, so the IR chunk stays
				// empty — but we must NOT fall through to the default
				// branch and drop the chunk entirely (that was the
				// root cause of the "tool_use lost on opus-4-8" symptom).
				Signature string `json:"signature"`
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
		case "signature_delta":
			// audit-stream-multimodal (2026-07-13): propagate the signature
			// through the IR so downstream serializer can preserve it on the
			// next Anthropic turn (without this, opus-4-8 multi-turn requests
			// lose the chain-of-thought verification token).
			chunk.Delta.ThinkingSignature = evt.Delta.Signature
			chunk.Delta.DeltaType = "signature"
		}

		return chunk, nil

	case "content_block_stop":
		// Block boundary marker. Callers that accumulated input_json_delta can
		// annotate this chunk with the completed arguments before forwarding.
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
			// audit-stream-multimodal (2026-07-13): OpenAI audio output delta
			if c.Delta.AudioDelta != nil {
				audioInner := map[string]any{}
				if c.Delta.AudioDelta.Data != "" {
					audioInner["data"] = c.Delta.AudioDelta.Data
				}
				if c.Delta.AudioDelta.Transcript != "" {
					audioInner["transcript"] = c.Delta.AudioDelta.Transcript
				}
				delta["audio"] = audioInner
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
			usageObj := map[string]any{
				"prompt_tokens":     c.Usage.PromptTokens,
				"completion_tokens": c.Usage.CompletionTokens,
				"total_tokens":      c.Usage.TotalTokens,
			}
			// audit-stream-multimodal (2026-07-13): detailed usage output
			promptDetails := map[string]any{}
			if c.Usage.CacheReadTokens != nil {
				promptDetails["cached_tokens"] = *c.Usage.CacheReadTokens
			}
			if c.Usage.ImageTokens != nil {
				promptDetails["image_tokens"] = *c.Usage.ImageTokens
			}
			if c.Usage.AudioTokens != nil {
				promptDetails["audio_tokens"] = *c.Usage.AudioTokens
			}
			if c.Usage.VideoTokens != nil {
				promptDetails["video_tokens"] = *c.Usage.VideoTokens
			}
			if len(promptDetails) > 0 {
				usageObj["prompt_tokens_details"] = promptDetails
			}
			completionDetails := map[string]any{}
			if c.Usage.ReasoningTokens != nil {
				completionDetails["reasoning_tokens"] = *c.Usage.ReasoningTokens
			}
			if len(completionDetails) > 0 {
				usageObj["completion_tokens_details"] = completionDetails
			}
			obj["usage"] = usageObj
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
			fmt.Fprintf(&output, "event: content_block_delta\ndata: %s\n\n", data)
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
			fmt.Fprintf(&output, "event: content_block_delta\ndata: %s\n\n", data)
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
				fmt.Fprintf(&output, "event: content_block_start\ndata: %s\n\n", data)
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
				fmt.Fprintf(&output, "event: content_block_delta\ndata: %s\n\n", data)
			}
		}

		return output.String()

	default:
		return ""
	}
}

// SerializeResponses serializes StreamChunk IR to OpenAI Responses API SSE
// format. Emits ONE OR MORE events per call depending on chunk content:
//
//   - ChunkTypeDelta (text):
//     event: response.output_text.delta
//
//   - ChunkTypeDelta (reasoning):
//     event: response.reasoning_text.delta
//
//   - ChunkTypeDelta (tool calls):
//     event: response.output_item.added           (when Name is non-empty, i.e. new tool call)
//     event: response.function_call_arguments.delta (when Arguments is non-empty)
//
//   - ChunkTypeError:
//     event: error
//
//   - ChunkTypeUsage / ChunkTypeDone:
//     ""  (no standalone event — orchestrator accumulates usage and emits
//     response.completed with full context)
//
// Caller (the bridge/orchestrator) is responsible for the surrounding
// response.created / response.output_item.added / response.content_part.added
// scaffolding at the start, and response.output_text.done /
// response.output_item.done / response.completed at the end.
//
// itemID is the `item_id` for the message item that holds the delta.
// For tool calls, each tool call gets its own id surfaced via tc.ID and
// is referenced as item_id in subsequent function_call_arguments.delta events.
//
// Phase E (2026-07-01): adds the Responses API slot to the IR serializer
// matrix (alongside SerializeOpenAI / SerializeAnthropic), so adding the
// protocol stays O(N) — one new serializer, no bridge rewrite.
func (c *StreamChunk) SerializeResponses(itemID string) string {
	if c == nil {
		return ""
	}

	if itemID == "" {
		itemID = "msg_stream"
	}

	switch c.Type {
	case ChunkTypeDone, ChunkTypeUsage:
		// Orchestrator (bridge) handles these: response.completed aggregates
		// accumulated usage; no standalone SSE event here.
		return ""

	case ChunkTypeError:
		if c.Error == nil {
			return ""
		}
		body := map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    c.Error.Type,
				"message": c.Error.Message,
				"code":    c.Error.Code,
			},
		}
		data, _ := json.Marshal(body)
		return fmt.Sprintf("event: error\ndata: %s\n\n", data)

	case ChunkTypeDelta:
		if c.Delta == nil {
			return ""
		}

		var output strings.Builder

		// 1) Text content → response.output_text.delta
		if c.Delta.Content != "" {
			body := map[string]any{
				"type":          "response.output_text.delta",
				"item_id":       itemID,
				"output_index":  0,
				"content_index": 0,
				"delta":         c.Delta.Content,
			}
			data, _ := json.Marshal(body)
			fmt.Fprintf(&output, "event: response.output_text.delta\ndata: %s\n\n", data)
		}

		// 2) Reasoning content → response.reasoning_text.delta
		if c.Delta.ReasoningContent != "" {
			body := map[string]any{
				"type":          "response.reasoning_text.delta",
				"item_id":       itemID,
				"output_index":  0,
				"content_index": 0,
				"delta":         c.Delta.ReasoningContent,
			}
			data, _ := json.Marshal(body)
			fmt.Fprintf(&output, "event: response.reasoning_text.delta\ndata: %s\n\n", data)
		}

		// 3) Tool calls → response.output_item.added (new) +
		//                 response.function_call_arguments.delta (continued args)
		for _, tc := range c.Delta.ToolCalls {
			if tc.Name != "" {
				// New tool call — surface as output_item.added so the
				// client's response.output[] grows.
				body := map[string]any{
					"type":         "response.output_item.added",
					"output_index": tc.Index,
					"item": map[string]any{
						"type":      "function_call",
						"id":        tc.ID,
						"call_id":   tc.ID,
						"name":      tc.Name,
						"arguments": "",
						"status":    "in_progress",
					},
				}
				data, _ := json.Marshal(body)
				fmt.Fprintf(&output, "event: response.output_item.added\ndata: %s\n\n", data)
			}
			if tc.Arguments != "" {
				// Continue existing tool call's argument stream.
				body := map[string]any{
					"type":         "response.function_call_arguments.delta",
					"item_id":      tc.ID,
					"output_index": tc.Index,
					"delta":        tc.Arguments,
				}
				data, _ := json.Marshal(body)
				fmt.Fprintf(&output, "event: response.function_call_arguments.delta\ndata: %s\n\n", data)
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

// ─── Gemini Stream Serializer (audit-gemini-stream, 2026-07-13) ─────────────

// SerializeGemini serializes a StreamChunk IR to the Gemini
// streamGenerateContent SSE format. Output is one `data: {...}\n\n`
// line per chunk (matching Gemini's wire format).
//
// Gemini SSE shape:
//
//	data: {"candidates":[{"content":{"parts":[...],"role":"model"},"index":0}]}
//	data: {"candidates":[{"content":{"parts":[...],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{...}}
//	data: [DONE]
//
// audit-gemini-stream (2026-07-13): Completes the Gemini native adapter by
// adding stream serialization. Map IR StreamDelta → Gemini parts:
//   - text       → {"text": "..."}
//   - reasoning  → {"thought": "..."} (Gemini 2.5+)
//   - tool_call  → {"functionCall": {"name": "...", "args": {...}}}
//
// Usage chunks emit usageMetadata with modality-aware token breakdowns.
func (c *StreamChunk) SerializeGemini() string {
	if c == nil {
		return ""
	}

	switch c.Type {
	case ChunkTypeDone:
		return "data: [DONE]\n\n"

	case ChunkTypeError:
		if c.Error == nil {
			return ""
		}
		// Gemini error events use promptFeedback.blockReason or inline error.
		// For simplicity we emit a candidate with an empty finishReason.
		body := map[string]any{
			"error": map[string]any{
				"code":    c.Error.Code,
				"message": c.Error.Message,
				"status":  c.Error.Type,
			},
		}
		data, _ := json.Marshal(body)
		return fmt.Sprintf("data: %s\n\n", data)

	case ChunkTypeUsage, ChunkTypeDelta:
		body := map[string]any{}

		// Build candidates array (most chunks have one candidate)
		if c.Type == ChunkTypeDelta || c.FinishReason != "" {
			candidate := map[string]any{"index": 0}

			if c.Delta != nil {
				parts := make([]map[string]any, 0)

				// Text content
				if c.Delta.Content != "" {
					parts = append(parts, map[string]any{"text": c.Delta.Content})
				}

				// Reasoning (Gemini 2.5+ thought)
				if c.Delta.ReasoningContent != "" {
					parts = append(parts, map[string]any{"thought": c.Delta.ReasoningContent})
				}

				// Tool calls (functionCall)
				for _, tc := range c.Delta.ToolCalls {
					if tc.Name != "" {
						var args any = map[string]any{}
						if tc.Arguments != "" {
							if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
								// Fallback: pass raw string
								args = tc.Arguments
							}
						}
						parts = append(parts, map[string]any{
							"functionCall": map[string]any{
								"name": tc.Name,
								"args": args,
							},
						})
					}
				}

				if len(parts) > 0 {
					candidate["content"] = map[string]any{
						"role":  "model",
						"parts": parts,
					}
				}
			}

			if c.FinishReason != "" {
				candidate["finishReason"] = mapOpenAIFinishReasonToGemini(c.FinishReason)
			}

			body["candidates"] = []map[string]any{candidate}
		}

		// Usage metadata (end-of-stream or usage chunks)
		if c.Usage != nil {
			body["usageMetadata"] = buildGeminiUsageMetadata(c.Usage)
		}

		// Model version echo (if known)
		if c.Model != "" {
			body["modelVersion"] = c.Model
		}

		data, _ := json.Marshal(body)
		return fmt.Sprintf("data: %s\n\n", data)

	default:
		return ""
	}
}

// buildGeminiUsageMetadata constructs the usageMetadata block from IR StreamUsage.
// Includes modality-aware token breakdowns for multimodal billing accuracy.
func buildGeminiUsageMetadata(usage *StreamUsage) map[string]any {
	md := map[string]any{
		"promptTokenCount":     usage.PromptTokens,
		"candidatesTokenCount": usage.CompletionTokens,
		"totalTokenCount":      usage.TotalTokens,
	}

	// Modality-aware prompt tokens (Gemini 2.0+)
	var promptDetails []map[string]any
	if usage.ImageTokens != nil && *usage.ImageTokens > 0 {
		promptDetails = append(promptDetails, map[string]any{
			"modality":   "IMAGE",
			"tokenCount": *usage.ImageTokens,
		})
	}
	if usage.AudioTokens != nil && *usage.AudioTokens > 0 {
		promptDetails = append(promptDetails, map[string]any{
			"modality":   "AUDIO",
			"tokenCount": *usage.AudioTokens,
		})
	}
	if len(promptDetails) > 0 {
		md["promptTokensDetails"] = promptDetails
	}

	// Cached content tokens (Gemini context caching)
	if usage.CacheReadTokens != nil && *usage.CacheReadTokens > 0 {
		md["cachedContentTokenCount"] = *usage.CacheReadTokens
	}

	// Reasoning tokens (Gemini 2.5+ thoughts)
	if usage.ReasoningTokens != nil && *usage.ReasoningTokens > 0 {
		md["thoughtsTokenCount"] = *usage.ReasoningTokens
	}

	return md
}

// mapOpenAIFinishReasonToGemini converts IR/OpenAI finish reasons to Gemini form.
func mapOpenAIFinishReasonToGemini(reason string) string {
	switch reason {
	case "stop":
		return "STOP"
	case "length":
		return "MAX_TOKENS"
	case "content_filter":
		return "SAFETY"
	case "tool_calls":
		return "STOP" // Gemini emits STOP with functionCall part
	default:
		return "STOP"
	}
}
