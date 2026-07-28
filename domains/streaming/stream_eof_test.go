package streaming

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStreamChatWithPendingCapture_EOFWithoutDoneAppendsDone(t *testing.T) {
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(
			"data: {\"id\":\"chunk-1\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n",
		)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	writer := httptest.NewRecorder()

	outcome := StreamChatWithPendingCapture(
		writer,
		resp,
		"minimax-m3",
		"MiniMax-M3",
		NewNormalizer(),
		nil,
		false,
		nil,
		nil,
	)

	assert.True(t, outcome.Interrupted)
	assert.Equal(t, "eof_without_done", outcome.Reason)
	assert.Equal(t, 2, outcome.ChunkCount)
	assert.Contains(t, writer.Body.String(), `"content":"hello"`)
	assert.True(t, strings.HasSuffix(writer.Body.String(), "data: [DONE]\n\n"))
}

// TestStreamChatWithPendingCapture_EOFWithoutDoneZeroChunks is the
// regression guard for the failure path: upstream closes mid-stream
// with chunks already delivered but then terminates without [DONE]
// after a parse-failure line. executor_chat.go isBenignEOF returns
// false here (chunks == 0), so the request is a real failure — but
// the same "eof_without_done" detail code is captured. The error_kind
// column must remain "eof_without_done" (NOT stream_read_error), per
// the 2026-07-29 decomposition in handler.go.
//
// We use a non-SSE line to drive chunkCount=0 (line is not parsed as
// a chunk), then EOF without [DONE]. This isolates the
// "eof_without_done + 0 valid chunks" path that previously fell into
// the stream_read_error bucket.
func TestStreamChatWithPendingCapture_EOFWithoutDoneZeroChunks(t *testing.T) {
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(
			"data: not-valid-json\n\n",
		)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	writer := httptest.NewRecorder()

	outcome := StreamChatWithPendingCapture(
		writer,
		resp,
		"minimax-m3",
		"MiniMax-M3",
		NewNormalizer(),
		nil,
		false,
		nil,
		nil,
	)

	assert.True(t, outcome.Interrupted)
	assert.Equal(t, "eof_without_done", outcome.Reason)
	// 2026-07-29: error_kind must equal detail_code (not stream_read_error)
	// so operator dashboards can distinguish a real empty-body failure
	// from a generic read error.
	assert.Equal(t, "eof_without_done", streamErrorKindForDetailCode(outcome.Reason))
	// Synthesised [DONE] must still be appended so clients don't hang.
	assert.True(t, strings.HasSuffix(writer.Body.String(), "data: [DONE]\n\n"))
}
