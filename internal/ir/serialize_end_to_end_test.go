package ir

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenAIToolResultToAnthropic tests the reverse: OpenAI tool → Anthropic tool_result.
// This validates end-to-end tool conversation flow.
func TestOpenAIToolResultToAnthropic(t *testing.T) {
	input := `{
		"model": "gpt-4",
		"messages": [
			{
				"role": "assistant",
				"tool_calls": [{
					"id": "call_123",
					"type": "function",
					"function": {
						"name": "get_weather",
						"arguments": "{\"city\":\"SF\"}"
					}
				}]
			},
			{
				"role": "tool",
				"tool_call_id": "call_123",
				"content": "Sunny, 72°F"
			},
			{
				"role": "user",
				"content": "Thanks!"
			}
		]
	}`

	ir, err := ParseOpenAI([]byte(input))
	require.NoError(t, err)
	require.Equal(t, 3, len(ir.Messages))

	// Serialize to Anthropic
	out, err := SerializeAnthropic(ir)
	require.NoError(t, err)

	var result map[string]any
	err = json.Unmarshal(out, &result)
	require.NoError(t, err)

	messages := result["messages"].([]any)
	require.Equal(t, 3, len(messages))

	// First message: assistant with tool_use
	first := messages[0].(map[string]any)
	assert.Equal(t, "assistant", first["role"])

	// Second message: user role with tool_result (this is the fix)
	second := messages[1].(map[string]any)
	assert.Equal(t, "user", second["role"], "OpenAI tool role must be converted to user role for Anthropic")
	secondContent := second["content"].([]any)
	require.Equal(t, 1, len(secondContent))
	toolResult := secondContent[0].(map[string]any)
	assert.Equal(t, "tool_result", toolResult["type"])
	assert.Equal(t, "call_123", toolResult["tool_use_id"])
	assert.Equal(t, "Sunny, 72°F", toolResult["content"])

	// Third: user text
	third := messages[2].(map[string]any)
	assert.Equal(t, "user", third["role"])
	assert.Equal(t, "Thanks!", third["content"])
}

// TestEndToEndAnthropicToOpenAIToolMessage tests the critical P0 fix flow:
// Anthropic user→tool_result → OpenAI tool message (for the relay direction).
func TestEndToEndAnthropicToOpenAIToolMessage(t *testing.T) {
	input := `{
		"model": "claude-3-opus",
		"messages": [
			{
				"role": "user",
				"content": "What's the weather?"
			},
			{
				"role": "assistant",
				"content": [
					{
						"type": "tool_use",
						"id": "toolu_abc",
						"name": "get_weather",
						"input": {"city": "SF"}
					}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "toolu_abc",
						"content": "Sunny, 72°F"
					}
				]
			}
		]
	}`

	ir, err := ParseAnthropic([]byte(input))
	require.NoError(t, err)
	require.Equal(t, 3, len(ir.Messages))

	// Serialize to OpenAI
	out, err := SerializeOpenAI(ir)
	require.NoError(t, err)

	var result map[string]any
	err = json.Unmarshal(out, &result)
	require.NoError(t, err)

	messages := result["messages"].([]any)
	require.Equal(t, 3, len(messages))

	// First: user text
	user1 := messages[0].(map[string]any)
	assert.Equal(t, "user", user1["role"])
	assert.Equal(t, "What's the weather?", user1["content"])

	// Second: assistant with tool_calls
	assistant := messages[1].(map[string]any)
	assert.Equal(t, "assistant", assistant["role"])
	toolCalls := assistant["tool_calls"].([]any)
	require.Equal(t, 1, len(toolCalls))
	tc := toolCalls[0].(map[string]any)
	assert.Equal(t, "toolu_abc", tc["id"])
	fn := tc["function"].(map[string]any)
	assert.Equal(t, "get_weather", fn["name"])

	// Third: tool message (THIS IS THE P0 FIX)
	toolMsg := messages[2].(map[string]any)
	assert.Equal(t, "tool", toolMsg["role"], "Anthropic user+tool_result must convert to OpenAI tool role")
	assert.Equal(t, "toolu_abc", toolMsg["tool_call_id"])
	assert.Equal(t, "Sunny, 72°F", toolMsg["content"])
}

func TestGeminiStructuredToolResultUsesLegacyTextCrossProtocol(t *testing.T) {
	ir := &InternalRequest{Messages: []Message{{
		Role:       "tool",
		ToolCallID: "gemini_call_lookup",
		Content: []ContentBlock{{Type: "tool_result", ToolResult: &ToolResult{
			ToolUseID:      "gemini_call_lookup",
			Content:        []ContentBlock{{Type: "text", Text: `{"status":"ok"}`}},
			GeminiResponse: json.RawMessage(`{"status":"native"}`),
		}}},
	}}}

	openAI, err := SerializeOpenAI(ir)
	require.NoError(t, err)
	var openAIResult struct {
		Messages []struct {
			Role       string `json:"role"`
			ToolCallID string `json:"tool_call_id"`
			Content    string `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(openAI, &openAIResult))
	require.Len(t, openAIResult.Messages, 1)
	assert.Equal(t, "tool", openAIResult.Messages[0].Role)
	assert.Equal(t, "gemini_call_lookup", openAIResult.Messages[0].ToolCallID)
	assert.Equal(t, `{"status":"ok"}`, openAIResult.Messages[0].Content)

	anthropic, err := SerializeAnthropic(ir)
	require.NoError(t, err)
	var anthropicResult struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type      string `json:"type"`
				ToolUseID string `json:"tool_use_id"`
				Content   string `json:"content"`
			} `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(anthropic, &anthropicResult))
	require.Len(t, anthropicResult.Messages, 1)
	assert.Equal(t, "user", anthropicResult.Messages[0].Role)
	require.Len(t, anthropicResult.Messages[0].Content, 1)
	assert.Equal(t, "tool_result", anthropicResult.Messages[0].Content[0].Type)
	assert.Equal(t, "gemini_call_lookup", anthropicResult.Messages[0].Content[0].ToolUseID)
	assert.Equal(t, `{"status":"ok"}`, anthropicResult.Messages[0].Content[0].Content)
}
