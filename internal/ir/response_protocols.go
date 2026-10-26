package ir

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ParseGeminiResponse parses a Gemini generateContent response into IR.
func ParseGeminiResponse(body []byte) (*InternalResponse, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("empty gemini response body")
	}

	var src struct {
		Candidates []struct {
			Content struct {
				Role  string `json:"role"`
				Parts []struct {
					Text         string          `json:"text"`
					Thought      json.RawMessage `json:"thought"` // thinking marker (bool on the real wire, legacy string tolerated)
					FunctionCall json.RawMessage `json:"functionCall"`
					// Audit R20 (2026-09-13): keep enough of the other known
					// part shapes to recognize them as "known-unsupported"
					// rather than fully unknown (inlineData carries media the
					// gateway does not synthesize into text).
					InlineData       json.RawMessage `json:"inlineData"`
					ThoughtSignature string          `json:"thoughtSignature"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
			// ★ 2026-10-27（Gemini 转换面补全轮）：厂方文档要求 IR 捕获
			// 引用元数据（docs/vendor-formats/gemini.md §特殊字段）。
			// ★ Gemini 的字段名是 `uri`（**不是** `url`）⇒ 归一时改名。
			CitationMetadata struct {
				Citations []struct {
					StartIndex int    `json:"startIndex"`
					EndIndex   int    `json:"endIndex"`
					URI        string `json:"uri"`
					Title      string `json:"title"`
				} `json:"citations"`
			} `json:"citationMetadata"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount        int `json:"promptTokenCount"`
			CandidatesTokenCount    int `json:"candidatesTokenCount"`
			TotalTokenCount         int `json:"totalTokenCount"`
			CachedContentTokenCount int `json:"cachedContentTokenCount"`
			ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
		} `json:"usageMetadata"`
		ModelVersion string `json:"modelVersion"`
	}
	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("unmarshal gemini response: %w", err)
	}

	resp := &InternalResponse{
		Model:          src.ModelVersion,
		SourceProtocol: ProtocolGeminiGenerate,
		Usage: ResponseUsage{
			PromptTokens:     src.UsageMetadata.PromptTokenCount,
			CompletionTokens: src.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      src.UsageMetadata.TotalTokenCount,
		},
	}
	// 2026-10-27 缓存标准对齐轮：与流式解析（parse_gemini_stream.go）对齐 ——
	// 非流式此前把 cachedContentTokenCount 整个丢弃，隐式缓存命中进不了 IR。
	if src.UsageMetadata.CachedContentTokenCount > 0 {
		v := src.UsageMetadata.CachedContentTokenCount
		resp.Usage.CacheReadTokens = &v
	}
	// 同一次对齐：thoughtsTokenCount → ReasoningTokens（流式侧同款映射），
	// 序列化侧经 thoughtsTokenCount 回到 Gemini 客户端。
	if src.UsageMetadata.ThoughtsTokenCount > 0 {
		v := src.UsageMetadata.ThoughtsTokenCount
		resp.Usage.ReasoningTokens = &v
	}
	if len(src.Candidates) == 0 {
		return resp, nil
	}

	candidate := src.Candidates[0]
	resp.Role = candidate.Content.Role
	if resp.Role == "model" {
		resp.Role = "assistant"
	}
	// R69: native value before the lossy mapping (RECITATION → content_filter),
	// mirroring parse_gemini_stream.go's streaming counterpart — lets
	// SerializeGeminiResponse hand Gemini clients back their own finishReason.
	resp.StopReason = candidate.FinishReason
	resp.FinishReason = mapGeminiFinishReason(candidate.FinishReason)
	// ★ 2026-10-27（Gemini 转换面补全轮）：捕获引用元数据（厂方文档要求
	// IR 捕获 ⇒ 可在序列化为 OpenAI 时映射为 annotations）。
	// ★ 无 URL 的引用没有意义（客户端点不动）⇒ 丢弃并计数，不进 IR。
	for _, c := range candidate.CitationMetadata.Citations {
		if strings.TrimSpace(c.URI) == "" {
			continue
		}
		resp.Citations = append(resp.Citations, ResponseCitation{
			URL:        c.URI,
			Title:      c.Title,
			StartIndex: c.StartIndex,
			EndIndex:   c.EndIndex,
		})
	}
	for index, part := range candidate.Content.Parts {
		isThought, legacyThoughtText := geminiThoughtMarker(part.Thought)
		switch {
		case isThought:
			// R34: real wire thought parts are `{"text": ..., "thought":
			// true}` — a string decode of `true` used to fail the whole
			// response parse. Legacy string-only form still tolerated.
			thoughtText := part.Text
			if thoughtText == "" {
				thoughtText = legacyThoughtText
			}
			if thoughtText != "" {
				resp.Content = append(resp.Content, ResponseContentBlock{Type: "thinking", Thinking: thoughtText})
				resp.ReasoningContent += thoughtText
			}
		case part.Text != "":
			resp.Content = append(resp.Content, ResponseContentBlock{Type: "text", Text: part.Text})
		case len(part.FunctionCall) > 0 && string(part.FunctionCall) != "null":
			var call struct {
				Name string          `json:"name"`
				Args json.RawMessage `json:"args"`
			}
			if err := json.Unmarshal(part.FunctionCall, &call); err != nil {
				return nil, fmt.Errorf("parse gemini candidate functionCall[%d]: %w", index, err)
			}
			if call.Name == "" {
				return nil, fmt.Errorf("parse gemini candidate functionCall[%d]: missing name", index)
			}
			// R34: unify the synthesized id shape with the request-side
			// parsers ("gemini_call_<name>_<idx>"). The old "<idx>_<name>"
			// shape round-tripped through openai/anthropic clients into a
			// second-turn functionResponse.name of "<idx>_<name>" — not the
			// real function name — breaking the tool loop on the next turn.
			id := fmt.Sprintf("gemini_call_%s_%d", call.Name, index)
			resp.Content = append(resp.Content, ResponseContentBlock{Type: "tool_use", ID: id, Name: call.Name, Input: call.Args})
			resp.ToolCalls = append(resp.ToolCalls, ResponseToolCall{ID: id, Name: call.Name, Arguments: string(call.Args), InputRaw: append(json.RawMessage(nil), call.Args...)})
		case len(part.InlineData) > 0 && string(part.InlineData) != "null":
			// Known-unsupported media part: keep the loss visible without
			// fabricating text from binary payloads.
			resp.recordUnknownBlockType("inlineData")
		case part.ThoughtSignature != "":
			// Signature-only part: a known Gemini shape that rides next to
			// thought/functionCall parts — nothing to synthesize, not a loss.
		default:
			// Gemini parts have no explicit type discriminator — an all-empty
			// decode means a part shape this parser does not know
			// (executableCode, videoMetadata, ...). Record it so the
			// response is attributed as unsupported, never as empty.
			resp.recordUnknownBlockType("gemini_part_unrecognized")
		}
	}
	return resp, nil
}

// ParseResponsesResponse parses an OpenAI Responses API response into IR.
func ParseResponsesResponse(body []byte) (*InternalResponse, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("empty responses response body")
	}

	var src struct {
		ID                string `json:"id"`
		CreatedAt         int64  `json:"created_at"`
		Model             string `json:"model"`
		Status            string `json:"status"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Output []struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Role      string          `json:"role"`
			Name      string          `json:"name"`
			CallID    string          `json:"call_id"`
			Arguments string          `json:"arguments"`
			Content   json.RawMessage `json:"content"`
			Summary   json.RawMessage `json:"summary"`
		} `json:"output"`
		Usage struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			TotalTokens        int `json:"total_tokens"`
			InputTokensDetails struct {
				CachedTokens     int `json:"cached_tokens"`
				CacheWriteTokens int `json:"cache_write_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("unmarshal responses response: %w", err)
	}
	if src.Status == "failed" {
		return nil, fmt.Errorf("responses response failed")
	}

	resp := &InternalResponse{
		ID:             src.ID,
		Model:          src.Model,
		Created:        src.CreatedAt,
		SourceProtocol: ProtocolOpenAIResponses,
		FinishReason:   mapResponsesStatus(src.Status, src.IncompleteDetails.Reason),
		Usage: ResponseUsage{
			PromptTokens:     src.Usage.InputTokens,
			CompletionTokens: src.Usage.OutputTokens,
			TotalTokens:      src.Usage.TotalTokens,
		},
	}
	// 2026-10-27 缓存标准对齐轮：Responses usage 的缓存命中在
	// input_tokens_details 下（与 chat 的 prompt_tokens_details 对位）。
	// 此前非流式解析丢弃它 ⇒ Responses 上游 → chat 客户端的非流式桥
	// （responses_mode_bridge）整段缓存命中不进 IR、不进账、不上屏。
	if src.Usage.InputTokensDetails.CachedTokens > 0 {
		v := src.Usage.InputTokensDetails.CachedTokens
		resp.Usage.CacheReadTokens = &v
	}
	if src.Usage.InputTokensDetails.CacheWriteTokens > 0 {
		v := src.Usage.InputTokensDetails.CacheWriteTokens
		resp.Usage.CacheWriteTokens = &v
	}
	for index, item := range src.Output {
		switch item.Type {
		case "message":
			resp.Role = item.Role
			if err := appendResponsesMessageContent(resp, item.Content); err != nil {
				return nil, fmt.Errorf("parse responses output[%d] message: %w", index, err)
			}
		case "function_call":
			if item.Name == "" || item.CallID == "" {
				return nil, fmt.Errorf("parse responses output[%d] function_call: missing name or call_id", index)
			}
			arguments := json.RawMessage(item.Arguments)
			resp.Content = append(resp.Content, ResponseContentBlock{Type: "tool_use", ID: item.CallID, Name: item.Name, Input: arguments})
			resp.ToolCalls = append(resp.ToolCalls, ResponseToolCall{ID: item.CallID, Name: item.Name, Arguments: item.Arguments, InputRaw: arguments})
		case "reasoning":
			text, err := responsesSummaryText(item.Summary)
			if err != nil {
				return nil, fmt.Errorf("parse responses output[%d] reasoning: %w", index, err)
			}
			if text != "" {
				resp.Content = append(resp.Content, ResponseContentBlock{Type: "thinking", Thinking: text})
				resp.ReasoningContent += text
			}
		default:
			// Audit R20 (2026-09-13): web_search_call / mcp_call /
			// file_search_call / ... were silently dropped before, so an
			// unknown-only Responses payload collapsed into an empty-looking
			// parse. Record the item type so OnlyUnsupportedBlocks can
			// attribute the response as unsupported instead of empty.
			resp.recordUnknownBlockType(item.Type)
		}
	}
	return resp, nil
}

func appendResponsesMessageContent(resp *InternalResponse, raw json.RawMessage) error {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return err
	}
	for _, block := range blocks {
		if block.Type == "output_text" || block.Type == "text" {
			resp.Content = append(resp.Content, ResponseContentBlock{Type: "text", Text: block.Text})
		} else {
			// Audit R20 (2026-09-13): non-text message content parts were
			// silently dropped; record them for unsupported-response
			// attribution (parity with the output-item switch above).
			resp.recordUnknownBlockType(block.Type)
		}
	}
	return nil
}

func responsesSummaryText(raw json.RawMessage) (string, error) {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", err
	}
	text := ""
	for _, block := range blocks {
		if block.Type == "summary_text" {
			text += block.Text
		}
	}
	return text, nil
}

func mapResponsesStatus(status, incompleteReason string) string {
	if status == "incomplete" {
		if incompleteReason == "content_filter" {
			return "content_filter"
		}
		return "length"
	}
	return "stop"
}
