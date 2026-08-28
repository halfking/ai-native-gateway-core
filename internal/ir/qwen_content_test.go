package ir

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseOpenAIResponse_QwenStructuredContent(t *testing.T) {
	body := []byte(`{
		"id":"qwen-response",
		"object":"chat.completion",
		"model":"qwen-plus",
		"choices":[{
			"message":{"role":"assistant","content":[{"text":"Hello "},{"text":"world"}]},
			"finish_reason":"stop"
		}]
	}`)

	response, err := ParseOpenAIResponse(body)
	require.NoError(t, err)
	require.Len(t, response.Content, 2)
	assert.Equal(t, ResponseContentBlock{Type: "text", Text: "Hello "}, response.Content[0])
	assert.Equal(t, ResponseContentBlock{Type: "text", Text: "world"}, response.Content[1])
}

func TestParseOpenAIResponse_ContentShapes(t *testing.T) {
	cases := []struct {
		name        string
		contentJSON string
		wantContent []ResponseContentBlock
	}{
		{
			name:        "standard string",
			contentJSON: `"hello"`,
			wantContent: []ResponseContentBlock{{Type: "text", Text: "hello"}},
		},
		{
			name:        "typed text block",
			contentJSON: `[{"type":"text","text":"hello"}]`,
			wantContent: []ResponseContentBlock{{Type: "text", Text: "hello"}},
		},
		{
			name:        "Qwen text block",
			contentJSON: `[{"text":"hello"}]`,
			wantContent: []ResponseContentBlock{{Type: "text", Text: "hello"}},
		},
		{
			name:        "empty array",
			contentJSON: `[]`,
			wantContent: nil,
		},
		{
			name:        "null",
			contentJSON: `null`,
			wantContent: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response, err := ParseOpenAIResponse([]byte(`{
				"id":"response",
				"choices":[{
					"message":{"role":"assistant","content":` + tc.contentJSON + `},
					"finish_reason":"stop"
				}]
			}`))
			require.NoError(t, err)
			assert.Equal(t, tc.wantContent, response.Content)
		})
	}
}

func TestParseOpenAIStreamChunk_QwenStructuredContent(t *testing.T) {
	cases := []struct {
		name        string
		contentJSON string
		wantContent string
		wantType    string
	}{
		{
			name:        "standard string",
			contentJSON: `"hello"`,
			wantContent: "hello",
			wantType:    "text",
		},
		{
			name:        "Qwen text array",
			contentJSON: `[{"text":"hello "},{"text":"world"}]`,
			wantContent: "hello world",
			wantType:    "text",
		},
		{
			name:        "empty array",
			contentJSON: `[]`,
			wantContent: "",
			wantType:    "",
		},
		{
			name:        "null",
			contentJSON: `null`,
			wantContent: "",
			wantType:    "",
		},
	}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				chunk, err := ParseOpenAIStreamChunk(`data: {
					"id":"qwen-stream",
					"object":"chat.completion.chunk",
					"model":"qwen-plus",
					"choices":[{"index":0,"delta":{"content":` + tc.contentJSON + `},"finish_reason":null}]
				}`)
				require.NoError(t, err)
				require.NotNil(t, chunk.Delta)
				assert.Equal(t, tc.wantContent, chunk.Delta.Content)
				assert.Equal(t, tc.wantType, chunk.Delta.DeltaType)
			})
		}
	}

func TestParseOpenAIResponse_QwenContentWithToolCalls(t *testing.T) {
	body := []byte(`{
		"id":"qwen-response",
		"object":"chat.completion",
		"model":"qwen-plus",
		"choices":[{
			"message":{
				"role":"assistant",
				"content":[{"text":"Let me search for that."}],
				"tool_calls":[{
					"id":"call_1",
					"type":"function",
					"function":{"name":"search","arguments":"{\"query\":\"test\"}"}
				}]
			},
			"finish_reason":"tool_calls"
		}]
	}`)

	response, err := ParseOpenAIResponse(body)
	require.NoError(t, err)
	require.Len(t, response.Content, 1)
	assert.Equal(t, "text", response.Content[0].Type)
	assert.Equal(t, "Let me search for that.", response.Content[0].Text)
	require.Len(t, response.ToolCalls, 1)
	assert.Equal(t, "call_1", response.ToolCalls[0].ID)
	assert.Equal(t, "search", response.ToolCalls[0].Name)
	assert.Equal(t, `{"query":"test"}`, response.ToolCalls[0].Arguments)
	assert.Equal(t, "tool_calls", response.FinishReason)
}
