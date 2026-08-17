package streaming

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

type closeUnblocksReadCloser struct {
	closed     chan struct{}
	closeOnce  sync.Once
	mu         sync.Mutex
	closeCalls int
}

func newCloseUnblocksReadCloser() *closeUnblocksReadCloser {
	return &closeUnblocksReadCloser{closed: make(chan struct{})}
}

func (r *closeUnblocksReadCloser) Read([]byte) (int, error) {
	<-r.closed
	return 0, io.ErrClosedPipe
}

func (r *closeUnblocksReadCloser) Close() error {
	r.mu.Lock()
	r.closeCalls++
	r.mu.Unlock()
	r.closeOnce.Do(func() { close(r.closed) })
	return nil
}

func (r *closeUnblocksReadCloser) CloseCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closeCalls
}

type blockTimeoutWriteResponseWriter struct {
	header  http.Header
	release chan struct{}
}

func newBlockTimeoutWriteResponseWriter() *blockTimeoutWriteResponseWriter {
	return &blockTimeoutWriteResponseWriter{header: http.Header{}, release: make(chan struct{})}
}

func (w *blockTimeoutWriteResponseWriter) Header() http.Header { return w.header }
func (w *blockTimeoutWriteResponseWriter) WriteHeader(int)     {}
func (w *blockTimeoutWriteResponseWriter) Flush()              {}
func (w *blockTimeoutWriteResponseWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("upstream first-byte timeout")) {
		<-w.release
	}
	return len(p), nil
}

// TestConvertChatRequestToAnthropic_OpenAIToAnthropic verifies the
// Q2 OpenAI→Anthropic request body conversion. The first system
// message must be promoted to the Anthropic top-level "system"
// field, and remaining messages keep their role.
func TestConvertChatRequestToAnthropic_OpenAIToAnthropic(t *testing.T) {
	in := []byte(`{
		"model": "claude-3-5-sonnet",
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "Hello"}
		],
		"max_tokens": 1024,
		"temperature": 0.5
	}`)
	out, err := ConvertChatRequestToAnthropic(in)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))

	assert.Equal(t, "claude-3-5-sonnet", got["model"])
	assert.Equal(t, "You are a helpful assistant.", got["system"])
	assert.Equal(t, float64(1024), got["max_tokens"])
	assert.Equal(t, float64(0.5), got["temperature"])
	msgs, ok := got["messages"].([]any)
	require.True(t, ok, "messages should be a JSON array, got %T", got["messages"])
	require.Len(t, msgs, 1, "system message should be lifted out of messages")
	first, _ := msgs[0].(map[string]any)
	assert.Equal(t, "user", first["role"])
}

// TestConvertAnthropicResponseToChat_NonStream verifies the Q3
// Anthropic→OpenAI response conversion (text content).
func TestConvertAnthropicResponseToChat_NonStream(t *testing.T) {
	in := []byte(`{
		"id": "msg_01",
		"type": "message",
		"role": "assistant",
		"model": "claude-3-5-sonnet-20241022",
		"content": [
			{"type": "text", "text": "Hello back!"}
		],
		"usage": {"input_tokens": 12, "output_tokens": 7},
		"stop_reason": "end_turn"
	}`)
	out, err := ConvertAnthropicResponseToChat(in, "claude-3-5-sonnet")
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "msg_01", got["id"])
	assert.Equal(t, "chat.completion", got["object"])
	assert.Equal(t, "claude-3-5-sonnet", got["model"])
	choices, _ := got["choices"].([]any)
	require.Len(t, choices, 1)
	first, _ := choices[0].(map[string]any)
	assert.Equal(t, "stop", first["finish_reason"])
	msg, _ := first["message"].(map[string]any)
	assert.Equal(t, "assistant", msg["role"])
	assert.Equal(t, "Hello back!", msg["content"])
	usage, _ := got["usage"].(map[string]any)
	assert.Equal(t, float64(12), usage["prompt_tokens"])
	assert.Equal(t, float64(7), usage["completion_tokens"])
}

// TestConvertAnthropicResponseToChat_ToolCalls verifies tool_use
// blocks become OpenAI tool_calls entries.
func TestConvertAnthropicResponseToChat_ToolCalls(t *testing.T) {
	in := []byte(`{
		"id": "msg_02",
		"type": "message",
		"role": "assistant",
		"model": "claude-3-5-sonnet-20241022",
		"content": [
			{"type": "tool_use", "id": "tu_1", "name": "get_weather", "input": {"city": "SF"}}
		],
		"usage": {"input_tokens": 5, "output_tokens": 9},
		"stop_reason": "tool_use"
	}`)
	out, err := ConvertAnthropicResponseToChat(in, "")
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	choices, _ := got["choices"].([]any)
	first, _ := choices[0].(map[string]any)
	assert.Equal(t, "tool_calls", first["finish_reason"])
	msg, _ := first["message"].(map[string]any)
	calls, _ := msg["tool_calls"].([]any)
	require.Len(t, calls, 1)
	first_call, _ := calls[0].(map[string]any)
	assert.Equal(t, "tu_1", first_call["id"])
	fn, _ := first_call["function"].(map[string]any)
	assert.Equal(t, "get_weather", fn["name"])
	assert.Equal(t, `{"city":"SF"}`, fn["arguments"])
}

func TestStreamAnthropicSSEToOpenAI_WrappedEOFIsCleanCompletion(t *testing.T) {
	resp := &http.Response{
		Body: &errorAfterDataReadCloser{
			data: []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"),
			err:  fmt.Errorf("wrapped: %w", io.EOF),
		},
		Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
	}
	rec := httptest.NewRecorder()

	out := StreamAnthropicSSEToOpenAI(rec, resp, "claude-test", "claude-test", "req-wrapped-eof", nil, nil)

	assert.False(t, out.Interrupted)
	assert.Contains(t, rec.Body.String(), `"content":"hello"`)
	assert.Contains(t, rec.Body.String(), "data: [DONE]")
}

func TestStreamOpenAIToAnthropicSSE_FirstByteTimeoutClosesBlockingBody(t *testing.T) {
	previousStore := streamConfigStore.Load()
	SetConfigStore(nil)
	t.Cleanup(func() { SetConfigStore(previousStore) })
	t.Setenv("LLM_GATEWAY_FIRST_BYTE_TIMEOUT", "1")
	body := newCloseUnblocksReadCloser()
	resp := &http.Response{
		Body:    body,
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	w := newBlockTimeoutWriteResponseWriter()
	done := make(chan StreamOutcome, 1)
	go func() {
		done <- StreamOpenAIToAnthropicSSE(
			w, resp, "claude-test", "upstream-test", "req-blocking-body", nil, nil,
		)
	}()

	select {
	case <-body.closed:
		close(w.release)
	case <-time.After(3 * time.Second):
		close(w.release)
		_ = body.Close()
		<-done
		t.Fatal("first-byte timeout did not close the blocking response body before rendering the timeout")
	}

	select {
	case outcome := <-done:
		assert.True(t, outcome.Interrupted)
		assert.Equal(t, "first_byte_timeout", outcome.Reason)
		assert.Equal(t, errorsx.KindStreamTimeout, outcome.Kind)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("stream bridge did not return promptly after closing the blocking body")
	}
	assert.Equal(t, 1, body.CloseCalls(), "underlying response body must be closed exactly once")
}

func TestStreamAnthropicPassthrough_ClientDisconnectWinsOverLaterUpstreamError(t *testing.T) {
	resp := &http.Response{
		Body: &errorAfterDataReadCloser{
			data: []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"),
			err:  errors.New("other side closed"),
		},
		Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
	}

	out := StreamAnthropicPassthrough(
		newDisconnectingStreamWriter(), resp,
		"claude-test", "claude-test", "req-client-close", nil, NewPendingCapturer(4096),
	)

	assert.True(t, out.Interrupted)
	assert.Equal(t, "client_write_failed", out.Reason)
	assert.Equal(t, errorsx.KindCanceled, out.Kind)
	assert.False(t, out.Resumable)
}

func TestStreamAnthropicPassthrough_OtherSideClosedIsNetworkError(t *testing.T) {
	resp := &http.Response{
		Body: &errorAfterDataReadCloser{
			data: []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"),
			err:  errors.New("other side closed"),
		},
		Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
	}
	rec := httptest.NewRecorder()

	out := StreamAnthropicPassthrough(rec, resp, "claude-test", "claude-test", "req-passthrough-close", nil, nil)

	assert.True(t, out.Interrupted)
	assert.Equal(t, "network_error", out.Reason)
	assert.Equal(t, errorsx.KindNetwork, out.Kind)
	assert.True(t, out.Resumable)
	assert.Equal(t, 1, out.ChunkCount)
	assert.Contains(t, rec.Body.String(), `"text":"hello"`)
}

// TestStreamAnthropicPassthrough_BytesForPassThrough ensures the
// passthrough writes every byte of the upstream SSE event stream to
// the client and records a capturer buffer when pc is supplied.
func TestStreamAnthropicPassthrough_BytesForPassThrough(t *testing.T) {
	body := strings.Join([]string{
		"event: message_start\n",
		"data: {\"type\":\"message_start\"}\n",
		"\n",
		"event: content_block_delta\n",
		"data: {\"type\":\"content_block_delta\"}\n",
		"\n",
		"event: message_stop\n",
		"data: {\"type\":\"message_stop\"}\n",
		"\n",
	}, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	rec := newBridgeWriter()
	pc := newBridgePendingCapturer(1024)
	out := StreamAnthropicPassthrough(rec, resp, "claude-3-5-sonnet", "claude-3-5-sonnet", "req-1", nil, pc)
	assert.Equal(t, body, rec.buf.String())
	assert.False(t, out.Interrupted)
}

func TestStreamAnthropicPassthrough_KeepaliveBeforeFirstSemanticFrameDoesNotCommit(t *testing.T) {
	body := ": upstream ping\n\ndata: {\"type\":\"content_block_delta\"}\n\n"
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
	writer := newBridgeWriter()
	out := StreamAnthropicPassthrough(writer, resp, "claude", "claude", "req-boundary", nil, nil)
	if out.Interrupted {
		t.Fatalf("outcome = %+v", out)
	}
	if !strings.HasPrefix(writer.buf.String(), ": upstream ping\n\n") {
		t.Fatalf("wire = %q, keepalive must precede first semantic frame", writer.buf.String())
	}
}

func TestStreamAnthropicPassthrough_FlushErrorAfterSemanticFrameIsClientFailure(t *testing.T) {
	resp := &http.Response{Body: io.NopCloser(strings.NewReader("data: {\"type\":\"content_block_delta\"}\n\n"))}
	writer := &initialFlushErrorWriter{header: make(http.Header), flushErr: errors.New("client gone")}
	out := StreamAnthropicPassthrough(writer, resp, "claude", "claude", "req-flush", nil, nil)
	if !out.Interrupted || out.Reason != "client_write_failed" || out.Kind != errorsx.KindCanceled {
		t.Fatalf("outcome = %+v, want client_write_failed cancellation", out)
	}
}

func TestStreamAnthropicPassthrough_ForwardsUnterminatedFinalFrame(t *testing.T) {
	const body = `data: {"type":"message_stop"}`
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
	rec := newBridgeWriter()

	out := StreamAnthropicPassthrough(rec, resp, "claude-3-5-sonnet", "claude-3-5-sonnet", "req-eof", nil, nil)

	assert.False(t, out.Interrupted)
	assert.Equal(t, body, rec.buf.String())
}

func TestStreamOpenAIToAnthropicSSE_SplitsDoneJoinedToJSON(t *testing.T) {
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(
			`data: {"id":"chunk-1","object":"chat.completion.chunk","choices":[{"delta":{"content":"planning"},"finish_reason":null}]}[DONE].`,
		)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	rec := httptest.NewRecorder()

	out := StreamOpenAIToAnthropicSSE(rec, resp, "gpt-5.6-sol", "gpt-5.6-sol", "req-combined-done", nil, nil)

	require.False(t, out.Interrupted)
	assert.Contains(t, rec.Body.String(), `"text":"planning"`)
	assert.Contains(t, rec.Body.String(), `event: message_stop`)
}

func TestStreamOpenAIToAnthropicSSE_OtherSideClosedIsNetworkError(t *testing.T) {
	resp := &http.Response{
		Body: &errorAfterDataReadCloser{
			data: []byte("data: {\"id\":\"chunk-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n"),
			err:  errors.New("other side closed"),
		},
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	rec := httptest.NewRecorder()

	out := StreamOpenAIToAnthropicSSE(rec, resp, "glm-5.2", "glm-5.2", "req-network-close", nil, nil)

	assert.True(t, out.Interrupted)
	assert.Equal(t, "network_error", out.Reason)
	assert.Equal(t, errorsx.KindNetwork, out.Kind)
	assert.Contains(t, rec.Body.String(), `"text":"hello"`)
}

func TestStreamAnthropicSSEToOpenAI_ConvertsMessageStartToOpenAIChunk(t *testing.T) {
	body := strings.Join([]string{
		"event: message_start\n",
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1d3XmXHys2Nmre53dzh0lQuE\",\"model\":\"claude-opus-4-8\",\"role\":\"assistant\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"type\":\"message\",\"usage\":{\"cache_creation_input_tokens\":115427,\"cache_read_input_tokens\":0,\"input_tokens\":14,\"output_tokens\":0}}}\n",
		"\n",
		"event: message_delta\n",
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":0}}\n",
		"\n",
		"event: message_stop\n",
		"data: {\"type\":\"message_stop\"}\n",
		"\n",
	}, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	rec := httptest.NewRecorder()
	out := StreamAnthropicSSEToOpenAI(rec, resp, "claude-opus-4-8", "claude-opus-4-8", "req-opus", nil, nil)
	require.False(t, out.Interrupted)

	output := rec.Body.String()
	assert.NotContains(t, output, `"type":"message_start"`)
	assert.Contains(t, output, `"choices":[`)
	assert.Contains(t, output, `"role":"assistant"`)
	assert.Contains(t, output, `data: [DONE]`)

	var firstChunk map[string]any
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		require.NoError(t, json.Unmarshal([]byte(payload), &firstChunk))
		break
	}
	require.NotNil(t, firstChunk)
	choices, ok := firstChunk["choices"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, choices)
	choice, ok := choices[0].(map[string]any)
	require.True(t, ok)
	delta, ok := choice["delta"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "assistant", delta["role"])
}

// TestConvertAnthropicResponseToChat_EmptyResponseErrors checks the
// "all content blocks unparseable" guard: when the body has zero
// text/tool/thinking content, the helper must return an error.
func TestConvertAnthropicResponseToChat_EmptyResponseErrors(t *testing.T) {
	in := []byte(`{
		"id": "msg_x",
		"type": "message",
		"role": "assistant",
		"model": "claude-3-5-sonnet",
		"content": [],
		"stop_reason": "end_turn"
	}`)
	_, err := ConvertAnthropicResponseToChat(in, "")
	require.Error(t, err)
}

// bridgeWriter is a minimal http.ResponseWriter that captures bytes
// and satisfies http.Flusher so the passthrough helpers can run.
type bridgeWriter struct {
	header http.Header
	buf    bytes.Buffer
}

func newBridgeWriter() *bridgeWriter { return &bridgeWriter{header: http.Header{}} }

func (w *bridgeWriter) Header() http.Header         { return w.header }
func (w *bridgeWriter) Write(b []byte) (int, error) { return w.buf.Write(b) }
func (w *bridgeWriter) WriteHeader(_ int)           {}
func (w *bridgeWriter) Flush()                      {}

// newBridgePendingCapturer returns a live capturer sized for tests.
func newBridgePendingCapturer(maxBytes int) *pendingCapturer {
	return NewPendingCapturer(maxBytes)
}
