package streaming

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// r1028 — Magpie emptyReply parity on OpenAI→Responses:
// agent-visible empty (no text, no named tool) must surface KindEmptyResponse
// so the executor retries / switches candidates instead of handing ZCode an
// empty_model_response. Mirrors Magpie saidAnything (thinking alone ≠ answer).

func responsesUpstream(t *testing.T, frames ...string) *http.Response {
	t.Helper()
	body := strings.Join(frames, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestR1028MiniMaxPhantomToolSlotsFailOver(t *testing.T) {
	// Production shape: empty id/name/args tool_calls + finish tool_calls,
	// often with completion_tokens=0. Must not fake success for the client.
	resp := responsesUpstream(t,
		`data: {"id":"chatcmpl-mm","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"","type":"function","function":{"name":"","arguments":""}},{"index":1,"id":"","type":"function","function":{"name":"","arguments":""}}]},"finish_reason":null}]}`+"\n\n",
		`data: {"id":"chatcmpl-mm","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":0,"total_tokens":10}}`+"\n\n",
		`data: [DONE]`+"\n\n",
	)
	rec := httptest.NewRecorder()
	out := StreamOpenAIToResponsesSSE(context.Background(), rec, resp, "minimax-m3", "minimax-m3", "req-r1028-phantom", nil, nil)

	require.True(t, out.Interrupted, "phantom-only stream must fail over")
	assert.Equal(t, errorsx.KindEmptyResponse, out.Kind)
	assert.Equal(t, "openai_empty_response", out.Reason)
	assert.True(t, out.Resumable, "executor must be able to retry / switch candidate")
	assert.NotContains(t, rec.Body.String(), `"status":"completed"`)
	assert.NotContains(t, rec.Body.String(), `"status":"incomplete"`)
}

func TestR1028ReasoningOnlyFailsOverLikeMagpieSaidAnything(t *testing.T) {
	// Magpie saidAnything: thinking alone is not an answer. ZCode reports
	// empty_model_response when there is no text and no tool call.
	resp := responsesUpstream(t,
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"thinking…"},"finish_reason":null}]}`+"\n\n",
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n",
		`data: [DONE]`+"\n\n",
	)
	rec := httptest.NewRecorder()
	out := StreamOpenAIToResponsesSSE(context.Background(), rec, resp, "minimax-m3", "minimax-m3", "req-r1028-reason", nil, nil)

	require.True(t, out.Interrupted)
	assert.Equal(t, errorsx.KindEmptyResponse, out.Kind)
	assert.True(t, out.Resumable)
	assert.NotContains(t, rec.Body.String(), `"status":"completed"`)
}

func TestR1028ReasoningPlusNamelessToolFailsOver(t *testing.T) {
	// Reasoning makes SerializeResponses emit, which previously populated
	// nameless toolStates and skipped the empty gate → completed with no
	// function_call (ZCode empty_model_response). Must fail over.
	resp := responsesUpstream(t,
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"x","tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"arguments":"{\"a\":1}"}}]},"finish_reason":null}]}`+"\n\n",
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n",
		`data: [DONE]`+"\n\n",
	)
	rec := httptest.NewRecorder()
	out := StreamOpenAIToResponsesSSE(context.Background(), rec, resp, "minimax-m3", "minimax-m3", "req-r1028-hybrid", nil, nil)

	require.True(t, out.Interrupted)
	assert.Equal(t, errorsx.KindEmptyResponse, out.Kind)
	assert.True(t, out.Resumable)
	body := rec.Body.String()
	assert.NotContains(t, body, `"status":"completed"`)
	assert.NotContains(t, body, `"type":"function_call"`)
}

func TestR1028NamedToolStillSucceeds(t *testing.T) {
	// Premise-in-presence: a real named tool must still complete successfully
	// (empty gate must not over-fire).
	resp := responsesUpstream(t,
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_bash","type":"function","function":{"name":"Bash","arguments":"{\"command\":\"ls\"}"}}]},"finish_reason":null}]}`+"\n\n",
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n",
		`data: [DONE]`+"\n\n",
	)
	rec := httptest.NewRecorder()
	out := StreamOpenAIToResponsesSSE(context.Background(), rec, resp, "minimax-m3", "minimax-m3", "req-r1028-named", nil, nil)

	require.False(t, out.Interrupted, "named tool must not be classified empty")
	assert.Empty(t, out.Kind)
	body := rec.Body.String()
	assert.Contains(t, body, `"name":"Bash"`)
	assert.Contains(t, body, `"status":"completed"`)
}

func TestR1028bHybridPhantomPlusNamedOnResponsesBridge(t *testing.T) {
	// 154 production (openai_responses): empty slot + Bash in one delta.
	// Chat-leg strip alone left this path leaking phantoms into capture and
	// (historically) into client-visible function_call scaffolding.
	resp := responsesUpstream(t,
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"","type":"","function":{"name":"","arguments":""}},{"index":1,"id":"call_bash","type":"function","function":{"name":"Bash","arguments":"{\"command\":\"pwd\"}"}}]},"finish_reason":null}]}`+"\n\n",
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n",
		`data: [DONE]`+"\n\n",
	)
	rec := httptest.NewRecorder()
	out := StreamOpenAIToResponsesSSE(context.Background(), rec, resp, "minimax-m3", "minimax-m3", "req-r1028b-hybrid", nil, nil)

	require.False(t, out.Interrupted, "hybrid must succeed on the named tool: %+v", out)
	body := rec.Body.String()
	assert.Contains(t, body, `"name":"Bash"`)
	assert.Contains(t, body, `"status":"completed"`)
	assert.NotContains(t, body, `"name":""`)
}

func TestR1028TextStillSucceeds(t *testing.T) {
	resp := responsesUpstream(t,
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}`+"\n\n",
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n",
		`data: [DONE]`+"\n\n",
	)
	rec := httptest.NewRecorder()
	out := StreamOpenAIToResponsesSSE(context.Background(), rec, resp, "minimax-m3", "minimax-m3", "req-r1028-text", nil, nil)

	require.False(t, out.Interrupted)
	assert.Contains(t, rec.Body.String(), `"delta":"hello"`)
	assert.Contains(t, rec.Body.String(), `"status":"completed"`)
}

func TestR1028FinishWithoutDoneEmptyFailsOver(t *testing.T) {
	// MiniMax often closes after finish_reason without [DONE]. Empty
	// agent-visible output must still be KindEmptyResponse (not eof_without_done).
	resp := responsesUpstream(t,
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`+"\n\n",
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n",
	)
	rec := httptest.NewRecorder()
	out := StreamOpenAIToResponsesSSE(context.Background(), rec, resp, "minimax-m3", "minimax-m3", "req-r1028-eof", nil, nil)

	require.True(t, out.Interrupted)
	assert.Equal(t, errorsx.KindEmptyResponse, out.Kind)
	assert.Equal(t, "openai_empty_response", out.Reason)
	assert.True(t, out.Resumable)
}
