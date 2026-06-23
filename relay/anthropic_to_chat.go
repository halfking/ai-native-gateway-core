package relay

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// ConvertAnthropicResponseToChat converts an Anthropic Messages
// response (non-stream) into OpenAI Chat Completions response.
// Used for Q3 (openai client <- anthropic upstream).
//
// Enhanced (2026-06-20): thinking blocks are now preserved in the
// reasoning_content field (OpenAI o1-style extended thinking support).
// The _kxg_meta field records statistics for audit/telemetry.
func ConvertAnthropicResponseToChat(in []byte, clientModel string) ([]byte, error) {
	var src struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Role    string `json:"role"`
		Model   string `json:"model"`
		Content []struct {
			Type      string         `json:"type"`
			Text      string         `json:"text"`
			ID        string         `json:"id"`
			Name      string         `json:"name"`
			Input     map[string]any `json:"input"`
			Thinking  string         `json:"thinking"`
			Signature string         `json:"signature"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(in, &src); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	outModel := src.Model
	if clientModel != "" {
		outModel = clientModel
	}
	var textParts []string
	var toolCalls []map[string]any
	var thinkingParts []string
	thinkingBlocks := 0
	for _, c := range src.Content {
		switch c.Type {
		case "text":
			if c.Text != "" {
				textParts = append(textParts, c.Text)
			}
		case "tool_use":
			argsJSON, err := json.Marshal(c.Input)
			if err != nil {
				// Skip this tool_use block; partial conversion is safer
				// than aborting the entire response.
				slog.Warn("tool_use_marshal_failed",
					"error", err,
					"tool_use_id", c.ID,
					"tool_name", c.Name,
					"model", src.Model,
					"message_id", src.ID)
				continue
			}
			toolCalls = append(toolCalls, map[string]any{
				"id":   c.ID,
				"type": "function",
				"function": map[string]any{
					"name":      c.Name,
					"arguments": string(argsJSON),
				},
			})
		case "thinking":
			thinkingBlocks++
			if c.Thinking != "" {
				thinkingParts = append(thinkingParts, c.Thinking)
			}
		default:
			// Handle unknown content types by attempting to extract text or thinking fields
			slog.Warn("unknown_content_type_in_anthropic_response",
				"type", c.Type,
				"model", src.Model,
				"has_text", c.Text != "",
				"has_thinking", c.Thinking != "",
				"message_id", src.ID)

			// Try to extract text field first
			if c.Text != "" {
				textParts = append(textParts, c.Text)
			} else if c.Thinking != "" {
				// Or thinking field as fallback
				thinkingParts = append(thinkingParts, c.Thinking)
			}
		}
	}
	msg := map[string]any{"role": "assistant"}
	if len(textParts) > 0 {
		msg["content"] = joinTextParts(textParts)
	} else if len(toolCalls) > 0 {
		msg["content"] = nil
	} else {
		msg["content"] = ""
	}

	// Preserve thinking blocks in reasoning_content field (OpenAI o1-style)
	if len(thinkingParts) > 0 {
		msg["reasoning_content"] = joinTextParts(thinkingParts)
	}

	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}

	// Detect empty responses (all content blocks were unknown types or empty)
	if len(textParts) == 0 && len(toolCalls) == 0 && len(thinkingParts) == 0 {
		slog.Error("empty_response_after_anthropic_to_openai_conversion",
			"model", src.Model,
			"message_id", src.ID,
			"content_blocks_count", len(src.Content),
			"input_tokens", src.Usage.InputTokens,
			"output_tokens", src.Usage.OutputTokens,
			"stop_reason", src.StopReason)
		return nil, fmt.Errorf("empty response from model %s: %d content blocks produced no extractable text/tool/thinking content",
			src.Model, len(src.Content))
	}

	finishReason := mapAnthropicFinishReasonToChat(src.StopReason)
	totalTokens := src.Usage.InputTokens + src.Usage.OutputTokens
	out := map[string]any{
		"id":      src.ID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   outModel,
		"choices": []map[string]any{{
			"index":         0,
			"message":       msg,
			"finish_reason": finishReason,
		}},
		"usage": map[string]any{
			"prompt_tokens":     src.Usage.InputTokens,
			"completion_tokens": src.Usage.OutputTokens,
			"total_tokens":      totalTokens,
		},
	}

	// Record thinking block statistics in metadata
	if thinkingBlocks > 0 {
		reasoningContent, _ := msg["reasoning_content"].(string)
		out["_kxg_meta"] = map[string]any{
			"has_thinking":            true,
			"thinking_blocks_count":   thinkingBlocks,
			"reasoning_content_chars": len(reasoningContent),
		}
	}

	result, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("marshal chat response: %w", err)
	}
	return result, nil
}

func joinTextParts(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "\n"
		}
		out += p
	}
	return out
}

func mapAnthropicFinishReasonToChat(reason string) string {
	switch reason {
	case "end_turn":
		return "stop"
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	case "refusal":
		return "content_filter"
	default:
		return "stop"
	}
}
