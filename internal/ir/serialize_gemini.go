package ir

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// SerializeGemini serializes an InternalRequest into a Gemini generateContent request body.
//
// Shape:
//
//	{
//	  "contents": [{"role":"user","parts":[{"text":"..."}]}],
//	  "systemInstruction": {"parts":[{"text":"..."}]},
//	  "tools": [{"functionDeclarations":[...]}],
//	  "toolConfig": {"functionCallingConfig":{"mode":"AUTO"}},
//	  "generationConfig": {...},
//	  "safetySettings": [...],
//	  "cachedContent": "..."
//	}
//
// audit-gemini-adapter (2026-07-13): Third outbound protocol serializer, completes
// the O(N) protocol matrix (OpenAI, Anthropic, Gemini).
func SerializeGemini(req *InternalRequest) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("request is nil")
	}

	out := map[string]any{}

	// System instruction
	if req.System != nil {
		si := buildGeminiSystemInstruction(req.System)
		if si != nil {
			out["systemInstruction"] = si
		}
	}

	// Contents (messages)
	contents := buildGeminiContents(req.Messages)
	if len(contents) > 0 {
		out["contents"] = contents
	}

	// Tools (function declarations)
	if len(req.Tools) > 0 {
		out["tools"] = buildGeminiTools(req.Tools)
	}

	// Tool config
	if req.ToolChoice != nil {
		if tc := buildGeminiToolConfig(req.ToolChoice); tc != nil {
			out["toolConfig"] = tc
		}
	}

	// Generation config (combines IR sampling params + Gemini-specific fields)
	if gc := buildGeminiGenerationConfig(req); gc != nil {
		out["generationConfig"] = gc
	}

	// Safety settings（2026-08-11 P3 修复）。
	//
	// 此前 parse_gemini.go 把 safetySettings 列入 knownFields（所以不进
	// Extensions），却从未赋值给 IR、从未序列化、也没有 loss 事件 —— 纯静默丢失。
	// 内容安全阈值被静默丢弃属于合规风险，不只是兼容性问题。
	if len(req.SafetySettings) > 0 {
		out["safetySettings"] = buildGeminiSafetySettings(req.SafetySettings)
	}

	// Cached content（同上，此前静默丢失）。
	if req.CachedContent != "" {
		out["cachedContent"] = req.CachedContent
	}

	// Step 4.10 (2026-07-28): explicit anomaly for cross-protocol losses.
	reportSerializeGeminiLosses(req)

	// Extensions 还原（2026-08-11 P2 修复）。
	//
	// 此前本序列化器**完全没有** Extensions 还原代码（grep Extensions 零命中），
	// 导致任何出向 Gemini 的请求，无论来源协议，未知字段全丢。
	restoreExtensions(out, req, ProtocolGeminiGenerate)

	return json.Marshal(out)
}

// buildGeminiSafetySettings 把 IR SafetySetting 渲染为 Gemini 线格式。
//
// Gemini 要求每个 harmCategory 最多一条。method 字段仅 Vertex AI 支持，
// Gemini Developer API 不认，因此仅在非空时输出。
func buildGeminiSafetySettings(settings []SafetySetting) []map[string]any {
	out := make([]map[string]any, 0, len(settings))
	for _, s := range settings {
		if s.Category == "" && s.Threshold == "" {
			continue
		}
		item := map[string]any{}
		if s.Category != "" {
			item["category"] = s.Category
		}
		if s.Threshold != "" {
			item["threshold"] = s.Threshold
		}
		if s.Method != "" {
			item["method"] = s.Method
		}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// reportSerializeGeminiLosses records IR fields that the Gemini generateContent
// wire format cannot faithfully represent. 2026-07-28 (Step 4 round 2).
//
// Gemini has a smaller conceptual surface than Anthropic / OpenAI; the most
// common losses are Anthropic thinking.signature and OpenAI Responses-only
// status / previous_response_id.
//
// 2026-07-28 (BLOCK review): same-protocol false-positive guards. When the
// IR source is itself Anthropic, fields like cache_control / documents /
// thinking.signature are native to Anthropic (not Gemini). The Gemini
// target's loss reporting must respect that the source is Anthropic — the
// field is genuinely a loss when going TO Gemini (different target), but
// the dedup key / source_protocol labeling must remain correct.
//
// Specifically, the BLOCK review calls out thinking.signature / redacted_thinking
// and other Anthropic-only fields: when SourceProtocol == AnthropicMessages
// AND target == Gemini, the field IS a loss (different target); when both
// source and target equal Anthropic we skip (handled in serialize_anthropic).
// Gemini → Gemini must not emit any cross-protocol loss.
func reportSerializeGeminiLosses(req *InternalRequest) {
	if req == nil {
		return
	}
	src := req.SourceProtocol
	// Same-protocol Gemini → Gemini is a no-op for loss reporting.
	if src == ProtocolGeminiGenerate {
		return
	}

	for i, msg := range req.Messages {
		for j, block := range msg.Content {
			if block.Thinking != nil && block.Thinking.Signature != "" {
				ReportProtocolLoss(
					requestIDFromIR(req),
					fieldPathMessageContent(i, j, "thinking.signature"),
					ifaceNonEmpty(src, ProtocolAnthropicMessages),
					ProtocolGeminiGenerate,
					"loss",
					"Anthropic thinking.signature has no Gemini equivalent",
					nil,
				)
			}
			if block.RedactedThinking != "" {
				ReportProtocolLoss(
					requestIDFromIR(req),
					fieldPathMessageContent(i, j, "redacted_thinking"),
					ifaceNonEmpty(src, ProtocolAnthropicMessages),
					ProtocolGeminiGenerate,
					"loss",
					"Anthropic redacted_thinking has no Gemini equivalent",
					nil,
				)
			}
		}
	}
	if req.PreviousResponseID != "" {
		ReportProtocolLoss(
			requestIDFromIR(req),
			"previous_response_id",
			ifaceNonEmpty(src, ProtocolOpenAIChat),
			ProtocolGeminiGenerate,
			"loss",
			"OpenAI Responses previous_response_id has no Gemini equivalent",
			nil,
		)
	}
	if len(req.CacheControl) > 0 {
		ReportProtocolLoss(
			requestIDFromIR(req),
			"cache_control",
			ifaceNonEmpty(src, ProtocolAnthropicMessages),
			ProtocolGeminiGenerate,
			"loss",
			"Anthropic cache_control has no Gemini equivalent",
			nil,
		)
	}
	if len(req.Documents) > 0 {
		ReportProtocolLoss(
			requestIDFromIR(req),
			"documents",
			ifaceNonEmpty(src, ProtocolAnthropicMessages),
			ProtocolGeminiGenerate,
			"loss",
			"Anthropic top-level documents have no Gemini equivalent",
			nil,
		)
	}
}

// buildGeminiSystemInstruction converts IR System → Gemini systemInstruction.
func buildGeminiSystemInstruction(sys *SystemPrompt) map[string]any {
	if sys == nil {
		return nil
	}
	parts := make([]map[string]any, 0)

	if len(sys.Parts) > 0 {
		for _, p := range sys.Parts {
			if p.Text != "" {
				parts = append(parts, map[string]any{"text": p.Text})
			}
		}
	} else if sys.Content != "" {
		parts = append(parts, map[string]any{"text": sys.Content})
	}

	if len(parts) == 0 {
		return nil
	}
	return map[string]any{"parts": parts}
}

// buildGeminiContents converts IR Messages → Gemini contents[].
// Maps role: "assistant" → "model", "tool" → "function".
func buildGeminiContents(messages []Message) []map[string]any {
	out := make([]map[string]any, 0, len(messages))

	for _, msg := range messages {
		role := msg.Role
		switch role {
		case "assistant":
			role = "model"
		case "tool":
			role = "function"
		case "system":
			// System messages are conveyed via systemInstruction, skip here
			continue
		}

		parts := make([]map[string]any, 0)

		// OpenAI Chat/Responses input may normalize tool calls into the
		// message-level ToolCalls field. Preserve those calls when the same IR
		// is sent to native Gemini instead of relying only on Anthropic blocks.
		for _, call := range msg.ToolCalls {
			name := call.Function.Name
			if name == "" {
				continue
			}
			args := map[string]any{}
			if call.Function.Arguments != "" {
				if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
					continue
				}
			}
			parts = append(parts, map[string]any{
				"functionCall": map[string]any{
					"name": name,
					"args": args,
				},
			})
		}

		// Chat Completions tool messages carry their result outside content.
		// Gemini requires the corresponding functionResponse part.
		hasToolResultBlock := false
		for _, block := range msg.Content {
			if block.Type == "tool_result" && block.ToolResult != nil {
				hasToolResultBlock = true
				break
			}
		}
		if !hasToolResultBlock && (msg.Role == "function" || (msg.Role == "tool" && msg.ToolCallID != "")) {
			name := msg.Name
			if name == "" {
				name = toolUseNameFromID(msg.ToolCallID)
			}
			if name != "" {
				parts = append(parts, map[string]any{
					"functionResponse": map[string]any{
						"name":     name,
						"response": map[string]any{"result": extractTextFromContent(msg.Content)},
					},
				})
			}
		}

		// Convert each ContentBlock
		for _, block := range msg.Content {
			switch block.Type {
			case "text":
				if block.Text != "" {
					parts = append(parts, map[string]any{"text": block.Text})
				}
			case "image":
				if block.Image != nil {
					if part := irImageToGeminiPart(block.Image); part != nil {
						parts = append(parts, part)
					}
				}
			case "audio":
				if block.Audio != nil {
					if part := irAudioToGeminiPart(block.Audio); part != nil {
						parts = append(parts, part)
					}
				}
			case "input_audio":
				// OpenAI Chat/Responses input_audio block. No canonicalization
				// converts this to type="audio", so without an explicit case it
				// would be silently dropped when routed to a Gemini upstream.
				if block.InputAudio != nil {
					if part := irInputAudioToGeminiPart(block.InputAudio); part != nil {
						parts = append(parts, part)
					}
				}
			case "video":
				if block.Video != nil {
					if part := irVideoToGeminiPart(block.Video); part != nil {
						parts = append(parts, part)
					}
				}
			case "document":
				if block.Document != nil && block.Document.Source != nil {
					if part := irDocumentToGeminiPart(block.Document); part != nil {
						parts = append(parts, part)
					}
				}
			case "tool_use":
				if block.ToolUse != nil {
					parts = append(parts, map[string]any{
						"functionCall": map[string]any{
							"name": block.ToolUse.Name,
							"args": block.ToolUse.Input,
						},
					})
				}
			case "tool_result":
				if block.ToolResult != nil {
					response := any(map[string]any{
						"result": extractTextFromContent(block.ToolResult.Content),
					})
					if block.ToolResult.GeminiResponse != nil {
						response = block.ToolResult.GeminiResponse
					}
					parts = append(parts, map[string]any{
						"functionResponse": map[string]any{
							"name":     toolUseNameFromID(block.ToolResult.ToolUseID),
							"response": response,
						},
					})
				}
			case "thinking":
				// Gemini 2.5+ thinking part (with includeThoughts=true)
				if block.Thinking != nil {
					parts = append(parts, map[string]any{
						"thought": block.Thinking.Thinking,
					})
				}
			}
		}

		if len(parts) == 0 {
			continue
		}
		out = append(out, map[string]any{
			"role":  role,
			"parts": parts,
		})
	}
	return out
}

func irImageToGeminiPart(img *ImageSource) map[string]any {
	if img.Type == "base64" || img.Data != "" {
		return map[string]any{
			"inlineData": map[string]any{
				"mimeType": imageMIME(img),
				"data":     img.Data,
			},
		}
	}
	uri := img.URL
	if img.FileURI != "" {
		uri = img.FileURI
	}
	// Cross-protocol file_id (OpenAI/Anthropic) → Gemini fileUri requires a
	// Files-API registry, out of scope here. Don't emit a fileData part with an
	// empty fileUri — it produces a malformed request. Returning nil lets the
	// caller skip this part rather than send a broken one.
	if uri == "" {
		return nil
	}
	return map[string]any{
		"fileData": map[string]any{
			"mimeType": imageMIME(img),
			"fileUri":  uri,
		},
	}
}

func irAudioToGeminiPart(audio *MediaSource) map[string]any {
	mt := audio.MediaType
	if mt == "" {
		mt = "audio/" + audio.Format
	}
	if audio.Type == "base64" || audio.Data != "" {
		return map[string]any{
			"inlineData": map[string]any{
				"mimeType": mt,
				"data":     audio.Data,
			},
		}
	}
	uri := audio.FileURI
	if uri == "" {
		uri = audio.URL
	}
	// Don't emit a fileData part with an empty fileUri (file_id-only media
	// cannot be resolved without a Files-API registry). See irImageToGeminiPart.
	if uri == "" {
		return nil
	}
	return map[string]any{
		"fileData": map[string]any{
			"mimeType": mt,
			"fileUri":  uri,
		},
	}
}

// irInputAudioToGeminiPart converts an OpenAI input_audio block to a Gemini
// inlineData part. InputAudioBlock.Data is already base64 (no prefix); Format
// is "wav"/"mp3"/etc. Returns nil if there is no payload to emit.
func irInputAudioToGeminiPart(ia *InputAudioBlock) map[string]any {
	if ia == nil || ia.Data == "" {
		return nil
	}
	mt := "audio/wav"
	if ia.Format != "" {
		mt = "audio/" + ia.Format
	}
	return map[string]any{
		"inlineData": map[string]any{
			"mimeType": mt,
			"data":     ia.Data,
		},
	}
}

func irVideoToGeminiPart(video *MediaSource) map[string]any {
	mt := video.MediaType
	if mt == "" {
		mt = "video/" + video.Format
	}
	if video.Type == "base64" || video.Data != "" {
		return map[string]any{
			"inlineData": map[string]any{
				"mimeType": mt,
				"data":     video.Data,
			},
		}
	}
	uri := video.FileURI
	if uri == "" {
		uri = video.URL
	}
	// Don't emit a fileData part with an empty fileUri (file_id-only media
	// cannot be resolved without a Files-API registry). See irImageToGeminiPart.
	if uri == "" {
		return nil
	}
	return map[string]any{
		"fileData": map[string]any{
			"mimeType": mt,
			"fileUri":  uri,
		},
	}
}

func irDocumentToGeminiPart(doc *DocumentBlock) map[string]any {
	mt := doc.MIMEType
	if doc.Source != nil && doc.Source.MediaType != "" {
		mt = doc.Source.MediaType
	}
	src := doc.Source
	switch src.Type {
	case "base64":
		return map[string]any{
			"inlineData": map[string]any{
				"mimeType": mt,
				"data":     src.Data,
			},
		}
	case "text", "csv", "":
		// Inline text payload. Gemini's inlineData.data MUST be base64-encoded
		// (the parser and the REST spec both treat it as base64). Emitting raw
		// text here would corrupt the document on the Gemini side. We base64
		// the UTF-8 bytes of the body, which round-trips losslessly.
		if mt == "" {
			if src.Type == "csv" {
				mt = "text/csv"
			} else {
				mt = "text/plain"
			}
		}
		return map[string]any{
			"inlineData": map[string]any{
				"mimeType": mt,
				"data":     base64.StdEncoding.EncodeToString([]byte(src.Data)),
			},
		}
	case "url":
		uri := src.URL
		if uri == "" {
			uri = src.Data
		}
		return map[string]any{
			"fileData": map[string]any{
				"mimeType": mt,
				"fileUri":  uri,
			},
		}
	case "file_id":
		// Cross-protocol file_id → Gemini fileUri requires a Files-API
		// registry mapping, which is out of scope here. Emit a fileData part
		// only if a resolvable URI was carried alongside the id.
		uri := src.URL
		if uri == "" {
			return nil
		}
		return map[string]any{
			"fileData": map[string]any{
				"mimeType": mt,
				"fileUri":  uri,
			},
		}
	default:
		uri := src.URL
		if uri == "" {
			uri = src.Data
		}
		if uri == "" {
			return nil
		}
		return map[string]any{
			"fileData": map[string]any{
				"mimeType": mt,
				"fileUri":  uri,
			},
		}
	}
}

func imageMIME(img *ImageSource) string {
	if img.MediaType != "" {
		return img.MediaType
	}
	return "image/png"
}

// extractTextFromContent concatenates text blocks into a single string.
func extractTextFromContent(blocks []ContentBlock) string {
	out := ""
	for _, b := range blocks {
		if b.Type == "text" {
			if out != "" {
				out += "\n"
			}
			out += b.Text
		}
	}
	return out
}

// toolUseNameFromID extracts the function name from a synthetic tool_use_id
// of the form "gemini_call_<name>" or "gemini_call_<name>_<partIdx>".
// The trailing "_<partIdx>" disambiguator (added in 2026-09-01 to fix P0-1
// parallel functionCall ID collisions) is stripped so a round-trip
// parse→serialize→parse yields the same function name.
func toolUseNameFromID(id string) string {
	const prefix = "gemini_call_"
	if len(id) <= len(prefix) || id[:len(prefix)] != prefix {
		return id
	}
	rest := id[len(prefix):]
	// Strip a single trailing "_<digits>" disambiguator if present.
	if i := strings.LastIndex(rest, "_"); i > 0 {
		tail := rest[i+1:]
		if tail != "" {
			allDigits := true
			for _, r := range tail {
				if r < '0' || r > '9' {
					allDigits = false
					break
				}
			}
			if allDigits {
				return rest[:i]
			}
		}
	}
	return rest
}

// buildGeminiTools converts IR Tools → Gemini functionDeclarations wrapper.
func buildGeminiTools(tools []ToolDefinition) []map[string]any {
	decls := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		decl := map[string]any{
			"name":        t.Name,
			"description": t.Description,
		}
		if t.Parameters != nil {
			decl["parameters"] = t.Parameters
		}
		decls = append(decls, decl)
	}
	return []map[string]any{
		{"functionDeclarations": decls},
	}
}

// buildGeminiToolConfig converts IR ToolChoice → Gemini functionCallingConfig.
func buildGeminiToolChoice(tc *ToolChoice) map[string]any {
	if tc == nil {
		return nil
	}
	cfg := map[string]any{}
	switch tc.Type {
	case "auto":
		cfg["mode"] = "AUTO"
	case "any", "required":
		cfg["mode"] = "ANY"
	case "none":
		cfg["mode"] = "NONE"
	case "tool":
		cfg["mode"] = "ANY"
		if tc.Name != "" {
			cfg["allowedFunctionNames"] = []string{tc.Name}
		}
	default:
		cfg["mode"] = "AUTO"
	}
	return cfg
}

// buildGeminiToolConfig wraps ToolChoice in the full toolConfig structure.
func buildGeminiToolConfig(tc *ToolChoice) map[string]any {
	inner := buildGeminiToolChoice(tc)
	if inner == nil {
		return nil
	}
	return map[string]any{
		"functionCallingConfig": inner,
	}
}

// buildGeminiGenerationConfig merges IR sampling params into Gemini's
// generationConfig block.
func buildGeminiGenerationConfig(req *InternalRequest) map[string]any {
	gc := map[string]any{}
	hasAny := false

	if req.Temperature != nil {
		gc["temperature"] = *req.Temperature
		hasAny = true
	}
	if req.TopP != nil {
		gc["topP"] = *req.TopP
		hasAny = true
	}
	if req.TopK != nil {
		gc["topK"] = *req.TopK
		hasAny = true
	}
	if req.MaxTokens > 0 {
		gc["maxOutputTokens"] = req.MaxTokens
		hasAny = true
	}
	if len(req.Stop) > 0 {
		gc["stopSequences"] = req.Stop
		hasAny = true
	}
	if req.Seed != nil {
		gc["seed"] = *req.Seed
		hasAny = true
	}
	if req.N > 0 {
		gc["candidateCount"] = req.N
		hasAny = true
	}
	// 2026-09-05 audit A-#3: penalty params were parsed into IR but never
	// serialized back, silently dropping them on the Gemini round trip.
	if req.PresencePenalty != nil {
		gc["presencePenalty"] = *req.PresencePenalty
		hasAny = true
	}
	if req.FrequencyPenalty != nil {
		gc["frequencyPenalty"] = *req.FrequencyPenalty
		hasAny = true
	}

	// Map response_format → responseMimeType/Schema
	if req.ResponseFormat != nil {
		switch req.ResponseFormat.Type {
		case "json_object":
			gc["responseMimeType"] = "application/json"
			hasAny = true
		case "json_schema":
			gc["responseMimeType"] = "application/json"
			if req.ResponseFormat.Schema != nil {
				gc["responseSchema"] = req.ResponseFormat.Schema
			}
			hasAny = true
		}
	}

	// Map ReasoningConfig → Gemini thinkingConfig.
	//
	// 2026-08-11: Extended to support effort-only configs (fills the gap where
	// an effort-only ReasoningConfig from an OpenAI client targeting Gemini
	// was previously silently dropped because BudgetTokens was nil).
	if req.Reasoning != nil {
		var budgetTokens *int
		includeThoughts := true
		disableThinking := false

		// Explicit budget takes priority.
		if req.Reasoning.BudgetTokens != nil {
			budgetTokens = req.Reasoning.BudgetTokens
		} else if req.Reasoning.Effort != "" {
			// Convert effort → budget using canonical table.
			b, ok := reasonnormEffortToBudget(req.Reasoning.Effort)
			if ok && b > 0 {
				budgetTokens = &b
			} else if ok && b == 0 {
				// effort=="none" maps to budget 0. On Gemini 2.5+ the
				// absence of thinkingConfig leaves the model at its default
				// (dynamic thinking ENABLED), so "none" must explicitly emit
				// the disable sentinel (thinkingBudget=0) below rather than
				// silently omitting the block.
				disableThinking = true
			}
		}

		if req.Reasoning.Type == "disabled" || disableThinking {
			// Gemini sentinel: thinkingBudget=0 means disabled.
			zero := 0
			gc["thinkingConfig"] = map[string]any{
				"thinkingBudget": zero,
			}
			hasAny = true
		} else if budgetTokens != nil {
			tc := map[string]any{
				"thinkingBudget": *budgetTokens,
			}
			// includeThoughts is always true here; the flag exists for
			// future per-dialect overrides.
			tc["includeThoughts"] = includeThoughts
			gc["thinkingConfig"] = tc
			hasAny = true
		}
	}

	if !hasAny {
		return nil
	}
	return gc
}
