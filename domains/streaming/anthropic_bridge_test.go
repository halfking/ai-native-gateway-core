package streaming

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	defer resp.Body.Close()

	rec := newBridgeWriter()
	pc := newBridgePendingCapturer(1024)
	out := StreamAnthropicPassthrough(rec, resp, "claude-3-5-sonnet", "claude-3-5-sonnet", "req-1", nil, pc)
	assert.Equal(t, body, rec.buf.String())
	assert.False(t, out.Interrupted)
}

// TestStreamAnthropicSSEToOpenAI_Opus48ReportedPayload reproduces
// the exact message_start payload reported in production: cache_creation
// tokens present, output_tokens=0, claude-opus-4-8. The Q3 path must
// never forward raw Anthropic events such as message_start or
// content_block_delta to the OpenAI client.
func TestStreamAnthropicSSEToOpenAI_Opus48ReportedPayload(t *testing.T) {
	body := strings.Join([]string{
		"event: message_start\n",
		"data: {\"type\":\"message_start\",\"message\":{\"content\":[],\"id\":\"msg_1d3XmXHys2Nmre53dzh0lQuE\",\"model\":\"claude-opus-4-8\",\"role\":\"assistant\",\"stop_reason\":null,\"stop_sequence\":null,\"type\":\"message\",\"usage\":{\"cache_creation_input_tokens\":115427,\"cache_read_input_tokens\":0,\"input_tokens\":14,\"output_tokens\":0}}}\n",
		"\n",
		"event: content_block_start\n",
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n",
		"\n",
		"event: content_block_delta\n",
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n",
		"\n",
		"event: message_delta\n",
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n",
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
	defer resp.Body.Close()

	rec := newBridgeWriter()
	out := StreamAnthropicSSEToOpenAI(rec, resp, "claude-opus-4-8", "claude-opus-4-8", "req-opus", nil, nil)
	require.False(t, out.Interrupted)

	output := rec.buf.String()
	t.Logf("gateway output:\n%s", output)

	// Upstream raw event names MUST NOT leak.
	for _, leak := range []string{
		`"type":"message_start"`,
		`"type":"content_block_start"`,
		`"type":"content_block_delta"`,
		`"type":"message_delta"`,
		`"type":"message_stop"`,
		"event: message_start",
		"event: content_block_delta",
		"event: message_stop",
	} {
		assert.NotContains(t, output, leak, "leaked raw Anthropic fragment %q", leak)
	}

	// Every data: payload must be an OpenAI chat.completion.chunk or [DONE].
	var (
		roleChunk  bool
		textChunk  bool
		finishSeen bool
		usageSeen  bool
		doneSeen   bool
	)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			doneSeen = true
			continue
		}
		var c map[string]any
		require.NoError(t, json.Unmarshal([]byte(payload), &c), "chunk must be valid JSON: %s", payload)
		assert.Equal(t, "chat.completion.chunk", c["object"], "non-chunk object leaked: %s", payload)
		choices, ok := c["choices"].([]any)
		require.True(t, ok, "chunk missing `choices` array: %s", payload)
		require.NotEmpty(t, choices, "chunk has empty `choices`: %s", payload)
		choice, ok := choices[0].(map[string]any)
		require.True(t, ok)
		delta, _ := choice["delta"].(map[string]any)
		if delta != nil {
			if delta["role"] == "assistant" {
				roleChunk = true
			}
			if s, _ := delta["content"].(string); s == "hi" {
				textChunk = true
			}
		}
		if choice["finish_reason"] != nil && choice["finish_reason"] != "" {
			finishSeen = true
		}
		if u, ok := c["usage"].(map[string]any); ok && u != nil {
			if u["prompt_tokens"] != nil {
				usageSeen = true
			}
		}
	}
	assert.True(t, roleChunk, "expected an assistant role prelude chunk")
	assert.True(t, textChunk, "expected a content delta carrying the upstream text")
	assert.True(t, finishSeen, "expected a finish_reason chunk")
	assert.True(t, usageSeen, "expected a usage chunk with prompt_tokens")
	assert.True(t, doneSeen, "expected the [DONE] sentinel")
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
