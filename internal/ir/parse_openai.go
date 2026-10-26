package ir

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// ParseOpenAI parses an OpenAI Chat Completions request body into InternalRequest.
//
// Extensions support (P0 fix, 2026-07-13):
// Unknown fields (vendor-specific params like reasoning_effort, web_search,
// etc.) are extracted into IR.Extensions for lossless passthrough.
// R52: repetition_penalty / mask_sensitive_info / bot_setting are promoted to
// first-class IR fields (no longer Extension passthrough); bot_setting falls
// back to Extensions only when the value fails to unmarshal (raw-byte
// fidelity via the extensions_restore dialect guard).
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

		// 2026-09-21 audit (P1-1, P2-3, P2-4): promote previously-Extension-only
		// fields to first-class IR fields so they can be type-checked, normalized,
		// and emitted deterministically.
		RepetitionPenalty *float64        `json:"repetition_penalty,omitempty"`
		MaskSensitiveInfo *bool           `json:"mask_sensitive_info,omitempty"`
		BotSetting        json.RawMessage `json:"bot_setting,omitempty"`

		// Legacy Completions text input (mutually exclusive with `messages`).
		// Promoted into a user message below — see the handling site.
		Prompt string `json:"prompt,omitempty"`
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
		// 2026-09-21 audit: promoted to first-class IR fields.
		"repetition_penalty": true, "mask_sensitive_info": true, "bot_setting": true,
		// Legacy Completions text input. Must be listed here, otherwise it is
		// ALSO captured into Extensions and paramreg then drops it as a
		// DialectResponses-only key — reintroducing exactly the data loss the
		// promotion below exists to prevent.
		"prompt": true,
	}

	extensions := make(map[string]json.RawMessage)
	for key, val := range rawMap {
		if !knownFields[key] && len(val) > 0 {
			extensions[key] = val
			// Step 4.10 (2026-07-28): record unknown-field anomaly once
			// per (request_id, source_protocol, field_path). Dedup is
			// enforced inside ReportUnknownField. Registered paramreg
			// fields (stream_options, thinking, …) go to Extensions only —
			// see ReportParseUnknownField.
			ReportParseUnknownField("unknown", ProtocolOpenAIChat, key, nil)
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
		RepetitionPenalty:  src.RepetitionPenalty,
		MaskSensitiveInfo:  src.MaskSensitiveInfo,
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

	// 2026-09-21 audit (P2-4): parse bot_setting into typed IR fields.
	// R52：畸形输入（非数组/元素非对象）不再静默吞——回退 Extensions 保留
	// 原始字节。键已在 knownFields 剔除名单内，需显式塞回：MiniMax 目标经
	// extensions_restore 的 Decide（IRHandled+Dialects 守卫）按原始字节
	// Restore，非 MiniMax 目标 Drop——与 KindDialectOnly 时代的逐字节透传
	// 保真等价，只是收敛到既有守卫机制。
	if src.BotSetting != nil && string(src.BotSetting) != "null" {
		var bots []BotSetting
		if err := json.Unmarshal(src.BotSetting, &bots); err == nil {
			ir.BotSetting = bots
		} else {
			ir.Extensions["bot_setting"] = src.BotSetting
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

	// Legacy OpenAI Completions carries the input in a top-level `prompt`
	// string instead of `messages`. Without this promotion the field falls
	// through to Extensions, where paramreg classifies `prompt` as a
	// DialectResponses-only key (the reusable prompt-TEMPLATE reference
	// {id,version,variables}) and drops it for an openai-chat source — the
	// request then went upstream with no content at all, silently losing the
	// user's entire input. Same key, two unrelated meanings; the meaning is
	// decided by which fields the body actually carries, not by the key name.
	//
	// `messages` wins when both are present: it is the richer, structured
	// form, and the legacy `prompt` is then a redundant echo. This matches
	// the Completions API itself, where `prompt` is a convenience alias.
	if len(ir.Messages) == 0 && len(src.Prompt) > 0 {
		ir.Messages = []Message{{
			Role:    "user",
			Content: []ContentBlock{{Type: "text", Text: src.Prompt}},
		}}
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
	default:
		// R72 §3.4: content 为 number/object/bool 等非法类型时，过去会静默
		// 落空——消息解析「成功」但内容为空，随后被 validate_and_fix.go 的
		// removeEmptyMessages 整条删除，请求带空会话发上游返 200，客户端
		// JSON 缺陷被转成「模型对空输入作答」。在源头拒为客户端错误（仓库
		// 现成的 *ParseError/KindClientBug 机制），不进 fix 层。
		return nil, &ParseError{
			Kind:    errorsx.KindClientBug,
			Message: fmt.Sprintf("message content must be a string, an array of content blocks, or null; got %s", jsonTypeName(content)),
		}
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
			// 2026-10-26（Gemini fileData 转发修复轮）：URI 引用形态
			// （input_audio.url，由 serializeOpenAIAudioBlock 的 file_uri
			// 分支产出）没有 data/format ⇒ parseOpenAIInputAudioBlock 返回
			// nil ⇒ 本分支此前什么都不留 ⇒ 执行器强制的 Parse→Validate→
			// Serialize 往返（finalizeOpenAIUpstreamBody）把 URI 整个丢掉，
			// 上游收到的是没有音频的请求。
			//
			// ★★ 2026-10-27（12h 审计轮）**实测复现并修掉**：★ 原处置是
			// ★★ 「按『形状已知但无载荷』块的既有惯例原样保留」⇒ 落进
			// ★★ `Type="raw"` + `RawContent=原文` ⇒ ★★ **但只有 Anthropic
			// ★★ 序列化器有 default 分支会把它重新吐出去**
			// ★★（serialize_anthropic.go 的 default ⇒ 还原 RawContent）；
			// ★★ ★★ **serialize_openai / serialize_gemini / serialize_responses /
			// ★★ serialize_ollama 四家都没有 default** ⇒ ★★ ⇒ **块被静默丢弃**。
			// ★★ 实测链（Gemini 客户端 → Gemini 上游）：
			// ★★   IR{type:"audio", FileURI:"gs://bucket/a.mp3"}
			// ★★   → OpenAI 线 {"type":"input_audio","input_audio":{"url":"gs://…"}}
			// ★★   → 往返再解析 ⇒ **type="raw"** ⇒
			// ★★   → Gemini 线 ⇒ **parts 里只剩文本，音频整个消失**
			// ★★ ⇒ 上游拿到的请求**没有音频**，而客户端**收不到任何报错**。
			//
			// ★★ 本轮处置：★ **不再靠 raw 兜底** ⇒ ★ 识别 URI 引用形态，
			// ★★ 用**已有的 MediaSource 载体**（Type="audio"）承载 ⇒ ⇒
			// ★★ 各线格式都能**按各自正确的形状**还原：
			// ★★   · OpenAI 线 ⇒ `input_audio{url}`（往返稳定）
			// ★★   · Gemini 线 ⇒ `fileData{fileUri}`（★ 厂商原生形状，不是 OpenAI 形状）
			// ★★ ★★ **不能**用「raw 原样回吐」来兜 Gemini：★ 那样会把
			// ★★ **OpenAI 形状**的块塞进 Gemini 线上 ⇒ 形状非法 ⇒ 反而更糟。
			//
			// ★★★ 判据是「**有没有载荷**」而不是「parse 返回 nil 与否」：
			// ★★★ `parseOpenAIInputAudioBlock` 只在 data 与 format **都**空时
			// ★★★ 返回 nil；★ **只带 format 不带 data** 时它返回**非 nil**
			// ★★★ （Data=""、Format="mp3"）⇒ ★★★ 若拿「nil 与否」当判据，
			// ★★★ **带 format 的 URI 形态会走错分支** ⇒ ★★★ 音频又丢
			// ★★★ ⇒ ★★★ ⇒ 判据必须是「**Data 为空**」（= 没有 base64 载荷）
			// ★★★ ⇒ 此时载荷在 URI 上 ⇒ 走 MediaSource 承载。
			if ia == nil || ia.Data == "" {
				if src := parseOpenAIAudioURISource(blockMap); src != nil {
					irBlock.Type = "audio"
					irBlock.Audio = src
					irBlock.InputAudio = nil
				} else {
					// ★★ 2026-10-27（12h 审计轮）：此处**不可**再走 raw 兜底。
					// ★★ raw 块会被序列化器的 `default:` 分支**原样回吐**
					// ★★ ⇒ 载荷为空的 input_audio 又变回
					// ★★ {"input_audio":{"data":"","format":"mp3"}}
					// ★★ ⇒ **上游必 400**（这正是本轮要修的形状）。
					// ★★ 而本轮补在 `case "input_audio"` 上的守卫
					// ★★ （`block.InputAudio.Data != ""`）**根本走不到**
					// ★★ —— 因为块已经不是 input_audio 了 ⇒ ★★ 守卫形同虚设。
					// ★★ ⇒ 正确做法：**保留 input_audio 类型、载荷留空**，
					// ★★ 让各线那条「载荷为空就不发」的守卫真正生效（丢弃该块）。
					irBlock.Type = "input_audio"
					irBlock.InputAudio = ia
					irBlock.RawContent = nil
				}
			}
		case "image":
			if img := parseOpenAIImageBlock(blockMap); img != nil && imageSourceHasPayload(img) {
				irBlock.Image = img
			} else {
				irBlock.Type = "raw"
				irBlock.RawContent = mustMarshalString(blockMap)
			}
		case "video_url":
			// 2026-10-27（R148 审计轮）：OpenAI 的 video_url 此前**没有任何
			// 分支** ⇒ 落进 default（Type 保持 "video_url"，RawContent 原样
			// 保留）。后果是「看起来对、其实是空的」：
			//
			//	OpenAI / Anthropic / Responses 三个序列化器各自的 default 分支
			//	都按 RawContent 兜底 ⇒ 块被原样发出去，形状正确；
			//	但 **block.Video 始终为 nil** ⇒ 走类型化分支的 Gemini
			//	序列化器（case "video" → irVideoToGeminiPart）拿不到任何载荷
			//	⇒ **视频块被静默丢弃**（用户看到的是「模型答了，但没看视频」）。
			//
			// ★ 这条路径是 2026-10-26 那轮**打开**的：那一轮把 video 补进
			// 分级模态门的接受臂（provider/client.go 的 candidateQuerySQLBase）
			// ⇒「OpenAI 客户端发 video_url → 路由到 Gemini 模型」从
			// 「503 无候选」变成「有候选、但视频消失」⇒ 那一轮是半修。
			//
			// 修法（**只往保守方向**）：解析成类型化 Video（MediaSource），
			// **同时保留 Type="video_url" 与 RawContent** ——
			//   · OpenAI/Anthropic/Responses 仍旧走各自的 default 分支，
			//     输出逐字节不变（不碰它们 ⇒ 不给这三个协议引入风险）；
			//   · 只有 Gemini 序列化器新增 "video_url" 分支后不再丢块。
			irBlock.RawContent = mustMarshalString(blockMap)
			if media := parseOpenAIMediaBlock(blockMap, "video"); media != nil {
				irBlock.Video = media
			}
		case "audio", "video":
			if media := parseOpenAIMediaBlock(blockMap, blockType); media != nil {
				if blockType == "audio" {
					irBlock.Audio = media
				} else {
					irBlock.Video = media
				}
			} else {
				irBlock.Type = "raw"
				irBlock.RawContent = mustMarshalString(blockMap)
			}
		case "document":
			if doc := parseOpenAIFileBlock(blockMap); doc != nil {
				irBlock.Document = doc
			} else {
				irBlock.Type = "raw"
				irBlock.RawContent = mustMarshalString(blockMap)
			}
		case "file":
			// OpenAI file input via Responses API
			irBlock.Document = parseOpenAIFileBlock(blockMap)
			if irBlock.Document == nil {
				irBlock.Type = "raw"
				irBlock.RawContent = mustMarshalString(blockMap)
			} else {
				irBlock.Type = "document" // Normalize to our internal type
			}
		case "input_file":
			// OpenAI Responses API file input variant
			irBlock.Document = parseOpenAIFileBlock(blockMap)
			if irBlock.Document == nil {
				irBlock.Type = "raw"
				irBlock.RawContent = mustMarshalString(blockMap)
			} else {
				irBlock.Type = "document"
			}
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

func imageSourceHasPayload(img *ImageSource) bool {
	return img != nil && (img.URL != "" || img.Data != "" || img.FileID != "" || img.FileURI != "")
}

func mustMarshalString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func parseOpenAIMediaBlock(block map[string]any, kind string) *MediaSource {
	media := &MediaSource{Kind: kind}
	carrier := block[kind+"_url"]
	if carrier == nil {
		carrier = block[kind]
	}
	if obj, ok := carrier.(map[string]any); ok {
		media.URL, _ = obj["url"].(string)
		media.Data, _ = obj["data"].(string)
		media.FileID, _ = obj["file_id"].(string)
		media.FileURI, _ = obj["file_uri"].(string)
		media.MediaType, _ = obj["mime_type"].(string)
		media.Format, _ = obj["format"].(string)
	} else if s, ok := carrier.(string); ok {
		media.URL = s
	}
	if media.URL != "" {
		media.Type = "url"
	} else if media.Data != "" {
		media.Type = "base64"
	} else if media.FileID != "" {
		media.Type = "file_id"
	} else if media.FileURI != "" {
		media.Type = "file_uri"
	}
	if media.Type == "" {
		return nil
	}
	return media
}

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
			// GAP-1 (Wave5, 2026-09-22): OpenAI 允许 system content 为块数组；
			// 手写转换器（chat_to_anthropic.go chatSystemContent）把全部 text
			// 块按 \n join。旧实现只保留首块，其余块静默丢失（数据丢失）。
			// 非 text 块（IR 侧合法存在）不参与 join，与手写侧报错语义的
			// 差异维持 IR 的宽容解析约定。
			var texts []string
			for _, block := range msg.Content {
				if block.Type == "text" {
					texts = append(texts, block.Text)
				}
			}
			system.Content = strings.Join(texts, "\n")
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

// parseOpenAIAudioURISource extracts the **URI-reference** form of an OpenAI
// audio block and returns it as a MediaSource (Type="audio").
//
// ★★ Why this exists (2026-10-27 · 12h 审计轮 · 实测复现):
// ★★ `parseOpenAIInputAudioBlock` only understands the **base64** form
// ★★ (`input_audio.data` + `input_audio.format`). The URI-reference form
// ★★ (`input_audio.url`, produced by serializeOpenAIAudioBlock's file_uri
// ★★ branch — i.e. by the gateway's own Gemini→OpenAI conversion) therefore
// ★★ parsed to nil and degraded to Type="raw" ⇒ and only the Anthropic
// ★★ serializer re-emits RawContent ⇒ the block was **silently dropped** on
// ★★ the Gemini / Responses / Ollama wires (upstream saw no audio at all).
//
// ★★ Shape handled here:
// ★★   {"type":"input_audio","input_audio":{"url":"https://…"|"gs://…",
// ★★                                   "file_id":"…", "format":"mp3"}}
// ★★ plus the flat variant ({"type":"input_audio","url":…}).
//
// ★★ Returns nil when there is no URI carrier (⇒ caller keeps the raw path).
func parseOpenAIAudioURISource(block map[string]any) *MediaSource {
	inner, _ := block["input_audio"].(map[string]any)
	get := func(key string) string {
		if inner != nil {
			if v, ok := inner[key].(string); ok && v != "" {
				return v
			}
		}
		if v, ok := block[key].(string); ok && v != "" {
			return v
		}
		return ""
	}
	url := get("url")
	fileID := get("file_id")
	format := get("format")
	if url == "" && fileID == "" {
		return nil
	}
	src := &MediaSource{
		Kind:   "audio",
		Format: format,
	}
	if fileID != "" {
		// ★ file_id 形态：★ Gemini 侧无法凭 file_id 还原（没有 Files-API
		// ★ 注册表）⇒ 仍按 file_id 承载，交给**能懂它的**那条线（如 OpenAI）
		// ★ 还原 ⇒ 而不是在这里编造一个 URI。
		src.Type = "file_id"
		src.FileID = fileID
	} else {
		src.Type = "url"
		src.URL = url
	}
	if src.MediaType == "" && format != "" {
		src.MediaType = "audio/" + format
	}
	return src
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

	if ia.Data == "" && ia.Format == "" {
		return nil
	}
	return ia
}

// parseOpenAIFileBlock parses an OpenAI file input block
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
		switch {
		case strings.HasPrefix(fd, "data:"):
			// RFC 2397 data URI: data:[<mediatype>][;base64],<data>
			// base64-encoded payload.
			idx := strings.Index(fd, "base64,")
			if idx >= 0 {
				src.MediaType = strings.TrimSuffix(strings.TrimPrefix(fd[:idx], "data:"), ";")
				src.Data = fd[idx+len("base64,"):]
				src.Type = "base64"
			} else {
				// Non-base64 data URI (e.g. "data:text/plain,hello").
				// Parse "<mediatype>,<payload>" per RFC 2397. Without this
				// branch src.Type stayed "file" and the block was silently
				// dropped by the src.Type=="file" guard below.
				comma := strings.Index(fd, ",")
				if comma >= 0 {
					src.MediaType = strings.TrimPrefix(fd[:comma], "data:")
					src.Data = fd[comma+1:]
				} else {
					src.MediaType = strings.TrimPrefix(fd, "data:")
				}
				src.Type = "text"
			}
		case strings.HasPrefix(fd, "http"):
			src.Type = "url"
			src.URL = fd
		default:
			src.Type = "text"
			src.Data = fd
		}
	}
	if mt, ok := inner["mime_type"].(string); ok && src.MediaType == "" {
		src.MediaType = mt
	}
	if fid, ok := inner["file_id"].(string); ok && fid != "" {
		// A-#18(b): unify the dual-track encoding — carry the id in
		// DocumentSource.FileID, the same field the Anthropic parser writes,
		// so cross-protocol serializers (serialize_anthropic) see a populated
		// FileID instead of an empty source. Data keeps a legacy projection
		// for consumers and session-restored rows that predate the unified
		// field; the convention comes from the upstream parsers
		// (parse_anthropic's source.url → Data projection in
		// parseAnthropicDocumentBlock, and parse_gemini's fileUri handling) —
		// this function's own URL branch writes URL only.
		src.Type = "file_id"
		src.FileID = fid
		src.Data = fid
	}
	if src.Type == "file" {
		return nil
	}
	db.Source = src
	return db
}
