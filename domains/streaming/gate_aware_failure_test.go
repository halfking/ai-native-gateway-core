package streaming

import (
	"bufio"
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

// hangingBody yields data once, then blocks on the next Read so the
// streamChunkTimeout path can fire.
//
// ★ 2026-10-30（耗时治理轮）：阻塞用 channel 而不是 time.Sleep —— 因为
// sse.LineReader.ReadLineWithContext（internal/sse/line_reader.go:122-129）
// 在超时后走的是「closer.Close() → 再 `<-results` 等读协程退出」这条路径。
// ⇒ 若 Close() 不解除 Read 的阻塞，那个读协程就要睡满整个 sleep 才退出
// ⇒ **超时设成 1s 也救不了**：测试仍会等满 sleep 时长。
// ★ 实测（修复前）：两个 gate-aware 测试各耗 10.00s ⇒ 全包 163.6s 里
// 它们的占比来自这个 sleep，而不是来自被测的超时门。
// ★ 现在 Close() 关掉 channel ⇒ 读协程立刻返回 ⇒ 测试耗时由「sleep 时长」
// 降到「chunk timeout 时长」（测试里是 1s）。
type hangingBody struct {
	data []byte
	done bool
	// unblock is closed by Close to release a blocked Read.
	//
	// ★ nil 是**合法取值**：本类型被 domains/streaming 的多个测试文件复用
	//   （r59_audit_regressions_test.go 等），那些构造点只填 data、不填本字段
	//   ⇒ nil channel 上 close 会 panic（"close of nil channel"）。
	//   ⇒ Read/Close 两处都必须按 nil 走「不阻塞」分支。
	unblock chan struct{}
}

func (h *hangingBody) Read(p []byte) (int, error) {
	if !h.done {
		h.done = true
		return copy(p, h.data), nil
	}
	if h.unblock != nil {
		<-h.unblock // released by Close, so the timeout path is not blocked
	}
	return 0, errors.New("upstream hang")
}

// Close releases a blocked Read. A no-op Close would make the reader
// goroutine outlive the timeout by the full sleep duration.
//
// ★ nil-safe + 幂等：unblock 为 nil 时直接返回；已关闭时不再重复 close。
func (h *hangingBody) Close() error {
	if h.unblock == nil {
		return nil
	}
	select {
	case <-h.unblock:
	default:
		close(h.unblock)
	}
	return nil
}

// TestStreamChat_StreamTimeoutGateAware covers the chat-path stream_timeout
// branch (stream.go:900). After one committed content chunk the upstream
// stops producing data; the chunk-timeout fires; the gate must mark the
// attempt non-resumable so the executor fails the task rather than
// duplicating the committed bytes on another supplier.
func TestStreamChat_StreamTimeoutGateAware(t *testing.T) {
	t.Setenv("LLM_GATEWAY_STREAM_CHUNK_TIMEOUT", "1")

	body := &hangingBody{
		data:    []byte("data: {\"id\":\"chunk-1\",\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n"),
		unblock: make(chan struct{}),
	}
	resp := &http.Response{
		Body:    body,
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	rec := httptest.NewRecorder()

	outcome := StreamChatWithPendingCapture(context.Background(),
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
		data:    []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"),
		unblock: make(chan struct{}),
	}
	resp := &http.Response{
		Body:    body,
		Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
	}
	rec := httptest.NewRecorder()

	outcome := StreamAnthropicPassthrough(context.Background(), rec, resp, "claude-test", "claude-test", "req-chunk-to", nil, nil)

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

	outcome := StreamChatWithPendingCapture(context.Background(),
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
