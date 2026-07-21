package streaming

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConvertResponsesInputItem_PreservesFunctionCall(t *testing.T) {
	cases := []struct {
		name string
		item map[string]any
	}{
		{
			name: "function_call",
			item: map[string]any{"type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": "{}"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			message, ok := convertResponsesInputItem(tc.item)
			require.True(t, ok)
			require.Equal(t, "assistant", message["role"])
			toolCalls := message["tool_calls"].([]any)
			require.Equal(t, "call_1", toolCalls[0].(map[string]any)["id"])
		})
	}
}

func TestConvertResponsesToChatBody_UsesFallbackForUnknownInput(t *testing.T) {
	var req responsesRequestBody
	require.NoError(t, json.Unmarshal([]byte(`{"model":"gpt-4o","input":[{"type":"input_audio"}]}`), &req))
	chatBody := convertResponsesToChatBody(&req)
	messages := chatBody["messages"].([]any)
	require.Len(t, messages, 1)
	require.Equal(t, "user", messages[0].(map[string]any)["role"])
}
