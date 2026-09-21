package ir

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// TestParseOpenAIResponse_GLMFinishReasonErrors verifies that GLM's error
// finish_reason values (network_error/sensitive/model_context_window_exceeded)
// are detected and returned as *ParseError rather than silently succeeding.
func TestParseOpenAIResponse_GLMFinishReasonErrors(t *testing.T) {
	cases := []struct {
		name         string
		finishReason string
		wantKind     errorsx.ErrorKind
		wantMsg      string
	}{
		{
			name:         "network_error",
			finishReason: "network_error",
			wantKind:     errorsx.KindNetwork,
			wantMsg:      "GLM network_error",
		},
		{
			name:         "sensitive",
			finishReason: "sensitive",
			wantKind:     errorsx.KindContentFilter,
			wantMsg:      "GLM content filter: sensitive",
		},
		{
			name:         "model_context_window_exceeded",
			finishReason: "model_context_window_exceeded",
			wantKind:     errorsx.KindContextLength,
			wantMsg:      "GLM context window exceeded",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{
				"id": "req1",
				"object": "chat.completion",
				"created": 1234567890,
				"model": "glm-4",
				"choices": [{
					"message": {
						"role": "assistant",
						"content": "partial output before error"
					},
					"finish_reason": "` + tc.finishReason + `"
				}],
				"usage": {
					"prompt_tokens": 10,
					"completion_tokens": 5,
					"total_tokens": 15
				}
			}`

			resp, err := ParseOpenAIResponse([]byte(body))
			require.Error(t, err, "should return error for GLM error finish_reason")
			assert.Nil(t, resp, "response should be nil on error")

			var parseErr *ParseError
			require.ErrorAs(t, err, &parseErr, "error should be *ParseError")
			assert.Equal(t, tc.wantKind, parseErr.Kind, "ErrorKind")
			assert.Equal(t, tc.wantMsg, parseErr.Message, "error message")
		})
	}
}

// TestParseOpenAIResponse_NormalFinishReasons verifies that normal finish_reason
// values (stop/length/tool_calls/content_filter) do NOT trigger errors.
func TestParseOpenAIResponse_NormalFinishReasons(t *testing.T) {
	normalReasons := []string{"stop", "length", "tool_calls", "content_filter"}

	for _, fr := range normalReasons {
		t.Run(fr, func(t *testing.T) {
			body := `{
				"id": "req1",
				"choices": [{
					"message": {"role": "assistant", "content": "hello"},
					"finish_reason": "` + fr + `"
				}],
				"usage": {"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8}
			}`

			resp, err := ParseOpenAIResponse([]byte(body))
			require.NoError(t, err, "normal finish_reason should not error")
			require.NotNil(t, resp, "response should not be nil")
			assert.Equal(t, fr, resp.FinishReason, "finish_reason should be preserved")
		})
	}
}
