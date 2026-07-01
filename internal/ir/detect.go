package ir

import (
	"encoding/json"
	"fmt"
)

// DetectProtocol examines a request body and determines which protocol it belongs to.
// Returns the protocol name and a confidence score (0.0-1.0).
func DetectProtocol(body []byte) (protocol string, confidence float64, err error) {
	if len(body) == 0 {
		return "unknown", 0.0, fmt.Errorf("empty body")
	}

	var raw json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return "unknown", 0.0, fmt.Errorf("invalid JSON: %w", err)
	}

	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		return "unknown", 0.0, fmt.Errorf("invalid JSON object: %w", err)
	}

	// Score based on field presence
	var openAIScore, anthropicScore float64

	// ── 决定性独占字段检测（2026-07-01 修复）──────────────────────
	// 某些字段是某协议独占的——对方 API 根本不接受。存在这些字段时是
	// 决定性信号，能盖过 messages[]（两协议共有）等中性字段的噪声。
	// 这修复了 documents/top_k/stop_sequences/metadata.user_id 等强
	// Anthropic 信号被 messages[] 的 OpenAI 噪声（0.3 分，归一化 0.1875）
	// 盖过导致的误判。
	anthropicExclusive := 0 // Anthropic 独占字段计数
	for _, k := range []string{"system", "thinking", "cache_control",
		"documents", "top_k", "stop_sequences"} {
		if _, ok := keys[k]; ok {
			anthropicExclusive++
		}
	}
	// metadata.user_id 也是 Anthropic 独占
	if meta, ok := keys["metadata"].(map[string]any); ok {
		if _, ok := meta["user_id"]; ok {
			anthropicExclusive++
		}
	}

	// OpenAI Chat Completions distinctive fields
	if _, ok := keys["messages"]; ok {
		openAIScore += 0.3
	}
	if _, ok := keys["frequency_penalty"]; ok {
		openAIScore += 0.15
	}
	if _, ok := keys["presence_penalty"]; ok {
		openAIScore += 0.15
	}
	if _, ok := keys["logprobs"]; ok {
		openAIScore += 0.1
	}
	if _, ok := keys["top_logprobs"]; ok {
		openAIScore += 0.1
	}
	if _, ok := keys["seed"]; ok {
		openAIScore += 0.1
	}
	if _, ok := keys["response_format"]; ok {
		openAIScore += 0.1
	}
	if _, ok := keys["n"]; ok {
		openAIScore += 0.1
	}
	// tools with "function" wrapper is very OpenAI
	if tools, ok := keys["tools"].([]any); ok && len(tools) > 0 {
		if fn, ok := tools[0].(map[string]any)["function"]; ok {
			if _, ok := fn.(map[string]any); ok {
				openAIScore += 0.2
			}
		}
	}
	// tool_choice string "auto"/"none" is common to both, but "required" is more OpenAI
	if tc, ok := keys["tool_choice"].(string); ok {
		if tc == "required" {
			openAIScore += 0.1
		}
	}
	// stop as array (both support) - neutral
	if stop, ok := keys["stop"]; ok {
		if _, ok := stop.([]any); ok {
			openAIScore += 0.05
		}
	}
	// user field is OpenAI style
	if _, ok := keys["user"]; ok {
		openAIScore += 0.1
	}
	// max_completion_tokens is OpenAI o1 series style
	if _, ok := keys["max_completion_tokens"]; ok {
		openAIScore += 0.15
	}

	// Anthropic Messages API distinctive fields
	if _, ok := keys["system"]; ok {
		anthropicScore += 0.25
	}
	if _, ok := keys["thinking"]; ok {
		anthropicScore += 0.3
	}
	if _, ok := keys["cache_control"]; ok {
		anthropicScore += 0.25
	}
	if _, ok := keys["documents"]; ok {
		anthropicScore += 0.2
	}
	if _, ok := keys["top_k"]; ok {
		anthropicScore += 0.2
	}
	if _, ok := keys["stop_sequences"]; ok {
		anthropicScore += 0.15
	}
	// metadata with user_id is Anthropic style
	if meta, ok := keys["metadata"].(map[string]any); ok {
		if _, ok := meta["user_id"]; ok {
			anthropicScore += 0.15
		}
	}
	// tools without "function" wrapper is Anthropic style
	if tools, ok := keys["tools"].([]any); ok && len(tools) > 0 {
		if fn, ok := tools[0].(map[string]any)["input_schema"]; ok {
			if _, ok := fn.(map[string]any); ok {
				anthropicScore += 0.2
			}
		}
		// tools with "name" at top level is Anthropic style
		if _, ok := tools[0].(map[string]any)["name"]; ok {
			if _, hasFn := tools[0].(map[string]any)["function"]; !hasFn {
				anthropicScore += 0.1
			}
		}
	}
	// tool_choice with "type": "tool" is Anthropic style
	if tc, ok := keys["tool_choice"].(map[string]any); ok {
		if tc["type"] == "tool" {
			anthropicScore += 0.1
		}
	}

	// Normalize scores to 0-1 range
	// Maximum possible scores:
	// OpenAI: 0.3+0.15+0.15+0.1+0.1+0.1+0.1+0.1+0.2+0.1+0.05+0.1+0.15 = 1.6
	// Anthropic: 0.25+0.3+0.25+0.2+0.2+0.15+0.15+0.2+0.2+0.1+0.1 = 2.1

	openAIScore = openAIScore / 1.6 // Normalize
	anthropicScore = anthropicScore / 2.1

	// Check model field for protocol hints (strong signal regardless of body scores)
	var modelHint string
	if model, ok := keys["model"].(string); ok {
		modelLower := toLower(model)
		if containsString(modelLower, "claude") {
			modelHint = "anthropic"
		} else if containsString(modelLower, "gpt") || containsString(modelLower, "chatgpt") {
			modelHint = "openai"
		}
	}

	// If model hint strongly suggests one protocol, use it
	//
	// Phase E (2026-07-01): body shape (messages[] = openai-chat
	// marker) is authoritative. The model hint is a tiebreaker ONLY
	// when the body has signals for one protocol but not the other.
	// When both protocols have body signals, defer to the body-score
	// comparison below — picking anthropic based on model hint would
	// mis-classify Chat Completions bodies that include anthropic
	// extension fields (system, thinking, etc.) and cause the
	// executor to skip the Q3 OpenAI translator.
	//
	// True Anthropic Messages bodies — those with strong anthropic
	// signals AND no messages[] — never reach this code path
	// because they're sent through /v1/messages, not /v1/chat/completions.
	if modelHint != "" {
		if modelHint == "anthropic" && openAIScore == 0 && anthropicScore > 0 {
			return ProtocolAnthropicMessages, 0.7, nil
		}
		if modelHint == "openai" && anthropicScore == 0 && openAIScore > 0 {
			return ProtocolOpenAIChat, 0.7, nil
		}
		// Body is completely empty: model hint is the only signal.
		if openAIScore == 0 && anthropicScore == 0 {
			if modelHint == "anthropic" {
				return ProtocolAnthropicMessages, 0.6, nil
			}
			return ProtocolOpenAIChat, 0.6, nil
		}
	}

	// Determine winner based on body scores
	//
	// 2026-07-01 修复：多个 Anthropic 独占字段同时出现是决定性强信号。
	// 单个字段（如 system）可能是 OpenAI 客户端扩展，但 system+thinking、
	// documents+cache_control 等组合几乎只可能是真正的 Anthropic Messages
	// 请求。阈值 ≥2 区分这两类场景，避免 messages[] 噪声误判。
	if anthropicExclusive >= 2 {
		return ProtocolAnthropicMessages, maxF(anthropicScore, 0.7), nil
	}
	if openAIScore > anthropicScore {
		if openAIScore < 0.15 {
			return "unknown", openAIScore, nil
		}
		return ProtocolOpenAIChat, openAIScore, nil
	} else if anthropicScore > openAIScore {
		if anthropicScore < 0.15 {
			return "unknown", anthropicScore, nil
		}
		return ProtocolAnthropicMessages, anthropicScore, nil
	}

	// Equal scores - use model hint if available
	if modelHint != "" {
		if modelHint == "anthropic" {
			return ProtocolAnthropicMessages, 0.6, nil
		}
		return ProtocolOpenAIChat, 0.6, nil
	}

	// If truly ambiguous, default to OpenAI (more common)
	if openAIScore > 0.1 {
		return ProtocolOpenAIChat, openAIScore, nil
	}

	return "unknown", 0.0, nil
}

func toLower(s string) string {
	result := make([]byte, len(s))
	for i, c := range s {
		if c >= 'A' && c <= 'Z' {
			result[i] = byte(c + 32)
		} else {
			result[i] = byte(c)
		}
	}
	return string(result)
}

// containsString is case-sensitive string contains.
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && indexString(s, substr) >= 0
}

func indexString(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// DetectProtocolByURL is a helper that can use URL path as a hint when body confidence is low.
func DetectProtocolByURL(body []byte, urlPath string) (protocol string, confidence float64, err error) {
	proto, conf, err := DetectProtocol(body)
	if err != nil {
		return "unknown", 0.0, err
	}

	// If confidence is high enough, use body-based detection
	if conf > 0.5 {
		return proto, conf, nil
	}

	// Fallback to URL-based detection for ambiguous cases
	if urlPath != "" {
		if containsBody(urlPath, "/v1/chat/completions") {
			return ProtocolOpenAIChat, 0.7, nil
		}
		if containsBody(urlPath, "/v1/messages") {
			return ProtocolAnthropicMessages, 0.7, nil
		}
	}

	// Return body-based result even if low confidence
	return proto, conf, nil
}

func containsBody(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// maxF 返回两个 float64 中的较大者（用于置信度计算）。
func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
