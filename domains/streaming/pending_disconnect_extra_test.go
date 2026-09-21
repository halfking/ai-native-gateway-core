package streaming

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStreamChatWithPendingCapture_DisconnectsWithoutPending_ReturnsEarly
// covers the "no pendingCapturer" path. The writer is a hard-failing
// http.ResponseWriter; the stream must observe the disconnect on the
// first chunk, mark the outcome as a client_write_failed failure with
// ChunkCount=0, and report Resumable=false (client disconnect is
// permanent; transparent retry cannot write headers to a dead
// connection — mirrors the pre-fix behavior for callers that opt out of
// pending-replay).
func TestStreamChatWithPendingCapture_DisconnectsWithoutPending_ReturnsEarly(t *testing.T) {
	body := strings.Join([]string{
		"data: {\"id\":\"chunk-1\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n",
		"data: [DONE]\n\n",
	}, "")
	resp := &http.Response{
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}

	cap := audit.NewStreamCapture()
	outcome := StreamChatWithPendingCapture(context.Background(),
		newDisconnectingStreamWriter(),
		resp,
		"gpt-test",
		"gpt-test",
		NewNormalizer(),
		cap,
		false,
		nil,
		nil,
	)

	assert.True(t, outcome.Interrupted)
	assert.Equal(t, "client_write_failed", outcome.Reason)
	assert.Equal(t, 0, outcome.ChunkCount)
	assert.False(t, outcome.Resumable)
	_, _, _, interrupted, _ := cap.Snapshot()
	assert.True(t, interrupted, "audit capture should be marked interrupted on client_write_failed")
}

// TestStreamChatWithPendingCapture_ContinuesUntilDoneWithCapturer verifies
// that the capturer receives the full upstream SSE even when the client
// disconnects on the very last line before [DONE].
func TestStreamChatWithPendingCapture_ContinuesUntilDoneWithCapturer(t *testing.T) {
	body := strings.Join([]string{
		"data: {\"id\":\"chunk-1\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n",
		"data: {\"id\":\"chunk-2\",\"choices\":[{\"delta\":{\"content\":\" world\"}}]}\n\n",
		"data: [DONE]\n\n",
	}, "")
	resp := &http.Response{
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	pc := NewPendingCapturer(1024)

	// disconnectAfterFlusher flips the failure flag only on the second
	// chunk so the first write still goes through. The capturer should
	// nonetheless receive every line from upstream.
	dw := &flusherAfterFirstDisconnectWriter{header: http.Header{}}
	outcome := StreamChatWithPendingCapture(context.Background(),
		dw,
		resp,
		"gpt-test",
		"gpt-test",
		NewNormalizer(),
		nil,
		false,
		nil,
		pc,
	)

	assert.False(t, outcome.Interrupted)
	bodyCaptured, state, ok := pc.Snapshot()
	require.True(t, ok)
	assert.Equal(t, "completed", state.Status)
	assert.Equal(t, body, string(bodyCaptured))
	assert.Equal(t, 2, dw.writes, "detached serialized writer must suppress later network writes")
}

// TestStreamAnthropicSSEToOpenAI_DisconnectsKeepsCapturer drives the
// Q3 Anthropic→OpenAI bridge with a disconnecting client and verifies
// the capturer still receives the translated OpenAI chunks.
//
// Updated per §4.2 outcome classification analysis: the client disconnects
// after most chunks have been written (after write 10), so the upstream
// completes and the outcome.Reason is "client_disconnected" (not
// "client_write_failed"). The capturer receives the full stream including
// [DONE] because the upstream finished before the final write attempt failed.
func TestStreamAnthropicSSEToOpenAI_DisconnectsKeepsCapturer(t *testing.T) {
	body := strings.Join([]string{
		"event: message_start\n",
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-opus-4-8\",\"role\":\"assistant\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"type\":\"message\",\"usage\":{\"input_tokens\":14,\"output_tokens\":0}}}\n",
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
	resp := &http.Response{
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
	}
	pc := NewPendingCapturer(8192)

	// Client disconnects after 3 successful writes.
	// With pending capturer, the stream continues and outcome is "client_disconnected".
	writer := newDisconnectingStreamWriterAfter(3)
	outcome := StreamAnthropicSSEToOpenAI(context.Background(),
		writer,
		resp,
		"claude-opus-4-8",
		"claude-opus-4-8",
		"req-q3",
		nil,
		pc,
	)

	t.Logf("Total writes: %d, Interrupted: %v, Reason: %s", writer.WriteCount(), outcome.Interrupted, outcome.Reason)

	assert.True(t, outcome.Interrupted, "outcome should be interrupted when client disconnects")
	assert.Equal(t, "client_disconnected", outcome.Reason)
	assert.Equal(t, errorsx.KindCanceled, outcome.Kind)
	bodyCaptured, state, ok := pc.Snapshot()
	require.True(t, ok)
	assert.Equal(t, "completed", state.Status)
	got := string(bodyCaptured)
	assert.Contains(t, got, `"role":"assistant"`)
	assert.Contains(t, got, `data: [DONE]`)
}

// TestStreamAnthropicSSEToOpenAI_EarlyDisconnect verifies that when the
// client disconnects early (before the upstream completes), BUT with a
// pending capturer present, the outcome is still classified as
// "client_disconnected" because the capturer allows the upstream to
// complete fully. This validates that pending capturer enables graceful
// completion even after client disconnect.
//
// Per §4.2 outcome classification analysis: the presence of pending capturer
// changes the behavior - the stream continues reading from upstream until
// completion, so upstreamCompleted=true in applyClientDisconnectOutcome.
//
// Note: "client_write_failed" only occurs when the FIRST write fails (headers
// not sent, ChunkCount=0). See TestStreamChatWithPendingCapture_DisconnectsWithoutPending_ReturnsEarly.
// Once any content is written successfully, subsequent disconnects result in
// "client_disconnected" (if upstream completes) because the decision is based
// on whether the upstream finished, not when the client left.
func TestStreamAnthropicSSEToOpenAI_EarlyDisconnect(t *testing.T) {
	body := strings.Join([]string{
		"event: message_start\n",
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-opus-4-8\",\"role\":\"assistant\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"type\":\"message\",\"usage\":{\"input_tokens\":14,\"output_tokens\":0}}}\n",
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
	resp := &http.Response{
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
	}
	pc := NewPendingCapturer(8192)

	// Client disconnects after only 2 successful writes (early in the stream).
	// However, because pending capturer is present, the stream continues
	// reading from upstream until completion. The outcome is "client_disconnected"
	// (not "client_write_failed") because the upstream completed successfully.
	outcome := StreamAnthropicSSEToOpenAI(context.Background(),
		newDisconnectingStreamWriterAfter(2),
		resp,
		"claude-opus-4-8",
		"claude-opus-4-8",
		"req-q3-early",
		nil,
		pc,
	)

	assert.True(t, outcome.Interrupted)
	assert.Equal(t, "client_disconnected", outcome.Reason,
		"with pending capturer, early disconnect still results in client_disconnected because upstream completes")
	assert.Equal(t, errorsx.KindCanceled, outcome.Kind)
	
	// The capturer receives the full stream because pending capturer allows
	// the upstream to complete even after client disconnect.
	bodyCaptured, state, ok := pc.Snapshot()
	require.True(t, ok)
	
	// The upstream completed, so state.Status should be "completed".
	assert.Equal(t, "completed", state.Status,
		"pending capturer allows upstream to complete despite early client disconnect")
	
	// The captured body should contain [DONE] because the stream completed.
	got := string(bodyCaptured)
	assert.Contains(t, got, `data: [DONE]`,
		"pending capturer receives full stream including [DONE] despite early client disconnect")
}

// TestStreamOpenAIToResponsesSSE_DisconnectsKeepsCapturer verifies that
// the Q3 OpenAI→Responses bridge keeps appending to the capturer when
// the client has gone away.
func TestStreamOpenAIToResponsesSSE_DisconnectsKeepsCapturer(t *testing.T) {
	body := strings.Join([]string{
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"},\"finish_reason\":null}]}\n\n",
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1,\"total_tokens\":4}}\n\n",
		"data: [DONE]\n\n",
	}, "")
	resp := &http.Response{
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	pc := NewPendingCapturer(8192)

	outcome := StreamOpenAIToResponsesSSE(context.Background(),
		newDisconnectingStreamWriter(),
		resp,
		"gpt-4o-mini",
		"gpt-4o-mini",
		"req-q3-responses",
		nil,
		pc,
	)

	assert.True(t, outcome.Interrupted)
	assert.Equal(t, "client_disconnected", outcome.Reason)
	assert.Equal(t, errorsx.KindCanceled, outcome.Kind)
	bodyCaptured, state, ok := pc.Snapshot()
	require.True(t, ok)
	assert.Equal(t, "completed", state.Status)
	got := string(bodyCaptured)
	assert.Contains(t, got, `event: response.created`)
	assert.Contains(t, got, `event: response.completed`)
}

func TestStreamOpenAIToAnthropicSSE_DisconnectsKeepsCapturer(t *testing.T) {
	body := strings.Join([]string{
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-test\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n",
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-test\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
		"data: [DONE]\n\n",
	}, "")
	resp := &http.Response{
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	pc := NewPendingCapturer(8192)

	outcome := StreamOpenAIToAnthropicSSE(context.Background(),
		newDisconnectingStreamWriter(), resp, "claude-test", "gpt-test", "req-q2", nil, pc,
	)

	assert.False(t, outcome.Interrupted)
	captured, state, ok := pc.Snapshot()
	require.True(t, ok)
	assert.Equal(t, "completed", state.Status)
	assert.Contains(t, string(captured), "event: message_start")
	assert.Contains(t, string(captured), "event: message_stop")
	assert.NotContains(t, string(captured), `\"choices\"`)
}

// the first chunk goes through. Used to exercise the "client goes away
// mid-stream" path while still allowing the upstream read loop to finish.
type flusherAfterFirstDisconnectWriter struct {
	header http.Header
	writes int
}

func (w *flusherAfterFirstDisconnectWriter) Header() http.Header { return w.header }

func (w *flusherAfterFirstDisconnectWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == 1 {
		return len(p), nil
	}
	return 0, errors.New("client disconnected")
}

func (w *flusherAfterFirstDisconnectWriter) WriteHeader(int) {}
func (w *flusherAfterFirstDisconnectWriter) Flush()          {}
