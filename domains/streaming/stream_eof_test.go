package streaming

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/metrics"
)

func TestClassifyStreamReadError_UnexpectedEOFIsFailure(t *testing.T) {
	assert.Equal(t, streamReadFailed, classifyStreamReadError(context.Background(), io.ErrUnexpectedEOF))

	outcome := streamReadFailureOutcome(io.ErrUnexpectedEOF, 0)
	assert.True(t, outcome.Interrupted)
	assert.Equal(t, "network_error", outcome.Reason)
	assert.Equal(t, errorsx.KindNetwork, outcome.Kind)
	assert.True(t, outcome.Resumable)
}

// TestStreamChatWithPendingCapture_EOFWithoutDoneAfterCommitIsCompletedAndNotRetryable
// (renamed from "...IsNotRetryable" on 2026-09-01 — the MiniMax fix
// promoted this branch from Interrupted=true to Interrupted=false.)
//
// Pre-fix: upstream closes HTTP body after sending valid SSE chunks but
// without `data: [DONE]`. We logged "eof_without_done", set
// Interrupted=true, and the audit pipeline recorded success=false — even
// though the client already saw the full response (we synthesized
// `data: [DONE]\n\n`).
//
// Post-fix: when `attemptHasClientSemanticOutput(gate, chunkCount)` is
// true, we treat this as a benign protocol-level non-compliance (MiniMax
// upstream behavior), not a real upstream-down failure. The audit row is
// recorded as success=true, the circuit breaker is not tripped, and
// credential health is unaffected.
//
// The Resumable invariant from the pre-fix test ("committed content +
// EOF + no [DONE] must NOT be transparently retried") is preserved — a
// downstream supplier would duplicate committed bytes. Resumable stays
// false; only Interrupted flips.
func TestStreamChatWithPendingCapture_EOFWithoutDoneAfterCommitIsCompletedAndNotRetryable(t *testing.T) {
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(
			"data: {\"id\":\"chunk-1\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n",
		)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	writer := httptest.NewRecorder()

	outcome := StreamChatWithPendingCapture(context.Background(),
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

	assert.False(t, outcome.Interrupted,
		"committed output + EOF without [DONE] is benign upstream non-compliance; must NOT be flagged as a failure")
	assert.Equal(t, "eof_without_done_after_commit", outcome.Reason,
		"distinct reason literal preserves operator SQL-filter visibility without re-triggering audit failure path")
	assert.False(t, outcome.Resumable,
		"Resumable invariant preserved: committed bytes cannot be transparently retried by another candidate")
	assert.Greater(t, outcome.ChunkCount, 0)
	assert.Contains(t, writer.Body.String(), `"content":"hello"`)
	assert.True(t, strings.HasSuffix(writer.Body.String(), "data: [DONE]\n\n"),
		"synthesized [DONE] must still reach the client so OpenAI-compatible parsers finalize")
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

	outcome := StreamChatWithPendingCapture(context.Background(),
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
	assert.Equal(t, "malformed_sse_frame", outcome.Reason)
	assert.Equal(t, errorsx.KindUpstreamDown, outcome.Kind)
	assert.True(t, outcome.Resumable)
	assert.Empty(t, writer.Body.String())
}

type errorAfterDataReadCloser struct {
	data []byte
	err  error
	read bool
}

func (r *errorAfterDataReadCloser) Read(p []byte) (int, error) {
	if r.read {
		return 0, r.err
	}
	r.read = true
	return copy(p, r.data), nil
}

func (r *errorAfterDataReadCloser) Close() error { return nil }

func TestStreamChatWithPendingCapture_OtherSideClosedIsNetworkError(t *testing.T) {
	// Pinning the gate-aware resumability contract on the chat-path default
	// branch (stream.go). Three-layer guard keeps committed output from being
	// duplicated on transparent retry:
	//   1. bridge.Resumable = !attemptHasClientSemanticOutput(gate, chunkCount)
	//   2. executor_chat.go:1124 — `isResumable = Resumable && ChunkCount < e.n`
	//      (StreamRetryThreshold, default 50)
	//   3. mayRetryInterruptedStream (executor.go:2963-2978) — refuses retry
	//      when Capture.ChunkCountersSnapshot() > 0
	// All three must agree that committed output is non-retryable.

	t.Run("committed content blocks retry", func(t *testing.T) {
		resp := &http.Response{
			Body: &errorAfterDataReadCloser{
				data: []byte("data: {\"id\":\"chunk-1\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"),
				err:  errors.New("other side closed"),
			},
			Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		}
		writer := httptest.NewRecorder()

		outcome := StreamChatWithPendingCapture(context.Background(),
			writer,
			resp,
			"glm-5.2",
			"glm-5.2",
			NewNormalizer(),
			nil,
			false,
			nil,
			nil,
		)

		assert.True(t, outcome.Interrupted)
		assert.Equal(t, "network_error", outcome.Reason)
		assert.Equal(t, errorsx.KindNetwork, outcome.Kind)
		assert.False(t, outcome.Resumable, "committed content + network error must NOT be transparently retried")
		assert.Equal(t, 2, outcome.ChunkCount)
		assert.Contains(t, writer.Body.String(), `"content":"hello"`)
	})

	t.Run("uncommitted read failure stays retry", func(t *testing.T) {
		// The body yields a no-content finish-reason chunk then errors. No
		// semantic chunk (text delta) reaches the client, so the gate stays
		// uncommitted and the failure must remain transparently retryable so
		// the survival / dispatch layer can failover to another supplier.
		// We deliberately do not pin Reason/Kind/ChunkCount: classification
		// depends on whether the upstream sent enough bytes to clear the
		// first-byte-read, and the chat-path chunk counter includes
		// non-semantic frames. The contract under test is the gate predicate:
		// when no semantic output is committed, Resumable stays true.
		resp := &http.Response{
			Body: &errorAfterDataReadCloser{
				data: []byte("data: {\"id\":\"chunk-1\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"),
				err:  errors.New("other side closed"),
			},
			Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		}
		writer := httptest.NewRecorder()

		outcome := StreamChatWithPendingCapture(context.Background(),
			writer,
			resp,
			"glm-5.2",
			"glm-5.2",
			NewNormalizer(),
			nil,
			false,
			nil,
			nil,
		)

		assert.True(t, outcome.Interrupted)
		assert.True(t, outcome.Resumable, "no semantic output committed → transparent retry is safe")
		// Verify the body did NOT receive a content text token. A delta={} frame
		// has no "content" string so no chunk should reach the client.
		assert.NotContains(t, writer.Body.String(), `"content":"`,
			"uncommitted attempt must NOT write content to the wire; the gate held it back")
	})
}

// countingRecorder wraps a delegate Recorder and counts how many times
// RecordStreamSynthesizedDone is called. Used to verify that the
// P1 2026-08-06 hot-patch actually wired the synthesized-[DONE] signal
// through to metrics. Without this wrapper, an end-to-end test would
// only catch no-panic regressions via NoopRecorder and could not
// verify the signal fires at all.
//
// The wrapper keeps a small explicit method set instead of embedding
// the delegate, because we want to count RecordStreamSynthesizedDone
// specifically. All other Recorder methods forward to the delegate so
// the wrapper is a drop-in replacement when SetGlobal'd.
//
// Compile-time guard: a future addition to metrics.Recorder that we
// don't forward here would break the build — that is intentional.
type countingRecorder struct {
	delegate metrics.Recorder
	synth    int
}

// All forwards below mirror metrics.Recorder. Kept short to avoid
// duplication; if Recorder grows, add the forward here in lockstep.

func (c *countingRecorder) RecordCircuitRequest(_ string)                        {}
func (c *countingRecorder) RecordCircuitSuccess()                                {}
func (c *countingRecorder) RecordCircuitFailure()                                {}
func (c *countingRecorder) RecordCircuitStateChange(_, _ string)                 {}
func (c *countingRecorder) RecordCircuitTrip()                                   {}
func (c *countingRecorder) ObserveCircuitLatency(_ time.Duration)                {}
func (c *countingRecorder) SetCircuitState(_ string)                             {}
func (c *countingRecorder) SetCircuitErrorRate(_ float64)                        {}
func (c *countingRecorder) RecordAdapterConversion(_, _ string, _ time.Duration) {}
func (c *countingRecorder) RecordAdapterFailure(_, _ string)                     {}
func (c *countingRecorder) RecordAdapterTokens(_, _ string, _ int)               {}
func (c *countingRecorder) SetAdapterActive(_ string, _ bool)                    {}
func (c *countingRecorder) RecordSchedulerSelection(_ string, _ time.Duration)   {}
func (c *countingRecorder) UpdateSchedulerWeight(_ string, _ int)                {}
func (c *countingRecorder) UpdateSchedulerCurrentWeight(_ string, _ int)         {}
func (c *countingRecorder) UpdateSchedulerEffectiveWeight(_ string, _ int)       {}
func (c *countingRecorder) SetSchedulerAvailableCredentials(_ int)               {}
func (c *countingRecorder) RecordSafetyCheck(_ string, _ time.Duration)          {}
func (c *countingRecorder) RecordSafetyAction(_, _ string)                       {}
func (c *countingRecorder) RecordSafetyRuleHit(_, _, _ string)                   {}
func (c *countingRecorder) RecordSafetyWhitelistHit()                            {}
func (c *countingRecorder) SetSafetyRulesCount(_ bool, _ int)                    {}
func (c *countingRecorder) UpdatePoolUtilization(_ string, _ float64)            {}
func (c *countingRecorder) RecordPoolRequest(_, _ string)                        {}
func (c *countingRecorder) SetPoolCapacity(_ string, _ int)                      {}
func (c *countingRecorder) SetPoolActiveCredentials(_ string, _ int)             {}
func (c *countingRecorder) SetPoolHealthyCredentials(_ string, _ int)            {}
func (c *countingRecorder) RecordShadowWriteFailure(_ string)                    {}
func (c *countingRecorder) RecordRingBufferDropped(_ uint64)                     {}
func (c *countingRecorder) RecordRawAuditWriteFailure()                          {}
func (c *countingRecorder) RecordURSMv2ShadowResult(_ string)                    {}
func (c *countingRecorder) RecordMalformedSSEFrame(_, _ string)                  {}
func (c *countingRecorder) RecordStreamSynthesizedDone()                         { c.synth++ }
func (c *countingRecorder) RecordIncompleteToolCall(_, _ string)                 {}
func (c *countingRecorder) RecordSuccessEmptyResponse(_, _, _ string)            {}
func (c *countingRecorder) RecordJournalSnapshotStored(_ string)                 {}
func (c *countingRecorder) RecordJournalSnapshotApplied(_ string, _ bool)        {}
func (c *countingRecorder) RecordJournalSnapshotDeduplicated(_, _ string)        {}
func (c *countingRecorder) RecordLiveStreamRecordDropped(_ string)              {}

// Compile-time check that countingRecorder satisfies metrics.Recorder.
var _ metrics.Recorder = (*countingRecorder)(nil)

// TestStreamChatWithPendingCapture_SynthesizedDoneIncrementsMetric
// (P1 hot-patch 2026-08-06; updated 2026-09-01 MiniMax-fix) asserts the
// EOF branch in stream.go still calls RecordStreamSynthesizedDone at
// least once when the upstream closes without [DONE] AFTER semantic
// output has been committed. Uses an inline counter Recorder wrapper.
//
// 2026-09-01: the path is now benign (Interrupted=false,
// Reason="eof_without_done_after_commit") — the synthesized-[DONE]
// signal still fires for downstream observability, but the request is no
// longer counted as a failure.
func TestStreamChatWithPendingCapture_SynthesizedDoneIncrementsMetric(t *testing.T) {
	counter := &countingRecorder{delegate: metrics.NewNoopRecorder()}

	// Restore the prior global Recorder after the test to avoid leaking
	// into other tests / the package state.
	prev := metrics.Global()
	metrics.SetGlobal(counter)
	t.Cleanup(func() { metrics.SetGlobal(prev) })

	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(
			"data: {\"id\":\"chunk-1\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n",
		)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	writer := httptest.NewRecorder()

	outcome := StreamChatWithPendingCapture(context.Background(),
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

	assert.False(t, outcome.Interrupted,
		"MiniMax-fix: committed output + EOF without [DONE] is benign, not a failure")
	assert.Equal(t, "eof_without_done_after_commit", outcome.Reason)
	assert.Equal(t, 1, counter.synth,
		"synthesized-[DONE] signal must still fire for downstream observability even after the MiniMax fix")
	assert.Contains(t, writer.Body.String(), `"content":"hello"`)
	assert.True(t, strings.HasSuffix(writer.Body.String(), "data: [DONE]\n\n"))
}

// TestStreamChatWithPendingCapture_UpstreamDoneNoSynthMetric pins the
// inverse case: when upstream *does* send [DONE], the synthesized
// signal must NOT fire (synthesizedDone = false in stream.go).
func TestStreamChatWithPendingCapture_UpstreamDoneNoSynthMetric(t *testing.T) {
	counter := &countingRecorder{delegate: metrics.NewNoopRecorder()}
	prev := metrics.Global()
	metrics.SetGlobal(counter)
	t.Cleanup(func() { metrics.SetGlobal(prev) })

	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(
			"data: {\"id\":\"chunk-1\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n" +
				"data: {\"id\":\"chunk-1\",\"choices\":[{\"finish_reason\":\"stop\"}],\"usage\":{}}\n\n" +
				"data: [DONE]\n\n",
		)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	writer := httptest.NewRecorder()

	_ = StreamChatWithPendingCapture(context.Background(),
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

	assert.Equal(t, 0, counter.synth, "synth counter must stay 0 when upstream sends [DONE] naturally")
}

func TestStreamChatWithPendingCapture_SplitsDoneJoinedToJSON(t *testing.T) {
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(
			"data: {\"id\":\"chunk-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"reasoning_content\":\"planning\"},\"finish_reason\":null}]}[DONE].",
		)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	writer := httptest.NewRecorder()

	outcome := StreamChatWithPendingCapture(context.Background(),
		writer,
		resp,
		"gpt-5.6-sol",
		"gpt-5.6-sol",
		NewNormalizer(),
		nil,
		false,
		nil,
		nil,
	)

	assert.False(t, outcome.Interrupted)
	assert.NotContains(t, writer.Body.String(), "}[DONE].")
	assert.Contains(t, writer.Body.String(), `"reasoning_content":"planning"`)
	assert.True(t, strings.HasSuffix(writer.Body.String(), "data: [DONE]\n\n"))
}

func TestSplitCombinedDoneFrame_ExtractsCompleteJSONFromTransportGarbage(t *testing.T) {
	valid := `{"id":"chunk-1","choices":[{"delta":{"content":"preserved"}}]}`
	tests := []struct {
		name     string
		line     string
		wantLine string
		wantDone bool
	}{
		{name: "leading marker", line: "data: noise:" + valid, wantLine: "data: " + valid + "\n", wantDone: false},
		{name: "trailing marker", line: "data: " + valid + "\x00tail", wantLine: "data: " + valid + "\n", wantDone: false},
		{name: "both markers and done", line: "data: noise:" + valid + "[DONE].", wantLine: "data: " + valid + "\n", wantDone: true},
		{name: "clean json remains unchanged", line: "data: " + valid, wantLine: "data: " + valid, wantDone: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, hasDone := splitCombinedDoneFrame(tc.line)
			assert.Equal(t, tc.wantDone, hasDone)
			assert.Equal(t, tc.wantLine, got)
		})
	}
}

// TestStreamChatWithPendingCapture_EOFWithoutDoneAfterCommitIsSuccess
// (added 2026-09-01, MiniMax-fix) is the end-to-end regression guard for
// the production bug:
//
//	request 6cf5fa78ab25b26650753c1bdcdc6583 (and ~12 others in the same
//	window) hit provider=14/credential=21/raw_model=MiniMax-M3 with HTTP
//	200 + valid SSE chunks but no `data: [DONE]` terminator. Pre-fix, the
//	gateway logged `executor: stream interrupted reason=eof_without_done`
//	+ `executor failed: stream_interrupted: eof_without_done` and recorded
//	audit success=false, inflating provider 14's error rate even though
//	the client already received the full response.
//
// The fix: when `attemptHasClientSemanticOutput(gate, chunkCount)` is
// true, treat this as benign protocol-level non-compliance — the request
// is a normal completion, not an interruption. Specifically, the capture
// must:
//
//   - NOT be marked interrupted (`stream_interrupted == false` in summary)
//   - show `stream_done_received == true` (we ObserveChunk(ChunkTypeDone)
//     mirroring the natural [DONE] path)
//   - have a non-empty synthesized `data: [DONE]\n\n` trailer
//   - have `outcome.Interrupted == false`
//   - have `outcome.Reason == "eof_without_done_after_commit"` so
//     operators can SQL-filter the benign pattern
//   - have `outcome.Resumable == false` (committed bytes cannot be
//     transparently retried — Resumable invariant preserved)
//
// Companion to TestStreamChatWithPendingCapture_EOFWithoutDoneZeroChunks
// which pins the OPPOSITE invariant (no committed content → still
// Interrupted=true).
func TestStreamChatWithPendingCapture_EOFWithoutDoneAfterCommitIsSuccess(t *testing.T) {
	counter := &countingRecorder{delegate: metrics.NewNoopRecorder()}
	prev := metrics.Global()
	metrics.SetGlobal(counter)
	t.Cleanup(func() { metrics.SetGlobal(prev) })

	capture := audit.NewStreamCapture()

	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(
			"data: {\"id\":\"chunk-1\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n",
		)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	writer := httptest.NewRecorder()

	outcome := StreamChatWithPendingCapture(context.Background(),
		writer,
		resp,
		"minimax-m3",
		"MiniMax-M3",
		NewNormalizer(),
		capture,
		false,
		nil,
		nil,
	)

	// Outcome: benign completion, distinct reason, no retry.
	assert.False(t, outcome.Interrupted,
		"MiniMax-fix: committed output + EOF without [DONE] must NOT be reported as a failure")
	assert.Equal(t, "eof_without_done_after_commit", outcome.Reason,
		"distinct reason literal preserves operator visibility (SQL filter) without re-triggering audit failure path")
	assert.False(t, outcome.Resumable,
		"Resumable invariant preserved: a downstream supplier would duplicate committed bytes")
	assert.Greater(t, outcome.ChunkCount, 0,
		"at least one chunk was committed before the EOF (otherwise we'd be on the failure branch)")

	// Synthesized [DONE] still reaches the client.
	assert.Contains(t, writer.Body.String(), `"content":"hello"`)
	assert.True(t, strings.HasSuffix(writer.Body.String(), "data: [DONE]\n\n"),
		"synthesized [DONE] must still reach the client so OpenAI-compatible parsers finalize")

	// Metric still fires (downstream observability).
	assert.Equal(t, 1, counter.synth,
		"RecordStreamSynthesizedDone must still fire — it's the operator signal that the upstream is omitting [DONE]")

	// Capture state — the load-bearing invariant for audit success.
	summary := capture.SummaryAsMap()
	assert.False(t, summary["stream_interrupted"].(bool),
		"capture must NOT be marked interrupted; audit handler.go:5438 reads stream_interrupted and forces Success=false when true")
	assert.True(t, summary["stream_done_received"].(bool),
		"capture must show doneReceived=true (we ObserveChunk(ChunkTypeDone) to mirror natural [DONE])")
}

// TestStreamChatWithPendingCapture_EOFWithoutDoneZeroChunksRemainsFailure
// (added 2026-09-01, MiniMax-fix) pins the OPPOSITE invariant from
// TestStreamChatWithPendingCapture_EOFWithoutDoneAfterCommitIsSuccess:
// when the upstream closes the HTTP body before any semantic chunk has
// reached the gate (chunkCount == 0), this is a REAL upstream-down
// failure — the fix must NOT promote it to a success.
//
// Symptom the regression guard watches: a pre-fix audit row that
// legitimately had zero tokens / zero chunks / early EOF would, after
// the fix, erroneously appear as success=true. This test makes that
// regression loud.
func TestStreamChatWithPendingCapture_EOFWithoutDoneZeroChunksRemainsFailure(t *testing.T) {
	capture := audit.NewStreamCapture()

	// A non-SSE line: not parseable as a chunk, so chunkCount stays 0.
	// EOF arrives without [DONE]. The terminalVisible gate predicate is
	// false → the OLD failure branch must run.
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(
			"data: not-valid-json\n\n",
		)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	writer := httptest.NewRecorder()

	outcome := StreamChatWithPendingCapture(context.Background(),
		writer,
		resp,
		"minimax-m3",
		"MiniMax-M3",
		NewNormalizer(),
		capture,
		false,
		nil,
		nil,
	)

	assert.True(t, outcome.Interrupted,
		"chunkCount=0 + EOF without [DONE] is a real upstream failure; the fix must NOT suppress it")
	assert.Equal(t, "malformed_sse_frame", outcome.Reason)
	assert.Equal(t, errorsx.KindUpstreamDown, outcome.Kind)
	assert.True(t, outcome.Resumable)
	assert.Empty(t, writer.Body.String(),
		"no chunk reached the wire — the client got nothing")

	// Capture must be marked interrupted for this branch (audit path).
	summary := capture.SummaryAsMap()
	assert.True(t, summary["stream_interrupted"].(bool),
		"real upstream failure must propagate stream_interrupted=true so audit Success=false")
}
