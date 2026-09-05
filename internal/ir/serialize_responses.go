package ir

import (
	"encoding/json"
	"fmt"
)

// SerializeResponsesRequest serializes an InternalRequest into an OpenAI
// Responses API request body (POST /v1/responses/create).
//
// This is the spec §7.1 IR main-path extension for the Responses *request*
// direction. It complements SerializeOpenAI/SerializeAnthropic/SerializeGemini
// (which all target the request direction) and SerializeResponsesResponse /
// StreamChunk.SerializeResponses (which target the response/stream directions).
//
// Wire shape (platform.openai.com/docs/api-reference/responses/create):
//
//	{
//	  "model": "gpt-4o",
//	  "instructions": "...",                 // ← req.System (string), not a message
//	  "input": [                             // ← req.Messages (replaces messages[])
//	    {"role":"user","content":[
//	      {"type":"input_text","text":"..."}
//	    ]},
//	    {"role":"assistant","content":[
//	      {"type":"output_text","text":"..."}
//	    ]}
//	  ],
//	  "max_output_tokens": 1024,             // ← req.MaxTokens (NOT max_tokens)
//	  "temperature": 0.7,
//	  "top_p": 0.9,
//	  "stream": true,
//	  "stop": ["END"],
//	  "tools": [                             // flat shape, no "function" wrapper
//	    {"type":"function","name":"...","description":"...","parameters":{...}}
//	  ],
//	  "tool_choice": "auto",
//	  "parallel_tool_calls": true,
//	  "previous_response_id": "resp_abc",
//	  "prompt_cache_key": "...",
//	  "metadata": {...},
//	  "user": "...",
//	  "store": true,
//	  "truncation": "auto",
//	  "reasoning": {"effort":"high"},
//	  "text": {"format":{"type":"json_object"}}
//	}
//
// Key divergences from Chat Completions (SerializeOpenAI):
//   - input[] replaces messages[]
//   - each message's content is a typed-block array; text blocks use
//     input_text (user/system/developer) or output_text (assistant)
//   - the system prompt is hoisted to a top-level "instructions" string,
//     NOT emitted as an input[] message
//   - max_output_tokens replaces max_tokens
//   - tools entries are flat ({type,name,description,parameters}) — no nested
//     {function:{...}} wrapper
//   - previous_response_id is native (no anomaly on Responses source)
func SerializeResponsesRequest(req *InternalRequest) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("request is nil")
	}

	out := map[string]any{
		"model": req.Model,
	}

	// System → top-level "instructions" (string). The Responses API does NOT
	// accept a role=system input message; the prompt goes here. Parts-only
	// systems (Anthropic array / Gemini systemInstruction parse output) are
	// flattened rather than silently dropped.
	if text := systemPlainText(req.System); text != "" {
		out["instructions"] = text
	}

	// Messages → input[] (array of {role, content:[typed blocks]}).
	if input := buildResponsesInput(req.Messages); len(input) > 0 {
		out["input"] = input
	}

	// max_output_tokens (NOT max_tokens).
	if req.MaxTokens > 0 {
		out["max_output_tokens"] = req.MaxTokens
	}

	// Sampling params (shared with Chat Completions).
	if req.Temperature != nil {
		out["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		out["top_p"] = *req.TopP
	}
	if req.Stream {
		out["stream"] = true
	}
	if len(req.Stop) > 0 {
		out["stop"] = req.Stop
	}

	// Tools (flat Responses shape).
	if len(req.Tools) > 0 {
		out["tools"] = buildResponsesTools(req.Tools)
	}

	// Tool choice.
	if req.ToolChoice != nil {
		out["tool_choice"] = buildResponsesToolChoice(req.ToolChoice)
	}
	if req.ParallelToolCalls != nil {
		out["parallel_tool_calls"] = *req.ParallelToolCalls
	}

	// Chained response id (native to Responses — no loss when source is Responses).
	if req.PreviousResponseID != "" {
		out["previous_response_id"] = req.PreviousResponseID
	}

	// OpenAI-compatible fields that the Responses API also accepts.
	if req.User != "" {
		out["user"] = req.User
	}
	if req.Metadata != nil {
		out["metadata"] = buildResponsesMetadata(req.Metadata)
	}
	if req.Store != nil {
		out["store"] = *req.Store
	}
	if req.Truncation != "" {
		out["truncation"] = req.Truncation
	}
	if req.PromptCacheKey != "" {
		out["prompt_cache_key"] = req.PromptCacheKey
	}
	if req.ServiceTier != "" {
		out["service_tier"] = req.ServiceTier
	}
	if req.SafetyIdentifier != "" {
		out["safety_identifier"] = req.SafetyIdentifier
	}

	// Reasoning (o-series / GPT-5). Responses API uses the structured
	// {"reasoning":{"effort":...}} form rather than the flat
	// reasoning_effort field of Chat Completions.
	if req.Reasoning != nil {
		if r := buildResponsesReasoning(req.Reasoning); r != nil {
			out["reasoning"] = r
		}
	}

	// Response format → text.format (Responses nests format under "text").
	if req.ResponseFormat != nil {
		fmtObj := map[string]any{
			"type": req.ResponseFormat.Type,
		}
		if req.ResponseFormat.Schema != nil {
			fmtObj["schema"] = req.ResponseFormat.Schema
		}
		out["text"] = map[string]any{"format": fmtObj}
	}

	// Step 4.10 (2026-07-28): emit explicit anomaly for cross-protocol losses
	// before serializing Extensions (Extensions restore is same-protocol only).
	reportSerializeResponsesLosses(req)

	// Extensions 还原：把 transport extractor 保留的非标顶层字段写回。
	//
	// 2026-08-11: 原实现只在 SourceProtocol == Responses 时还原，导致
	// Codex CLI 之外的客户端（如 Claude Code）路由到 Responses 上游时丢字段。
	// 改为注册表驱动，见 restoreExtensions。
	restoreExtensions(out, req, ProtocolOpenAIResponses)

	return json.Marshal(out)
}

// buildResponsesInput converts IR Messages to the Responses API input[] array.
//
// Role mapping:
//   - "system" messages are dropped here (the system prompt is hoisted to
//     top-level "instructions" by SerializeResponsesRequest); keeping a
//     system role in input[] would be rejected by the API.
//   - "tool" role messages (OpenAI Chat tool results) are mapped to a
//     function_call_output input item so the conversation round-trips.
//   - all other roles pass through unchanged.
//
// Content block type mapping (text divergence from Chat Completions):
//   - user/developer/system text  → {"type":"input_text","text":...}
//   - assistant text              → {"type":"output_text","text":...}
//   - image                       → {"type":"input_image","image_url":{...}}
//   - file/document               → {"type":"input_file","file_data":...}
func buildResponsesInput(messages []Message) []map[string]any {
	input := make([]map[string]any, 0, len(messages))

	for _, msg := range messages {
		// System role is conveyed via "instructions", never as an input item.
		if msg.Role == "system" {
			continue
		}

		// Chat Completions "tool" role → Responses function_call_output item.
		if msg.Role == "tool" {
			item := buildResponsesFunctionCallOutput(msg)
			if item != nil {
				input = append(input, item)
			}
			continue
		}
		// Anthropic tool_result carried inside a user message → same mapping.
		if msg.Role == "user" && hasToolResultBlock(msg.Content) {
			item := buildResponsesFunctionCallOutput(msg)
			if item != nil {
				input = append(input, item)
			}
			continue
		}

		item := map[string]any{
			"role": msg.Role,
		}

		// Assistant tool calls become a function_call input item alongside any
		// output_text blocks.
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			content := buildResponsesContent(msg)
			// Emit one function_call item per tool call (Responses expects each
			// call as its own input item), then any text as a separate message.
			for _, call := range msg.ToolCalls {
				args := call.Function.Arguments
				if args == "" {
					args = "{}"
				}
				// E5: sanitize name + ensure item-id prefix for Responses wire shape.
				sanitizedName := SanitizeResponsesFunctionName(call.Function.Name)
				prefixedID := EnsureResponsesItemIDPrefix(call.ID, "function_call")
				input = append(input, map[string]any{
					"type":      "function_call",
					"id":        prefixedID,
					"call_id":   call.ID,
					"name":      sanitizedName,
					"arguments": args,
				})
			}
			if len(content) > 0 {
				item["content"] = content
				input = append(input, item)
			}
			continue
		}

		content := buildResponsesContent(msg)
		if len(content) > 0 {
			item["content"] = content
		} else if len(msg.Content) == 0 {
			// Preserve empty-content messages (some clients send a placeholder
			// assistant turn) as an empty content array rather than dropping.
			item["content"] = []map[string]any{}
		}
		input = append(input, item)
	}

	return input
}

// buildResponsesContent converts a single Message's content blocks to the
// Responses typed-block array. Text blocks pick their type from the message
// role: assistant→output_text, everything else→input_text.
func buildResponsesContent(msg Message) []map[string]any {
	textType := "input_text"
	if msg.Role == "assistant" {
		textType = "output_text"
	}

	result := make([]map[string]any, 0, len(msg.Content))
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				result = append(result, map[string]any{
					"type": textType,
					"text": block.Text,
				})
			}
		case "image":
			if block.Image != nil {
				result = append(result, buildResponsesInputImage(block.Image))
			}
		case "input_audio":
			if block.InputAudio != nil {
				// Responses audio input uses input_audio with a data+format body.
				result = append(result, map[string]any{
					"type": "input_audio",
					"input_audio": map[string]any{
						"data":   block.InputAudio.Data,
						"format": block.InputAudio.Format,
					},
				})
			}
		case "audio":
			if block.Audio != nil {
				result = append(result, buildResponsesInputAudio(block.Audio))
			}
		case "document":
			if block.Document != nil {
				result = append(result, buildResponsesInputFile(block.Document))
			}
		case "tool_use":
			// Assistant tool_use blocks are represented as function_call items
			// at the input[] level (handled by the caller), not as content.
		case "tool_result":
			// Tool results are function_call_output items (handled by caller).
		default:
			// Unknown block: pass through captured raw JSON when available.
			if raw, ok := block.RawContent.(string); ok && raw != "" {
				var original map[string]any
				if err := json.Unmarshal([]byte(raw), &original); err == nil {
					result = append(result, original)
				}
			}
		}
	}
	return result
}

// buildResponsesInputImage maps an IR image block to a Responses input_image.
// Responses accepts either an image_url or a file_id; base64 is rebuilt as a
// data URI (same convention as SerializeOpenAI's image_url handling).
//
// A-#18(c): file_id images are emitted natively as
// {"type":"input_image","file_id":...} — mirroring parseResponsesInputImage,
// which reads input_image.file_id — instead of a broken image_url:"" that
// corrupted the same-protocol round trip.
func buildResponsesInputImage(img *ImageSource) map[string]any {
	// FileID takes precedence, matching the parser (a parsed file_id sets
	// Type="file_id" even when image_url was also present).
	if img.FileID != "" {
		block := map[string]any{
			"type":    "input_image",
			"file_id": img.FileID,
		}
		if img.Detail != "" {
			block["detail"] = img.Detail
		}
		return block
	}

	url := img.URL
	if url == "" && img.Data != "" {
		mt := img.MediaType
		if mt == "" {
			mt = "image/png"
		}
		url = "data:" + mt + ";base64," + img.Data
	}

	block := map[string]any{
		"type":      "input_image",
		"image_url": url,
	}
	// Detail is optional but accepted.
	if img.Detail != "" {
		block["detail"] = img.Detail
	}
	return block
}

// buildResponsesInputAudio maps a generic audio MediaSource to a Responses
// input_audio block (base64 data + format).
func buildResponsesInputAudio(audio *MediaSource) map[string]any {
	block := map[string]any{"type": "input_audio"}
	inner := map[string]any{}
	if audio.Data != "" {
		inner["data"] = audio.Data
	}
	if audio.Format != "" {
		inner["format"] = audio.Format
	}
	block["input_audio"] = inner
	return block
}

// buildResponsesInputFile maps a document block to a Responses input_file.
// Responses expects {file_data: <data URI or url>, filename?, mime_type?}.
func buildResponsesInputFile(doc *DocumentBlock) map[string]any {
	if doc == nil || doc.Source == nil {
		return nil
	}
	block := map[string]any{"type": "input_file"}
	inner := map[string]any{}
	if doc.Title != "" {
		inner["filename"] = doc.Title
	}
	mt := doc.MIMEType
	if doc.Source.MediaType != "" {
		mt = doc.Source.MediaType
	}
	switch doc.Source.Type {
	case "base64":
		if mt == "" {
			mt = "application/pdf"
		}
		if mt != "" {
			inner["mime_type"] = mt
		}
		inner["file_data"] = "data:" + mt + ";base64," + doc.Source.Data
	case "url":
		url := doc.Source.URL
		if url == "" { // compatibility with pre-canonical IR rows
			url = doc.Source.Data
		}
		inner["file_data"] = url
	case "file_id":
		// A-#18(b): prefer the unified FileID field; fall back to Data for
		// legacy rows (parse_openai used to encode the id there, and session
		// restore collapses FileID into Data).
		fid := doc.Source.FileID
		if fid == "" {
			fid = doc.Source.Data
		}
		inner["file_id"] = fid
	default:
		// text/unknown: carry the raw data as file_data.
		if mt != "" {
			inner["mime_type"] = mt
		}
		inner["file_data"] = doc.Source.Data
	}
	for key, value := range inner {
		block[key] = value
	}
	return block
}

// hasToolResultBlock reports whether any content block is a tool_result.
func hasToolResultBlock(blocks []ContentBlock) bool {
	for _, b := range blocks {
		if b.Type == "tool_result" && b.ToolResult != nil {
			return true
		}
	}
	return false
}

// buildResponsesFunctionCallOutput converts an OpenAI "tool" role message (or
// an Anthropic user message carrying a tool_result block) into a Responses
// function_call_output input item.
//
// Shape: {"type":"function_call_output","call_id":"...","output":"<text>"}
func buildResponsesFunctionCallOutput(msg Message) map[string]any {
	// Prefer the explicit ToolResult block content.
	for _, b := range msg.Content {
		if b.Type == "tool_result" && b.ToolResult != nil {
			return map[string]any{
				"type":    "function_call_output",
				"call_id": b.ToolResult.ToolUseID,
				"output":  extractTextFromContent(b.ToolResult.Content),
			}
		}
	}
	// Fall back to the Chat Completions tool message shape.
	callID := msg.ToolCallID
	if callID == "" {
		return nil
	}
	return map[string]any{
		"type":    "function_call_output",
		"call_id": callID,
		"output":  extractTextFromContent(msg.Content),
	}
}

// buildResponsesTools converts IR ToolDefinitions to the flat Responses tools
// shape: {"type":"function","name":...,"description":...,"parameters":...}.
//
// Unlike Chat Completions there is no nested {"function":{...}} wrapper.
// Provider-specific tool types (web_search, file_search, ...) are passed
// through verbatim from their captured Raw bytes on same-protocol routes.
func buildResponsesTools(tools []ToolDefinition) []map[string]any {
	result := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		if !tool.IsFunction() && len(tool.Raw) > 0 {
			var obj map[string]any
			if err := json.Unmarshal(tool.Raw, &obj); err == nil && obj != nil {
				result = append(result, obj)
				continue
			}
			// Fall through to function shape if Raw is unparseable (defensive).
		}
		entry := map[string]any{
			"type": "function",
			// E5: sanitize function name for Responses API constraints
			// (≤64 chars, [a-zA-Z0-9_-]). Skipped when name is empty (caller
			// should pre-validate tool definition).
			"name": SanitizeResponsesFunctionName(tool.Name),
		}
		if tool.Description != "" {
			entry["description"] = tool.Description
		}
		if tool.Parameters != nil {
			entry["parameters"] = tool.Parameters
		}
		result = append(result, entry)
	}
	return result
}

// buildResponsesToolChoice converts IR ToolChoice to the Responses tool_choice.
// "auto"/"none"/"required" → bare string; forced tool →
// {"type":"function","name":...}. Anthropic "any" maps to "required" (same
// normalization SerializeOpenAI applies).
func buildResponsesToolChoice(tc *ToolChoice) any {
	if tc == nil {
		return nil
	}
	switch tc.Type {
	case "auto", "none", "required":
		return tc.Type
	case "any":
		return "required"
	case "tool":
		return map[string]any{
			"type": "function",
			// E5: sanitize function name to match Responses API constraints.
			"name": SanitizeResponsesFunctionName(tc.Name),
		}
	}
	return tc.Type
}

// buildResponsesMetadata maps IR Metadata to the Responses metadata object.
// Responses metadata is a flat {key:value} map; user_id is preserved.
func buildResponsesMetadata(m *Metadata) map[string]any {
	out := map[string]any{}
	if m.UserID != "" {
		out["user_id"] = m.UserID
	}
	if m.RequestID != "" {
		out["request_id"] = m.RequestID
	}
	for k, v := range m.Other {
		out[k] = v
	}
	return out
}

// buildResponsesReasoning maps IR ReasoningConfig to the Responses reasoning
// object. Responses uses {"effort":"low"|"medium"|"high"} (no separate toggle
// like Anthropic thinking).
func buildResponsesReasoning(r *ReasoningConfig) map[string]any {
	if r == nil {
		return nil
	}
	out := map[string]any{}
	if r.Effort != "" {
		out["effort"] = r.Effort
	}
	if r.Type != "" {
		// Some Responses-compatible providers accept a summary toggle.
		out["summary"] = r.Type
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Responses API sanitization constants (E5 / docs/omni-ref3 §7.3).
//
// Per platform.openai.com/docs/api-reference/responses:
//   - tool/function names: ≤64 chars, allowed chars [a-zA-Z0-9_-]
//   - item IDs use server-assigned prefixes: rs_* (response), fc_* (function_call),
//     msg_* (message), fco_* (function_call_output). Client-supplied IDs must
//     match the prefix the server expects, otherwise the request is rejected.
const (
	// responsesMaxFunctionNameLen matches the OpenAI Responses API limit.
	responsesMaxFunctionNameLen = 64
	// responsesItemIDPrefixes mirrors server-side item type tags.
	responsesItemIDPrefixes = "rs_|fc_|msg_|fco_|item_"
)

// SanitizeResponsesFunctionName normalizes a function/tool name for use in the
// Responses API. The OpenAI Responses API rejects names that exceed 64 chars
// or contain characters outside [a-zA-Z0-9_-].
//
// Rules:
//   - truncate to 64 chars
//   - replace disallowed chars with '_'
//   - prefix with 'fn_' if the result would otherwise be empty or start with
//     a digit (Responses also rejects names starting with a digit in some
//     modes; the prefix is harmless when already present)
//
// This is safe to call repeatedly (idempotent). Returns "" only when input is
// "" (caller can decide to skip the tool).
func SanitizeResponsesFunctionName(name string) string {
	if name == "" {
		return ""
	}

	// Replace disallowed chars with '_'.
	out := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '_', c == '-':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}

	// If leading char is a digit, prefix with 'fn_' (collisions are harmless).
	if len(out) > 0 && out[0] >= '0' && out[0] <= '9' {
		out = append([]byte("fn_"), out...)
	}

	// Truncate to max length last (after prefixing) so the prefix is preserved.
	if len(out) > responsesMaxFunctionNameLen {
		out = out[:responsesMaxFunctionNameLen]
	}

	return string(out)
}

// EnsureResponsesItemIDPrefix rewrites a client-supplied item id to carry one
// of the recognized server-side prefixes (rs_, fc_, msg_, fco_, item_).
//
// The Responses API rejects item ids that don't carry a known prefix. This
// helper:
//   - detects an existing recognized prefix and returns the id unchanged
//   - otherwise infers the type from call shape and prepends the matching tag
//
// typeHint is one of "function_call", "function_call_output", "message", or
// "" (no hint → defaults to "item_"). Returns the original id when it is
// already prefixed or empty.
func EnsureResponsesItemIDPrefix(id, typeHint string) string {
	if id == "" {
		return id
	}
	// Known server-side prefixes. Match by full-length prefix.
	knownPrefixes := []string{"rs_", "fc_", "msg_", "fco_", "item_"}
	for _, p := range knownPrefixes {
		if len(id) >= len(p) && id[:len(p)] == p {
			return id
		}
	}

	switch typeHint {
	case "function_call":
		return "fc_" + id
	case "function_call_output":
		return "fco_" + id
	case "message":
		return "msg_" + id
	default:
		return "item_" + id
	}
}

// request wire format cannot faithfully represent. Spec §10 Step 4.10.
//
// Same-protocol guard: when the IR source is itself the Responses API
// (ProtocolOpenAIResponses), every field below is native and recording a loss
// would be a false positive — the round-trip is lossless. An empty
// SourceProtocol is treated as the cross-protocol default (preserves the
// historical behavior of tests that build IR directly without setting it).
//
// Reported losses (cross-protocol only):
//   - Anthropic-only fields: thinking, thinking.signature, redacted_thinking,
//     cache_control, documents, mcp_servers, context_management, container,
//     top_k
//   - OpenAI Chat-only fields that Responses does not accept:
//     frequency_penalty, presence_penalty, logprobs, top_logprobs, n,
//     logit_bias, prediction, verbosity, web_search_options, modalities,
//     audio (Responses has no direct equivalents for these)
func reportSerializeResponsesLosses(req *InternalRequest) {
	if req == nil {
		return
	}
	src := req.SourceProtocol

	// Same-protocol Responses → Responses: no losses.
	if src == ProtocolOpenAIResponses {
		return
	}

	// top_k: Anthropic-native; Responses has no top_k.
	if req.TopK != nil {
		ReportProtocolLoss(
			requestIDFromIR(req),
			"top_k",
			ifaceNonEmpty(src, ProtocolAnthropicMessages),
			ProtocolOpenAIResponses,
			"loss",
			"top_k is Anthropic-only; Responses API has no equivalent",
			map[string]any{"top_k_value": *req.TopK},
		)
	}

	// Per-message thinking.signature / redacted_thinking (Anthropic concept).
	for i, msg := range req.Messages {
		for j, block := range msg.Content {
			if block.Thinking != nil && block.Thinking.Signature != "" {
				ReportProtocolLoss(
					requestIDFromIR(req),
					fieldPathMessageContent(i, j, "thinking.signature"),
					ifaceNonEmpty(src, ProtocolAnthropicMessages),
					ProtocolOpenAIResponses,
					"loss",
					"Anthropic thinking.signature cannot be expressed on Responses API",
					map[string]any{"message_index": i, "content_index": j},
				)
			}
			if block.RedactedThinking != "" {
				ReportProtocolLoss(
					requestIDFromIR(req),
					fieldPathMessageContent(i, j, "redacted_thinking"),
					ifaceNonEmpty(src, ProtocolAnthropicMessages),
					ProtocolOpenAIResponses,
					"loss",
					"Anthropic redacted_thinking cannot be expressed on Responses API",
					nil,
				)
			}
		}
	}

	// Anthropic-only top-level fields.
	if len(req.CacheControl) > 0 {
		ReportProtocolLoss(
			requestIDFromIR(req),
			"cache_control",
			ifaceNonEmpty(src, ProtocolAnthropicMessages),
			ProtocolOpenAIResponses,
			"loss",
			"Anthropic cache_control cannot be expressed on Responses API",
			nil,
		)
	}
	if len(req.Documents) > 0 {
		ReportProtocolLoss(
			requestIDFromIR(req),
			"documents",
			ifaceNonEmpty(src, ProtocolAnthropicMessages),
			ProtocolOpenAIResponses,
			"loss",
			"Anthropic top-level documents cannot be expressed on Responses API",
			nil,
		)
	}
	if req.Thinking != nil {
		ReportProtocolLoss(
			requestIDFromIR(req),
			"thinking",
			ifaceNonEmpty(src, ProtocolAnthropicMessages),
			ProtocolOpenAIResponses,
			"loss",
			"Anthropic thinking config cannot be expressed on Responses API",
			nil,
		)
	}
	if len(req.MCPServers) > 0 {
		ReportProtocolLoss(
			requestIDFromIR(req),
			"mcp_servers",
			ifaceNonEmpty(src, ProtocolAnthropicMessages),
			ProtocolOpenAIResponses,
			"loss",
			"Anthropic mcp_servers cannot be expressed on Responses API",
			nil,
		)
	}
	if req.ContextManagement != nil {
		ReportProtocolLoss(
			requestIDFromIR(req),
			"context_management",
			ifaceNonEmpty(src, ProtocolAnthropicMessages),
			ProtocolOpenAIResponses,
			"loss",
			"Anthropic context_management cannot be expressed on Responses API",
			nil,
		)
	}
	if req.Container != nil {
		ReportProtocolLoss(
			requestIDFromIR(req),
			"container",
			ifaceNonEmpty(src, ProtocolAnthropicMessages),
			ProtocolOpenAIResponses,
			"loss",
			"Anthropic container cannot be expressed on Responses API",
			nil,
		)
	}

	// OpenAI Chat Completions-only fields Responses does not accept directly.
	chatOnly := []struct {
		field string
		have  bool
	}{
		{"frequency_penalty", req.FrequencyPenalty != nil},
		{"presence_penalty", req.PresencePenalty != nil},
		{"logprobs", req.Logprobs != nil},
		{"top_logprobs", req.TopLogprobs != nil},
		{"n", req.N > 0},
		{"logit_bias", len(req.LogitBias) > 0},
		{"prediction", req.Prediction != nil},
		{"verbosity", req.Verbosity != ""},
		{"web_search_options", req.WebSearchOptions != nil},
		{"modalities", len(req.Modalities) > 0},
		{"audio", req.AudioConfig != nil},
	}
	for _, f := range chatOnly {
		if !f.have {
			continue
		}
		ReportProtocolLoss(
			requestIDFromIR(req),
			f.field,
			ifaceNonEmpty(src, ProtocolOpenAIChat),
			ProtocolOpenAIResponses,
			"loss",
			"OpenAI Chat Completions-only field has no direct Responses API equivalent",
			nil,
		)
	}
}
