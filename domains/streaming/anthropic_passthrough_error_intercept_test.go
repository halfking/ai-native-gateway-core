package streaming

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// 2026-08-17 upstream error-event interception tests (Q4 anthropic
// passthrough). Relay-style upstreams answer a failing turn with a terminal
// `event: error` frame whose message packs a multi-line internal diagnostic
// blob, then close the connection. These tests pin the three behaviors the
// interception must guarantee:
//
//  1. the blob never reaches the client verbatim,
//  2. nothing-client-visible interruptions stay transparently retryable,
//  3. client-visible attempts get the gateway's own structured error event
//     and pin the capture's sent-chunk counter so the executor cannot
//     approve a duplicate-content retry.

// relayDiagnosticErrorBody mimics the exact failure shape observed in
// production: an api_error whose message embeds provider/model/request
// diagnostics plus the transport's last words.
const relayDiagnosticErrorBody = "event: error\n" +
	"data: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"Turn execution failed\\nprovider=ef7bed64-de6f-42d8-86f2-eab4b62d9812 model=glm-5.2 request=3117711a-1c15-417d-91e6-60c33a9a823d reason=network_error retryable=true\\nterminated\\nother side closed\"}}\n\n"

const contentDeltaFrame = "event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"

const overloadErrorFrame = "event: error\n" +
	"data: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"

func passthroughResp(body string) *http.Response {
	return &http.Response{
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
	}
}

// The relay's diagnostic blob as the first (and only) frame: the buffered
// attempt gate holds everything uncommitted, so the interception must leave
// the client wire empty and the interruption transparently retryable.
func TestStreamAnthropicPassthrough_ErrorEventFirstFrameIsSuppressedAndRetryable(t *testing.T) {
	rec := httptest.NewRecorder()
	pc := NewPendingCapturer(64 * 1024)

	out := StreamAnthropicPassthrough(context.Background(), rec, passthroughResp(relayDiagnosticErrorBody),
		"claude-test", "claude-test", "req-intercept-first", nil, pc)

	assert.True(t, out.Interrupted)
	assert.Equal(t, "upstream_error", out.Reason)
	assert.Equal(t, errorsx.KindUpstreamDown, out.Kind)
	assert.True(t, out.Resumable, "nothing client-visible: the executor must fail over transparently")
	assert.Equal(t, 0, out.ChunkCount)
	assert.Equal(t, "", rec.Body.String(), "relay diagnostic blob must not reach the client")
	assert.Contains(t, string(pc.buffer), "Turn execution failed",
		"the pending-capture side channel must keep the raw upstream payload for audit")
}

// Content first (commits the attempt), then the relay's terminal error: the
// client keeps the content it already saw and receives the gateway's own
// structured error envelope instead of the relay's internal blob.
func TestStreamAnthropicPassthrough_ErrorEventAfterContentRendersStructuredError(t *testing.T) {
	rec := httptest.NewRecorder()

	out := StreamAnthropicPassthrough(context.Background(), rec, passthroughResp(contentDeltaFrame+overloadErrorFrame),
		"claude-test", "claude-test", "req-intercept-commit", nil, nil)

	assert.True(t, out.Interrupted)
	assert.Equal(t, "upstream_error", out.Reason)
	assert.Equal(t, errorsx.KindUpstreamOverloaded, out.Kind)
	assert.False(t, out.Resumable, "client saw content plus a terminal error; a retry would duplicate")
	wire := rec.Body.String()
	assert.Contains(t, wire, `"text":"hello"`)
	assert.Contains(t, wire, "event: error")
	assert.Contains(t, wire, "upstream stream error: overloaded_error")
	assert.NotContains(t, wire, "Overloaded", "upstream error message is filtered, not forwarded")
}

// Regression for the false-success path: an error event followed by a clean
// EOF previously returned a non-interrupted outcome, recording a failed turn
// as a success.
func TestStreamAnthropicPassthrough_ErrorEventThenCleanEOFIsInterrupted(t *testing.T) {
	rec := httptest.NewRecorder()

	out := StreamAnthropicPassthrough(context.Background(), rec, passthroughResp(relayDiagnosticErrorBody),
		"claude-test", "claude-test", "req-intercept-eof", nil, nil)

	assert.True(t, out.Interrupted, "error event + clean EOF must not record success")
	assert.Equal(t, "upstream_error", out.Reason)
}

// A bare error data payload without the `event: error` line (relay quirk)
// is intercepted the same way.
func TestStreamAnthropicPassthrough_StandaloneErrorPayloadIsIntercepted(t *testing.T) {
	rec := httptest.NewRecorder()

	out := StreamAnthropicPassthrough(context.Background(), rec,
		passthroughResp("data: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"slow down\"}}\n\n"),
		"claude-test", "claude-test", "req-intercept-standalone", nil, nil)

	assert.True(t, out.Interrupted)
	assert.Equal(t, "upstream_error", out.Reason)
	assert.Equal(t, errorsx.KindRateLimit, out.Kind)
	assert.True(t, out.Resumable)
	assert.Equal(t, "", rec.Body.String())
}

// A mid-stream read failure after client-visible content must pin the
// capture's sent-chunk counter so mayRetryInterruptedStream refuses a
// duplicate-content retry (RecordChunkSent was only wired on the
// OpenAI→OpenAI bridge before).
func TestStreamAnthropicPassthrough_ReadFailureAfterContentRecordsSentChunks(t *testing.T) {
	resp := &http.Response{
		Body: &errorAfterDataReadCloser{
			data: []byte(contentDeltaFrame),
			err:  errors.New("other side closed"),
		},
		Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
	}
	rec := httptest.NewRecorder()
	capture := &audit.StreamCapture{}

	out := StreamAnthropicPassthrough(context.Background(), rec, resp, "claude-test", "claude-test", "req-pin-sent", capture, nil)

	assert.True(t, out.Interrupted)
	assert.Equal(t, "network_error", out.Reason)
	sent, _ := capture.ChunkCountersSnapshot()
	assert.GreaterOrEqual(t, sent, 1, "executor retry gate must see the client-visible chunks")
}

// An `event: error` declaration is terminal regardless of the payload's
// shape: even a malformed / non-envelope data payload is intercepted rather
// than forwarded (the SSE event name itself signals the terminal error).
func TestStreamAnthropicPassthrough_ErrorEventLineWithNonErrorPayloadIsTerminal(t *testing.T) {
	// Perverse upstream: an `event: error` declaration over a content delta.
	body := "event: error\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"
	rec := httptest.NewRecorder()

	out := StreamAnthropicPassthrough(context.Background(), rec, passthroughResp(body),
		"claude-test", "claude-test", "req-hold-terminal", nil, nil)

	assert.True(t, out.Interrupted)
	assert.Equal(t, "upstream_error", out.Reason)
	assert.Equal(t, errorsx.KindUpstreamDown, out.Kind)
	assert.True(t, out.Resumable)
	assert.Equal(t, "", rec.Body.String())
}

// `event: error` followed immediately by EOF (frame never completed) still
// classifies as an interrupted upstream error.
func TestStreamAnthropicPassthrough_ErrorEventLineThenEOFIsInterrupted(t *testing.T) {
	rec := httptest.NewRecorder()

	out := StreamAnthropicPassthrough(context.Background(), rec, passthroughResp("event: error\n"),
		"claude-test", "claude-test", "req-hold-eof", nil, nil)

	assert.True(t, out.Interrupted)
	assert.Equal(t, "upstream_error", out.Reason)
}

func TestClassifyAnthropicStreamError(t *testing.T) {
	cases := []struct {
		errType string
		want    errorsx.ErrorKind
	}{
		{"overloaded_error", errorsx.KindUpstreamOverloaded},
		{"rate_limit_error", errorsx.KindRateLimit},
		{"authentication_error", errorsx.KindAuth},
		{"permission_error", errorsx.KindAuth},
		{"timeout", errorsx.KindTimeout},
		{"timeout_error", errorsx.KindTimeout},
		{"api_error", errorsx.KindUpstreamDown},
		{"", errorsx.KindUpstreamDown},
	}
	for _, tc := range cases {
		payload := []byte(`{"type":"error","error":{"type":"` + tc.errType + `","message":"x"}}`)
		assert.Equal(t, tc.want, classifyAnthropicStreamError(tc.errType, payload), "errType=%q", tc.errType)
	}
}

func TestIsAnthropicErrorPayload(t *testing.T) {
	assert.True(t, isAnthropicErrorPayload(`{"type":"error","error":{"type":"api_error","message":"boom"}}`))
	assert.True(t, isAnthropicErrorPayload(`{"error":{"message":"bare shape"}}`))
	assert.False(t, isAnthropicErrorPayload(`{"type":"content_block_delta"}`))
	assert.False(t, isAnthropicErrorPayload(`{"error":null}`))
	assert.False(t, isAnthropicErrorPayload(""))
	assert.False(t, isAnthropicErrorPayload("[DONE]"))
	assert.False(t, isAnthropicErrorPayload("not json"))
}
