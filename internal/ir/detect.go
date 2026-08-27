package ir

import (
	"encoding/json"
	"fmt"
)

// DetectProtocol examines a request body and determines which protocol it belongs to.
// Returns the protocol name and a confidence score (0.0-1.0).
//
// Supported protocols:
//   - ProtocolOpenAIChat        ("openai-chat")        — Chat Completions API
//   - ProtocolAnthropicMessages ("anthropic-messages") — Anthropic Messages API
//   - ProtocolGeminiGenerate    ("gemini-generate")    — Gemini generateContent API
//   - ProtocolOpenAIResponses   ("openai-responses")   — OpenAI Responses API
//
// audit-gemini-detect (2026-07-13): Adds Gemini detection alongside the
// existing OpenAI/Anthropic scoring. Gemini-exclusive fields (contents,
// systemInstruction, generationConfig, safetySettings, tools[].functionDeclarations,
// toolConfig, cachedContent) act as decisive signals.
//
// Detection priority:
//
//  1. If `contents` is present → Gemini (Gemini's authoritative array;
//     OpenAI/Anthropic use `messages`).
//  2. If `systemInstruction` is present → Gemini (Anthropic uses `system`).
//  3. If ≥2 Gemini-exclusive fields appear → Gemini.
//  4. If a Responses-exclusive field appears → OpenAI Responses.
//  5. If ≥2 Anthropic-exclusive fields appear → Anthropic.
//  6. Body-shape scores (messages[] vs system/thinking/etc.) determine the winner.
//  7. Model name hint resolves truly empty bodies.
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

	// ── Gemini-exclusive field detection (audit-gemini-detect) ────────
	//
	// Gemini 独占：contents / systemInstruction / generationConfig / safetySettings
	//              toolConfig / cachedContent / tools[].functionDeclarations
	geminiExclusive := 0
	if _, ok := keys["contents"]; ok {
		geminiExclusive++
	}
	if _, ok := keys["systemInstruction"]; ok {
		geminiExclusive++
	}
	if _, ok := keys["generationConfig"]; ok {
		geminiExclusive++
	}
	if _, ok := keys["safetySettings"]; ok {
		geminiExclusive++
	}
	if _, ok := keys["toolConfig"]; ok {
		geminiExclusive++
	}
	if _, ok := keys["cachedContent"]; ok {
		geminiExclusive++
	}
	if tools, ok := keys["tools"].([]any); ok && len(tools) > 0 {
		if _, ok := tools[0].(map[string]any)["functionDeclarations"]; ok {
			geminiExclusive++
		}
	}

	// Quick wins: contents or systemInstruction alone is decisive Gemini signal
	if _, ok := keys["contents"]; ok {
		return ProtocolGeminiGenerate, maxF(0.85+float64(geminiExclusive-1)*0.03, 0.85), nil
	}
	if _, ok := keys["systemInstruction"]; ok {
		return ProtocolGeminiGenerate, maxF(0.85+float64(geminiExclusive-1)*0.03, 0.85), nil
	}

	// Strong Gemini signal: 2+ exclusive fields
	if geminiExclusive >= 2 {
		return ProtocolGeminiGenerate, maxF(0.75+float64(geminiExclusive-2)*0.05, 0.75), nil
	}

	// Responses-exclusive fields must be handled before the Anthropic/OpenAI
	// heuristic: Responses clients use input[] instead of messages[], and their
	// body otherwise has several fields shared with Chat Completions.
	for _, key := range []string{
		"input", "instructions", "previous_response_id", "max_output_tokens",
		"truncation", "prompt_cache_key", "safety_identifier",
	} {
		if _, ok := keys[key]; ok {
			return ProtocolOpenAIResponses, 0.85, nil
		}
	}

	// ── Anthropic-exclusive field detection ────────────────────────
	anthropicExclusive := 0
	for _, k := range []string{"system", "thinking", "cache_control",
		"documents", "top_k", "stop_sequences"} {
		if _, ok := keys[k]; ok {
			anthropicExclusive++
		}
	}
	if meta, ok := keys["metadata"].(map[string]any); ok {
		if _, ok := meta["user_id"]; ok {
			anthropicExclusive++
		}
	}
	if tools, ok := keys["tools"].([]any); ok && len(tools) > 0 {
		if _, ok := tools[0].(map[string]any)["input_schema"]; ok {
			anthropicExclusive++
		}
	}

	// Strong Anthropic signal: 2+ exclusive fields
	if anthropicExclusive >= 2 {
		return ProtocolAnthropicMessages, maxF(0.7+float64(anthropicExclusive-2)*0.05, 0.7), nil
	}

	// ── Body-shape scoring for OpenAI vs Anthropic ──────────────────
	var openAIScore, anthropicScore float64

	// OpenAI distinctive fields
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
	if tools, ok := keys["tools"].([]any); ok && len(tools) > 0 {
		if fn, ok := tools[0].(map[string]any)["function"]; ok {
			if _, ok := fn.(map[string]any); ok {
				openAIScore += 0.2
			}
		}
	}
	if tc, ok := keys["tool_choice"].(string); ok {
		if tc == "required" {
			openAIScore += 0.1
		}
	}
	if stop, ok := keys["stop"]; ok {
		if _, ok := stop.([]any); ok {
			openAIScore += 0.05
		}
	}
	if _, ok := keys["user"]; ok {
		openAIScore += 0.1
	}
	if _, ok := keys["max_completion_tokens"]; ok {
		openAIScore += 0.15
	}

	// Anthropic distinctive fields
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
	if meta, ok := keys["metadata"].(map[string]any); ok {
		if _, ok := meta["user_id"]; ok {
			anthropicScore += 0.15
		}
	}
	if tools, ok := keys["tools"].([]any); ok && len(tools) > 0 {
		if _, ok := tools[0].(map[string]any)["input_schema"]; ok {
			anthropicScore += 0.2
		}
		if _, ok := tools[0].(map[string]any)["name"]; ok {
			if _, hasFn := tools[0].(map[string]any)["function"]; !hasFn {
				anthropicScore += 0.1
			}
		}
	}
	if tc, ok := keys["tool_choice"].(map[string]any); ok {
		if tc["type"] == "tool" {
			anthropicScore += 0.1
		}
	}

	// Normalize scores
	openAIScore = openAIScore / 1.6
	anthropicScore = anthropicScore / 2.1

	// Model field hint (tiebreaker for empty bodies)
	var modelHint string
	if model, ok := keys["model"].(string); ok {
		modelLower := toLower(model)
		switch {
		case containsString(modelLower, "claude"):
			modelHint = "anthropic"
		case containsString(modelLower, "gemini"):
			modelHint = "gemini"
		case containsString(modelLower, "gpt"), containsString(modelLower, "chatgpt"):
			modelHint = "openai"
		}
	}

	// Model hint for empty bodies (no body signals)
	if modelHint != "" && openAIScore == 0 && anthropicScore == 0 && geminiExclusive == 0 {
		switch modelHint {
		case "gemini":
			return ProtocolGeminiGenerate, 0.6, nil
		case "anthropic":
			return ProtocolAnthropicMessages, 0.6, nil
		case "openai":
			return ProtocolOpenAIChat, 0.6, nil
		}
	}

	// Single Gemini exclusive field (already filtered ≥2 above)
	if geminiExclusive == 1 && openAIScore == 0 && anthropicScore == 0 {
		return ProtocolGeminiGenerate, 0.7, nil
	}

	// OpenAI vs Anthropic score comparison
	if openAIScore > anthropicScore {
		// Model hint override: openAIScore=0 + anthropic hint → Anthropic
		if modelHint == "anthropic" && openAIScore == 0 && anthropicScore > 0 {
			return ProtocolAnthropicMessages, 0.7, nil
		}
		if openAIScore < 0.15 {
			return "unknown", openAIScore, nil
		}
		return ProtocolOpenAIChat, openAIScore, nil
	} else if anthropicScore > openAIScore {
		// Model hint override: anthropicScore>0 + anthropic hint → Anthropic
		// (covers `{claude, system}` → Anthropic via hint boost)
		if modelHint == "anthropic" && openAIScore == 0 && anthropicScore > 0 {
			return ProtocolAnthropicMessages, 0.7, nil
		}
		if anthropicScore < 0.15 {
			return "unknown", anthropicScore, nil
		}
		return ProtocolAnthropicMessages, anthropicScore, nil
	}

	// Equal scores - use model hint if available
	if modelHint != "" {
		switch modelHint {
		case "gemini":
			return ProtocolGeminiGenerate, 0.6, nil
		case "anthropic":
			return ProtocolAnthropicMessages, 0.6, nil
		case "openai":
			return ProtocolOpenAIChat, 0.6, nil
		}
	}

	// If truly ambiguous, default to OpenAI
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

// DetectProtocolByURL is a helper that uses URL path as a hint when body
// confidence is low.
//
// Supported URL hints:
//   - /v1/chat/completions              → OpenAI Chat Completions
//   - /v1/messages                      → Anthropic Messages
//   - /v1/responses                     → OpenAI Responses
//   - /v1beta/models/{m}:generateContent → Gemini generateContent
//   - /v1/models/{m}:generateContent   → Gemini generateContent
//   - :streamGenerateContent            → Gemini streaming endpoint
//
// audit-gemini-detect (2026-07-13): When body-based detection produces a
// confident verdict (≥ 0.5) it wins; otherwise URL takes precedence. This
// balances body-shape authority with URL-based routing for ambiguous bodies.
func DetectProtocolByURL(body []byte, urlPath string) (protocol string, confidence float64, err error) {
	proto, conf, err := DetectProtocol(body)
	if err != nil {
		// Metadata extraction can run before the request body is buffered.
		// In that case the URL remains authoritative for known gateway routes.
		if len(body) != 0 {
			return "unknown", 0.0, err
		}
		proto, conf = "unknown", 0
	}

	// High-confidence body detection is authoritative
	if conf >= 0.5 && proto != "unknown" {
		return proto, conf, nil
	}

	// Otherwise URL-based detection takes precedence for routing
	if urlPath != "" {
		switch {
		case containsBody(urlPath, "/v1/chat/completions"):
			return ProtocolOpenAIChat, 0.7, nil
		case containsBody(urlPath, "/v1/messages"):
			return ProtocolAnthropicMessages, 0.7, nil
		case containsBody(urlPath, "/v1/responses"):
			return ProtocolOpenAIResponses, 0.7, nil
		case containsBody(urlPath, ":generateContent"),
			containsBody(urlPath, ":streamGenerateContent"):
			return ProtocolGeminiGenerate, 0.7, nil
		case containsBody(urlPath, "/v1beta/models/"),
			containsBody(urlPath, "/v1/models/"):
			if conf < 0.5 {
				return ProtocolGeminiGenerate, 0.6, nil
			}
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

// maxF returns the larger of two float64 values.
func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
