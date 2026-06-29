package streaming

// chat_to_anthropic_response_test.go covers the Q2 (anthropic client ←
// openai upstream) non-stream response conversion. The pre-fix behaviour
// was to forward the raw OpenAI chat.completion body straight to the
// Anthropic client, which rejected it because Anthropic Messages expects
// `content[]` + `stop_reason` rather than `choices[].message.content`.
//
// These tests pin the converter that closes the gap when the IR feature
// flag is off (`LLM_GATEWAY_IR_CONVERTER`). The IR-path behaviour is
// pinned separately by `internal/ir/response_test.go`.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertChatResponseToAnthropic_TextOnly(t *testing.T) {
	in := []byte(`{
		"id": "chatcmpl-1",
		"object": "chat.completion",
		"created": 1782000000,
		"model": "gpt-4o-mini",
		"choices": [{
			"index": 0,
			"finish_reason": "stop",
			"message": {
				"role": "assistant",
				"content": "Hello, world."
			}
		}],
		"usage": {
			"prompt_tokens": 14,
			"completion_tokens": 3,
			"total_tokens": 17
		}
	}`)

	got, err := ConvertChatResponseToAnthropic(in, "claude-3-5-sonnet", "req-q2-text")
	require.NoError(t, err)

	var msg map[string]any
	require.NoError(t, json.Unmarshal(got, &msg))

	assert.Equal(t, "message", msg["type"])
	assert.Equal(t, "assistant", msg["role"])
	assert.Equal(t, "claude-3-5-sonnet", msg["model"])
	assert.Equal(t, "end_turn", msg["stop_reason"], "OpenAI stop → Anthropic end_turn")
	assert.Nil(t, msg["stop_sequence"])

	content, ok := msg["content"].([]any)
	require.True(t, ok, "Anthropic response must have content[]")
	require.Len(t, content, 1)
	block := content[0].(map[string]any)
	assert.Equal(t, "text", block["type"])
	assert.Equal(t, "Hello, world.", block["text"])

	usage := msg["usage"].(map[string]any)
	assert.Equal(t, float64(14), usage["input_tokens"])
	assert.Equal(t, float64(3), usage["output_tokens"])

	assert.Equal(t, "msg_req-q2-text", msg["id"])
}

func TestConvertChatResponseToAnthropic_ToolCalls(t *testing.T) {
	in := []byte(`{
		"id": "chatcmpl-2",
		"object": "chat.completion",
		"created": 1782000000,
		"model": "gpt-4o-mini",
		"choices": [{
			"index": 0,
			"finish_reason": "tool_calls",
			"message": {
				"role": "assistant",
				"content": null,
				"tool_calls": [{
					"id": "call_abc",
					"type": "function",
					"function": {
						"name": "get_weather",
						"arguments": "{\"city\":\"SF\"}"
					}
				}]
			}
		}],
		"usage": {"prompt_tokens": 8, "completion_tokens": 5, "total_tokens": 13}
	}`)

	got, err := ConvertChatResponseToAnthropic(in, "claude-3-5-sonnet", "req-q2-tools")
	require.NoError(t, err)

	var msg map[string]any
	require.NoError(t, json.Unmarshal(got, &msg))

	assert.Equal(t, "tool_use", msg["stop_reason"], "OpenAI tool_calls → Anthropic tool_use")

	content, ok := msg["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 1)
	block := content[0].(map[string]any)
	assert.Equal(t, "tool_use", block["type"])
	assert.Equal(t, "call_abc", block["id"])
	assert.Equal(t, "get_weather", block["name"])
	// input is unmarshalled back to an object
	input, ok := block["input"].(map[string]any)
	require.True(t, ok, "tool_use input must be a JSON object, got %T", block["input"])
	assert.Equal(t, "SF", input["city"])
}

func TestConvertChatResponseToAnthropic_ReasoningContentAsThinkingBlock(t *testing.T) {
	in := []byte(`{
		"id": "chatcmpl-3",
		"object": "chat.completion",
		"model": "gpt-4o-mini",
		"choices": [{
			"index": 0,
			"finish_reason": "stop",
			"message": {
				"role": "assistant",
				"reasoning_content": "Let me think about this carefully.",
				"content": "Hi there!"
			}
		}],
		"usage": {"prompt_tokens": 4, "completion_tokens": 7, "total_tokens": 11}
	}`)

	got, err := ConvertChatResponseToAnthropic(in, "claude-3-5-sonnet", "req-q2-think")
	require.NoError(t, err)

	var msg map[string]any
	require.NoError(t, json.Unmarshal(got, &msg))

	content, ok := msg["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 2, "reasoning_content must become a thinking block before the text block")
	assert.Equal(t, "thinking", content[0].(map[string]any)["type"])
	assert.Equal(t, "Let me think about this carefully.", content[0].(map[string]any)["thinking"])
	assert.Equal(t, "text", content[1].(map[string]any)["type"])
	assert.Equal(t, "Hi there!", content[1].(map[string]any)["text"])
}

func TestConvertChatResponseToAnthropic_EmbeddedThinkSplit(t *testing.T) {
	in := []byte(`{
		"id": "chatcmpl-4",
		"model": "gpt-4o-mini",
		"choices": [{
			"finish_reason": "stop",
			"message": {
				"role": "assistant",
				"content": "<think>minimax-style trace</think>\n\nHi there!"
			}
		}],
		"usage": {"prompt_tokens": 2, "completion_tokens": 8, "total_tokens": 10}
	}`)

	got, err := ConvertChatResponseToAnthropic(in, "claude-3-5-sonnet", "req-q2-embedded")
	require.NoError(t, err)

	var msg map[string]any
	require.NoError(t, json.Unmarshal(got, &msg))

	content, ok := msg["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 2, "embedded <think> must be split into a thinking block")
	assert.Equal(t, "thinking", content[0].(map[string]any)["type"])
	assert.Equal(t, "minimax-style trace", content[0].(map[string]any)["thinking"])
	assert.Equal(t, "Hi there!", content[1].(map[string]any)["text"])
}

func TestConvertChatResponseToAnthropic_NonOpenAIShapeGetsEmptyAnthropicResponse(t *testing.T) {
	// Unparseable input MUST NOT be corrupted: the helper returns the
	// original bytes so the executor can decide what to do (it currently
	// logs the error and forwards as-is).
	out, err := ConvertChatResponseToAnthropic([]byte(`not-json`), "m", "req")
	require.NoError(t, err)
	assert.Equal(t, []byte(`not-json`), out)

	// JSON but not OpenAI-shaped (no "choices" key) does NOT passthrough:
	// the helper emits a valid Anthropic Messages envelope with an
	// empty text content block. This matches the relay behaviour
	// (`_to-be-deprecated/relay.convertChatResponseToAnthropic`) and
	// keeps the Anthropic SDK from blowing up on a malformed body.
	out, err = ConvertChatResponseToAnthropic([]byte(`{"foo":1}`), "m", "req")
	require.NoError(t, err)
	var msg map[string]any
	require.NoError(t, json.Unmarshal(out, &msg))
	assert.Equal(t, "message", msg["type"])
	assert.Equal(t, "assistant", msg["role"])
	// Default stop_reason when upstream did not supply choices is "stop"
	// (we only map to "end_turn" when we actually saw a finish_reason).
	assert.Equal(t, "stop", msg["stop_reason"])
	content, ok := msg["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 1)
	block := content[0].(map[string]any)
	assert.Equal(t, "text", block["type"])
	assert.Equal(t, "", block["text"])
}

func TestConvertChatResponseToAnthropic_LengthFinishReason(t *testing.T) {
	in := []byte(`{
		"choices": [{
			"finish_reason": "length",
			"message": {"role": "assistant", "content": "partial..."}
		}],
		"usage": {"prompt_tokens": 1, "completion_tokens": 2}
	}`)

	got, err := ConvertChatResponseToAnthropic(in, "m", "req")
	require.NoError(t, err)

	var msg map[string]any
	require.NoError(t, json.Unmarshal(got, &msg))
	assert.Equal(t, "max_tokens", msg["stop_reason"], "OpenAI length → Anthropic max_tokens")
}
