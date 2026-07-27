package ir

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ParseOpenAI parses an OpenAI Chat Completions request body into InternalRequest.
//
// Extensions support (P0 fix, 2026-07-13):
// Unknown fields (vendor-specific params like reasoning_effort, web_search,
// bot_setting, etc.) are extracted into IR.Extensions for lossless passthrough.
func ParseOpenAI(body []byte) (*InternalRequest, error) {
	// Phase 1: Parse to map to capture ALL fields (including unknown ones)
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(body, &rawMap); err != nil {
		return nil, fmt.Errorf("unmarshal openai body to map: %w", err)
	}

	// Phase 2: Parse known fields to struct
	var src struct {
		Model               string          `json:"model"`
		Messages            json.RawMessage `json:"messages"`
		MaxTokens           *int            `json:"max_tokens"`
		MaxCompletionTokens *int            `json:"max_completion_tokens,omitempty"`
		Temperature         *float64        `json:"temperature,omitempty"`
		TopP                *float64        `json:"top_p,omitempty"`
		Stop                json.RawMessage `json:"stop,omitempty"`
		Stream              *bool           `json:"stream,omitempty"`
		Tools               json.RawMessage `json:"tools,omitempty"`
		ToolChoice          json.RawMessage `json:"tool_choice,omitempty"`
		FrequencyPenalty    *float64        `json:"frequency_penalty,omitempty"`
		PresencePenalty     *float64        `json:"presence_penalty,omitempty"`
		LogProbs            *bool           `json:"logprobs,omitempty"`
		TopLogProbs         *int            `json:"top_logprobs,omitempty"`
		Seed                *int64          `json:"seed,omitempty"`
		ResponseFormat      json.RawMessage `json:"response_format,omitempty"`
		N                   *int            `json:"n,omitempty"`
		User                string          `json:"user,omitempty"`
		ParallelToolCalls   *bool           `json:"parallel_tool_calls,omitempty"`

		// audit-provider-multimodal (2026-07-13): personalized fields
		ReasoningEffort    string          `json:"reasoning_effort,omitempty"`
		Modalities         json.RawMessage `json:"modalities,omitempty"`
		Audio              json.RawMessage `json:"audio,omitempty"`
		LogitBias          json.RawMessage `json:"logit_bias,omitempty"`
		Store              *bool           `json:"store,omitempty"`
		ServiceTier        string          `json:"service_tier,omitempty"`
		Prediction         json.RawMessage `json:"prediction,omitempty"`
		Verbosity          string          `json:"verbosity,omitempty"`
		WebSearchOptions   json.RawMessage `json:"web_search_options,omitempty"`
		PromptCacheKey     string          `json:"prompt_cache_key,omitempty"`
		SafetyIdentifier   string          `json:"safety_identifier,omitempty"`
		PreviousResponseID string          `json:"previous_response_id,omitempty"`
		Truncation         string          `json:"truncation,omitempty"`
	}

	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("unmarshal openai body: %w", err)
	}

	// Phase 3: Extract unknown fields to Extensions
	knownFields := map[string]bool{
		"model": true, "messages": true, "max_tokens": true, "max_completion_tokens": true,
		"temperature": true, "top_p": true, "stop": true, "stream": true,
		"tools": true, "tool_choice": true, "frequency_penalty": true, "presence_penalty": true,
		"logprobs": true, "top_logprobs": true, "seed": true, "response_format": true,
		"n": true, "user": true, "parallel_tool_calls": true,
		// audit-provider-multimodal (2026-07-13): recognized structured fields
		"reasoning_effort": true, "modalities": true, "audio": true, "logit_bias": true,
		"store": true, "service_tier": true, "prediction": true, "verbosity": true,
		"web_search_options": true, "prompt_cache_key": true, "safety_identifier": true,
		"previous_response_id": true, "truncation": true,
	}

	extensions := make(map[string]json.RawMessage)
	for key, val := range rawMap {
		if !knownFields[key] && len(val) > 0 && string(val) != "null" {
			extensions[key] = val
		}
	}

	ir := &InternalRequest{
		Model:              src.Model,
		SourceProtocol:     ProtocolOpenAIChat,
		FrequencyPenalty:   src.FrequencyPenalty,
		PresencePenalty:    src.PresencePenalty,
		Logprobs:           src.LogProbs,
		TopLogprobs:        src.TopLogProbs,
		Seed:               src.Seed,
		N:                  derefInt(src.N),
		User:               src.User,
		ParallelToolCalls:  src.ParallelToolCalls,
		Store:              src.Store,
		ServiceTier:        src.ServiceTier,
		Verbosity:          src.Verbosity,
		PromptCacheKey:     src.PromptCacheKey,
		SafetyIdentifier:   src.SafetyIdentifier,
		PreviousResponseID: src.PreviousResponseID,
		Truncation:         src.Truncation,
		Extensions:         extensions, // P0 fix: preserve unknown fields
	}

	// audit-provider-multimodal (2026-07-13): Reasoning effort (OpenAI o1/o3)
	if src.ReasoningEffort != "" {
		ir.Reasoning = &ReasoningConfig{Effort: src.ReasoningEffort}
	}

	// Modalities (OpenAI TTS multimodal output config)
	if src.Modalities != nil && string(src.Modalities) != "null" {
		var mods []string
		if err := json.Unmarshal(src.Modalities, &mods); err == nil {
			ir.Modalities = mods
		}
	}

	// Audio output config (OpenAI TTS)
	if src.Audio != nil && string(src.Audio) != "null" {
		var ac AudioConfig
		if err := json.Unmarshal(src.Audio, &ac); err == nil {
			ir.AudioConfig = &ac
		}
	}

	// Logit bias (OpenAI)
	if src.LogitBias != nil && string(src.LogitBias) != "null" {
		var lb map[string]float64
		if err := json.Unmarshal(src.LogitBias, &lb); err == nil {
			ir.LogitBias = lb
		}
	}

	// Prediction (OpenAI)
	if src.Prediction != nil && string(src.Prediction) != "null" {
		var p Prediction
		if err := json.Unmarshal(src.Prediction, &p); err == nil {
			ir.Prediction = &p
		}
	}

	// Web search options (OpenAI)
	if src.WebSearchOptions != nil && string(src.WebSearchOptions) != "null" {
		var wso WebSearchOptions
		if err := json.Unmarshal(src.WebSearchOptions, &wso); err == nil {
			ir.WebSearchOptions = &wso
		}
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
		case "input_audio":
			// audit-provider-multimodal (2026-07-13): OpenAI chat audio input
			// {type:"input_audio", input_audio:{data, format}}
			ia := parseOpenAIInputAudioBlock(blockMap)
			irBlock.InputAudio = ia
		case "image", "audio", "video", "document":
			// audit-provider-multimodal (2026-07-13): pass-through multimodal blocks
			if img := parseOpenAIImageBlock(blockMap); img != nil {
				irBlock.Image = img
			}
		case "file":
			// OpenAI file input via Responses API
			irBlock.Document = parseOpenAIFileBlock(blockMap)
			irBlock.Type = "document" // Normalize to our internal type
		case "input_file":
			// OpenAI Responses API file input variant
			irBlock.Document = parseOpenAIFileBlock(blockMap)
			irBlock.Type = "document"
		default:
			raw, _ := json.Marshal(blockMap)
			irBlock.RawContent = string(raw)
		}
		result = append(result, irBlock)
	}
	return result, nil
}

// parseOpenAIImageBlock parses an OpenAI image_url content block.
//
// 增强逻辑（2026-07-01）：区分 HTTP(S) URL 和 data URI (base64)。
// 原实现将所有 url 统一存入 ImageSource.URL，丢失了 base64 的 media type 和
// data 信息，导致协议转换（OpenAI→Anthropic）时 base64 图片无法正确还原为
// Anthropic 的 source={type:base64, media_type, data} 结构，上游 LLM 收到的
// 图片不可用（这是"图片经网关后丢失"bug 的根因）。
//
// 现在：
//   - data:image/png;base64,...  → Type="base64", MediaType+Data 被解析填充
//   - https://example.com/x.png  → Type="url"
//
// URL 字段始终保留原始值（兼容旧序列化路径）。
func parseOpenAIImageBlock(block map[string]any) *ImageSource {
	img := &ImageSource{Type: "url"}

	urlObj, ok := block["image_url"].(map[string]any)
	if !ok {
		return img
	}

	url, _ := urlObj["url"].(string)
	img.URL = url

	// P1-1 fix (2026-07-13): Preserve detail parameter
	// detail can be "low", "high", "auto" — controls image resolution/token usage
	if detail, ok := urlObj["detail"].(string); ok {
		img.Detail = detail
	}

	// 解析 data URI，填充 base64 专用字段
	if mediaType, data, isBase64 := parseOpenAIDataURI(url); isBase64 {
		img.Type = "base64"
		img.MediaType = mediaType
		img.Data = data
	}

	return img
}

// parseOpenAIDataURI 尽力解析 data URI，返回 (mediaType, base64Data, ok)。
// 非严格校验：即使格式略有偏差也尽量提取 media type 和 data。
// 非 data URI 直接返回 ok=false。
//
// 例如 "data:image/png;base64,iVBOR..." → ("image/png", "iVBOR...", true)
func parseOpenAIDataURI(uri string) (mediaType, data string, ok bool) {
	const prefix = "data:"
	if !strings.HasPrefix(uri, prefix) {
		return "", "", false
	}
	body := uri[len(prefix):]
	comma := strings.IndexByte(body, ',')
	if comma < 0 {
		return "", "", false
	}
	header := body[:comma]
	payload := body[comma+1:]

	mediaType = "application/octet-stream"
	isBase64 := false
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		switch {
		case part == "base64":
			isBase64 = true
		case part != "" && !strings.Contains(part, "=") && strings.Contains(part, "/"):
			mediaType = part
		}
	}
	if !isBase64 || payload == "" {
		return "", "", false
	}
	return mediaType, payload, true
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

		// 2026-07-27 (F-1): provider-specific tool types (OpenAI web_search,
		// code_interpreter, file_search; anything with type != "function"
		// and no function sub-object) have no name/parameters and were
		// previously dropped. Capture their type + verbatim bytes so a
		// same-protocol serialize can pass them through unchanged.
		toolType, _ := tool["type"].(string)
		_, hasFunction := tool["function"]
		if toolType != "" && toolType != "function" && !hasFunction {
			rawBytes, _ := json.Marshal(tool)
			result = append(result, ToolDefinition{
				Type: toolType,
				Raw:  rawBytes,
			})
			continue
		}

		td := ToolDefinition{}

		// Handle nested function object (OpenAI standard format).
		// P0 fix (2026-06-24): marshalAnyToRaw is required here, not a
		// `.(json.RawMessage)` type assertion — see helper comment.
		if fn, ok := tool["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok {
				td.Name = name
			}
			if desc, ok := fn["description"].(string); ok {
				td.Description = desc
			}
			td.Parameters = marshalAnyToRaw(sanitizeInputSchema(fn["parameters"]))
		} else {
			// Flat format or Anthropic-style tool def.
			if name, ok := tool["name"].(string); ok {
				td.Name = name
			}
			if desc, ok := tool["description"].(string); ok {
				td.Description = desc
			}
			// Try input_schema (Anthropic style) or parameters.
			if p := marshalAnyToRaw(sanitizeInputSchema(tool["input_schema"])); p != nil {
				td.Parameters = p
			} else {
				td.Parameters = marshalAnyToRaw(sanitizeInputSchema(tool["parameters"]))
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
	// P0 fix (2026-06-24): use marshalAnyToRaw, not `.(json.RawMessage)`
	// (see helper comment).
	result.Schema = marshalAnyToRaw(rf["json_schema"])

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

// marshalAnyToRaw converts a value extracted from a generic-decoded JSON
// object (map[string]any / []any) back into json.RawMessage. Returns nil
// when v is absent or un-marshalable.
//
// Why this helper exists: when json.Unmarshal decodes into a `map[string]any`
// (or `any`), nested objects/arrays become nested `map[string]any` / `[]any`
// — NOT json.RawMessage. A naked `.(json.RawMessage)` assertion on such a
// value silently fails and the field is dropped (see 2026-06-24 P0 fix:
// OpenAI tool parameters / Anthropic input_schema were lost end-to-end,
// causing `tools[]` to be sent to upstream providers without `input_schema`).
//
// Used by parse_openai.go (tools.parameters, response_format.json_schema)
// and parse_anthropic.go (tools.input_schema).
func marshalAnyToRaw(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	if raw, ok := v.(json.RawMessage); ok && len(raw) > 0 && string(raw) != "null" {
		return raw
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// derefInt safely dereferences an int pointer.
func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// parseOpenAIInputAudioBlock parses an OpenAI input_audio content block.
// audit-provider-multimodal (2026-07-13): OpenAI chat audio input support.
// Shape: { type:"input_audio", input_audio:{ data:"<base64>", format:"wav"|"mp3" } }
func parseOpenAIInputAudioBlock(block map[string]any) *InputAudioBlock {
	ia := &InputAudioBlock{}

	if inner, ok := block["input_audio"].(map[string]any); ok {
		if d, ok := inner["data"].(string); ok {
			ia.Data = d
		}
		if f, ok := inner["format"].(string); ok {
			ia.Format = f
		}
	}

	// Flat shape fallback
	if ia.Data == "" {
		if d, ok := block["data"].(string); ok {
			ia.Data = d
		}
	}
	if ia.Format == "" {
		if f, ok := block["format"].(string); ok {
			ia.Format = f
		}
	}

	return ia
}

// parseOpenAIFileBlock parses an OpenAI file input block (Responses API).
// audit-provider-multimodal (2026-07-13): PDF/text file input via Responses API.
// Shape: { type:"file", file:{ filename, file_data } }
//
//	{ type:"input_file", input_file:{...} }
func parseOpenAIFileBlock(block map[string]any) *DocumentBlock {
	db := &DocumentBlock{Kind: "file"}

	var inner map[string]any
	if v, ok := block["file"].(map[string]any); ok {
		inner = v
	} else if v, ok := block["input_file"].(map[string]any); ok {
		inner = v
	} else {
		inner = block
	}

	src := &DocumentSource{Type: "file"}
	if fn, ok := inner["filename"].(string); ok {
		db.Title = fn
	}
	if fd, ok := inner["file_data"].(string); ok {
		if strings.HasPrefix(fd, "data:") {
			idx := strings.Index(fd, "base64,")
			if idx >= 0 {
				src.MediaType = strings.TrimSuffix(strings.TrimPrefix(fd[:idx], "data:"), ";")
				src.Data = fd[idx+len("base64,"):]
				src.Type = "base64"
			}
		} else if strings.HasPrefix(fd, "http") {
			src.Type = "url"
			src.Data = fd
		} else {
			src.Type = "text"
			src.Data = fd
		}
	}
	if mt, ok := inner["mime_type"].(string); ok && src.MediaType == "" {
		src.MediaType = mt
	}
	db.Source = src
	return db
}
