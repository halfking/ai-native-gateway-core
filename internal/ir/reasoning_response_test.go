package ir

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReasoningContent_OpenAIRoundTrip(t *testing.T) {
	response, err := ParseOpenAIResponse([]byte(`{
		"id":"vendor-response",
		"model":"deepseek-reasoner",
		"choices":[{
			"message":{"role":"assistant","content":"answer","reasoning_content":"private reasoning"},
			"finish_reason":"stop"
		}]
	}`))
	require.NoError(t, err)

	serialized, err := SerializeOpenAIResponse(response, "deepseek-reasoner")
	require.NoError(t, err)

	var payload struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(serialized, &payload))
	require.Len(t, payload.Choices, 1)
	assert.Equal(t, "answer", payload.Choices[0].Message.Content)
	assert.Equal(t, "private reasoning", payload.Choices[0].Message.ReasoningContent)
}

func TestSerializeAnthropicResponse_PreservesSignedThinking(t *testing.T) {
	response := &InternalResponse{
		ID:    "msg-signed",
		Model: "claude-test",
		Role:  "assistant",
		Content: []ResponseContentBlock{
			{Type: "thinking", Thinking: "verified thinking", Signature: "sig_abc"},
			{Type: "text", Text: "answer"},
		},
	}

	serialized, err := SerializeAnthropicResponse(response, "claude-test")
	require.NoError(t, err)

	var payload struct {
		Content []struct {
			Type      string `json:"type"`
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
			Text      string `json:"text"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(serialized, &payload))
	require.Len(t, payload.Content, 2)
	assert.Equal(t, "thinking", payload.Content[0].Type)
	assert.Equal(t, "verified thinking", payload.Content[0].Thinking)
	assert.Equal(t, "sig_abc", payload.Content[0].Signature)
	assert.Equal(t, "text", payload.Content[1].Type)
	assert.Equal(t, "answer", payload.Content[1].Text)
}

func TestSerializeAnthropicResponse_DoesNotForgeThinkingForVendorReasoning(t *testing.T) {
	response := &InternalResponse{
		ID:               "vendor-response",
		Model:            "qwen-reasoning",
		Role:             "assistant",
		ReasoningContent: "vendor reasoning without Anthropic signature",
		Content:          []ResponseContentBlock{{Type: "text", Text: "answer"}},
	}

	serialized, err := SerializeAnthropicResponse(response, "claude-test")
	require.NoError(t, err)

	var payload struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(serialized, &payload))
	require.Len(t, payload.Content, 1)
	assert.Equal(t, "text", payload.Content[0].Type)
	assert.Equal(t, "answer", payload.Content[0].Text)
}
