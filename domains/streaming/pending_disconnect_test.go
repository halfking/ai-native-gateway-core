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
	"github.com/stretchr/testify/require"
)

type disconnectingStreamWriter struct {
	header     http.Header
	writes     int
	failAfter  int // Fail after this many successful writes (0 = fail immediately)
}

func (w *disconnectingStreamWriter) Header() http.Header { return w.header }

func (w *disconnectingStreamWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes > w.failAfter {
		return 0, errors.New("client disconnected")
	}
	return len(p), nil
}

func (w *disconnectingStreamWriter) WriteHeader(int) {}
func (w *disconnectingStreamWriter) Flush()          {}

// WriteCount returns the number of Write calls made (for testing/debugging)
func (w *disconnectingStreamWriter) WriteCount() int {
	return w.writes
}

// newDisconnectingStreamWriter creates a writer that fails immediately on first write.
// Use newDisconnectingStreamWriterAfter for controlled disconnect timing.
func newDisconnectingStreamWriter() *disconnectingStreamWriter {
	return &disconnectingStreamWriter{header: http.Header{}, failAfter: 0}
}

// newDisconnectingStreamWriterAfter creates a writer that succeeds for the first
// n writes, then fails on the (n+1)th write. Use this to simulate client
// disconnects at specific points in the stream.
func newDisconnectingStreamWriterAfter(n int) *disconnectingStreamWriter {
	return &disconnectingStreamWriter{header: http.Header{}, failAfter: n}
}

func TestStreamChatWithPendingCaptureContinuesAfterClientDisconnect(t *testing.T) {
	body := strings.Join([]string{
		"data: {\"id\":\"chunk-1\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n",
		"data: [DONE]\n\n",
	}, "")
	resp := &http.Response{
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	pc := NewPendingCapturer(1024)

	outcome := StreamChatWithPendingCapture(context.Background(),
		newDisconnectingStreamWriter(),
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
	assert.Contains(t, string(bodyCaptured), `"content":"hello"`)
	assert.Contains(t, string(bodyCaptured), "data: [DONE]")
}

func TestPendingCapturer_OverflowFailsReplay(t *testing.T) {
	pc := NewPendingCapturer(8)
	pc.append("data: ok\n")
	pc.append("data: overflow\n")
	pc.finalize(StreamOutcome{})

	body, state, ok := pc.Snapshot()
	require.True(t, ok)
	assert.Equal(t, "failed", state.Status)
	assert.Equal(t, "pending_capture_overflow", state.ErrMessage)
	assert.True(t, state.Overflowed)
	assert.LessOrEqual(t, len(body), 8)
}
func TestStreamAnthropicPassthroughContinuesAfterClientDisconnect(t *testing.T) {
	body := strings.Join([]string{
		"event: message_start\n",
		"data: {\"type\":\"message_start\"}\n",
		"\n",
		"event: message_stop\n",
		"data: {\"type\":\"message_stop\"}\n",
		"\n",
	}, "")
	resp := &http.Response{
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
	}
	pc := NewPendingCapturer(1024)

	// P1-2: caller-supplied ctx is authoritative (was resp.Request.Context()).
	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()
	outcome := StreamAnthropicPassthrough(reqCtx,
		newDisconnectingStreamWriter(),
		resp,
		"claude-test",
		"claude-test",
		"request-1",
		nil,
		pc,
	)

	assert.False(t, outcome.Interrupted)
	bodyCaptured, state, ok := pc.Snapshot()
	require.True(t, ok)
	assert.Equal(t, "completed", state.Status)
	assert.Equal(t, body, string(bodyCaptured))
}
