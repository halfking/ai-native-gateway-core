package streaming

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newHoldbackGateWriter mimics the survival coordinator's per-attempt writer:
// a buffered gate with the L1 holdback window armed (5s / DefaultHoldbackMaxChunks).
// Semantic frames arriving while the window is open are held WITHOUT advancing
// the gate commit state — exactly the production shape that left short streams
// with commit_state=metadata at finish (survival_attempt_start
// holdback_window_ms=5000) and skipped terminal rendering.
func newHoldbackGateWriter(t *testing.T, rec *httptest.ResponseRecorder, protocol ClientProtocol) *GateWriter {
	t.Helper()
	sw := NewSerializedStreamWriter(rec)
	gate := NewAttemptCommitGate(context.Background(), protocol, sw, GateOptions{
		Mode:           GateModeBuffered,
		HoldbackWindow: 5 * time.Second,
	})
	return NewGateWriterWithResponse(gate, rec)
}

// TestStreamOpenAIToResponsesSSE_HoldbackWindowStillEmitsCompleted pins the
// 2026-09-13 fix: a /v1/responses stream that ends entirely inside the
// survival holdback window must still flush the held deltas AND emit
// response.completed. Before the fix the bridge saw MayWriteTerminal()==false
// (commit_state stuck at metadata), skipped the terminal envelope, and codex
// ≥0.80 hung on an unterminated stream.
func TestStreamOpenAIToResponsesSSE_HoldbackWindowStillEmitsCompleted(t *testing.T) {
	rec := httptest.NewRecorder()
	gw := newHoldbackGateWriter(t, rec, ProtocolOpenAIResponses)

	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(
			`data: {"choices":[{"delta":{"content":"hello holdback"},"finish_reason":null}]}` + "\n\n" +
				`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
				"data: [DONE]\n\n",
		)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}

	out := StreamOpenAIToResponsesSSE(context.Background(), gw, resp, "gpt-6-astra", "gpt-6-astra", "req-holdback-done-1234567", nil, nil)

	require.False(t, out.Interrupted)
	body := rec.Body.String()
	assert.Contains(t, body, `"delta":"hello holdback"`)
	assert.Contains(t, body, "event: response.output_text.done")
	assert.Contains(t, body, "event: response.completed")
	assert.Contains(t, body, `"status":"completed"`)
}

// TestStreamAnthropicSSEToResponses_HoldbackWindowStillEmitsCompleted is the
// Anthropic-upstream counterpart: same holdback shape, terminal envelope must
// still render after the forced holdback flush.
func TestStreamAnthropicSSEToResponses_HoldbackWindowStillEmitsCompleted(t *testing.T) {
	rec := httptest.NewRecorder()
	gw := newHoldbackGateWriter(t, rec, ProtocolOpenAIResponses)

	upstreamBody := strings.Join([]string{
		"event: message_start\n",
		`data: {"type":"message_start","message":{"id":"msg_up","model":"claude-opus-4-8","role":"assistant","content":[],"stop_reason":null,"usage":{"input_tokens":4,"output_tokens":0}}}` + "\n",
		"\n",
		"event: content_block_delta\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"anthropic holdback"}}` + "\n",
		"\n",
		"event: message_delta\n",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}` + "\n",
		"\n",
		"event: message_stop\n",
		`data: {"type":"message_stop"}` + "\n",
		"\n",
	}, "")
	resp := &http.Response{
		Body:    io.NopCloser(strings.NewReader(upstreamBody)),
		Header:  make(http.Header),
		Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
	}

	out := StreamAnthropicSSEToResponses(context.Background(), gw, resp, "claude-opus-4-8", "claude-opus-4-8", "req-holdback-ant-12345678", nil, nil)

	require.False(t, out.Interrupted)
	body := rec.Body.String()
	assert.Contains(t, body, `"delta":"anthropic holdback"`)
	assert.Contains(t, body, "event: response.completed")
}

// TestStreamOpenAIToAnthropicSSE_HoldbackWindowStillWritesTail pins the Q2
// counterpart of the fix: a /v1/messages stream ending inside the holdback
// window must still emit message_delta/message_stop. Before the fix the
// client saw "message_start but no content blocks" (claude code 529 /
// keep-alive-only pathology on relay upstreams).
func TestStreamOpenAIToAnthropicSSE_HoldbackWindowStillWritesTail(t *testing.T) {
	rec := httptest.NewRecorder()
	gw := newHoldbackGateWriter(t, rec, ProtocolAnthropic)

	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(
			`data: {"choices":[{"delta":{"content":"hi holdback"},"finish_reason":null}]}` + "\n\n" +
				`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
				"data: [DONE]\n\n",
		)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}

	out := StreamOpenAIToAnthropicSSE(context.Background(), gw, resp, "claude-sonnet-5", "claude-sonnet-5", "req-q2-holdback-12345678", nil, nil)

	require.False(t, out.Interrupted)
	body := rec.Body.String()
	assert.Contains(t, body, `"text":"hi holdback"`)
	assert.Contains(t, body, "event: message_delta")
	assert.Contains(t, body, "event: message_stop")
}

// TestStreamAnthropicSSEToOpenAI_HoldbackWindowStillWritesTail pins the Q3
// counterpart of the fix (2026-09-13): an Anthropic upstream streamed to an
// OpenAI chat client (the production relay wiring) that ends entirely inside
// the holdback window must still emit the closing finish_reason chunk,
// usage, and [DONE]. Before the fix those terminal chunks were rendered INTO
// the gate after message_stop and stranded in the holdback buffer — delivered
// only if the survival coordinator happened to Finish() this gate; a bridge
// invoked without the coordinator left chat clients on an unterminated
// stream. The bridge now force-closes the window before rendering the
// terminal block.
func TestStreamAnthropicSSEToOpenAI_HoldbackWindowStillWritesTail(t *testing.T) {
	rec := httptest.NewRecorder()
	gw := newHoldbackGateWriter(t, rec, ProtocolOpenAIChat)

	upstreamBody := strings.Join([]string{
		"event: message_start\n",
		`data: {"type":"message_start","message":{"id":"msg_up","model":"claude-opus-4-8","role":"assistant","content":[],"stop_reason":null,"usage":{"input_tokens":4,"output_tokens":0}}}` + "\n",
		"\n",
		"event: content_block_delta\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"q3 holdback"}}` + "\n",
		"\n",
		"event: message_delta\n",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}` + "\n",
		"\n",
		"event: message_stop\n",
		`data: {"type":"message_stop"}` + "\n",
		"\n",
	}, "")
	resp := &http.Response{
		Body:    io.NopCloser(strings.NewReader(upstreamBody)),
		Header:  make(http.Header),
		Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
	}

	out := StreamAnthropicSSEToOpenAI(context.Background(), gw, resp, "gpt-5-cli", "claude-opus-4-8", "req-q3-holdback-12345678", nil, nil)

	require.False(t, out.Interrupted)
	body := rec.Body.String()
	assert.Contains(t, body, `"content":"q3 holdback"`)
	assert.Contains(t, body, `"finish_reason":"stop"`)
	assert.Contains(t, body, "data: [DONE]")
}

// TestStreamOpenAIToAnthropicSSE_BenignEOFAfterFinishReason pins the Q2
// benign-EOF parity fix (2026-09-13): an upstream that closes the stream
// right after the finish_reason chunk WITHOUT [DONE] (minimax-style relay
// behavior) must complete successfully — message_delta/message_stop render
// — instead of being classified eof_without_done and discarding a fully
// delivered short stream for failover.
func TestStreamOpenAIToAnthropicSSE_BenignEOFAfterFinishReason(t *testing.T) {
	rec := httptest.NewRecorder()

	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(
			`data: {"choices":[{"delta":{"content":"benign eof"},"finish_reason":null}]}` + "\n\n" +
				`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n",
		)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}

	out := StreamOpenAIToAnthropicSSE(context.Background(), rec, resp, "claude-sonnet-5", "claude-sonnet-5", "req-q2-benign-eof-123456", nil, nil)

	require.False(t, out.Interrupted)
	body := rec.Body.String()
	assert.Contains(t, body, "benign eof")
	assert.Contains(t, body, "event: message_delta")
	assert.Contains(t, body, "event: message_stop")
}
