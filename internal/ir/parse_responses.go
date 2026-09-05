package ir

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	jsonObject = "object"
	jsonArray  = "array"
	jsonString = "string"
	jsonNumber = "number"
	jsonBool   = "bool"
	jsonNull   = "null"
)

// ParseResponses parses an OpenAI Responses API request body
// (POST /v1/responses/create) into an InternalRequest.
//
// This is the spec §7.1 IR main-path extension for the Responses input
// direction. It reverses the SerializeResponsesRequest mapping:
//
//	"model"               → req.Model
//	"instructions"        → req.System (string, not a message)
//	"input"               → req.Messages
//	    "input_text" / "output_text" → ContentBlock{Type:"text"}
//	    "input_image"                → image
//	    "input_file"                 → document
//	    role "system"/"developer"    → merged into req.System
//	    role "user"/"assistant"      → Message{Role:...}
//	    string (simplified form)     → single user message
//	"max_output_tokens"   → req.MaxTokens
//	"temperature"/"top_p" → req.Temperature / req.TopP
//	"stream"              → req.Stream
//	"tools"               → req.Tools (flat Responses shape → ToolDefinition)
//	"tool_choice"         → req.ToolChoice
//	"previous_response_id" → req.PreviousResponseID
//
// SourceProtocol is set to ProtocolOpenAIResponses. Unknown top-level
// fields are preserved in req.Extensions and recorded as anomalies via
// ReportUnknownField (spec §10 Step 4.10).
func ParseResponses(body []byte) (*InternalRequest, error) {
	// nil/empty body: degrade safely into an empty IR with the protocol set.
	if len(body) == 0 {
		return &InternalRequest{SourceProtocol: ProtocolOpenAIResponses}, nil
	}

	if kind := jsonKind(body); kind != jsonObject {
		return nil, fmt.Errorf("responses body must be a JSON object, got %s", kind)
	}

	// Phase 1: decode into a raw map so unknown fields can be captured.
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(body, &rawMap); err != nil {
		return nil, fmt.Errorf("unmarshal responses body to map: %w", err)
	}

	// Phase 2: decode known fields.
	var src struct {
		Model              string          `json:"model"`
		Instructions       string          `json:"instructions"`
		Input              json.RawMessage `json:"input"`
		MaxOutputTokens    *int            `json:"max_output_tokens"`
		Temperature        *float64        `json:"temperature,omitempty"`
		TopP               *float64        `json:"top_p,omitempty"`
		Stop               json.RawMessage `json:"stop,omitempty"`
		Stream             *bool           `json:"stream,omitempty"`
		Tools              json.RawMessage `json:"tools,omitempty"`
		ToolChoice         json.RawMessage `json:"tool_choice,omitempty"`
		ParallelToolCalls  *bool           `json:"parallel_tool_calls,omitempty"`
		PreviousResponseID string          `json:"previous_response_id,omitempty"`
		User               string          `json:"user,omitempty"`
		Metadata           json.RawMessage `json:"metadata,omitempty"`
		Store              *bool           `json:"store,omitempty"`
		Truncation         string          `json:"truncation,omitempty"`
		PromptCacheKey     string          `json:"prompt_cache_key,omitempty"`
		ServiceTier        string          `json:"service_tier,omitempty"`
		SafetyIdentifier   string          `json:"safety_identifier,omitempty"`
		Reasoning          json.RawMessage `json:"reasoning,omitempty"`
		Text               json.RawMessage `json:"text,omitempty"` // {format:{type,...}}
	}
	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("unmarshal responses body: %w", err)
	}

	// Phase 3: capture unknown top-level fields into Extensions + anomaly.
	knownFields := map[string]bool{
		"model": true, "instructions": true, "input": true,
		"max_output_tokens": true, "temperature": true, "top_p": true,
		"stop": true, "stream": true, "tools": true, "tool_choice": true,
		"parallel_tool_calls": true, "previous_response_id": true,
		"user": true, "metadata": true, "store": true, "truncation": true,
		"prompt_cache_key": true, "service_tier": true, "safety_identifier": true,
		"reasoning": true, "text": true,
	}
	extensions := make(map[string]json.RawMessage)
	for key, val := range rawMap {
		if !knownFields[key] && len(val) > 0 && string(val) != "null" {
			extensions[key] = val
			ReportUnknownField("unknown", ProtocolOpenAIResponses, key, nil)
		}
	}

	req := &InternalRequest{
		Model:              src.Model,
		SourceProtocol:     ProtocolOpenAIResponses,
		ParallelToolCalls:  src.ParallelToolCalls,
		Store:              src.Store,
		Truncation:         src.Truncation,
		PromptCacheKey:     src.PromptCacheKey,
		ServiceTier:        src.ServiceTier,
		SafetyIdentifier:   src.SafetyIdentifier,
		PreviousResponseID: src.PreviousResponseID,
		User:               src.User,
		Extensions:         extensions,
	}

	// instructions → System (string).
	if strings.TrimSpace(src.Instructions) != "" {
		req.System = &SystemPrompt{Content: src.Instructions}
	}

	if src.MaxOutputTokens != nil {
		req.MaxTokens = *src.MaxOutputTokens
	}
	if src.Temperature != nil {
		req.Temperature = src.Temperature
	}
	if src.TopP != nil {
		req.TopP = src.TopP
	}
	if src.Stop != nil && string(src.Stop) != "null" {
		if err := requireJSONKind("stop", src.Stop, jsonString, jsonArray); err != nil {
			return nil, err
		}
		req.Stop = parseStringArray(src.Stop)
	}
	if src.Stream != nil {
		req.Stream = *src.Stream
	}

	// input → Messages.
	if src.Input != nil && string(src.Input) != "null" {
		if err := requireJSONKind("input", src.Input, jsonString, jsonArray); err != nil {
			return nil, err
		}
		messages, systemFromInput, err := parseResponsesInput(src.Input)
		if err != nil {
			return nil, fmt.Errorf("parse input: %w", err)
		}
		req.Messages = messages
		// system/developer roles encountered inside input[] merge into System.
		// If both instructions and an inline system message exist, append the
		// inline text (deliberately lossy for a rare combination).
		if systemFromInput != "" {
			if req.System == nil {
				req.System = &SystemPrompt{Content: systemFromInput}
			} else if req.System.Content == "" {
				req.System.Content = systemFromInput
			} else {
				req.System.Content = req.System.Content + "\n" + systemFromInput
			}
		}
	}

	// tools → req.Tools (flat Responses shape).
	if src.Tools != nil && string(src.Tools) != "null" {
		if err := requireJSONKind("tools", src.Tools, jsonArray); err != nil {
			return nil, err
		}
		tools, err := parseResponsesTools(src.Tools)
		if err != nil {
			return nil, fmt.Errorf("parse tools: %w", err)
		}
		req.Tools = tools
	}

	// tool_choice → req.ToolChoice.
	if src.ToolChoice != nil && string(src.ToolChoice) != "null" {
		if err := requireJSONKind("tool_choice", src.ToolChoice, jsonString, jsonObject); err != nil {
			return nil, err
		}
		tc, err := parseResponsesToolChoice(src.ToolChoice)
		if err != nil {
			return nil, fmt.Errorf("parse tool_choice: %w", err)
		}
		req.ToolChoice = tc
	}

	// metadata → req.Metadata (flat {key:value} map).
	if src.Metadata != nil && string(src.Metadata) != "null" {
		if err := requireJSONKind("metadata", src.Metadata, jsonObject); err != nil {
			return nil, err
		}
		if md := parseResponsesMetadata(src.Metadata); md != nil {
			req.Metadata = md
		}
	}

	// reasoning → req.Reasoning ({effort:...} or {summary:...}).
	if src.Reasoning != nil && string(src.Reasoning) != "null" {
		if err := requireJSONKind("reasoning", src.Reasoning, jsonObject); err != nil {
			return nil, err
		}
		req.Reasoning = parseResponsesReasoning(src.Reasoning)
	}

	// text.format → req.ResponseFormat (Responses nests format under "text").
	if src.Text != nil && string(src.Text) != "null" {
		if err := requireJSONKind("text", src.Text, jsonObject); err != nil {
			return nil, err
		}
		req.ResponseFormat = parseResponsesTextFormat(src.Text)
	}

	return req, nil
}

func jsonKind(raw []byte) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "empty"
	}
	switch trimmed[0] {
	case '{':
		return jsonObject
	case '[':
		return jsonArray
	case '"':
		return jsonString
	case 't', 'f':
		return jsonBool
	case 'n':
		return jsonNull
	default:
		return jsonNumber
	}
}

func requireJSONKind(field string, raw json.RawMessage, allowed ...string) error {
	kind := jsonKind(raw)
	for _, candidate := range allowed {
		if kind == candidate {
			return nil
		}
	}
	return fmt.Errorf("%s must be %s, got %s", field, strings.Join(allowed, " or "), kind)
}

// parseResponsesInput converts the Responses "input" field into IR Messages.
// "input" may be a string (simplified form → one user message) or an array of
// items. Returns the collected messages plus any concatenated system/developer
// text (which the Responses API represents as instructions, not messages).
func parseResponsesInput(raw json.RawMessage) (messages []Message, systemText string, err error) {
	// Simplified form: a bare string.
	var asString string
	if jerr := json.Unmarshal(raw, &asString); jerr == nil {
		return []Message{{
			Role:    "user",
			Content: []ContentBlock{{Type: "text", Text: asString}},
		}}, "", nil
	}

	var items []json.RawMessage
	if uerr := json.Unmarshal(raw, &items); uerr != nil {
		return nil, "", fmt.Errorf("unmarshal input array: %w", uerr)
	}

	messages = make([]Message, 0, len(items))
	for i, itemRaw := range items {
		var item map[string]any
		if uerr := json.Unmarshal(itemRaw, &item); uerr != nil {
			return nil, "", fmt.Errorf("unmarshal input[%d]: %w", i, uerr)
		}
		msg, inlineSystem, perr := parseResponsesInputItem(item)
		if perr != nil {
			return nil, "", fmt.Errorf("parse input[%d]: %w", i, perr)
		}
		if inlineSystem != "" {
			if systemText != "" {
				systemText += "\n"
			}
			systemText += inlineSystem
			continue
		}
		if msg != nil {
			messages = append(messages, *msg)
		}
	}
	return messages, systemText, nil
}

// parseResponsesInputItem converts a single input[] item into an IR Message.
// Returns (msg, inlineSystem, err): inlineSystem is set for system/developer
// roles so the caller can hoist them into req.System; msg is nil for those.
func parseResponsesInputItem(item map[string]any) (*Message, string, error) {
	role, _ := item["role"].(string)
	itemType, _ := item["type"].(string)

	// Responses tool-call history items do not carry a message role. Handle
	// them before the generic type-item fallback so tool semantics survive the
	// conversion into the shared IR.
	if itemType == "function_call" {
		name, _ := item["name"].(string)
		args, _ := item["arguments"].(string)
		if args == "" {
			args = "{}"
		}
		callID, _ := item["call_id"].(string)
		if callID == "" {
			callID, _ = item["id"].(string)
		}
		call := ToolCall{ID: callID, Type: "function"}
		call.Function.Name = name
		call.Function.Arguments = args
		return &Message{
			Role:      "assistant",
			ToolCalls: []ToolCall{call},
		}, "", nil
	}
	if itemType == "function_call_output" {
		callID, _ := item["call_id"].(string)
		output := extractItemText(item)
		return &Message{
			Role:       "tool",
			ToolCallID: callID,
			Content: []ContentBlock{{
				Type: "tool_result",
				ToolResult: &ToolResult{
					ToolUseID: callID,
					Content:   []ContentBlock{{Type: "text", Text: output}},
				},
			}},
		}, "", nil
	}

	// Roleless Responses input items are client input, not assistant output.
	// Normalize the documented shorthand forms before the unknown-item fallback
	// so their text survives protocol conversion and multi-turn compression.
	if role == "" {
		switch itemType {
		case "input_text", "text":
			text, _ := item["text"].(string)
			return &Message{Role: "user", Content: []ContentBlock{{Type: "text", Text: text}}}, "", nil
		case "message":
			role = "user"
		}
	}

	// "message"-typed items carry a normal {role, content}. Unknown typed
	// items are preserved as raw assistant content rather than dropped.
	if itemType != "" && itemType != "message" && role == "" {
		raw, _ := json.Marshal(item)
		return &Message{
			Role:    "assistant",
			Content: []ContentBlock{{Type: "raw", RawContent: string(raw)}},
		}, "", nil
	}

	switch role {
	case "system", "developer":
		// Hoist into System rather than emitting a message.
		return nil, extractItemText(item), nil
	}

	msg := &Message{Role: role}

	// content may be a string or an array of typed blocks.
	switch content := item["content"].(type) {
	case string:
		msg.Content = []ContentBlock{{Type: "text", Text: content}}
	case []any:
		blocks, err := parseResponsesContentBlocks(content, role)
		if err != nil {
			return nil, "", err
		}
		msg.Content = blocks
	case nil:
		msg.Content = []ContentBlock{}
	default:
		// Unknown content shape: preserve raw.
		raw, _ := json.Marshal(content)
		msg.Content = []ContentBlock{{Type: "raw", RawContent: string(raw)}}
	}

	// function_call items (assistant tool calls) carry name/arguments directly.
	if name, _ := item["name"].(string); name != "" {
		args, _ := item["arguments"].(string)
		if args == "" {
			args = "{}"
		}
		callID, _ := item["call_id"].(string)
		if callID == "" {
			callID, _ = item["id"].(string)
		}
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{
			ID:   callID,
			Type: "function",
		})
		msg.ToolCalls[len(msg.ToolCalls)-1].Function.Name = name
		msg.ToolCalls[len(msg.ToolCalls)-1].Function.Arguments = args
		if msg.Role == "" {
			msg.Role = "assistant"
		}
	}

	// function_call_output items map to a tool-role message.
	// (Handled by the early `itemType == "function_call_output"` branch above;
	// a second copy here was unreachable and removed.)

	return msg, "", nil
}

// parseResponsesContentBlocks converts a Responses content array into IR
// ContentBlocks. textType picks the normalization target based on role.
func parseResponsesContentBlocks(blocks []any, role string) ([]ContentBlock, error) {
	result := make([]ContentBlock, 0, len(blocks))
	for _, block := range blocks {
		blockMap, ok := block.(map[string]any)
		if !ok {
			continue
		}
		blockType, _ := blockMap["type"].(string)
		irBlock := ContentBlock{}

		switch blockType {
		case "input_text", "output_text":
			// Both normalize to the IR "text" type.
			irBlock.Type = "text"
			if text, ok := blockMap["text"].(string); ok {
				irBlock.Text = text
			}
		case "input_image":
			irBlock.Type = "image"
			irBlock.Image = parseResponsesInputImage(blockMap)
		case "input_file":
			irBlock.Type = "document"
			irBlock.Document = parseOpenAIFileBlock(blockMap)
		case "input_audio":
			irBlock.Type = "input_audio"
			irBlock.InputAudio = parseOpenAIInputAudioBlock(blockMap)
		case "text":
			irBlock.Type = "text"
			if text, ok := blockMap["text"].(string); ok {
				irBlock.Text = text
			}
		default:
			raw, _ := json.Marshal(blockMap)
			irBlock.Type = blockType
			irBlock.RawContent = string(raw)
		}
		if irBlock.Type == "" {
			irBlock.Type = "text"
		}
		result = append(result, irBlock)
	}
	return result, nil
}

// parseResponsesInputImage maps a Responses input_image block to an IR
// ImageSource. Responses accepts image_url (string or object) or file_id.
func parseResponsesInputImage(block map[string]any) *ImageSource {
	img := &ImageSource{Type: "url"}

	// image_url may be a string or an object {url, detail}.
	switch iu := block["image_url"].(type) {
	case string:
		img.URL = iu
		if mt, data, isBase64 := parseOpenAIDataURI(iu); isBase64 {
			img.Type = "base64"
			img.MediaType = mt
			img.Data = data
		}
	case map[string]any:
		if u, ok := iu["url"].(string); ok {
			img.URL = u
			if mt, data, isBase64 := parseOpenAIDataURI(u); isBase64 {
				img.Type = "base64"
				img.MediaType = mt
				img.Data = data
			}
		}
		if detail, ok := iu["detail"].(string); ok {
			img.Detail = detail
		}
	}

	if fid, ok := block["file_id"].(string); ok && fid != "" {
		img.FileID = fid
		img.Type = "file_id"
	}
	if detail, ok := block["detail"].(string); ok && detail != "" {
		img.Detail = detail
	}
	return img
}

// extractItemText pulls text content out of a content array or a top-level
// "output"/"text" field of a Responses input item.
func extractItemText(item map[string]any) string {
	if out, ok := item["output"].(string); ok && out != "" {
		return out
	}
	if content, ok := item["content"].([]any); ok {
		var b strings.Builder
		for _, c := range content {
			if cm, ok := c.(map[string]any); ok {
				if t, _ := cm["text"].(string); t != "" {
					if b.Len() > 0 {
						b.WriteString("\n")
					}
					b.WriteString(t)
				}
			}
		}
		return b.String()
	}
	if content, ok := item["content"].(string); ok {
		return content
	}
	return ""
}

// parseResponsesTools converts the flat Responses tools[] shape into IR
// ToolDefinitions: {type:"function", name, description, parameters}.
// Provider-specific tool types (web_search, file_search, ...) are captured
// verbatim into Raw.
func parseResponsesTools(raw json.RawMessage) ([]ToolDefinition, error) {
	// Decode each tool element as a raw message first so we can give a
	// precise error for non-object elements (the previous implementation
	// silently skipped strings/numbers — which masked malformed clients).
	var rawTools []json.RawMessage
	if err := json.Unmarshal(raw, &rawTools); err != nil {
		return nil, fmt.Errorf("unmarshal tools: %w", err)
	}

	result := make([]ToolDefinition, 0, len(rawTools))
	for i, item := range rawTools {
		if jsonKind(item) != jsonObject {
			return nil, fmt.Errorf("tools[%d] must be object, got %s", i, jsonKind(item))
		}
		var tool map[string]any
		if err := json.Unmarshal(item, &tool); err != nil {
			return nil, fmt.Errorf("unmarshal tools[%d]: %w", i, err)
		}
		toolType, _ := tool["type"].(string)

		// Provider-specific tool types (web_search, file_search, ...) have no
		// function name; capture verbatim for same-protocol passthrough.
		if toolType != "" && toolType != "function" {
			if _, hasName := tool["name"]; !hasName {
				rawBytes, _ := json.Marshal(tool)
				result = append(result, ToolDefinition{
					Type: toolType,
					Raw:  rawBytes,
				})
				continue
			}
		}

		td := ToolDefinition{}
		if name, ok := tool["name"].(string); ok {
			td.Name = name
		}
		if desc, ok := tool["description"].(string); ok {
			td.Description = desc
		}
		td.Parameters = marshalAnyToRaw(sanitizeInputSchema(tool["parameters"]))
		result = append(result, td)
	}
	return result, nil
}

// parseResponsesToolChoice converts the Responses tool_choice into IR.
// Accepts "auto"/"none"/"required" or {type:"function", name:...}.
func parseResponsesToolChoice(raw json.RawMessage) (*ToolChoice, error) {
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

// parseResponsesMetadata converts the Responses metadata object into IR.
// user_id → Metadata.UserID; everything else → Other.
func parseResponsesMetadata(raw json.RawMessage) *Metadata {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	if len(m) == 0 {
		return nil
	}
	md := &Metadata{Other: map[string]string{}}
	for k, v := range m {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if k == "user_id" {
			md.UserID = s
			continue
		}
		md.Other[k] = s
	}
	return md
}

// parseResponsesReasoning converts {effort:...} / {summary:...} into IR.
func parseResponsesReasoning(raw json.RawMessage) *ReasoningConfig {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	if len(m) == 0 {
		return nil
	}
	r := &ReasoningConfig{}
	if e, ok := m["effort"].(string); ok {
		r.Effort = e
	}
	if s, ok := m["summary"].(string); ok {
		// Responses "summary" toggle corresponds to the IR Reasoning.Type.
		r.Type = s
	}
	return r
}

// parseResponsesTextFormat converts the Responses "text":{"format":{...}}
// wrapper into IR ResponseFormat.
func parseResponsesTextFormat(raw json.RawMessage) *ResponseFormat {
	var wrapper struct {
		Format map[string]any `json:"format"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil
	}
	if len(wrapper.Format) == 0 {
		return nil
	}
	rf := &ResponseFormat{}
	if t, ok := wrapper.Format["type"].(string); ok {
		rf.Type = t
	}
	rf.Schema = marshalAnyToRaw(wrapper.Format["schema"])
	if rf.Schema == nil {
		rf.Schema = marshalAnyToRaw(wrapper.Format["json_schema"])
	}
	return rf
}
