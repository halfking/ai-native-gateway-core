package ir

import (
	"bytes"
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
	ID             string
	Model          string
	Created        int64  // Unix timestamp (OpenAI style); 0 if not available
	Role           string // "assistant" (Anthropic uses top-level role field)
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

	// Extensions carries non-standard top-level fields extracted by the
	// transport layer for lossless round-trip conversion (same semantics as
	// InternalRequest.Extensions).
	Extensions map[string]json.RawMessage
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

	// Signature is the Anthropic chain-of-thought verification token
	// returned with thinking blocks. Populated only for type=thinking.
	// PR-2 (2026-06-24): required for opus-4-8 multi-turn round-trip —
	// without it the next turn is rejected with HTTP 400 and the
	// model loses the prior tool_use context.
	Signature string
}

// ResponseToolCall represents a tool call from the assistant.
type ResponseToolCall struct {
	ID   string
	Name string
	// Arguments is the JSON-stringified tool input. Populated from
	// OpenAI's `tool_calls[].function.arguments` verbatim so non-JSON
	// strings (e.g. provider-specific edge cases) round-trip without
	// reinterpretation. For Anthropic parsed via ParseAnthropicResponse
	// this is the marshalled `tool_use.input` and InputRaw carries the
	// same payload for callers that need the raw bytes.
	Arguments string
	// InputRaw is the raw JSON of the tool input. It is populated by
	// the parser (Anthropic `tool_use.input`, OpenAI
	// `function.arguments` parsed as JSON) and preferred by serializers
	// that want a lossless wire-round-trip. May be empty if the upstream
	// payload could not be parsed as a JSON object.
	InputRaw json.RawMessage
}

// ResponseUsage holds token usage statistics.
// audit-ir-multimodal (2026-07-13): Extended to support cache tokens,
// reasoning tokens, and multimodal (vision/audio/video) token breakdowns
// for accurate billing across all providers.
type ResponseUsage struct {
	// Basic token counts (always present)
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int

	// Cache tokens (Anthropic, OpenAI with prompt caching)
	// nil = not applicable for this provider/request
	CacheReadTokens  *int // Cache hit tokens (billed at reduced rate)
	CacheWriteTokens *int // Cache creation tokens (billed at premium rate)

	// Reasoning tokens (DeepSeek R1, OpenAI reasoning models)
	ReasoningTokens *int

	// Multimodal token breakdowns (Vision, Audio, Video)
	// Enables separate billing for different modalities
	ImageTokens *int // Vision input tokens
	AudioTokens *int // Audio input/output tokens
	VideoTokens *int // Video input tokens

	// Provider-specific token counts (e.g., Doubao seed_token_usage)
	ProviderTokens *int
}

// ─── Parse ─────────────────────────────────────────────────────────────────

// ParseAnthropicResponse parses an Anthropic Messages API response body into IR.
func ParseAnthropicResponse(body []byte) (*InternalResponse, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("empty response body")
	}
	var src struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Role    string `json:"role"`
		Model   string `json:"model"`
		Content []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			Thinking  string          `json:"thinking"`
			Signature string          `json:"signature"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"` // audit-ir-multimodal (2026-07-13)
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`     // audit-ir-multimodal (2026-07-13)
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

	// audit-ir-multimodal (2026-07-13): Extract Anthropic cache tokens
	// Previously these fields were ignored in non-streaming responses,
	// causing billing inaccuracy for Anthropic requests with prompt caching.
	if src.Usage.CacheCreationInputTokens > 0 {
		v := src.Usage.CacheCreationInputTokens
		ir.Usage.CacheWriteTokens = &v
	}
	if src.Usage.CacheReadInputTokens > 0 {
		v := src.Usage.CacheReadInputTokens
		ir.Usage.CacheReadTokens = &v
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
			// 2026-07-27: Preserve the raw tool_use.input so downstream
			// serializers (Anthropic→OpenAI) don't lose arguments when
			// the payload is a JSON scalar (string/number/bool) instead
			// of an object. We still expose Arguments for callers that
			// only need the string form.
			arguments := ""
			if len(c.Input) > 0 {
				trimmed := bytes.TrimSpace(c.Input)
				if json.Valid(trimmed) {
					arguments = string(trimmed)
				} else {
					arguments = string(trimmed)
				}
			}
			ir.ToolCalls = append(ir.ToolCalls, ResponseToolCall{
				ID:        c.ID,
				Name:      c.Name,
				Arguments: arguments,
				InputRaw:  append(json.RawMessage(nil), c.Input...),
			})
		case "thinking":
			if c.Thinking != "" {
				ir.ReasoningContent += c.Thinking
			}
			// PR-2 (2026-06-24): always append the thinking block, even
			// when Thinking text is empty, so the signature survives
			// the parse → serialize round-trip. Some upstream responses
			// ship a redacted_thinking with only a signature; dropping
			// it here would break the next-turn request.
			ir.Content = append(ir.Content, ResponseContentBlock{
				Type:      "thinking",
				Thinking:  c.Thinking,
				Signature: c.Signature,
			})
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
				Role      string `json:"role"`
				Content   any    `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
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
			// audit-ir-multimodal (2026-07-13): OpenAI detailed usage fields
			PromptTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
				AudioTokens  int `json:"audio_tokens"`
				ImageTokens  int `json:"image_tokens"`
				VideoTokens  int `json:"video_tokens"`
			} `json:"prompt_tokens_details"`
			CompletionTokensDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
				AudioTokens     int `json:"audio_tokens"`
			} `json:"completion_tokens_details"`
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

	// audit-ir-multimodal (2026-07-13): Extract OpenAI detailed usage fields
	// for accurate multimodal billing (vision, audio, video) and cache tokens.
	if src.Usage.PromptTokensDetails.CachedTokens > 0 {
		v := src.Usage.PromptTokensDetails.CachedTokens
		ir.Usage.CacheReadTokens = &v
	}
	if src.Usage.PromptTokensDetails.ImageTokens > 0 {
		v := src.Usage.PromptTokensDetails.ImageTokens
		ir.Usage.ImageTokens = &v
	}
	if src.Usage.PromptTokensDetails.AudioTokens > 0 {
		v := src.Usage.PromptTokensDetails.AudioTokens
		ir.Usage.AudioTokens = &v
	}
	if src.Usage.PromptTokensDetails.VideoTokens > 0 {
		v := src.Usage.PromptTokensDetails.VideoTokens
		ir.Usage.VideoTokens = &v
	}
	if src.Usage.CompletionTokensDetails.ReasoningTokens > 0 {
		v := src.Usage.CompletionTokensDetails.ReasoningTokens
		ir.Usage.ReasoningTokens = &v
	}
	if src.Usage.CompletionTokensDetails.AudioTokens > 0 {
		// Audio output tokens (GPT-4o Audio)
		v := src.Usage.CompletionTokensDetails.AudioTokens
		if ir.Usage.AudioTokens == nil {
			ir.Usage.AudioTokens = &v
		} else {
			// Sum input + output audio tokens
			total := *ir.Usage.AudioTokens + v
			ir.Usage.AudioTokens = &total
		}
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
			// 2026-07-27: Preserve the raw arguments payload as JSON
			// when possible so downstream OpenAI/Anthropic serializers
			// emit byte-equivalent arguments instead of an empty string
			// for non-object inputs.
			rawArgs := json.RawMessage(nil)
			if tc.Function.Arguments != "" {
				if json.Valid([]byte(tc.Function.Arguments)) {
					rawArgs = json.RawMessage(tc.Function.Arguments)
				} else {
					// Some upstreams wrap arguments in a literal that
					// isn't valid JSON (e.g. an unquoted string). Keep
					// the original bytes so we don't lose data.
					rawArgs = json.RawMessage(tc.Function.Arguments)
				}
			}
			ir.ToolCalls = append(ir.ToolCalls, ResponseToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
				InputRaw:  rawArgs,
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
		// 2026-07-27: Prefer the preserved raw JSON payload. Fall back to
		// the legacy string form when the parser couldn't produce valid
		// JSON (e.g. non-object tool inputs).
		arguments := tc.Arguments
		if len(tc.InputRaw) > 0 && json.Valid(tc.InputRaw) {
			arguments = string(tc.InputRaw)
		}
		toolCalls = append(toolCalls, map[string]any{
			"id":   tc.ID,
			"type": "function",
			"function": map[string]any{
				"name":      tc.Name,
				"arguments": arguments,
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
			"index":         0,
			"message":       msg,
			"finish_reason": finishReason,
		}},
		"usage": buildOpenAIUsageObject(&ir.Usage),
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

// buildOpenAIUsageObject builds OpenAI usage object with detailed token breakdowns.
// audit-ir-multimodal (2026-07-13): Supports cache tokens, reasoning tokens,
// and multimodal token fields (image, audio, video) for accurate billing.
func buildOpenAIUsageObject(usage *ResponseUsage) map[string]any {
	usageObj := map[string]any{
		"prompt_tokens":     usage.PromptTokens,
		"completion_tokens": usage.CompletionTokens,
		"total_tokens":      usage.TotalTokens,
	}

	// prompt_tokens_details (cache, multimodal input)
	promptDetails := make(map[string]any)
	if usage.CacheReadTokens != nil && *usage.CacheReadTokens > 0 {
		promptDetails["cached_tokens"] = *usage.CacheReadTokens
	}
	if usage.ImageTokens != nil && *usage.ImageTokens > 0 {
		promptDetails["image_tokens"] = *usage.ImageTokens
	}
	if usage.AudioTokens != nil && *usage.AudioTokens > 0 {
		promptDetails["audio_tokens"] = *usage.AudioTokens
	}
	if usage.VideoTokens != nil && *usage.VideoTokens > 0 {
		promptDetails["video_tokens"] = *usage.VideoTokens
	}
	if len(promptDetails) > 0 {
		usageObj["prompt_tokens_details"] = promptDetails
	}

	// completion_tokens_details (reasoning, audio output)
	completionDetails := make(map[string]any)
	if usage.ReasoningTokens != nil && *usage.ReasoningTokens > 0 {
		completionDetails["reasoning_tokens"] = *usage.ReasoningTokens
	}
	if len(completionDetails) > 0 {
		usageObj["completion_tokens_details"] = completionDetails
	}

	return usageObj
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
		"id":            ir.ID,
		"type":          "message",
		"role":          ir.Role,
		"model":         model,
		"content":       content,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage":         buildAnthropicUsageObject(&ir.Usage),
	}

	return json.Marshal(out)
}

// buildAnthropicUsageObject builds Anthropic usage object with cache token details.
// audit-ir-multimodal (2026-07-13): Exports cache_creation_input_tokens and
// cache_read_input_tokens for accurate Anthropic prompt caching billing.
func buildAnthropicUsageObject(usage *ResponseUsage) map[string]any {
	usageObj := map[string]any{
		"input_tokens":  usage.PromptTokens,
		"output_tokens": usage.CompletionTokens,
	}

	// Anthropic cache token fields
	if usage.CacheWriteTokens != nil && *usage.CacheWriteTokens > 0 {
		usageObj["cache_creation_input_tokens"] = *usage.CacheWriteTokens
	}
	if usage.CacheReadTokens != nil && *usage.CacheReadTokens > 0 {
		usageObj["cache_read_input_tokens"] = *usage.CacheReadTokens
	}

	return usageObj
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
			thinking := map[string]any{
				"type":     "thinking",
				"thinking": c.Thinking,
			}
			// PR-2 (2026-06-24): emit signature on serialize so the
			// next Anthropic turn can verify the chain-of-thought.
			// Anthropic rejects requests missing the field.
			if c.Signature != "" {
				thinking["signature"] = c.Signature
			}
			content = append(content, thinking)
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
		if existingIDs[tc.ID] {
			continue
		}
		// 2026-07-27: Use the preserved raw JSON input when available so
		// non-object payloads (string/number/bool) round-trip instead of
		// being silently replaced with an empty object.
		var input any = map[string]any{}
		if len(tc.InputRaw) > 0 && json.Valid(tc.InputRaw) {
			if err := json.Unmarshal(tc.InputRaw, &input); err != nil {
				input = string(tc.InputRaw)
			}
		} else if tc.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Arguments), &input); err != nil {
				input = tc.Arguments
			}
		}
		content = append(content, map[string]any{
			"type":  "tool_use",
			"id":    tc.ID,
			"name":  tc.Name,
			"input": input,
		})
	}

	if len(content) == 0 {
		content = append(content, map[string]any{"type": "text", "text": ""})
	}
	return content
}

// SerializeResponsesResponse serializes an InternalResponse into a complete
// OpenAI Responses API response body (non-stream). Used for /v1/responses
// when upstream is OpenAI Chat Completions or Anthropic Messages.
//
// The Responses API wraps message content in an `output[]` array whose
// items are typed (`message` | `function_call` | `reasoning`). Reasoning
// content is emitted as a separate `reasoning` item ahead of any text or
// tool-call items so SDK clients can render the thinking trace distinctly.
//
// ID strategy:
//   - response.id  ← ir.ID when set, else "resp_" + short hash of model+time
//   - message.id   ← "msg_" + short hash derived from respID
//   - function_call.id ← "{msgID}_fc_{index}"
//
// Phase E (2026-07-01): adds the Responses API slot to the IR response
// serializer matrix (alongside SerializeOpenAIResponse /
// SerializeAnthropicResponse), so adding the protocol stays O(N) — one
// new serializer, no handler rewrite.
func SerializeResponsesResponse(ir *InternalResponse, clientModel string) ([]byte, error) {
	if ir == nil {
		return nil, fmt.Errorf("response is nil")
	}

	model := ir.Model
	if clientModel != "" {
		model = clientModel
	}

	respID := ir.ID
	if respID == "" {
		respID = "resp_" + shortIDFromSeed(model+":"+time.Now().Format(time.RFC3339Nano))
	}
	msgID := "msg_" + shortIDFromSeed(respID)

	created := ir.Created
	if created == 0 {
		created = time.Now().Unix()
	}

	status := mapFinishReasonToResponsesStatus(ir.FinishReason)

	output := buildResponsesResponseOutput(ir, msgID, status)

	resp := map[string]any{
		"id":         respID,
		"object":     "response",
		"created_at": created,
		"model":      model,
		"status":     status,
		"output":     output,
		"usage": map[string]any{
			"input_tokens":  ir.Usage.PromptTokens,
			"output_tokens": ir.Usage.CompletionTokens,
			"total_tokens":  ir.Usage.TotalTokens,
		},
	}

	return json.Marshal(resp)
}

// buildResponsesResponseOutput assembles the Responses API `output[]` array
// from IR. Ordering mirrors what domains/streaming/responses.go previously
// hand-wrote: reasoning first (if any), then either a single message item
// or one function_call item per tool call.
func buildResponsesResponseOutput(ir *InternalResponse, msgID, status string) []map[string]any {
	output := make([]map[string]any, 0, 2)

	if ir.ReasoningContent != "" {
		output = append(output, map[string]any{
			"type": "reasoning",
			"id":   msgID + "_reasoning",
			"summary": []map[string]any{{
				"type": "summary_text",
				"text": ir.ReasoningContent,
			}},
		})
	}

	// Aggregate text content from IR.Content blocks (type=text).
	textContent := ""
	for _, c := range ir.Content {
		if c.Type == "text" {
			textContent += c.Text
		}
	}

	if len(ir.ToolCalls) > 0 {
		for index, tc := range ir.ToolCalls {
			item := map[string]any{
				"type":      "function_call",
				"id":        msgID + "_fc_" + itoa(index),
				"call_id":   tc.ID,
				"name":      tc.Name,
				"arguments": tc.Arguments,
				"status":    "completed",
			}
			output = append(output, item)
		}
		return output
	}

	output = append(output, map[string]any{
		"type":   "message",
		"id":     msgID,
		"status": status,
		"role":   "assistant",
		"content": []map[string]any{{
			"type":        "output_text",
			"text":        textContent,
			"annotations": []any{},
		}},
	})
	return output
}

// mapFinishReasonToResponsesStatus maps the unified (OpenAI-form)
// finish_reason to a Responses API status string.
func mapFinishReasonToResponsesStatus(reason string) string {
	switch reason {
	case "length", "max_tokens":
		return "incomplete"
	case "content_filter", "refusal":
		return "incomplete"
	default:
		// "stop" / "end_turn" / "tool_calls" / "tool_use" / "" → completed
		return "completed"
	}
}

// itoa is a small strconv-free integer formatter for tool call indices.
// Tool calls rarely exceed a handful per response, so a tiny allocation-free
// path keeps the hot serializer simple.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// shortIDFromSeed hashes a seed string (model name, response id, timestamp)
// into a 24-char alphanumeric suffix suitable for use in Responses API
// response.id / message.id fields. Uses FNV-1a — fast, allocation-free,
// and the IDs don't need to be cryptographically random (they are opaque
// to the client and only need to be unique within a single response).
func shortIDFromSeed(seed string) string {
	const (
		offset64 uint64 = 14695981039346656037
		prime64  uint64 = 1099511628211
		alphabet        = "0123456789abcdefghijklmnopqrstuvwxyz"
	)
	h := offset64
	for i := 0; i < len(seed); i++ {
		h ^= uint64(seed[i])
		h *= prime64
	}
	// Expand to 24 chars by chaining FNV rounds with salt mixing so
	// adjacent seeds don't share long prefixes in the output.
	out := make([]byte, 24)
	var salt uint64
	for i := 0; i < 24; i++ {
		salt = salt*31 + uint64(i+1)
		h ^= salt
		h *= prime64
		out[i] = alphabet[h%uint64(len(alphabet))]
	}
	return string(out)
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

// ─── Gemini Native Response Serializer (audit-gateway-gemini, 2026-07-13) ─────

// SerializeGeminiResponse serializes an InternalResponse into a Gemini
// generateContent response body.
//
// Gemini response shape:
//
//	{
//	  "candidates": [{
//	    "content": {"parts": [...], "role": "model"},
//	    "finishReason": "STOP",
//	    "index": 0
//	  }],
//	  "usageMetadata": {"promptTokenCount": ..., "candidatesTokenCount": ..., "totalTokenCount": ...},
//	  "modelVersion": "..."
//	}
//
// audit-gateway-gemini (2026-07-13): Completes the gateway-side of the
// Gemini native protocol. Used by handler_gemini.go when converting
// upstream OpenAI/Anthropic responses back to Gemini-native format.
func SerializeGeminiResponse(irResp *InternalResponse, clientModel string) ([]byte, error) {
	if irResp == nil {
		return nil, fmt.Errorf("response is nil")
	}

	model := irResp.Model
	if clientModel != "" {
		model = clientModel
	}

	candidate := map[string]any{
		"index": 0,
	}

	parts := make([]map[string]any, 0)
	for _, c := range irResp.Content {
		switch c.Type {
		case "text":
			if c.Text != "" {
				parts = append(parts, map[string]any{"text": c.Text})
			}
		case "tool_use":
			if c.ID != "" && c.Name != "" {
				var args any = map[string]any{}
				if c.Input != nil {
					var parsed any
					if err := json.Unmarshal(c.Input, &parsed); err == nil {
						args = parsed
					}
				}
				parts = append(parts, map[string]any{
					"functionCall": map[string]any{
						"name": c.Name,
						"args": args,
					},
				})
			}
		case "thinking":
			if c.Thinking != "" {
				parts = append(parts, map[string]any{"thought": c.Thinking})
			}
		}
	}

	emittedToolIDs := make(map[string]bool)
	for _, c := range irResp.Content {
		if c.Type == "tool_use" {
			emittedToolIDs[c.ID] = true
		}
	}
	for _, tc := range irResp.ToolCalls {
		if tc.ID == "" || tc.Name == "" {
			continue
		}
		if emittedToolIDs[tc.ID] {
			continue
		}
		var args any = map[string]any{}
		if tc.Arguments != "" {
			var parsed any
			if err := json.Unmarshal([]byte(tc.Arguments), &parsed); err == nil {
				args = parsed
			}
		}
		parts = append(parts, map[string]any{
			"functionCall": map[string]any{
				"name": tc.Name,
				"args": args,
			},
		})
	}

	if len(parts) > 0 {
		candidate["content"] = map[string]any{
			"role":  "model",
			"parts": parts,
		}
	}

	if irResp.FinishReason != "" {
		candidate["finishReason"] = mapFinishReasonToGemini(irResp.FinishReason)
	}

	out := map[string]any{
		"candidates":    []map[string]any{candidate},
		"usageMetadata": buildGeminiResponseUsageMetadata(&irResp.Usage),
	}

	if model != "" {
		out["modelVersion"] = model
	}

	return json.Marshal(out)
}

// buildGeminiResponseUsageMetadata is the non-stream counterpart of
// buildGeminiUsageMetadata in stream.go. Maps IR ResponseUsage → Gemini
// usageMetadata with modality-aware breakdowns.
func buildGeminiResponseUsageMetadata(usage *ResponseUsage) map[string]any {
	md := map[string]any{
		"promptTokenCount":     usage.PromptTokens,
		"candidatesTokenCount": usage.CompletionTokens,
		"totalTokenCount":      usage.TotalTokens,
	}

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

	if usage.CacheReadTokens != nil && *usage.CacheReadTokens > 0 {
		md["cachedContentTokenCount"] = *usage.CacheReadTokens
	}

	if usage.ReasoningTokens != nil && *usage.ReasoningTokens > 0 {
		md["thoughtsTokenCount"] = *usage.ReasoningTokens
	}

	return md
}

// mapFinishReasonToGemini converts IR/OpenAI finish reasons to Gemini form.
func mapFinishReasonToGemini(reason string) string {
	switch reason {
	case "stop":
		return "STOP"
	case "length":
		return "MAX_TOKENS"
	case "content_filter":
		return "SAFETY"
	case "tool_calls":
		return "STOP" // Gemini emits STOP with functionCall parts
	default:
		return "STOP"
	}
}
