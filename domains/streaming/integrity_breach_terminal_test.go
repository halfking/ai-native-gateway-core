package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── shared integrity-breach test scaffolding ───────────────────────────────

// breachObserver is a minimal audit.StreamTextObserver that latches a breach
// the first time its predicate matches the observed assistant text. Mirrors
// the audit package's fakeTextObserver (not importable from here without a
// test-only cycle).
type breachObserver struct {
	mu     sync.Mutex
	breach bool
	match  func(s string) bool
}

func (b *breachObserver) ObserveText(s string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.match(s) {
		b.breach = true
	}
	return b.breach
}

func (b *breachObserver) BreachReason() string { return "integrity_repeated_content" }

func (b *breachObserver) RepeatedContentHash() (string, int, int, int, bool) {
	return "", 0, 0, 0, false
}

func (b *breachObserver) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.breach = false
}

// sseServer (diagnostic_context_test.go) serves body as an SSE response.

// ─── #3 chat protocol: committed breach → finish_reason=length + [DONE] ─────

func TestStreamChatCommittedIntegrityBreachEmitsChatTerminal(t *testing.T) {
	upstream := strings.Join([]string{
		`data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"hello world"}}]}` + "\n\n",
		`data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"LOOPLOOP"}}]}` + "\n\n",
		`data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n",
		"data: [DONE]\n\n",
	}, "")
	capture := audit.NewStreamCapture()
	capture.SetTextObserver(&breachObserver{match: func(s string) bool {
		return strings.Contains(s, "LOOP")
	}})

	resp := sseServer(t, upstream)
	rec := httptest.NewRecorder()
	out := StreamChatWithCapture(context.Background(), rec, resp, "m", "m", nil, capture)

	require.True(t, out.Interrupted)
	assert.Equal(t, "integrity_repeated_content", out.Reason)

	body := rec.Body.String()
	// The committed client must receive a well-formed chat.completions ending:
	// exactly one length finish_reason frame followed by [DONE].
	assert.Contains(t, body, `"finish_reason":"length"`)
	assert.Contains(t, body, "data: [DONE]\n\n")
	assert.Equal(t, 1, strings.Count(body, `"finish_reason":"length"`),
		"exactly one interrupted-tail terminal expected, body=%q", body)
	// The breaching chunk itself must not leak.
	assert.NotContains(t, body, "LOOPLOOP")
}

// ─── #3 chat protocol: uncommitted breach → outcome-only, no terminal ───────

func TestStreamChatUncommittedIntegrityBreachStaysSilent(t *testing.T) {
	upstream := strings.Join([]string{
		`data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"LOOPLOOP"}}]}` + "\n\n",
		"data: [DONE]\n\n",
	}, "")
	capture := audit.NewStreamCapture()
	capture.SetTextObserver(&breachObserver{match: func(s string) bool {
		return strings.Contains(s, "LOOP")
	}})

	resp := sseServer(t, upstream)
	rec := httptest.NewRecorder()
	out := StreamChatWithCapture(context.Background(), rec, resp, "m", "m", nil, capture)

	require.True(t, out.Interrupted)
	// Nothing reached the wire → transparent failover stays available.
	assert.True(t, out.Resumable)

	// Regression pin: an uncommitted attempt must stay byte-silent so the
	// survival coordinator can discard the buffer — no terminal frames.
	body := rec.Body.String()
	assert.NotContains(t, body, `"finish_reason"`)
	assert.NotContains(t, body, "data: [DONE]")
}

// ─── #3 anthropic protocol: committed breach → message_stop=max_tokens ──────

func TestStreamOpenAIToAnthropicCommittedIntegrityBreachEmitsAnthropicTail(t *testing.T) {
	upstream := strings.Join([]string{
		`data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"hello world"}}]}` + "\n\n",
		`data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"LOOPLOOP"}}]}` + "\n\n",
		`data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n",
		"data: [DONE]\n\n",
	}, "")
	capture := audit.NewStreamCapture()
	capture.SetTextObserver(&breachObserver{match: func(s string) bool {
		return strings.Contains(s, "LOOP")
	}})

	resp := sseServer(t, upstream)
	rec := httptest.NewRecorder()
	out := StreamOpenAIToAnthropicSSE(context.Background(), rec, resp, "m", "m", "req-anth breach", capture, nil)

	require.True(t, out.Interrupted)
	assert.Equal(t, "integrity_repeated_content", out.Reason)

	body := rec.Body.String()
	// The committed client must receive the Anthropic protocol terminal:
	// message_delta with stop_reason=max_tokens (mapAnthropicStopReason("length"))
	// followed by message_stop.
	assert.Contains(t, body, "event: message_delta")
	assert.Contains(t, body, `"stop_reason":"max_tokens"`)
	assert.Contains(t, body, "event: message_stop")
}

// ─── #3 anthropic protocol: uncommitted breach → outcome-only ────────────────

func TestStreamOpenAIToAnthropicUncommittedIntegrityBreachStaysSilent(t *testing.T) {
	// The <think> prefix forces the text accumulator into buffering mode, so
	// the breaching delta is held bridge-side and never reaches the gate —
	// the attempt stays uncommitted (chunkCount alone does not commit).
	upstream := strings.Join([]string{
		`data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"<think>LOOPLOOP"}}]}` + "\n\n",
		"data: [DONE]\n\n",
	}, "")
	capture := audit.NewStreamCapture()
	capture.SetTextObserver(&breachObserver{match: func(s string) bool {
		return strings.Contains(s, "LOOP")
	}})

	resp := sseServer(t, upstream)
	rec := httptest.NewRecorder()
	out := StreamOpenAIToAnthropicSSE(context.Background(), rec, resp, "m", "m", "req-anth silent", capture, nil)

	require.True(t, out.Interrupted)
	// Regression pin: uncommitted breach renders no protocol terminal —
	// the pre-declared scaffolding is discardable for transparent failover.
	body := rec.Body.String()
	assert.NotContains(t, body, "message_stop")
	assert.NotContains(t, body, `"stop_reason":"max_tokens"`)
	assert.NotContains(t, body, "message_start", "uncommitted metadata must stay buffered")
}

// ─── #2 survival terminal latch: one attempt, one terminal ──────────────────

func TestChatInterruptedTailLatchesCoordinatorGate(t *testing.T) {
	rec := httptest.NewRecorder()
	gate := NewAttemptCommitGate(context.Background(), ProtocolOpenAIChat,
		NewSerializedStreamWriter(rec), GateOptions{Mode: GateModeBuffered})
	gw := NewGateWriterWithResponse(gate, rec)

	require.NoError(t, gate.WriteFrame("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"))
	require.True(t, gate.Committed())

	writeChatInterruptedTail(gw, nil, gate, nil)
	require.True(t, gate.TerminalRendered())
	body := rec.Body.String()
	assert.Equal(t, 1, strings.Count(body, `"finish_reason":"length"`))
	assert.Equal(t, 1, strings.Count(body, "data: [DONE]\n\n"))

	terminalCalls := 0
	(&SurvivalCoordinator{Terminal: func(TaskDecision, bool) { terminalCalls++ }}).renderTerminal(
		TaskDecision{Action: TaskActionResumeBlocked, Reason: "committed_output"}, gate, false)
	assert.Equal(t, 0, terminalCalls, "coordinator must not add a second Chat terminal")
}

func TestAnthropicInterruptedTailLatchesCoordinatorGate(t *testing.T) {
	rec := httptest.NewRecorder()
	gate := NewAttemptCommitGate(context.Background(), ProtocolAnthropic,
		NewSerializedStreamWriter(rec), GateOptions{Mode: GateModeBuffered})
	gw := NewGateWriterWithResponse(gate, rec)

	require.NoError(t, gate.WriteFrame("event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"))
	require.True(t, gate.Committed())

	writeAnthropicInterruptedTail(gw, gw, nil, gate, 1, "msg_test", "model", 0, 0, nil)
	require.True(t, gate.TerminalRendered())
	body := rec.Body.String()
	assert.Equal(t, 1, strings.Count(body, `"stop_reason":"max_tokens"`))
	assert.Equal(t, 1, strings.Count(body, "event: message_stop"))

	terminalCalls := 0
	(&SurvivalCoordinator{Terminal: func(TaskDecision, bool) { terminalCalls++ }}).renderTerminal(
		TaskDecision{Action: TaskActionResumeBlocked, Reason: "committed_output"}, gate, false)
	assert.Equal(t, 0, terminalCalls, "coordinator must not add an error after message_stop")
}

func TestInterruptedTailWriteFailureLeavesCoordinatorEligible(t *testing.T) {
	for _, tc := range []struct {
		name     string
		protocol ClientProtocol
		commit   string
		tail     func(http.ResponseWriter, *AttemptCommitGate)
	}{
		{
			name:     "chat",
			protocol: ProtocolOpenAIChat,
			commit:   "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n",
			tail: func(w http.ResponseWriter, gate *AttemptCommitGate) {
				writeChatInterruptedTail(w, nil, gate, nil)
			},
		},
		{
			name:     "anthropic",
			protocol: ProtocolAnthropic,
			commit:   "event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n",
			tail: func(w http.ResponseWriter, gate *AttemptCommitGate) {
				writeAnthropicInterruptedTail(w, w.(http.Flusher), nil, gate, 1, "msg_test", "model", 0, 0, nil)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := &trackingFlusher{}
			gate := NewAttemptCommitGate(context.Background(), tc.protocol,
				NewSerializedStreamWriter(wire), GateOptions{Mode: GateModeBuffered})
			gw := NewGateWriter(gate)
			require.NoError(t, gate.WriteFrame(tc.commit))
			require.True(t, gate.Committed())

			wire.fail = true
			tc.tail(gw, gate)
			assert.False(t, gate.TerminalRendered(), "partial terminal must not suppress fallback")

			terminalCalls := 0
			(&SurvivalCoordinator{Terminal: func(TaskDecision, bool) { terminalCalls++ }}).renderTerminal(
				TaskDecision{Action: TaskActionResumeBlocked, Reason: "committed_output"}, gate, false)
			assert.Equal(t, 1, terminalCalls, "coordinator must retain fallback authority")
		})
	}
}

func TestCommittedBreachRendersSingleResponsesTerminal(t *testing.T) {
	rec := httptest.NewRecorder()
	gate := NewAttemptCommitGate(context.Background(), ProtocolOpenAIResponses,
		NewSerializedStreamWriter(rec), GateOptions{Mode: GateModeImmediate})
	scaffold := newResponsesScaffold(rec, rec, "req-single-terminal", "test-model")

	// The bridge renders the interrupted terminal (committed breach).
	scaffold.finishInterrupted(gate, "partial output", "integrity_repeated_content", 3, 2)

	body := rec.Body.String()
	require.Contains(t, body, `"status":"incomplete"`)
	// #11: the interruption reason is passed through, not hardcoded.
	assert.Contains(t, body, `"incomplete_details":{"reason":"integrity_repeated_content"}`)
	assert.True(t, gate.TerminalRendered(), "bridge terminal must latch the gate")

	// The survival coordinator must NOT append a second terminal
	// (response.failed after completed(incomplete) = protocol breach).
	terminalCalls := 0
	c := &SurvivalCoordinator{Terminal: func(d TaskDecision, committed bool) {
		terminalCalls++
	}}
	c.renderTerminal(TaskDecision{Action: TaskActionResumeBlocked, Reason: "committed_output"}, gate, false)
	assert.Equal(t, 0, terminalCalls,
		"renderTerminal must be suppressed after the bridge rendered the terminal")
	assert.NotContains(t, rec.Body.String(), "response.failed")
}

func TestRenderTerminalStillFiresForUnlatchedGate(t *testing.T) {
	// The l2_alignment_miss path renders through a VOIDED replay gate whose
	// latch is never set — behavior must be unchanged (terminal still fires).
	rec := httptest.NewRecorder()
	gate := NewAttemptCommitGate(context.Background(), ProtocolOpenAIResponses,
		NewSerializedStreamWriter(rec), GateOptions{Mode: GateModeBuffered})
	require.False(t, gate.TerminalRendered())

	var gotCommitted *bool
	c := &SurvivalCoordinator{Terminal: func(d TaskDecision, committed bool) {
		gotCommitted = &committed
	}}
	c.renderTerminal(TaskDecision{Action: TaskActionResumeBlocked, Reason: "l2_alignment_miss"}, gate, true)
	require.NotNil(t, gotCommitted, "Terminal seam must still fire for an unlatched gate")
	assert.True(t, *gotCommitted, "out-of-band clientCommitted must be preserved")
}

// ─── #10 pendingArgs bound: nameless arg accumulator over 1 MiB ──────────────

func namelessArgsFrame(args string) string {
	return `data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"type":"function","function":{"name":"","arguments":"` + args + `"}}]}}]}` + "\n\n"
}

func TestAnthropicStreamPendingArgsOverflowUncommittedOutcomeOnly(t *testing.T) {
	// Nameless fragments only: nothing semantic ever reaches the client, so
	// the attempt stays uncommitted → structured outcome WITHOUT terminal.
	var b strings.Builder
	for i := 0; i < 70; i++ { // 70 × 16KiB ≈ 1.12 MiB > 1 MiB cap
		b.WriteString(namelessArgsFrame(strings.Repeat("A", 16*1024)))
	}
	b.WriteString("data: [DONE]\n\n")
	upstream := b.String()

	resp := sseServer(t, upstream)
	rec := httptest.NewRecorder()
	out := StreamOpenAIToAnthropicSSE(context.Background(), rec, resp, "m", "m", "req-overflow", nil, nil)

	require.True(t, out.Interrupted)
	assert.Equal(t, "anthropic_stream_arg_accumulator_limit", out.Reason)
	assert.Equal(t, errorsx.KindConversion, out.Kind)
	assert.False(t, out.Resumable, "conversion overflow must not silently retry the same attempt")

	// Uncommitted → outcome-only: no protocol terminal on the wire.
	body := rec.Body.String()
	assert.NotContains(t, body, "message_stop")
	assert.NotContains(t, body, `"stop_reason":"max_tokens"`)
}

func TestAnthropicStreamPendingArgsOverflowCommittedGetsTail(t *testing.T) {
	var b strings.Builder
	// A content delta commits the attempt first.
	b.WriteString(`data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"hello world"}}]}` + "\n\n")
	// Then the runaway nameless argument accumulator blows the cap.
	for i := 0; i < 70; i++ { // 70 × 16KiB ≈ 1.12 MiB > 1 MiB cap
		b.WriteString(namelessArgsFrame(strings.Repeat("A", 16*1024)))
	}
	b.WriteString("data: [DONE]\n\n")
	upstream := b.String()

	resp := sseServer(t, upstream)
	rec := httptest.NewRecorder()
	out := StreamOpenAIToAnthropicSSE(context.Background(), rec, resp, "m", "m", "req-overflow-committed", nil, nil)

	require.True(t, out.Interrupted)
	assert.Equal(t, "anthropic_stream_arg_accumulator_limit", out.Reason)
	assert.Equal(t, errorsx.KindConversion, out.Kind)

	// Committed → the client still gets the Anthropic protocol terminal.
	body := rec.Body.String()
	assert.Contains(t, body, "event: message_stop")
	assert.Contains(t, body, `"stop_reason":"max_tokens"`)
}
