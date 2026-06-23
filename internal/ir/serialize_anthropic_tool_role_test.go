package ir

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSerializeAnthropic_ToolRoleConversion verifies that OpenAI tool role messages
// are correctly converted to Anthropic user+tool_result format.
func TestSerializeAnthropic_ToolRoleConversion(t *testing.T) {
	input := `{
		"model": "gpt-4",
		"messages": [
			{
				"role": "assistant",
				"tool_calls": [{
					"id": "call_xyz",
					"type": "function",
					"function": {
						"name": "get_weather",
						"arguments": "{\"city\":\"SF\"}"
					}
				}]
			},
			{
				"role": "tool",
				"tool_call_id": "call_xyz",
				"content": "Sunny, 72°F"
			}
		]
	}`

	ir, err := ParseOpenAI([]byte(input))
	require.NoError(t, err)

	out, err := SerializeAnthropic(ir)
	require.NoError(t, err)

	var result map[string]any
	err = json.Unmarshal(out, &result)
	require.NoError(t, err)

	messages := result["messages"].([]any)
	require.Equal(t, 2, len(messages), "OpenAI tool result should be converted to Anthropic user+tool_result format")

	// The tool message should be converted to user role with tool_result content
	toolResultMsg := messages[1].(map[string]any)
	assert.Equal(t, "user", toolResultMsg["role"], "OpenAI tool role must be converted to user role for Anthropic")

	content := toolResultMsg["content"].([]any)
	require.Equal(t, 1, len(content), "Should have one tool_result block")

	toolResult := content[0].(map[string]any)
	assert.Equal(t, "tool_result", toolResult["type"])
	assert.Equal(t, "call_xyz", toolResult["tool_use_id"])
	assert.Equal(t, "Sunny, 72°F", toolResult["content"])
}
