package ir

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestToolRoleRoundTrip tests that OpenAI tool messages round-trip correctly.
func TestToolRoleRoundTrip(t *testing.T) {
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
			}
		]
	}`

	ir, err := ParseOpenAI([]byte(input))
	require.NoError(t, err)
	require.Equal(t, 2, len(ir.Messages))

	// Verify assistant message with tool_calls
	assert.Equal(t, "assistant", ir.Messages[0].Role)
	assert.Equal(t, 1, len(ir.Messages[0].ToolCalls))
	assert.Equal(t, "call_123", ir.Messages[0].ToolCalls[0].ID)
	assert.Equal(t, "get_weather", ir.Messages[0].ToolCalls[0].Function.Name)

	// Verify tool message
	assert.Equal(t, "tool", ir.Messages[1].Role)
	assert.Equal(t, "call_123", ir.Messages[1].ToolCallID)
	assert.Equal(t, 1, len(ir.Messages[1].Content))
	assert.Equal(t, "text", ir.Messages[1].Content[0].Type)
	assert.Equal(t, "Sunny, 72°F", ir.Messages[1].Content[0].Text)

	// Serialize back to OpenAI
	out, err := SerializeOpenAI(ir)
	require.NoError(t, err)

	var result map[string]any
	err = json.Unmarshal(out, &result)
	require.NoError(t, err)

	messages := result["messages"].([]any)
	require.Equal(t, 2, len(messages))

	// Verify tool message preserved
	toolMsg := messages[1].(map[string]any)
	assert.Equal(t, "tool", toolMsg["role"])
	assert.Equal(t, "call_123", toolMsg["tool_call_id"])
	assert.Equal(t, "Sunny, 72°F", toolMsg["content"])
}

// TestAnthropicToolResultToOpenAITool tests that Anthropic tool_result blocks
// are converted to OpenAI tool messages.
func TestAnthropicToolResultToOpenAITool(t *testing.T) {
	input := `{
		"model": "claude-3-opus",
		"messages": [
			{
				"role": "user",
				"content": [{
					"type": "tool_result",
					"tool_use_id": "toolu_123",
					"content": "Result text"
				}]
			}
		]
	}`

	ir, err := ParseAnthropic([]byte(input))
	require.NoError(t, err)
	require.Equal(t, 1, len(ir.Messages))

	// Verify message has tool_result block
	assert.Equal(t, "user", ir.Messages[0].Role)
	assert.Equal(t, 1, len(ir.Messages[0].Content))
	assert.Equal(t, "tool_result", ir.Messages[0].Content[0].Type)
	assert.NotNil(t, ir.Messages[0].Content[0].ToolResult)
	assert.Equal(t, "toolu_123", ir.Messages[0].Content[0].ToolResult.ToolUseID)

	// Serialize to OpenAI
	out, err := SerializeOpenAI(ir)
	require.NoError(t, err)

	var result map[string]any
	err = json.Unmarshal(out, &result)
	require.NoError(t, err)

	messages := result["messages"].([]any)
	require.Equal(t, 1, len(messages))

	// Anthropic's user message with tool_result should become OpenAI tool message
	toolMsg := messages[0].(map[string]any)
	assert.Equal(t, "tool", toolMsg["role"])
	assert.Equal(t, "toolu_123", toolMsg["tool_call_id"])
	assert.Equal(t, "Result text", toolMsg["content"])
}

// TestAnthropicToolResultMultipleBlocks tests tool results with multiple content blocks.
func TestAnthropicToolResultMultipleBlocks(t *testing.T) {
	input := `{
		"model": "claude-3-opus",
		"messages": [
			{
				"role": "user",
				"content": [{
					"type": "tool_result",
					"tool_use_id": "toolu_456",
					"content": [
						{"type": "text", "text": "Part 1"},
						{"type": "text", "text": "Part 2"}
					]
				}]
			}
		]
	}`

	ir, err := ParseAnthropic([]byte(input))
	require.NoError(t, err)

	// Serialize to OpenAI
	out, err := SerializeOpenAI(ir)
	require.NoError(t, err)

	var result map[string]any
	err = json.Unmarshal(out, &result)
	require.NoError(t, err)

	messages := result["messages"].([]any)
	toolMsg := messages[0].(map[string]any)

	assert.Equal(t, "tool", toolMsg["role"])
	assert.Equal(t, "toolu_456", toolMsg["tool_call_id"])
	assert.Equal(t, "Part 1\nPart 2", toolMsg["content"])
}

// TestToolMessageWithName tests that tool messages preserve optional name field.
func TestToolMessageWithName(t *testing.T) {
	input := `{
		"model": "gpt-4",
		"messages": [
			{
				"role": "tool",
				"tool_call_id": "call_789",
				"name": "get_weather",
				"content": "Weather data"
			}
		]
	}`

	ir, err := ParseOpenAI([]byte(input))
	require.NoError(t, err)

	// Serialize back
	out, err := SerializeOpenAI(ir)
	require.NoError(t, err)

	var result map[string]any
	err = json.Unmarshal(out, &result)
	require.NoError(t, err)

	messages := result["messages"].([]any)
	toolMsg := messages[0].(map[string]any)

	assert.Equal(t, "tool", toolMsg["role"])
	assert.Equal(t, "call_789", toolMsg["tool_call_id"])
	assert.Equal(t, "get_weather", toolMsg["name"])
	assert.Equal(t, "Weather data", toolMsg["content"])
}

// TestEmptyToolResult tests that empty tool results serialize correctly.
func TestEmptyToolResult(t *testing.T) {
	input := `{
		"model": "claude-3-opus",
		"messages": [
			{
				"role": "user",
				"content": [{
					"type": "tool_result",
					"tool_use_id": "toolu_empty",
					"content": ""
				}]
			}
		]
	}`

	ir, err := ParseAnthropic([]byte(input))
	require.NoError(t, err)

	out, err := SerializeOpenAI(ir)
	require.NoError(t, err)

	var result map[string]any
	err = json.Unmarshal(out, &result)
	require.NoError(t, err)

	messages := result["messages"].([]any)
	toolMsg := messages[0].(map[string]any)

	assert.Equal(t, "tool", toolMsg["role"])
	assert.Equal(t, "toolu_empty", toolMsg["tool_call_id"])
	assert.Equal(t, "", toolMsg["content"])
}
