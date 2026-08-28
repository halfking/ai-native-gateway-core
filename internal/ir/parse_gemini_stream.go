package ir

import (
	"encoding/base64"
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

	// StreamChunk represents exactly one candidate. Reject multi-candidate
	// Gemini chunks explicitly rather than silently discarding candidates[1:].
	if len(raw.Candidates) > 1 {
		return nil, fmt.Errorf("gemini stream chunk contains %d candidates; StreamChunk supports one candidate", len(raw.Candidates))
	}
	if len(raw.Candidates) == 0 {
		return chunk, nil
	}

	cand := raw.Candidates[0]
	chunk.CandidateIndex = cand.Index

	// Usage can arrive alongside a final candidate delta. StreamChunk can carry
	// both, so retain the candidate instead of dropping its terminal content.
	if chunk.Type != ChunkTypeUsage {
		chunk.Type = ChunkTypeDelta
	}
	chunk.Delta = &StreamDelta{}
	var audioData []byte
	for _, p := range cand.Content.Parts {
		switch {
		case p.Thought != "":
			// Multiple parts from one candidate belong to the same delta.
			chunk.Delta.ReasoningContent += p.Thought
		case p.Text != "":
			chunk.Delta.Content += p.Text
		case len(p.FunctionCall) > 0 && string(p.FunctionCall) != "null":
			var fc struct {
				Name string          `json:"name"`
				Args json.RawMessage `json:"args"`
			}
			if err := json.Unmarshal(p.FunctionCall, &fc); err != nil {
				return nil, fmt.Errorf("unmarshal gemini functionCall: %w", err)
			}
			// Gemini may send an argument continuation before the function name.
			// Preserve it as a tool delta rather than dropping it.
			toolCall := StreamToolCallDelta{
				Index:     cand.Index,
				Type:      "function",
				Name:      fc.Name,
				Arguments: string(fc.Args),
			}
			if fc.Name != "" {
				toolCall.ID = "gemini_call_" + fc.Name
			}
			chunk.Delta.ToolCalls = append(chunk.Delta.ToolCalls, toolCall)
		case len(p.InlineData) > 0 && string(p.InlineData) != "null":
			var inlineData struct {
				MIMEType string `json:"mimeType"`
				Data     string `json:"data"`
			}
			if err := json.Unmarshal(p.InlineData, &inlineData); err != nil {
				return nil, fmt.Errorf("unmarshal gemini inlineData: %w", err)
			}
			if !strings.HasPrefix(inlineData.MIMEType, "audio/") {
				return nil, fmt.Errorf("gemini stream inlineData MIME type %q is not representable as audio", inlineData.MIMEType)
			}
			if chunk.Delta.AudioDelta != nil && chunk.Delta.AudioDelta.MIMEType != inlineData.MIMEType {
				return nil, fmt.Errorf("gemini stream contains mixed audio MIME types %q and %q", chunk.Delta.AudioDelta.MIMEType, inlineData.MIMEType)
			}
			decoded, err := base64.StdEncoding.DecodeString(inlineData.Data)
			if err != nil {
				return nil, fmt.Errorf("decode gemini inlineData audio: %w", err)
			}
			if chunk.Delta.AudioDelta == nil {
				chunk.Delta.AudioDelta = &StreamAudioDelta{MIMEType: inlineData.MIMEType}
			}
			audioData = append(audioData, decoded...)
		}
	}
	if chunk.Delta.AudioDelta != nil {
		chunk.Delta.AudioDelta.Data = base64.StdEncoding.EncodeToString(audioData)
	}
	chunk.Delta.DeltaType = geminiDeltaType(chunk.Delta)
	if cand.FinishReason != "" {
		chunk.FinishReason = mapGeminiFinishReason(cand.FinishReason)
	}

	return chunk, nil
}

func geminiDeltaType(delta *StreamDelta) string {
	switch {
	case delta == nil:
		return ""
	case delta.Content != "":
		return "text"
	case delta.ReasoningContent != "":
		return "reasoning"
	case len(delta.ToolCalls) > 0:
		return "tool_call"
	case delta.AudioDelta != nil:
		return "audio"
	default:
		return ""
	}
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
