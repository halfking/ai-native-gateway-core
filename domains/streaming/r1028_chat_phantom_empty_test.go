package streaming

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// r1028 chat-leg Magpie emptyReply: MiniMax often closes after
// finish_reason=tool_calls without [DONE], with only nameless phantom
// tool_calls slots. Chat path must surface KindEmptyResponse (not benign
// success) so the executor retries / switches candidates.

func TestR1028ChatPhantomToolCallsFinishWithoutDoneFailsOver(t *testing.T) {
	withEarlyEmptyThreshold(t, 0) // force the finish_reason empty path, not early-empty

	body := strings.Join([]string{
		`data: {"id":"chatcmpl-mm","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"","type":"function","function":{"name":"","arguments":""}},{"index":1,"id":"","type":"function","function":{"name":"","arguments":""}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"chatcmpl-mm","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":0,"total_tokens":10}}` + "\n\n",
	}, "")
	resp := &http.Response{
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	rec := httptest.NewRecorder()
	out := StreamChatWithPendingCapture(context.Background(),
		rec, resp, "minimax-m3", "MiniMax-M3", NewNormalizer(), nil, false, nil, nil)

	require.True(t, out.Interrupted, "phantom-only chat stream must fail over")
	assert.Equal(t, errorsx.KindEmptyResponse, out.Kind)
	assert.Equal(t, "openai_empty_response", out.Reason)
	assert.True(t, out.Resumable)
	// No agent-visible success terminal on the wire.
	assert.NotContains(t, rec.Body.String(), `"content":`)
}

func TestR1028ChatNamedToolFinishWithoutDoneStillSucceeds(t *testing.T) {
	withEarlyEmptyThreshold(t, 0)

	body := strings.Join([]string{
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_bash","type":"function","function":{"name":"Bash","arguments":"{\"command\":\"ls\"}"}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n",
	}, "")
	resp := &http.Response{
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	rec := httptest.NewRecorder()
	out := StreamChatWithPendingCapture(context.Background(),
		rec, resp, "minimax-m3", "MiniMax-M3", NewNormalizer(), nil, false, nil, nil)

	require.False(t, out.Interrupted, "named tool must still complete via benign-EOF")
	assert.Empty(t, out.Kind)
	assert.Contains(t, rec.Body.String(), `"name":"Bash"`)
	assert.True(t, strings.HasSuffix(rec.Body.String(), "data: [DONE]\n\n"))
}
