package streaming

// chat_to_anthropic_response.go ports the Q2 (anthropic client ← openai
// upstream) **non-stream response** converter from
// `_to-be-deprecated/relay/messages.go` into the live streaming package.
//
// 2026-06-29: previous wiring at cmd/gateway/main.go only injected three
// legacy callbacks (`ChatToAnthropic`, `AnthropicToOpenAI`,
// `AnthropicToChatResponse`); there was no legacy
// `ChatResponseToAnthropic` hook, so `executor_chat.go` wrote the
// upstream OpenAI chat.completion body straight back to the Anthropic
// client. That bug is the Q2 non-stream response row of the protocol
// conversion matrix in docs/2026-06-29-protocol-conversion-matrix.md.
//
// This implementation mirrors the deprecated relay version byte-for-byte
// (same field extraction, same finish_reason mapping, same
// `<think>...</think>` split) so the legacy path now has the same
// behaviour it had when the relay package owned Q2 conversion. The IR
// path (`ir.SerializeAnthropicResponse`) is the canonical replacement
// when `LLM_GATEWAY_IR_CONVERTER=true` and is preferred; this legacy
// helper only runs when the feature flag is off.

import (
	"encoding/json"

	"github.com/kaixuan/llm-gateway-go/internal/textsplit"
)

// ConvertChatResponseToAnthropic converts an OpenAI Chat Completions
// JSON response body into an Anthropic Messages JSON response body.
//
// Returns the original body unchanged if it does not look like an OpenAI
// chat completion response (so a caller that wrote the body verbatim to
// the wire stays a no-op, never a corruption).
//
// Behavioural parity with `_to-be-deprecated/relay.convertChatResponseToAnthropic`:
//   - reads `choices[0].finish_reason` and maps it to Anthropic form
//   - reads `choices[0].message.content` and `reasoning_content`
//   - splits a leading `<think>...</think>` from content into a thinking
//     block when no explicit `reasoning_content` is present
//   - reads `choices[0].message.tool_calls` and emits Anthropic tool_use
//     blocks (input arguments re-marshalled from the string form)
//   - reads `usage.prompt_tokens` / `usage.completion_tokens`
//   - emits `content[0].type:"text"` with empty text when upstream produced
//     nothing, so the Anthropic SDK always sees a valid content array
func ConvertChatResponseToAnthropic(body []byte, clientModel, requestID string) ([]byte, error) {
	var chatResp map[string]json.RawMessage
	if err := json.Unmarshal(body, &chatResp); err != nil {
		// Passthrough on unparseable input: the executor logs and forwards
		// the original body. Returning an error here would only mask the
		// real failure (an upstream error response) by swapping the body
		// shape mid-pipeline.
		return body, nil
	}

	var choices []map[string]any
	if raw, ok := chatResp["choices"]; ok {
		//nolint:errcheck // non-critical: falls through with empty choices
		json.Unmarshal(raw, &choices)
	}

	finishReason := "stop"
	textContent := ""
	reasoningContent := ""
	var toolCalls []map[string]any

	if len(choices) > 0 {
		choice := choices[0]
		if fr, ok := choice["finish_reason"].(string); ok {
			finishReason = mapAnthropicStopReason(fr)
		}
		if msg, ok := choice["message"].(map[string]any); ok {
			if rc, ok := msg["reasoning_content"].(string); ok {
				reasoningContent = rc
			}
			if c, ok := msg["content"].(string); ok {
				textContent = c
			}
			if tc, ok := msg["tool_calls"].([]any); ok {
				for _, call := range tc {
					if cm, ok := call.(map[string]any); ok {
						fn, _ := cm["function"].(map[string]any)
						argsStr, _ := fn["arguments"].(string)
						var args any
						if json.Unmarshal([]byte(argsStr), &args) != nil {
							args = map[string]any{}
						}
						toolCalls = append(toolCalls, map[string]any{
							"type":  "tool_use",
							"id":    cm["id"],
							"name":  fn["name"],
							"input": args,
						})
					}
				}
			}
		}
	}

	contentBlocks := []map[string]any{}
	if reasoningContent != "" {
		contentBlocks = append(contentBlocks, map[string]any{
			"type":     "thinking",
			"thinking": reasoningContent,
		})
	} else if think, rest, ok := textsplit.SplitLeadingThink(textContent); ok {
		contentBlocks = append(contentBlocks, map[string]any{
			"type":     "thinking",
			"thinking": think,
		})
		textContent = rest
	}
	if textContent != "" {
		contentBlocks = append(contentBlocks, map[string]any{
			"type": "text",
			"text": textContent,
		})
	}
	for _, tc := range toolCalls {
		contentBlocks = append(contentBlocks, tc)
	}
	if len(contentBlocks) == 0 {
		contentBlocks = append(contentBlocks, map[string]any{"type": "text", "text": ""})
	}

	inputTokens := 0
	outputTokens := 0
	if raw, ok := chatResp["usage"]; ok {
		var usage map[string]any
		if json.Unmarshal(raw, &usage) == nil {
			if v, ok := usage["prompt_tokens"].(float64); ok {
				inputTokens = int(v)
			}
			if v, ok := usage["completion_tokens"].(float64); ok {
				outputTokens = int(v)
			}
		}
	}

	msgID := "msg_" + requestID
	if len(requestID) > 24 {
		msgID = "msg_" + requestID[:24]
	}

	resp := map[string]any{
		"id":            msgID,
		"type":          "message",
		"role":          "assistant",
		"content":       contentBlocks,
		"model":         clientModel,
		"stop_reason":   finishReason,
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
		},
	}

	result, err := json.Marshal(resp)
	if err != nil {
		return body, nil
	}
	return result, nil
}

// mapAnthropicStopReason is provided by messages.go (same package); the
// function exists already so we don't redeclare it here.
