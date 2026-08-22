package streaming

import (
	"bufio"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin the gate-aware boundary contract for three failure
// sites that previously hardcoded Resumable=true. After the third-round
// fix:
//
//  1. stream_timeout (stream.go:900) is gate-aware: a chunk-timeout
//     AFTER the client already saw semantic output must NOT be
//     transparently retried.
//
//  2. stream_chunk_timeout in the Anthropic passthrough
//     (anthropic_bridge.go:285) follows the same rule.
//
//  3. The mid-loop clientWriteFailure closure (stream.go:557-570) marks
//     Resumable=false unconditionally: client disconnect is permanent
//     and a transparent retry cannot write headers to a dead
//     connection.
//
// All three are part of the "client connection is preserved across
// supplier node switches" guarantee — the system must not waste an
// upstream call on a path that cannot possibly succeed.

// hangingBody yields data once, then blocks indefinitely on the next
// Read so the streamChunkTimeout path can fire.
type hangingBody struct {
	data []byte
	done bool
}

func (h *hangingBody) Read(p []byte) (int, error) {
	if !h.done {
		h.done = true
		return copy(p, h.data), nil
	}
	time.Sleep(10 * time.Second) // exceed chunk timeout in tests
	return 0, errors.New("upstream hang")
}

func (h *hangingBody) Close() error { return nil }

// TestStreamChat_StreamTimeoutGateAware covers the chat-path stream_timeout
// branch (stream.go:900). After one committed content chunk the upstream
// stops producing data; the chunk-timeout fires; the gate must mark the
// attempt non-resumable so the executor fails the task rather than
// duplicating the committed bytes on another supplier.
func TestStreamChat_StreamTimeoutGateAware(t *testing.T) {
	t.Setenv("LLM_GATEWAY_STREAM_CHUNK_TIMEOUT", "1")

	body := &hangingBody{
		data: []byte("data: {\"id\":\"chunk-1\",\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n"),
	}
	resp := &http.Response{
		Body:    body,
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	rec := httptest.NewRecorder()

	outcome := StreamChatWithPendingCapture(
		rec, resp, "gpt-test", "gpt-test",
		NewNormalizer(), nil, false, nil, nil,
	)

	require.True(t, outcome.Interrupted)
	require.Equal(t, "stream_timeout", outcome.Reason)
	assert.Greater(t, outcome.ChunkCount, 0, "at least one content chunk must have reached the recorder")
	assert.False(t, outcome.Resumable,
		"stream_timeout after committed content must NOT be transparently retried")
	assert.Contains(t, rec.Body.String(), "hello", "the first chunk must already be on the wire")
}

// TestStreamAnthropicPassthrough_StreamChunkTimeoutGateAware covers the
// Anthropic passthrough stream_chunk_timeout branch
// (anthropic_bridge.go:285). Same gate-aware rule: a per-chunk timeout
// after the gate committed content must block transparent retry.
func TestStreamAnthropicPassthrough_StreamChunkTimeoutGateAware(t *testing.T) {
	t.Setenv("LLM_GATEWAY_STREAM_CHUNK_TIMEOUT", "1")

	body := &hangingBody{
		data: []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"),
	}
	resp := &http.Response{
		Body:    body,
		Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
	}
	rec := httptest.NewRecorder()

	outcome := StreamAnthropicPassthrough(rec, resp, "claude-test", "claude-test", "req-chunk-to", nil, nil)

	require.True(t, outcome.Interrupted)
	require.Equal(t, "stream_chunk_timeout", outcome.Reason)
	assert.Greater(t, outcome.ChunkCount, 0)
	assert.False(t, outcome.Resumable,
		"stream_chunk_timeout after committed content must NOT be transparently retried")
	assert.Contains(t, rec.Body.String(), "hello")
}

// TestStreamChat_ClientWriteFailureClosureNotResumable covers the
// loop-level clientWriteFailure closure (stream.go:557-570). The client
// disconnects after a content chunk has been buffered/written; the
// closure fires and must mark the outcome non-resumable regardless of
// gate commit state. The dead-connection retry is a pure wasted
// upstream call.
func TestStreamChat_ClientWriteFailureClosureNotResumable(t *testing.T) {
	body := strings.Join([]string{
		"data: {\"id\":\"chunk-1\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n",
		"data: [DONE]\n\n",
	}, "")
	resp := &http.Response{
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}

	// flusherAfterFirstDisconnectWriter: first Write succeeds, every
	// subsequent one fails. Defined in pending_disconnect_extra_test.go.
	dw := &flusherAfterFirstDisconnectWriter{header: http.Header{}}

	outcome := StreamChatWithPendingCapture(
		dw, resp, "gpt-test", "gpt-test",
		NewNormalizer(), nil, false, nil, nil,
	)

	require.True(t, outcome.Interrupted)
	require.Equal(t, "client_write_failed", outcome.Reason)
	assert.Equal(t, 0, outcome.ChunkCount,
		"the closure fires before the per-chunk counter is incremented; the recorder writer does not increase ChunkCount via the bridge path")
	assert.False(t, outcome.Resumable,
		"client disconnect is permanent; transparent retry cannot write headers to a dead connection")
	assert.GreaterOrEqual(t, dw.writes, 1, "at least the first chunk write must succeed before the disconnect")
}

// _ = bufio import silences the unused-import guard if the file shrinks
// to nothing in a future refactor.
var _ = bufio.NewReader