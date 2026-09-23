package streaming

// R59 48h audit round (2026-09-23) regression钉桩.
//
// Covers the S1 findings fixed this round:
//
//	F2 — responses bridge benign EOF after finish_reason (chat-bridge
//	     parity, d8a7849fd shape): must complete the turn, not fail it
//	     as eof_without_done.
//	F3 — committed stream_timeout frame: root-level code/reason/retryable
//	     (client frame-root reader parity) + synthesized [DONE] so strict
//	     SDK parsers finalize instead of hanging after the blackhole
//	     latch suppresses every later write.
//	F4 — anthropic→openai bridge upstream error-event render point latches
//	     TerminalRendered (mirrors the already-latched chunk_timeout /
//	     stream_read_error branches).
//
// Sensitivity discipline (154 critical-audit R1/R2): each test asserts
// behaviour the pre-fix code did NOT produce; reverting the fix must turn
// the test red.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F2: OpenAI→responses bridge, finish_reason chunk then EOF without [DONE].
func TestResponsesBridges_BenignEOFAfterFinishReasonCompletes(t *testing.T) {
	isolateResponsesRuntimeConfig(t)

	body := "data: {\"id\":\"chunk-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"tool args\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chunk-2\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"

	resp := &http.Response{
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/responses", nil),
	}
	rec := httptest.NewRecorder()

	out := StreamOpenAIToResponsesSSE(context.Background(), rec, resp,
		"gpt-test", "gpt-test", "req-r59-benign-eof", nil, nil)

	assert.False(t, out.Interrupted,
		"finish_reason already received means the stream completed semantically — EOF without [DONE] must NOT fail the turn (chat-bridge d8a7849fd parity)")

	wire := rec.Body.String()
	assert.Contains(t, wire, "tool args", "committed content stays on the wire")
	assert.Contains(t, wire, "response.completed", "proper responses terminal must be rendered")
	assert.Contains(t, wire, `"status":"completed"`,
		"benign close completes normally — not the incomplete shape of the failure path")
	assert.NotContains(t, wire, "eof_without_done",
		"benign close must not carry the eof_without_done failure classification")
}

// F3: committed-output stream_timeout frame carries root fields + [DONE].
func TestStreamChatWithPendingCapture_CommittedTimeoutFrameCarriesDoneAndRootFields(t *testing.T) {
	t.Setenv("LLM_GATEWAY_STREAM_CHUNK_TIMEOUT", "1")

	counter := &countingRecorder{delegate: metrics.NewNoopRecorder()}
	prev := metrics.Global()
	metrics.SetGlobal(counter)
	t.Cleanup(func() { metrics.SetGlobal(prev) })

	body := &hangingBody{
		data: []byte("data: {\"id\":\"chunk-1\",\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n"),
	}
	resp := &http.Response{
		Body:    body,
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	rec := httptest.NewRecorder()

	outcome := StreamChatWithPendingCapture(context.Background(),
		rec, resp, "gpt-test", "gpt-test", NewNormalizer(), nil, false, nil, nil,
	)

	require.True(t, outcome.Interrupted)
	require.Equal(t, "stream_timeout", outcome.Reason)
	require.True(t, outcome.TerminalRendered)

	wire := rec.Body.String()
	require.Contains(t, wire, "hello", "committed chunk stays on the wire")
	// Root-level duplication: the client reads code/reason/retryable from
	// the frame root (df60575b evidence), not from inside the error object.
	require.Contains(t, wire, `},"code":"stream_timeout","reason":"stream_timeout","retryable":true}`,
		"timeout envelope must duplicate code/reason/retryable at the frame root (shape parity with eof_without_done)")
	require.Contains(t, wire, "data: [DONE]\n\n",
		"synthesized [DONE] must follow the timeout frame — the blackhole latch suppresses every later write, so without it strict SDK parsers hang")
	assert.Equal(t, 1, counter.synth, "RecordStreamSynthesizedDone fires for the synthesized terminator")
}

// F4: anthropic→openai bridge upstream error-event branch latches
// TerminalRendered after writing its error chunk + [DONE].
func TestStreamAnthropicSSEToOpenAI_ErrorEventAfterCommitLatchesTerminal(t *testing.T) {
	// Immediate gate mode: the content delta commits on write, mirroring the
	// committed-output state the live double-terminal needed. In the default
	// buffered mode the held delta is discarded pre-commit and the
	// interruption stays transparently retryable (no frame, no latch).
	restore := setAttemptGateForTest(true, GateModeImmediate)
	defer restore()

	anthropicSSE := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"test-model\",\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"committed\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: error\n" +
		"data: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"upstream failed\"}}\n\n"

	rec := httptest.NewRecorder()
	resp := &http.Response{
		Body:    io.NopCloser(strings.NewReader(anthropicSSE)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}

	out := StreamAnthropicSSEToOpenAI(context.Background(), rec, resp,
		"gpt-5", "claude-sonnet-5", "req-r59-err-latch", nil, nil)

	require.True(t, out.Interrupted)
	require.True(t, out.TerminalRendered,
		"the bridge wrote an error chunk + [DONE] — the terminal latch must land on the outcome so the dispatch funnel wraps the sentinel and the handler blackholes any second terminal")

	wire := rec.Body.String()
	assert.Contains(t, wire, "committed", "committed content stays on the wire")
	assert.Contains(t, wire, `"code":"api_error"`, "sanitized error envelope is on the wire")
	assert.Contains(t, wire, "data: [DONE]\n\n", "error chunk carries its terminator")
}
