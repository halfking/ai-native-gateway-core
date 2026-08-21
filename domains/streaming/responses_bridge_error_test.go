package streaming

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/stretchr/testify/require"
)

func TestResponsesBridges_PostCommitUpstreamErrorFinishesIncomplete(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantReason string
		run        func(http.ResponseWriter, *http.Response) StreamOutcome
	}{
		{
			name:       "anthropic",
			wantReason: "upstream_error",

			body: "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n" +
				"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"upstream failed\"}}\n\n",
			run: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
				return StreamAnthropicSSEToResponses(w, resp, "claude-test", "claude-test", "req-error-anthropic", nil, nil)
			},
		},
		{
			name:       "openai",
			wantReason: "eof_without_done",

			body: "data: {\"id\":\"chunk-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n" +
				"data: {\"error\":{\"message\":\"upstream failed\"}}\n\n",
			run: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
				return StreamOpenAIToResponsesSSE(w, resp, "gpt-test", "gpt-test", "req-error-openai", nil, nil)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{
				Body:    io.NopCloser(strings.NewReader(tc.body)),
				Request: httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/stream", nil),
			}
			rec := httptest.NewRecorder()
			out := tc.run(rec, resp)

			require.True(t, out.Interrupted)
			require.Equal(t, tc.wantReason, out.Reason)
			require.Equal(t, errorsx.KindUpstreamDown, out.Kind)
			require.False(t, out.Resumable)
			require.Greater(t, out.ChunkCount, 0)
			require.Contains(t, rec.Body.String(), "hello")
			require.Contains(t, rec.Body.String(), "response.completed")
			require.Contains(t, rec.Body.String(), `"status":"incomplete"`)
		})
	}
}
