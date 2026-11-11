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
		Model           string          `json:"model"`
		Instructions    string          `json:"instructions"`
		Input           json.RawMessage `json:"input"`
		MaxOutputTokens *int            `json:"max_output_tokens"`
		// 2026-11-12（批 5 迁移语义对齐）：max_completion_tokens 是 chat 线的
		// 同义别名（o1+ 现代形态）。此前 parse_responses 不消费 ⇒ 落 Extensions
		// 原样透传，与 parse_openai（消费进 MaxTokens → 规范化为 max_tokens）
		// 不对称。客户端意图 = 输出上限，别名应归一。
		MaxCompletionTokens *int            `json:"max_completion_tokens,omitempty"`
		Temperature         *float64        `json:"temperature,omitempty"`
		TopP                *float64        `json:"top_p,omitempty"`
		Stop                json.RawMessage `json:"stop,omitempty"`
		Stream              *bool           `json:"stream,omitempty"`
		Tools               json.RawMessage `json:"tools,omitempty"`
		ToolChoice          json.RawMessage `json:"tool_choice,omitempty"`
		ParallelToolCalls   *bool           `json:"parallel_tool_calls,omitempty"`
		PreviousResponseID  string          `json:"previous_response_id,omitempty"`
		User                string          `json:"user,omitempty"`
		Metadata            json.RawMessage `json:"metadata,omitempty"`
		Store               *bool           `json:"store,omitempty"`
		Truncation          string          `json:"truncation,omitempty"`
		PromptCacheKey      string          `json:"prompt_cache_key,omitempty"`
		ServiceTier         string          `json:"service_tier,omitempty"`
		SafetyIdentifier    string          `json:"safety_identifier,omitempty"`
		Reasoning           json.RawMessage `json:"reasoning,omitempty"`
		Text                json.RawMessage `json:"text,omitempty"` // {format:{type,...}}

		// 2026-11-12（批 5 迁移语义对齐 · 第二批）：OpenAI chat 族参数在
		// responses wire 上到达时（客户端 SDK 混发 / 中继转发）此前不消费
		// ⇒ 落 Extensions ⇒ 经 restoreExtensions **原样**写进目标 body ——
		// 对 anthropic 等严格上游是整单 400（upstream_bytes golden 实测抓到
		// response_format）。消费集与 parse_openai 对齐：意图进 IR 结构化
		// 字段，由各序列化器统一裁决（发射 / 披露丢弃），不再裸透传。
		ResponseFormat    json.RawMessage `json:"response_format,omitempty"`
		ReasoningEffort   string          `json:"reasoning_effort,omitempty"`
		FrequencyPenalty  *float64        `json:"frequency_penalty,omitempty"`
		PresencePenalty   *float64        `json:"presence_penalty,omitempty"`
		LogProbs          *bool           `json:"logprobs,omitempty"`
		TopLogProbs       *int            `json:"top_logprobs,omitempty"`
		Seed              *int64          `json:"seed,omitempty"`
		N                 *int            `json:"n,omitempty"`
		LogitBias         json.RawMessage `json:"logit_bias,omitempty"`
		Prediction        json.RawMessage `json:"prediction,omitempty"`
		Verbosity         string          `json:"verbosity,omitempty"`
		WebSearchOptions  json.RawMessage `json:"web_search_options,omitempty"`
		Modalities        json.RawMessage `json:"modalities,omitempty"`
		Audio             json.RawMessage `json:"audio,omitempty"`
		RepetitionPenalty *float64        `json:"repetition_penalty,omitempty"`
	}
	// 2026-11-11 客户端错误修正：数值/布尔参数被客户端写成字符串时，
	// 先做一次**无歧义**的容错修正再解析（详见 coerce_numeric.go）。
	if fixed, changed := coerceNumericStrings(body, &src); changed {
		body = fixed
		// Phase 1 的 rawMap 也要同步（Extensions 取值基于它）。
		if err := json.Unmarshal(fixed, &rawMap); err != nil {
			return nil, fmt.Errorf("unmarshal responses body to map: %w", err)
		}
	}

	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("unmarshal responses body: %w", err)
	}

	// Phase 3: capture unknown top-level fields into Extensions + anomaly.
	knownFields := map[string]bool{
		"model": true, "instructions": true, "input": true,
		"max_output_tokens": true, "max_completion_tokens": true, "temperature": true, "top_p": true,
		"stop": true, "stream": true, "tools": true, "tool_choice": true,
		"parallel_tool_calls": true, "previous_response_id": true,
		"user": true, "metadata": true, "store": true, "truncation": true,
		"prompt_cache_key": true, "service_tier": true, "safety_identifier": true,
		"reasoning": true, "text": true,
		// 2026-11-12（批 5）：chat 族参数消费集对齐（见 src struct 注释）。
		"response_format": true, "reasoning_effort": true,
		"frequency_penalty": true, "presence_penalty": true,
		"logprobs": true, "top_logprobs": true, "seed": true, "n": true,
		"logit_bias": true, "prediction": true, "verbosity": true,
		"web_search_options": true, "modalities": true, "audio": true,
		"repetition_penalty": true,
	}
	extensions := make(map[string]json.RawMessage)
	for key, val := range rawMap {
		if !knownFields[key] && len(val) > 0 && string(val) != "null" {
			extensions[key] = val
			ReportParseUnknownField("unknown", ProtocolOpenAIResponses, key, nil)
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
	// 别名归一：max_completion_tokens 与 max_output_tokens 同义；显式
	// max_output_tokens 优先（两个都发时取 Responses 线的原生名）。
	if src.MaxCompletionTokens != nil && src.MaxOutputTokens == nil {
		req.MaxTokens = *src.MaxCompletionTokens
	}
	// 2026-11-12（批 5）：chat 族参数 → IR 结构化字段（映射与 parse_openai
	// 同源；parseOpenAIResponseFormat 直接复用）。
	if src.ResponseFormat != nil && string(src.ResponseFormat) != "null" {
		rf, err := parseOpenAIResponseFormat(src.ResponseFormat)
		if err != nil {
			return nil, fmt.Errorf("parse response_format: %w", err)
		}
		req.ResponseFormat = rf
	}
	if src.ReasoningEffort != "" {
		// reasoning_effort（chat 形）与 reasoning 对象（responses 形）同义；
		// 对象显式优先（与 serialize 层 Thinking > Reasoning 的先序一致）。
		if req.Reasoning == nil {
			req.Reasoning = &ReasoningConfig{Effort: src.ReasoningEffort}
		}
	}
	req.FrequencyPenalty = src.FrequencyPenalty
	req.PresencePenalty = src.PresencePenalty
	req.Logprobs = src.LogProbs
	req.TopLogprobs = src.TopLogProbs
	req.Seed = src.Seed
	if src.N != nil {
		req.N = *src.N
	}
	if src.LogitBias != nil && string(src.LogitBias) != "null" {
		var lb map[string]float64
		if err := json.Unmarshal(src.LogitBias, &lb); err == nil {
			req.LogitBias = lb
		} else {
			reportClientShapeFix(ProtocolOpenAIResponses, "logit_bias", "unmarshal_failed_dropped",
				map[string]any{"err": err.Error()})
		}
	}
	if src.Prediction != nil && string(src.Prediction) != "null" {
		var p Prediction
		if err := json.Unmarshal(src.Prediction, &p); err == nil {
			req.Prediction = &p
		}
	}
	req.Verbosity = src.Verbosity
	if src.WebSearchOptions != nil && string(src.WebSearchOptions) != "null" {
		var wso WebSearchOptions
		if err := json.Unmarshal(src.WebSearchOptions, &wso); err == nil {
			req.WebSearchOptions = &wso
		}
	}
	if src.Modalities != nil && string(src.Modalities) != "null" {
		var mods []string
		if err := json.Unmarshal(src.Modalities, &mods); err == nil {
			req.Modalities = mods
		}
	}
	if src.Audio != nil && string(src.Audio) != "null" {
		var ac AudioConfig
		if err := json.Unmarshal(src.Audio, &ac); err == nil {
			req.AudioConfig = &ac
		}
	}
	req.RepetitionPenalty = src.RepetitionPenalty
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
			// r1030: one assistant turn in the Responses wire format is a
			// sequence of output items — [message, function_call, ...] — and
			// spec-conforming clients (AI-SDK/ZCode) replay them adjacently
			// in input[]. A function_call that directly follows an assistant
			// message belongs to that same turn: merge it into the preceding
			// message instead of emitting two adjacent assistant messages.
			// MiniMax-M3 stopped issuing tool calls entirely when the chat
			// wire carried the turn split in two; the merged form is the
			// canonical chat-history shape every chat-dialect upstream
			// expects.
			if msg.Role == "assistant" && len(msg.ToolCalls) > 0 && len(msg.Content) == 0 &&
				len(messages) > 0 {
				prev := &messages[len(messages)-1]
				// Consecutive function_call items (parallel calls of one
				// turn) keep appending to the same assistant message; a
				// function_call after a tool result or a user message is its
				// own turn and stays unmerged.
				if prev.Role == "assistant" {
					prev.ToolCalls = append(prev.ToolCalls, msg.ToolCalls...)
					continue
				}
			}
			messages = append(messages, *msg)
		}
	}
	return messages, systemText, nil
}

// redactReasoningItem 去掉 reasoning 信封里**不可外泄**的内容，只留描述性字段。
//
// ★ 为什么这样切（★ 2026-11-11 审计 P2-I）：★ 诊断只需要知道「这是一个 reasoning item」
// ★ ★（★ type / id / 有无 summary / 有无 encrypted_content）★ ★ ⇒ ★★ 而
// ★★ `summary` 是**模型的思考文本**、★ ★ `encrypted_content` 是**不透明加密推理块**
// ★★ ⇒ ★★ 两者都**不得**进日志/落库面 ⇒ ★★ 逐字保留即等于外泄。
// ★★ 保留「有没有」而不保留「是什么」⇒ ★★ 诊断信息不丢、★ ★ 敏感内容不外泄。
func redactReasoningItem(item map[string]any) map[string]any {
	out := map[string]any{"type": item["type"]}
	if id, ok := item["id"]; ok {
		if s, ok := id.(string); ok {
			out["id"] = s
		}
	}
	if _, ok := item["summary"]; ok {
		out["has_summary"] = true
	}
	if _, ok := item["encrypted_content"]; ok {
		out["has_encrypted_content"] = true
	}
	return out
}

// parseResponsesInputItem converts a single input[] item into an IR Message.
// Returns (msg, inlineSystem, err): inlineSystem is set for system/developer
// roles so the caller can hoist them into req.System; msg is nil for those.
func parseResponsesInputItem(item map[string]any) (*Message, string, error) {
	role, _ := item["role"].(string)
	itemType, _ := item["type"].(string)

	// 2026-11-09 (claude-opus-5-5 empty_model_response incident on 211):
	// Responses clients (AI-SDK / ZCode) replay reasoning items from previous
	// turns inside input[]: {"type":"reasoning","id":"rs_...","summary":[...],
	// "encrypted_content":...}. These carry no role and used to fall into the
	// unknown-item fallback below, becoming an assistant message with a raw
	// content block; the Anthropic serializer's default branch then forwarded
	// the item VERBATIM onto the Messages wire, where {"type":"reasoning"} is
	// not a legal content-block type → upstream 400 on every turn whose
	// history contained reasoning (apiclaude "Malformed conversation content").
	// Drop the item here with an anomaly record: Anthropic reasoning has its
	// own thinking mechanism (signature-gated), and replaying foreign reasoning
	// envelopes is never wire-legal on any anthropic-messages upstream.
	if itemType == "reasoning" {
		// ★ 2026-11-11 审计 P2-I：★ **不要把整个 reasoning 信封塞进 metadata**
		// ★★ 原实现传 `item` 整体 ⇒ ★★ `summary`（★ 模型的思考文本）与
		// ★★ `encrypted_content`（★ 不透明加密推理块）会**逐字**进 anomaly 事件
		// ★★ ⇒ ★★ 随 slog.Warn("ir.anomaly") 落进**可全文检索**的日志面
		// ★★ ⇒ ★★ 也随 format-anomaly 采集落库（★ domains/streaming/anomaly_harvester.go）
		// ★★ ⇒ ★★ 这是**思考内容外泄**面，且对「这是不是一个 reasoning item」这个诊断**零增益**。
		// ⇒ ★★ 修法：★ 只留**不可逆推内容**的描述性字段（★ type / id / 有无 summary / 有无 encrypted_content）。
		// ★★ 去重键不含 metadata（★ anomaly_reporter.go dedupKey）⇒ ★★ 脱敏**不改变**去重行为。
		ReportParseUnknownField("unknown", ProtocolOpenAIResponses, "input[].type=reasoning (dropped before IR; not wire-legal on anthropic-messages)", redactReasoningItem(item))
		return nil, "", nil
	}

	// Responses tool-call history items do not carry a message role. Handle
	// them before the generic type-item fallback so tool semantics survive the
	// conversion into the shared IR.
	if itemType == "function_call" {
		name, _ := item["name"].(string)
		args, _ := item["arguments"].(string)
		// 2026-11-11 审计第二轮（批 2）：空串→"{}"（既有行为，改走统一
		// helper 使其可观测）；非法 JSON 披露。
		args = coerceToolCallArguments(args, ProtocolOpenAIResponses)
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
			// 2026-11-11 审计第二轮（批 2）：message 型 item 缺 role 的兜底
			// 保留（既有行为），补披露使其可观测。
			reportClientShapeFix(ProtocolOpenAIResponses, "input[].role", "inferred_user",
				map[string]any{"inferred": "user", "item_type": itemType})
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
		// 2026-11-11 审计第二轮（批 2）：同上，统一 helper。
		args = coerceToolCallArguments(args, ProtocolOpenAIResponses)
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
	for bi, block := range blocks {
		blockMap, ok := block.(map[string]any)
		if !ok {
			// 2026-11-11 审计第二轮（批 2）：非对象块此前静默跳过。
			reportSkippedElement(ProtocolOpenAIResponses, "input[].content[]", bi, block)
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
			// ★★ 2026-10-27（12h 审计轮）：本分支此前与 parse_openai.go 的
			// ★★ **同款判据错误**，且那条错误在 parse_openai.go 上已经被修过
			// ★★ （见 parse_openai.go 的 input_audio 分支注释）。
			// ★★ 旧判据是「parse 返回 nil 与否」⇒ ★★ 只带 format 不带 data
			// ★★ 时 `parseOpenAIInputAudioBlock` 返回**非 nil**（Data=""、
			// ★★ Format="mp3"）⇒ ★★ 于是**不走** raw 兜底 ⇒ ★★ 块留在
			// ★★ Type="input_audio" 而载荷为空 ⇒ ★★ 被两条线各自的
			// ★★ 「载荷为空就不发」守卫丢掉 ⇒ ★★ **音频静默消失**。
			// ★★ ⇒ 判据必须是「**有没有载荷**」（Data 是否为空），
			// ★★ 且 URI 形态必须走**有类型的载体**（MediaSource），
			// ★★ 让各线按各自正确的形状还原（OpenAI 线 input_audio{url} /
			// ★★ Gemini 线 fileData{fileUri}）。
			ia := parseOpenAIInputAudioBlock(blockMap)
			if ia == nil || ia.Data == "" {
				if src := parseOpenAIAudioURISource(blockMap); src != nil {
					irBlock.Type = "audio"
					irBlock.Audio = src
					irBlock.InputAudio = nil
					break
				}
				// 没有载荷也没有 URI ⇒ 这个块**不可表达** ⇒ 标成 input_audio
				// 且载荷为空 ⇒ 各线的「载荷为空就不发」守卫会把它丢掉。
				// ★ 不可再走 raw 兜底：raw 会被 default 分支**原样回吐**
				// ⇒ 又变回 {"data":"","format":"mp3"} ⇒ 上游必 400。
				irBlock.Type = "input_audio"
				irBlock.InputAudio = ia
				irBlock.RawContent = nil
				break
			}
			irBlock.Type = "input_audio"
			irBlock.InputAudio = ia
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
		// RISK-2（2026-10-29）：Responses flat 形同样收集顶层非标准键。
		if _, leftovers, ok2 := splitToolRaw(item); ok2 {
			td.Extra = toolExtrasFrom(leftovers, irToolKnownKeysOpenAI)
		}
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
