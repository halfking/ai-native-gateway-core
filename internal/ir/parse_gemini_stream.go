package ir

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ParseGeminiStreamChunk parses a Gemini streamGenerateContent SSE line into StreamChunk IR.
//
// Gemini SSE format (one JSON object per `data:` line):
//
//	data: {"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"},
//	                     "finishReason":"STOP","index":0}],
//	        "usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},
//	        "modelVersion":"gemini-2.5-pro"}
//
//	data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"get_weather",
//	                                                    "args":{"city":"Beijing"}}}],"role":"model"},
//	                     "finishReason":"STOP","index":0}],
//	        "usageMetadata":{...}}
//
//	data: {"candidates":[{"content":{"parts":[{"thought":"thinking..."}],"role":"model"},...}]}
//
//	data: [DONE]
//
// audit-gemini-stream (2026-07-13): Completes the Gemini native adapter by
// adding stream support (previously only non-stream).
func ParseGeminiStreamChunk(line string) (*StreamChunk, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, fmt.Errorf("empty line")
	}

	// Extract "data:" prefix (Gemini uses "data:" without trailing space,
	// unlike OpenAI's "data: ". We accept both forms.)
	payload := line
	if strings.HasPrefix(payload, "data: ") {
		payload = strings.TrimPrefix(payload, "data: ")
		payload = strings.TrimSpace(payload)
	} else if strings.HasPrefix(payload, "data:") {
		payload = strings.TrimPrefix(payload, "data:")
		payload = strings.TrimSpace(payload)
	} else {
		return nil, fmt.Errorf("not a data line: %s", line)
	}

	// Handle [DONE] sentinel
	if payload == "[DONE]" {
		return &StreamChunk{
			Type:           ChunkTypeDone,
			SourceProtocol: ProtocolGeminiGenerate,
		}, nil
	}

	// Parse JSON
	var raw struct {
		Candidates []struct {
			Index   int `json:"index"`
			Content struct {
				Role  string `json:"role"`
				Parts []struct {
					Text         string          `json:"text"`
					Thought      string          `json:"thought"` // Gemini 2.5+ thinking
					FunctionCall json.RawMessage `json:"functionCall"`
					InlineData   json.RawMessage `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata *struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`

			// Detailed usage (Gemini 2.0+ multimodal billing)
			PromptTokensDetails []struct {
				Modality   string `json:"modality"` // "TEXT" | "IMAGE" | "AUDIO"
				TokenCount int    `json:"tokenCount"`
			} `json:"promptTokensDetails"`
			CompletionTokensDetails []struct {
				Modality   string `json:"modality"`
				TokenCount int    `json:"tokenCount"`
			} `json:"completionTokensDetails"`

			// Cached content token accounting (Gemini context caching)
			CachedContentTokenCount int `json:"cachedContentTokenCount"`

			// Reasoning tokens (Gemini 2.5+ thinking)
			ThoughtsTokenCount int `json:"thoughtsTokenCount"`
		} `json:"usageMetadata"`
		ModelVersion string `json:"modelVersion"`
	}

	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return nil, fmt.Errorf("unmarshal gemini chunk: %w", err)
	}

	chunk := &StreamChunk{
		SourceProtocol: ProtocolGeminiGenerate,
		Model:          raw.ModelVersion,
	}

	// Parse usage (Gemini emits usage at the end-of-stream chunk)
	if raw.UsageMetadata != nil {
		chunk.Type = ChunkTypeUsage
		chunk.Usage = &StreamUsage{
			PromptTokens:     raw.UsageMetadata.PromptTokenCount,
			CompletionTokens: raw.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      raw.UsageMetadata.TotalTokenCount,
		}

		// audit-gemini-stream (2026-07-13): detailed usage with modality breakdowns
		for _, d := range raw.UsageMetadata.PromptTokensDetails {
			switch d.Modality {
			case "IMAGE":
				v := d.TokenCount
				chunk.Usage.ImageTokens = &v
			case "AUDIO":
				v := d.TokenCount
				chunk.Usage.AudioTokens = &v
			}
		}
		for _, d := range raw.UsageMetadata.CompletionTokensDetails {
			if d.Modality == "AUDIO" {
				v := d.TokenCount
				if chunk.Usage.AudioTokens == nil {
					chunk.Usage.AudioTokens = &v
				} else {
					total := *chunk.Usage.AudioTokens + v
					chunk.Usage.AudioTokens = &total
				}
			}
		}
		if raw.UsageMetadata.CachedContentTokenCount > 0 {
			v := raw.UsageMetadata.CachedContentTokenCount
			chunk.Usage.CacheReadTokens = &v
		}
		if raw.UsageMetadata.ThoughtsTokenCount > 0 {
			v := raw.UsageMetadata.ThoughtsTokenCount
			chunk.Usage.ReasoningTokens = &v
		}
	}

	// Parse candidate content
	if len(raw.Candidates) > 0 {
		cand := raw.Candidates[0]

		// If we already set usage, this is a usage chunk; still capture finishReason
		if chunk.Type == ChunkTypeUsage {
			if cand.FinishReason != "" {
				chunk.FinishReason = mapGeminiFinishReason(cand.FinishReason)
			}
			return chunk, nil
		}

		// Otherwise this is a delta chunk
		chunk.Type = ChunkTypeDelta

		for _, p := range cand.Content.Parts {
			switch {
			case p.Thought != "":
				// Gemini 2.5+ reasoning delta
				if chunk.Delta == nil {
					chunk.Delta = &StreamDelta{}
				}
				chunk.Delta.ReasoningContent = p.Thought
				chunk.Delta.DeltaType = "reasoning"
			case p.Text != "":
				if chunk.Delta == nil {
					chunk.Delta = &StreamDelta{}
				}
				chunk.Delta.Content = p.Text
				chunk.Delta.DeltaType = "text"
			case len(p.FunctionCall) > 0 && string(p.FunctionCall) != "null":
				// Streamed functionCall
				var fc struct {
					Name string          `json:"name"`
					Args json.RawMessage `json:"args"`
				}
				if err := json.Unmarshal(p.FunctionCall, &fc); err == nil && fc.Name != "" {
					if chunk.Delta == nil {
						chunk.Delta = &StreamDelta{}
					}
					chunk.Delta.ToolCalls = []StreamToolCallDelta{{
						Index:     cand.Index,
						ID:        "gemini_call_" + fc.Name,
						Type:      "function",
						Name:      fc.Name,
						Arguments: string(fc.Args),
					}}
					chunk.Delta.DeltaType = "tool_call"
				}
			}
		}

		if cand.FinishReason != "" {
			chunk.FinishReason = mapGeminiFinishReason(cand.FinishReason)
		}
	}

	return chunk, nil
}

// mapGeminiFinishReason converts Gemini finishReason strings to the IR's
// OpenAI-form finish reasons for unified reporting.
//
// Gemini finish reasons:
//   - "STOP"            → model finished naturally
//   - "MAX_TOKENS"      → output token limit hit
//   - "SAFETY"          → blocked by safety filter
//   - "RECITATION"      → recitation block
//   - "BLOCKLIST"       → blocklist hit
//   - "PROHIBITED_CONTENT" → prohibited content
//   - "SPII"           → sensitive personally identifiable info
//   - "MALFORMED_FUNCTION_CALL" → bad function call
//   - "OTHER"           → other
func mapGeminiFinishReason(reason string) string {
	switch reason {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		return "content_filter"
	case "MALFORMED_FUNCTION_CALL":
		return "tool_calls"
	default:
		return "stop"
	}
}
