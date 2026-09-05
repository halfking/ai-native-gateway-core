package ir

import (
	"encoding/json"
	"fmt"
)

// SerializeOpenAI serializes an InternalRequest into an OpenAI Chat Completions request body.
func SerializeOpenAI(req *InternalRequest) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("request is nil")
	}

	out := map[string]any{
		"model": req.Model,
	}

	// MaxTokens (prefer the explicit field, note: OpenAI uses max_tokens not max_completion_tokens for Chat)
	if req.MaxTokens > 0 {
		out["max_tokens"] = req.MaxTokens
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

	// Stop sequences
	if len(req.Stop) > 0 {
		out["stop"] = req.Stop
	}

	// OpenAI-only fields
	if req.FrequencyPenalty != nil {
		out["frequency_penalty"] = *req.FrequencyPenalty
	}
	if req.PresencePenalty != nil {
		out["presence_penalty"] = *req.PresencePenalty
	}
	if req.Logprobs != nil {
		out["logprobs"] = *req.Logprobs
	}
	if req.TopLogprobs != nil {
		out["top_logprobs"] = *req.TopLogprobs
	}
	if req.Seed != nil {
		out["seed"] = *req.Seed
	}
	if req.N > 0 {
		out["n"] = req.N
	}
	if req.User != "" {
		out["user"] = req.User
	}

	// Response format
	if req.ResponseFormat != nil {
		rf := map[string]any{"type": req.ResponseFormat.Type}
		if req.ResponseFormat.Schema != nil {
			rf["json_schema"] = req.ResponseFormat.Schema
		}
		out["response_format"] = rf
	}

	// audit-provider-multimodal (2026-07-13): Personalized provider fields
	if req.Reasoning != nil && req.Reasoning.Effort != "" {
		out["reasoning_effort"] = req.Reasoning.Effort
	}
	if len(req.Modalities) > 0 {
		out["modalities"] = req.Modalities
	}
	if req.AudioConfig != nil {
		ac := map[string]any{}
		if req.AudioConfig.Voice != "" {
			ac["voice"] = req.AudioConfig.Voice
		}
		if req.AudioConfig.Format != "" {
			ac["format"] = req.AudioConfig.Format
		}
		if req.AudioConfig.Speed > 0 {
			ac["speed"] = req.AudioConfig.Speed
		}
		out["audio"] = ac
	}
	if len(req.LogitBias) > 0 {
		out["logit_bias"] = req.LogitBias
	}
	if req.Store != nil {
		out["store"] = *req.Store
	}
	if req.ServiceTier != "" {
		out["service_tier"] = req.ServiceTier
	}
	if req.Prediction != nil {
		out["prediction"] = map[string]any{
			"type":    req.Prediction.Type,
			"content": req.Prediction.Content,
		}
	}
	if req.Verbosity != "" {
		out["verbosity"] = req.Verbosity
	}
	if req.WebSearchOptions != nil {
		wso := map[string]any{}
		if req.WebSearchOptions.ContextSize != "" {
			wso["context_size"] = req.WebSearchOptions.ContextSize
		} else if req.WebSearchOptions.SearchContextSize != "" {
			wso["search_context_size"] = req.WebSearchOptions.SearchContextSize
		}
		if req.WebSearchOptions.UserLocation != nil {
			loc := map[string]any{}
			if req.WebSearchOptions.UserLocation.Type != "" {
				loc["type"] = req.WebSearchOptions.UserLocation.Type
			}
			if req.WebSearchOptions.UserLocation.City != "" {
				loc["city"] = req.WebSearchOptions.UserLocation.City
			}
			if req.WebSearchOptions.UserLocation.Country != "" {
				loc["country"] = req.WebSearchOptions.UserLocation.Country
			}
			if req.WebSearchOptions.UserLocation.Region != "" {
				loc["region"] = req.WebSearchOptions.UserLocation.Region
			}
			if req.WebSearchOptions.UserLocation.Timezone != "" {
				loc["timezone"] = req.WebSearchOptions.UserLocation.Timezone
			}
			wso["user_location"] = loc
		}
		out["web_search_options"] = wso
	}
	if req.PromptCacheKey != "" {
		out["prompt_cache_key"] = req.PromptCacheKey
	}
	if req.SafetyIdentifier != "" {
		out["safety_identifier"] = req.SafetyIdentifier
	}
	if req.PreviousResponseID != "" {
		out["previous_response_id"] = req.PreviousResponseID
	}
	if req.Truncation != "" {
		out["truncation"] = req.Truncation
	}

	// Messages (system prompt becomes first message)
	messages := serializeOpenAIMessages(req)
	// Step 4.10 (2026-07-28): emit explicit anomaly when source fields
	// cannot be expressed on the OpenAI Chat Completions wire format.
	reportSerializeOpenAILosses(req)
	if len(messages) > 0 {
		out["messages"] = messages
	}

	// Tools
	if len(req.Tools) > 0 {
		tools := serializeOpenAITools(req.Tools)
		out["tools"] = tools
	}

	// Tool choice
	if req.ToolChoice != nil {
		out["tool_choice"] = serializeOpenAIToolChoice(req.ToolChoice)
	}
	if req.ParallelToolCalls != nil {
		out["parallel_tool_calls"] = *req.ParallelToolCalls
	}

	// Restore Extensions（厂商私有 / 未知字段）。
	//
	// 2026-08-11: 原实现带 `SourceProtocol == ProtocolOpenAIChat` 门禁，导致
	// 跨协议路由（Claude Code anthropic → DeepSeek openai-chat，本网关最核心的
	// 链路）一个 Extensions 字段都还原不了。现改为按字段查参数注册表决策：
	// 未知字段无条件透传，方言私有字段按目标能力裁剪。
	// 详见 restoreExtensions 与 docs/参数全量兼容/01-审计基线与研究结论.md。
	restoreExtensions(out, req, ProtocolOpenAIChat)

	// Validate tool_call integrity before sending to upstream.
	//
	// 2026-07-27 (F-6, re-audited): the len(messages) > 2 gate is INTENTIONAL
	// (see serialize_anthropic.go for the full rationale). Orphaned-tool-result
	// detection only applies to multi-turn contexts where corruption is
	// detectable; 1-2 message slices may be legitimate continuation fragments.
	// Do not weaken this gate without moving validation to the request boundary.
	if len(messages) > 2 {
		if err := validateToolCallIntegrity(messages); err != nil {
			return nil, fmt.Errorf("tool_call validation failed: %w", err)
		}
	}

	return json.Marshal(out)
}

// systemPlainText flattens IR System to plain text for text-only system
// surfaces (OpenAI system message, Responses instructions). Content wins when
// set; otherwise Anthropic/Gemini parse paths leave Parts-only systems, which
// must not be silently dropped.
func systemPlainText(sys *SystemPrompt) string {
	if sys == nil {
		return ""
	}
	if sys.Content != "" {
		return sys.Content
	}
	parts := make([]string, 0, len(sys.Parts))
	for _, block := range sys.Parts {
		if block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return joinTextParts(parts)
}

// serializeOpenAIMessages converts IR messages to OpenAI format.
func serializeOpenAIMessages(req *InternalRequest) []map[string]any {
	messages := make([]map[string]any, 0, len(req.Messages)+1)

	// Prepend system message if present
	if text := systemPlainText(req.System); text != "" {
		messages = append(messages, map[string]any{
			"role":    "system",
			"content": text,
		})
	}

	// Convert each message
	for _, msg := range req.Messages {
		messages = append(messages, serializeOpenAIMessage(msg))
	}

	return messages
}

// serializeOpenAIMessage converts a single IR Message to OpenAI format.
func serializeOpenAIMessage(msg Message) map[string]any {
	out := map[string]any{
		"role": msg.Role,
	}

	// Special handling for tool role messages
	// Anthropic sends tool results as user messages with tool_result content blocks
	// OpenAI expects tool role messages with tool_call_id and content
	if msg.Role == "user" && len(msg.Content) > 0 {
		// Check if this is actually a tool result (Anthropic format)
		for _, block := range msg.Content {
			if block.Type == "tool_result" && block.ToolResult != nil {
				// Convert to OpenAI tool message
				out["role"] = "tool"
				out["tool_call_id"] = block.ToolResult.ToolUseID

				// Extract text content from tool result
				var textParts []string
				for _, cb := range block.ToolResult.Content {
					if cb.Type == "text" {
						textParts = append(textParts, cb.Text)
					}
				}

				if len(textParts) > 0 {
					out["content"] = joinTextParts(textParts)
				} else {
					out["content"] = ""
				}

				// Optional name field
				if msg.Name != "" {
					out["name"] = msg.Name
				}

				return out
			}
		}
	}

	// Handle tool role with explicit ToolCallID
	if msg.Role == "tool" {
		if msg.ToolCallID != "" {
			out["tool_call_id"] = msg.ToolCallID
		}
		if msg.Name != "" {
			out["name"] = msg.Name
		}

		// Extract content from blocks
		if len(msg.Content) > 0 {
			if len(msg.Content) == 1 && msg.Content[0].Type == "text" {
				out["content"] = msg.Content[0].Text
			} else {
				// Multiple blocks or tool_result - extract text
				var textParts []string
				for _, block := range msg.Content {
					if block.Type == "text" {
						textParts = append(textParts, block.Text)
					} else if block.Type == "tool_result" && block.ToolResult != nil {
						for _, cb := range block.ToolResult.Content {
							if cb.Type == "text" {
								textParts = append(textParts, cb.Text)
							}
						}
					}
				}
				out["content"] = joinTextParts(textParts)
			}
		}

		return out
	}

	// Handle content for non-tool roles
	if len(msg.Content) == 0 {
		// Empty content - may still have tool_calls
		out["content"] = ""
		if len(msg.ToolCalls) > 0 {
			out["tool_calls"] = serializeOpenAIToolCalls(msg.ToolCalls)
		}
	} else if len(msg.Content) == 1 && msg.Content[0].Type == "text" && msg.ToolCalls == nil {
		// Simple text content - use string format
		out["content"] = msg.Content[0].Text
	} else {
		// Multimodal content or tool_calls
		content := serializeOpenAIMessageContent(msg.Content)
		out["content"] = content

		// Add tool_calls for assistant messages
		if len(msg.ToolCalls) > 0 {
			out["tool_calls"] = serializeOpenAIToolCalls(msg.ToolCalls)
		}
	}

	// Handle name for other roles
	if msg.Name != "" && msg.Role != "tool" {
		out["name"] = msg.Name
	}

	return out
}

// joinTextParts joins text parts with newlines.
func joinTextParts(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		return parts[0]
	}
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += "\n"
		}
		result += p
	}
	return result
}

// serializeOpenAIMessageContent converts IR content blocks to OpenAI content array.
func serializeOpenAIMessageContent(blocks []ContentBlock) []map[string]any {
	result := make([]map[string]any, 0, len(blocks))

	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				result = append(result, map[string]any{
					"type": "text",
					"text": block.Text,
				})
			}
		case "image":
			if block.Image != nil {
				// 序列化回 OpenAI image_url 格式。
				// 如果是从 Anthropic base64 转来的（URL 为空但 Data 有值），
				// 重建 data URI 以保证 OpenAI 上游可读。
				url := block.Image.URL
				if url == "" && block.Image.Data != "" {
					mt := block.Image.MediaType
					if mt == "" {
						mt = "image/png"
					}
					url = "data:" + mt + ";base64," + block.Image.Data
				}

				// A-#18(c): file_id-only 图片在 Chat Completions 上没有
				// image_url 表达。输出 image_url:"" 会产生上游拒收的空字段，
				// 因此跳过该块；损失由 reportSerializeOpenAILosses 显式上报
				// （本函数拿不到 message 索引与 SourceProtocol）。
				if url == "" && block.Image.FileID != "" {
					continue
				}

				imageURL := map[string]any{"url": url}
				// P1-1 fix (2026-07-13): Restore detail parameter if present
				if block.Image.Detail != "" {
					imageURL["detail"] = block.Image.Detail
				}

				result = append(result, map[string]any{
					"type":      "image_url",
					"image_url": imageURL,
				})
			}
		case "audio":
			// audit-provider-multimodal (2026-07-13): OpenAI audio output config in messages
			if block.Audio != nil {
				result = append(result, serializeOpenAIAudioBlock(block.Audio))
			}
		case "input_audio":
			// audit-provider-multimodal (2026-07-13): OpenAI chat audio input
			if block.InputAudio != nil {
				result = append(result, map[string]any{
					"type": "input_audio",
					"input_audio": map[string]any{
						"data":   block.InputAudio.Data,
						"format": block.InputAudio.Format,
					},
				})
			}
		case "video", "document":
			// audit-provider-multimodal (2026-07-13): pass-through for Qwen-VL video, OpenAI file input
			if block.Document != nil {
				result = append(result, serializeOpenAIDocumentBlock(block.Document))
			} else if block.Video != nil {
				result = append(result, serializeOpenAIVideoBlock(block.Video))
			}
		case "tool_use":
			// tool_use blocks are converted to OpenAI tool_calls format and
			// stored in message.ToolCalls (handled by the caller), not in the
			// content array.
		case "tool_result":
			// For tool results in content blocks, serialize as text
			if block.ToolResult != nil {
				var text string
				for _, cb := range block.ToolResult.Content {
					if cb.Type == "text" {
						text += cb.Text + "\n"
					}
				}
				if text != "" {
					result = append(result, map[string]any{
						"type": "text",
						"text": text,
					})
				}
			}
		default:
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

// serializeOpenAIToolCalls converts IR ToolCalls to OpenAI format.
func serializeOpenAIToolCalls(calls []ToolCall) []map[string]any {
	result := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		result = append(result, map[string]any{
			"id":   call.ID,
			"type": "function",
			"function": map[string]any{
				"name":      call.Function.Name,
				"arguments": call.Function.Arguments,
			},
		})
	}
	return result
}

// serializeOpenAITools converts IR ToolDefinitions to OpenAI format.
func serializeOpenAITools(tools []ToolDefinition) []map[string]any {
	result := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		// 2026-07-27 (F-1): provider-specific tool types (web_search,
		// code_interpreter, file_search, ...) are passed through verbatim
		// from their captured Raw bytes on same-protocol routes, instead of
		// being collapsed into a broken {type:"function",function:{name:""}}.
		if !tool.IsFunction() && len(tool.Raw) > 0 {
			var obj map[string]any
			if err := json.Unmarshal(tool.Raw, &obj); err == nil && obj != nil {
				result = append(result, obj)
				continue
			}
			// Fall through to function shape if Raw is unparseable (defensive).
		}
		result = append(result, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  tool.Parameters,
			},
		})
	}
	return result
}

// serializeOpenAIToolChoice converts IR ToolChoice to OpenAI format.
func serializeOpenAIToolChoice(tc *ToolChoice) any {
	if tc == nil {
		return nil
	}

	switch tc.Type {
	case "auto", "none":
		return tc.Type
	// "any" is Anthropic's spelling of "the model must call a tool". OpenAI's
	// API does not accept "any" (it returns 400) — the semantically closest
	// accepted value is "required". Map it so an Anthropic-protocol client
	// (tool_choice:"any") routed to an OpenAI upstream does not get rejected.
	// "required" is already a native OpenAI value, pass it through unchanged.
	case "any":
		return "required"
	case "required":
		return "required"
	case "tool":
		return map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": tc.Name,
			},
		}
	}

	return tc.Type
}

// validateToolCallIntegrity checks that all tool messages have matching assistant tool_calls.
// This prevents upstream provider errors (e.g., MiniMax "tool id not found (2013)") caused by
// client-side context compression bugs that delete assistant messages but leave orphaned tool results.
func validateToolCallIntegrity(messages []map[string]any) error {
	toolCallIDs := make(map[string]bool)

	// 1. Collect all assistant tool_call IDs
	for _, msg := range messages {
		if role, _ := msg["role"].(string); role == "assistant" {
			toolCallsAny := msg["tool_calls"]
			if toolCallsAny == nil {
				continue
			}

			// Handle both []interface{} (from json.Unmarshal) and []map[string]any (from Go code)
			switch tcs := toolCallsAny.(type) {
			case []interface{}:
				for _, tc := range tcs {
					if tcMap, ok := tc.(map[string]interface{}); ok {
						if id, _ := tcMap["id"].(string); id != "" {
							toolCallIDs[id] = true
						}
					}
				}
			case []map[string]any:
				for _, tc := range tcs {
					if id, _ := tc["id"].(string); id != "" {
						toolCallIDs[id] = true
					}
				}
			}
		}
	}

	// 2. Check that all tool messages have matching IDs
	var orphans []string
	for _, msg := range messages {
		if role, _ := msg["role"].(string); role == "tool" {
			if id, _ := msg["tool_call_id"].(string); id != "" {
				if !toolCallIDs[id] {
					orphans = append(orphans, id)
				}
			}
		}
	}

	if len(orphans) > 0 {
		// Limit displayed orphans to first 3 to avoid huge error messages
		displayOrphans := orphans
		if len(displayOrphans) > 3 {
			displayOrphans = displayOrphans[:3]
		}
		return fmt.Errorf("found %d orphaned tool result(s) without matching assistant tool_calls: %v (likely client bug: assistant messages with tool_calls were removed during context compression)",
			len(orphans), displayOrphans)
	}

	return nil
}

// serializeOpenAIAudioBlock converts an audio content block (type "audio",
// e.g. originating from a Gemini source) to OpenAI-compatible output.
// OpenAI Chat Completions audio input uses block type "input_audio", not
// "audio"; emitting "audio" produces a block OpenAI rejects.
// audit-provider-multimodal (2026-07-13): OpenAI audio output config + Gemini speech_config.
func serializeOpenAIAudioBlock(audio *MediaSource) map[string]any {
	// OpenAI Chat input audio block type is "input_audio".
	block := map[string]any{"type": "input_audio"}
	// input_audio carries data/url/file_id in the nested object.
	inner := map[string]any{}
	switch audio.Type {
	case "base64":
		inner["data"] = audio.Data
		if audio.Format != "" {
			inner["format"] = audio.Format
		}
	case "url":
		inner["url"] = audio.URL
		if audio.Format != "" {
			inner["format"] = audio.Format
		}
	case "file_id":
		inner["file_id"] = audio.FileID
	default:
		// Fall back: if we have raw base64 data, emit it.
		if audio.Data != "" {
			inner["data"] = audio.Data
			if audio.Format != "" {
				inner["format"] = audio.Format
			}
		} else if audio.URL != "" {
			inner["url"] = audio.URL
		}
	}
	if len(inner) > 0 {
		block["input_audio"] = inner
	}
	return block
}

// serializeOpenAIVideoBlock converts a video content block to OpenAI-compatible output.
// audit-provider-multimodal (2026-07-13): For Qwen-VL video and Gemini video input
// (OpenAI does not have native video, so we pass through as input_file with video MIME).
func serializeOpenAIVideoBlock(video *MediaSource) map[string]any {
	block := map[string]any{"type": "video_url"}
	urlStr := video.URL
	if video.Type == "base64" && video.Data != "" {
		mt := video.MediaType
		if mt == "" {
			mt = "video/mp4"
		}
		urlStr = "data:" + mt + ";base64," + video.Data
	}
	inner := map[string]any{"url": urlStr}
	if video.MediaType != "" {
		inner["mime_type"] = video.MediaType
	}
	block["video_url"] = inner
	return block
}

// serializeOpenAIDocumentBlock converts a document content block to OpenAI file input.
// audit-provider-multimodal (2026-07-13): For PDF/text/csv via OpenAI Responses file input.
func serializeOpenAIDocumentBlock(doc *DocumentBlock) map[string]any {
	if doc == nil || doc.Source == nil {
		return nil
	}
	block := map[string]any{"type": "file"}
	fileInner := map[string]any{}
	if doc.Title != "" {
		fileInner["filename"] = doc.Title
	}
	switch doc.Source.Type {
	case "base64":
		if doc.Source.MediaType != "" {
			fileInner["mime_type"] = doc.Source.MediaType
		}
		// file_data is the data URI form (matches OpenAI Responses API)
		mt := doc.Source.MediaType
		if mt == "" {
			mt = "application/pdf"
		}
		fileInner["file_data"] = "data:" + mt + ";base64," + doc.Source.Data
	case "url":
		url := doc.Source.URL
		if url == "" { // compatibility with pre-canonical IR rows
			url = doc.Source.Data
		}
		fileInner["file_data"] = url
	case "file", "file_id":
		// 2026-09-05 round2 复审: Anthropic-native Files API documents parse
		// with Type="file" (parse_anthropic keeps the wire type as-is), so
		// "file" must take the same path as the IR-internal "file_id" —
		// previously it fell through with no case and the id was silently
		// dropped, leaving a filename-only block. Prefer the unified FileID
		// field; fall back to Data for legacy rows (parse_openai used to
		// encode the id there, and session restore collapses FileID into
		// Data).
		fid := doc.Source.FileID
		if fid == "" {
			fid = doc.Source.Data
		}
		// 空值护栏: a double-empty source has no identity left — never emit
		// file_id:""; reportSerializeOpenAILosses records the loss instead.
		if fid != "" {
			fileInner["file_id"] = fid
		}
	case "text":
		fileInner["file_data"] = doc.Source.Data
	}
	block["file"] = fileInner
	return block
}

// reportSerializeOpenAILosses is the cross-protocol anomaly hook called by
// SerializeOpenAI for every IR field that the OpenAI Chat Completions wire
// format cannot faithfully represent. It is a recording-only side effect;
// the wire format is unchanged.
//
// 2026-07-28 (Step 4 round 2, §10 Step 4.10): when an Anthropic IR carries a
// thinking.signature (no OpenAI equivalent) or a CacheControl / Documents
// block, the loss must be explicit. Same for any top_k value (OpenAI Chat
// has no top_k field).
//
// 2026-07-28 (BLOCK review): same-protocol false-positive guards. The
// BLOCK review identified three false-positive classes:
//
//  1. Anthropic-only fields (cache_control / mcp_servers / thinking.signature
//     / documents / context_management / container) being reported when the
//     IR source is itself OpenAI Chat (same-protocol, target=OpenAI Chat).
//     Note: Anthropic source + OpenAI target IS cross-protocol and we
//     still report there (e.g. the original
//     TestAnomaly_AnthropicThinkingSig_LostOnOpenAITarget fixture).
//  2. OpenAI-only fields (frequency_penalty / presence_penalty / logprobs
//     / top_logprobs / n / response_format / logit_bias / store /
//     service_tier / prediction / verbosity / web_search_options /
//     safety_identifier / parallel_tool_calls / modalities / audio) being
//     reported when the IR source is itself OpenAI Chat (same-protocol).
//  3. previous_response_id being reported when the IR source is already
//     OpenAI Chat or OpenAI Responses (both accept it natively).
//
// An empty SourceProtocol is treated as "unknown / cross-protocol default"
// to preserve historical fixture behavior for tests that build IR directly
// without setting SourceProtocol.
func reportSerializeOpenAILosses(req *InternalRequest) {
	if req == nil {
		return
	}
	src := req.SourceProtocol

	// top_k: Anthropic-native field. Loss on cross-protocol. The only
	// same-protocol skip is when source == OpenAI Chat or Responses (both
	// accept the field via ExtensionsBag; round-trip is lossless).
	if req.TopK != nil && src != ProtocolOpenAIChat && src != ProtocolOpenAIResponses {
		ReportProtocolLoss(
			requestIDFromIR(req),
			"top_k",
			ifaceNonEmpty(src, ProtocolAnthropicMessages),
			ProtocolOpenAIChat,
			"loss",
			"top_k is Anthropic-only; OpenAI Chat Completions has no equivalent",
			map[string]any{"top_k_value": *req.TopK},
		)
	}
	// Per-message thinking.signature / redacted_thinking: Anthropic
	// concept. Skip when source == OpenAI Chat (same-protocol with target).
	for i, msg := range req.Messages {
		for j, block := range msg.Content {
			// A-#18(c): a Files-API file_id image has no image_url
			// representation on Chat Completions — the content serializer
			// drops the block (never emits image_url:""). No same-protocol
			// guard: parse_openai never produces FileID images, so any
			// FileID here is cross-protocol or session-restored, and the
			// drop is a real loss in both cases.
			//
			// 2026-09-05 round2 复审: the block.Type == "image" guard keeps
			// the report truthful if a future writer ever attaches Image to
			// a raw-passthrough block (which the wire would keep verbatim —
			// reporting that as lost would be a false positive).
			if block.Type == "image" && block.Image != nil && block.Image.FileID != "" && block.Image.URL == "" && block.Image.Data == "" {
				ReportProtocolLoss(
					requestIDFromIR(req),
					fieldPathMessageContent(i, j, "image.file_id"),
					ifaceNonEmpty(src, ProtocolAnthropicMessages),
					ProtocolOpenAIChat,
					"loss",
					"file_id image reference cannot be expressed as an OpenAI Chat image_url; block dropped",
					map[string]any{"message_index": i, "content_index": j},
				)
			}
			// 2026-09-05 round2 复审: a document block typed as a Files-API
			// reference whose FileID and Data are both empty serializes as a
			// filename-only file block — the identity is unrecoverable on the
			// wire. Same no-same-protocol-guard rationale as the file_id
			// image above: parsers always fill the id, so a double-empty
			// source is a real loss (degenerate programmatic IR only).
			if block.Document != nil && block.Document.Source != nil &&
				(block.Document.Source.Type == "file" || block.Document.Source.Type == "file_id") &&
				block.Document.Source.FileID == "" && block.Document.Source.Data == "" {
				ReportProtocolLoss(
					requestIDFromIR(req),
					fieldPathMessageContent(i, j, "document.file_id"),
					ifaceNonEmpty(src, ProtocolAnthropicMessages),
					ProtocolOpenAIChat,
					"loss",
					"file_id document reference carries neither FileID nor Data; upstream receives a filename-only file block",
					map[string]any{"message_index": i, "content_index": j},
				)
			}
			// 2026-09-05 round2 复审: Anthropic tool_result blocks accept
			// image children, but every Chat tool path flattens tool content
			// to text — nested images are dropped on the wire. Walk the
			// nested content so the drop is reported like any other loss
			// (nested text does survive and is not reported here).
			if block.ToolResult != nil {
				for k, cb := range block.ToolResult.Content {
					if cb.Type != "image" || cb.Image == nil {
						continue
					}
					ReportProtocolLoss(
						requestIDFromIR(req),
						fieldPathMessageContent(i, j, "tool_result.content["+smallItoa(k)+"].image"),
						ifaceNonEmpty(src, ProtocolAnthropicMessages),
						ProtocolOpenAIChat,
						"loss",
						"image inside tool_result cannot be expressed on OpenAI Chat; tool content is flattened to text and the image is dropped",
						map[string]any{"message_index": i, "content_index": j, "tool_content_index": k},
					)
				}
			}
			if src == ProtocolOpenAIChat {
				continue
			}
			if block.Thinking != nil && block.Thinking.Signature != "" {
				ReportProtocolLoss(
					requestIDFromIR(req),
					fieldPathMessageContent(i, j, "thinking.signature"),
					ifaceNonEmpty(src, ProtocolAnthropicMessages),
					ProtocolOpenAIChat,
					"loss",
					"Anthropic thinking signature cannot be expressed on OpenAI Chat Completions",
					map[string]any{"message_index": i, "content_index": j},
				)
			}
			if block.RedactedThinking != "" {
				ReportProtocolLoss(
					requestIDFromIR(req),
					fieldPathMessageContent(i, j, "redacted_thinking"),
					ifaceNonEmpty(src, ProtocolAnthropicMessages),
					ProtocolOpenAIChat,
					"loss",
					"Anthropic redacted_thinking cannot be expressed on OpenAI Chat Completions",
					nil,
				)
			}
		}
	}
	// Cache control / documents / thinking / mcp_servers / context_management
	// / container are Anthropic-specific. Skip when source == OpenAI Chat
	// (same-protocol with target = OpenAI Chat).
	if src != ProtocolOpenAIChat {
		if len(req.CacheControl) > 0 {
			ReportProtocolLoss(
				requestIDFromIR(req),
				"cache_control",
				ifaceNonEmpty(src, ProtocolAnthropicMessages),
				ProtocolOpenAIChat,
				"loss",
				"Anthropic cache_control cannot be expressed on OpenAI Chat Completions",
				nil,
			)
		}
		if len(req.Documents) > 0 {
			ReportProtocolLoss(
				requestIDFromIR(req),
				"documents",
				ifaceNonEmpty(src, ProtocolAnthropicMessages),
				ProtocolOpenAIChat,
				"loss",
				"Anthropic top-level documents cannot be expressed on OpenAI Chat Completions",
				nil,
			)
		}
		if req.Thinking != nil {
			ReportProtocolLoss(
				requestIDFromIR(req),
				"thinking",
				ifaceNonEmpty(src, ProtocolAnthropicMessages),
				ProtocolOpenAIChat,
				"loss",
				"Anthropic thinking config cannot be expressed on OpenAI Chat Completions",
				nil,
			)
		}
		if len(req.MCPServers) > 0 {
			ReportProtocolLoss(
				requestIDFromIR(req),
				"mcp_servers",
				ifaceNonEmpty(src, ProtocolAnthropicMessages),
				ProtocolOpenAIChat,
				"loss",
				"Anthropic mcp_servers cannot be expressed on OpenAI Chat Completions",
				nil,
			)
		}
		if req.ContextManagement != nil {
			ReportProtocolLoss(
				requestIDFromIR(req),
				"context_management",
				ifaceNonEmpty(src, ProtocolAnthropicMessages),
				ProtocolOpenAIChat,
				"loss",
				"Anthropic context_management cannot be expressed on OpenAI Chat Completions",
				nil,
			)
		}
		if req.Container != nil {
			ReportProtocolLoss(
				requestIDFromIR(req),
				"container",
				ifaceNonEmpty(src, ProtocolAnthropicMessages),
				ProtocolOpenAIChat,
				"loss",
				"Anthropic container cannot be expressed on OpenAI Chat Completions",
				nil,
			)
		}
	}

	// OpenAI-only fields (BLOCK review spec §10 Step 4.10 round 2). The
	// per-field guard is `src != ProtocolOpenAIChat`: when the source is
	// itself Chat, these fields are native and the loss event is a false
	// positive. An empty SourceProtocol still triggers (cross-protocol
	// fallback) to preserve historical fixture behavior.
	if src != ProtocolOpenAIChat {
		openAIOnly := []struct {
			field string
			have  bool
		}{
			{"frequency_penalty", req.FrequencyPenalty != nil},
			{"presence_penalty", req.PresencePenalty != nil},
			{"logprobs", req.Logprobs != nil},
			{"top_logprobs", req.TopLogprobs != nil},
			{"n", req.N > 0},
			{"response_format", req.ResponseFormat != nil},
			{"logit_bias", len(req.LogitBias) > 0},
			{"store", req.Store != nil},
			{"service_tier", req.ServiceTier != ""},
			{"prediction", req.Prediction != nil},
			{"verbosity", req.Verbosity != ""},
			{"web_search_options", req.WebSearchOptions != nil},
			{"safety_identifier", req.SafetyIdentifier != ""},
			{"parallel_tool_calls", req.ParallelToolCalls != nil},
			{"modalities", len(req.Modalities) > 0},
			{"audio", req.AudioConfig != nil},
		}
		for _, f := range openAIOnly {
			if !f.have {
				continue
			}
			ReportProtocolLoss(
				requestIDFromIR(req),
				f.field,
				ifaceNonEmpty(src, ProtocolOpenAIChat),
				ProtocolOpenAIChat,
				"loss",
				"OpenAI Chat Completions-only field cannot be expressed on non-OpenAI target",
				nil,
			)
		}
	}

	// previous_response_id is OpenAI Responses-API. Chat accepts it (the
	// IR serializer emits it as a Chat wire field) and so does Responses;
	// no loss event when the source is one of the OpenAI family. Only
	// Anthropic / Gemini sources carry a real loss. Per BLOCK review:
	// "仅在 SourceProtocol 既不是 ProtocolOpenAIChat 也不是 ProtocolOpenAIResponses
	// 时上报". When SourceProtocol is empty we treat it as cross-protocol
	// default (Anthropic fallback) and emit.
	if req.PreviousResponseID != "" {
		isOpenAISource := src == ProtocolOpenAIChat || src == ProtocolOpenAIResponses
		if !isOpenAISource {
			ReportProtocolLoss(
				requestIDFromIR(req),
				"previous_response_id",
				ifaceNonEmpty(src, ProtocolOpenAIChat),
				ProtocolOpenAIChat,
				"loss",
				"OpenAI Responses previous_response_id is not portable to OpenAI Chat when source is non-OpenAI",
				nil,
			)
		}
	}
}

// requestIDFromIR returns the request id from the IR's first message raw
// content if present, else "unknown". Today the IR does not carry an
// explicit RequestID field; we keep a placeholder for cross-protocol
// tests. Future revision may add RequestID to InternalRequest directly.
func requestIDFromIR(_ *InternalRequest) string {
	return "unknown"
}

// ifaceNonEmpty returns s if non-empty, otherwise the fallback.
func ifaceNonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// fieldPathMessageContent produces a stable dotted JSON pointer for
// messages[i].content[j].<leaf>.
func fieldPathMessageContent(i, j int, leaf string) string {
	return "messages[" + smallItoa(i) + "].content[" + smallItoa(j) + "]." + leaf
}

// smallItoa is a tiny local helper that avoids importing strconv for a few
// hot call sites; keeps the anomaly code footprint minimal. We give it a
// distinct name to avoid colliding with response.go's itoa.
func smallItoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	buf := [20]byte{}
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
