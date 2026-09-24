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

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/errorsx"
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

// TestStreamChatWithPendingCapture_EOFWithoutDoneAfterCommitIsStructuredError
// (renamed 2026-09-22 from "...IsCompletedAndNotRetryable" — the §11.6
// production fix inverts the 2026-09-01 "MiniMax-fix" benign completion
// branch).
//
// Pre-fix-2026-09-01: upstream closed after committed content, gateway
// synthesized [DONE], outcome.Interrupted=false, audit success=true.
//
// 2026-09-01 MiniMax-fix: same benign behaviour with a distinct
// `eof_without_done_after_commit` reason literal. This was the §11.6
// pseudo-success the task explicitly forbids.
//
// 2026-09-22 §11.6 production fix: HTTP 200 SSE without [DONE] is
// NEVER treated as success, even after semantic chunks were committed.
// The committed stream cannot be transparently failed over (would
// duplicate bytes), so per §11.6 the gateway returns a "structured
// error" instead of a silent 200. Specifically:
//   - capture.MarkInterruptedWithReason("eof_without_done") so audit
//     records success=false, failure_detail_code="eof_without_done",
//     error_kind="eof_without_done"
//   - Wire shape: the previously-committed content stays on the wire,
//     followed by `data: {"error":{...,"code":"eof_without_done"}}`
//     and a synthesized `data: [DONE]\n\n` so OpenAI-compatible
//     parsers finalize cleanly
//   - outcome.Interrupted=true, Kind=KindUpstreamDown (NOT the prior
//     `KindEmptyResponse` non-failure kind)
//   - outcome.Resumable=false (committed bytes cannot be transparently
//     retried by another candidate — invariant preserved across all
//     three revisions of this branch)
//   - RecordStreamSynthesizedDone metric still fires because the
//     synthesized terminator IS injected on the wire
//
// Operators split committed-but-truncated from uncommitted EOF via
// SQL filter `chunk_count > 0 AND failure_detail_code = 'eof_without_done'`.
func TestStreamChatWithPendingCapture_EOFWithoutDoneAfterCommitIsStructuredError(t *testing.T) {
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

	// §11.6: HTTP 200 SSE without [DONE] is NEVER a success. Even when
	// semantic chunks were committed, the request is now flagged as a
	// failure so the audit pipeline, circuit breaker, and credential
	// health all see the truncation. The previous "benign
	// non-compliance" behaviour (commit 05c79fbe9) is what §11.6
	// forbids as "silent 200 with pseudo-success".
	assert.True(t, outcome.Interrupted,
		"§11.6: HTTP 200 SSE without [DONE] must be reported as a failure even when semantic chunks were committed")
	assert.Equal(t, "eof_without_done", outcome.Reason,
		"reason literal must be in audit.isInterruptionCode list so failure_detail_code column is populated")
	assert.Equal(t, errorsx.KindUpstreamDown, outcome.Kind,
		"§11.6: structured-error path must carry the upstream-down failure kind (was KindEmptyResponse in the prior benign branch)")
	assert.False(t, outcome.Resumable,
		"Resumable invariant preserved: committed bytes cannot be transparently retried by another candidate")
	assert.Greater(t, outcome.ChunkCount, 0)

	// Wire shape: previously committed content stays, followed by the
	// structured error envelope and the synthesized [DONE] so SDK
	// parsers finalize. The order matters: error before [DONE] gives
	// OpenAI-compatible clients a chance to observe the failure and
	// still close the stream cleanly.
	body := writer.Body.String()
	assert.Contains(t, body, `"content":"hello"`,
		"previously committed content must remain on the wire — the client owns the partial response")
	assert.Contains(t, body, `"type":"upstream_incomplete"`,
		"structured SSE error envelope must reach the client so the SDK observes the failure (NOT a silent 200)")
	assert.Contains(t, body, `"code":"eof_without_done"`,
		"error code mirrors the audit failure_detail_code so client-side error handlers and server-side logs agree")
	assert.Contains(t, body, `"retryable":true`,
		"2026-09-23: committed-output truncation is a transient upstream class; agent clients must re-send the turn instead of hard-failing (user report #17)")
	assert.True(t, strings.HasSuffix(body, "data: [DONE]\n\n"),
		"synthesized [DONE] must still reach the client so OpenAI-compatible parsers finalize")
	assert.Less(t, strings.Index(body, `"type":"upstream_incomplete"`), strings.Index(body, "data: [DONE]"),
		"error envelope must precede synthesized [DONE] so SDKs observe the failure before finalization")
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
func (c *countingRecorder) RecordRawSinkFlush(string, time.Duration, int)        {}
func (c *countingRecorder) RecordRawSinkDropped(string, string, uint64)          {}
func (c *countingRecorder) RecordRawSinkCloseDrain(string, time.Duration, bool)  {}
func (c *countingRecorder) RecordRawSinkFrameLookup(string, string)              {}
func (c *countingRecorder) RecordURSMv2ShadowResult(_ string)                    {}
func (c *countingRecorder) RecordMalformedSSEFrame(_, _ string)                  {}
func (c *countingRecorder) RecordStreamSynthesizedDone()                         { c.synth++ }
func (c *countingRecorder) RecordIncompleteToolCall(_, _ string)                 {}
func (c *countingRecorder) RecordSuccessEmptyResponse(_, _, _ string)            {}
func (c *countingRecorder) RecordJournalSnapshotStored(_ string)                 {}
func (c *countingRecorder) RecordJournalSnapshotApplied(_ string, _ bool)        {}
func (c *countingRecorder) RecordJournalSnapshotDeduplicated(_, _ string)        {}
func (c *countingRecorder) RecordLiveStreamRecordDropped(_ string)               {}

// Compile-time check that countingRecorder satisfies metrics.Recorder.
var _ metrics.Recorder = (*countingRecorder)(nil)

// TestStreamChatWithPendingCapture_SynthesizedDoneIncrementsMetric
// (P1 hot-patch 2026-08-06; updated 2026-09-01 MiniMax-fix; updated
// 2026-09-22 §11.6 production fix) asserts the EOF branch in stream.go
// still calls RecordStreamSynthesizedDone when the upstream closes
// without [DONE] AFTER semantic output has been committed.
//
// 2026-09-22 §11.6: the synthesized terminator IS still injected on
// the wire (OpenAI SDKs need it to finalize their stream parsers), so
// RecordStreamSynthesizedDone keeps firing. The signal is now
// observability-only — the request itself is a failure, but the metric
// tells operators "this upstream is omitting [DONE]" regardless of
// whether the chunk count was zero or non-zero. Uses an inline counter
// Recorder wrapper.
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

	// §11.6 fix: this is a failure, not a benign completion. The metric
	// still fires (we DO synthesize [DONE] on the wire), but the request
	// is now flagged as Interrupted so audit success=false.
	assert.True(t, outcome.Interrupted,
		"§11.6: committed output + EOF without [DONE] is now a structured-error failure, not a benign completion")
	assert.Equal(t, "eof_without_done", outcome.Reason,
		"reason literal is in audit.isInterruptionCode list so failure_detail_code is populated")
	assert.Equal(t, 1, counter.synth,
		"synthesized-[DONE] signal must still fire — the synthesized terminator IS injected on the wire so OpenAI SDKs finalize; the metric tells operators which upstreams are omitting [DONE]")
	assert.Contains(t, writer.Body.String(), `"content":"hello"`)
	assert.True(t, strings.HasSuffix(writer.Body.String(), "data: [DONE]\n\n"),
		"synthesized [DONE] must still reach the wire so SDK parsers finalize even though the request is now a failure")
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

// TestStreamChatWithPendingCapture_EOFWithoutDoneAfterCommitIsStructuredErrorCapture
// (renamed 2026-09-22 from "...EOFWithoutDoneAfterCommitIsSuccess" — the
// §11.6 production fix inverts the 2026-09-01 "MiniMax-fix" benign
// completion branch on the capture-audit axis too).
//
// 2026-09-22 §11.6 contract:
//   - HTTP 200 SSE without [DONE] is NEVER a success, even when semantic
//     chunks were already committed to the client.
//   - The capture MUST reflect the failure so the audit pipeline writes
//     success=false, failure_detail_code="eof_without_done",
//     error_kind="eof_without_done". Otherwise §11.6's
//     "不向客户端返回伪成功" promise is hollow — the request_logs row
//     would still look like a success while the wire shape is a
//     structured error.
//
// This test is the end-to-end guard for that invariant. It uses a real
// audit.NewStreamCapture so the SummaryAsMap() output is the same path
// the production handler.go:5438 reads from. The capture must:
//
//   - BE marked interrupted (`stream_interrupted == true` in summary) so
//     audit Success=false lands in request_logs
//   - publish `failure_detail_code == "eof_without_done"` via
//     isInterruptionCode path in audit.go (so the SQL filter
//     `failure_detail_code='eof_without_done'` catches both committed
//     and uncommitted cases)
//   - have `outcome.Interrupted == true`
//   - have `outcome.Reason == "eof_without_done"` (in
//     audit.isInterruptionCode list)
//   - have `outcome.Kind == errorsx.KindUpstreamDown`
//   - have `outcome.Resumable == false` (committed bytes cannot be
//     transparently retried by another candidate — Resumable invariant
//     preserved across all three revisions of this branch)
//
// Companion to TestStreamChatWithPendingCapture_EOFWithoutDoneZeroChunksRemainsFailure
// which pins the OPPOSITE branch (no committed content → still
// Interrupted=true, Resumable=true).
func TestStreamChatWithPendingCapture_EOFWithoutDoneAfterCommitIsStructuredErrorCapture(t *testing.T) {
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

	// Outcome: §11.6 structured-error failure, distinct reason, no retry.
	assert.True(t, outcome.Interrupted,
		"§11.6: committed output + EOF without [DONE] is now a structured-error failure, not a benign completion")
	assert.Equal(t, "eof_without_done", outcome.Reason,
		"reason literal is in audit.isInterruptionCode list so failure_detail_code column is populated")
	assert.Equal(t, errorsx.KindUpstreamDown, outcome.Kind,
		"§11.6: structured-error path must carry the upstream-down failure kind (was KindEmptyResponse in the prior benign branch)")
	assert.False(t, outcome.Resumable,
		"Resumable invariant preserved: a downstream supplier would duplicate committed bytes")
	assert.Greater(t, outcome.ChunkCount, 0,
		"at least one chunk was committed before the EOF (otherwise we'd be on the uncommitted failure branch)")

	// Wire shape: previously committed content stays, followed by the
	// structured error envelope and the synthesized [DONE] so SDK
	// parsers finalize.
	body := writer.Body.String()
	assert.Contains(t, body, `"content":"hello"`,
		"previously committed content must remain on the wire — the client owns the partial response")
	assert.Contains(t, body, `"type":"upstream_incomplete"`,
		"structured SSE error envelope must reach the client so the SDK observes the failure (NOT a silent 200)")
	assert.True(t, strings.HasSuffix(body, "data: [DONE]\n\n"),
		"synthesized [DONE] must still reach the client so OpenAI-compatible parsers finalize")

	// Metric still fires (downstream observability) — the synthesized
	// terminator IS injected on the wire regardless of the audit
	// classification.
	assert.Equal(t, 1, counter.synth,
		"RecordStreamSynthesizedDone must still fire — it's the operator signal that the upstream is omitting [DONE]")

	// Capture state — the load-bearing invariant for audit failure.
	summary := capture.SummaryAsMap()
	assert.True(t, summary["stream_interrupted"].(bool),
		"§11.6: capture MUST be marked interrupted so audit handler.go:5438 writes Success=false; otherwise pseudo-success leaks into request_logs even though the wire is a failure")
	assert.Equal(t, "eof_without_done", summary["failure_detail_code"],
		"failure_detail_code column must equal the reason literal so SQL filter `failure_detail_code='eof_without_done'` matches the row")
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

// TestStreamChatWithPendingCapture_Section11_6_PseudoSuccessGuard (added
// 2026-09-22) is the wire-shape + audit-contract regression guard for
// the §11.6 production fix. It pins every clause of §11.6 of
// comprehensive-test-plan.md:
//
//	"HTTP 200 空响应或 SSE 无 [DONE] | 不向客户端返回伪成功，切换或
//	返回结构化错误"
//
// Concretely:
//
//  1. Wire shape — the client receives a structured SSE error envelope
//     AND a synthesized [DONE] terminator. The error envelope MUST come
//     first so OpenAI SDKs observe the failure before finalization.
//  2. HTTP status — the gateway has already committed 200 + SSE headers
//     (per the §11.6 "切换或返回结构化错误" path; transparent retry is
//     impossible because the client already owns the partial response).
//     The fix MUST NOT escalate to 4xx/5xx mid-stream — that would
//     corrupt the wire protocol. The structured error IS the §11.6
//     "structured error" path for committed streams.
//  3. Audit capture — `stream_interrupted=true` so handler.go:5438
//     writes Success=false into request_logs; `failure_detail_code =
//     "eof_without_done"` so SQL filter
//     `failure_detail_code='eof_without_done'` matches.
//  4. Executor classification — Kind=KindUpstreamDown so
//     e.shouldWriteCredentialStateOnConfirmedFailure / circuit-breaker
//     demote the chronically-truncating credential. Resumable=false
//     because the client owns the committed bytes.
//  5. No silent 200 — the wire shape contains the
//     "upstream_incomplete" error envelope. Without it, §11.6 would be
//     violated even if the audit row says failure: the client SDK sees
//     a clean 200 + content + [DONE] and never learns the stream was
//     truncated.
//
// This test complements the three earlier tests in this package by
// asserting the wire shape end-to-end. The earlier tests pin individual
// invariant slices (Reason literal, capture summary, metric counter);
// this one pins the wire bytes + audit chain simultaneously, so a future
// refactor that breaks the order of "error envelope → [DONE]" fails
// loudly.
func TestStreamChatWithPendingCapture_Section11_6_PseudoSuccessGuard(t *testing.T) {
	counter := &countingRecorder{delegate: metrics.NewNoopRecorder()}
	prev := metrics.Global()
	metrics.SetGlobal(counter)
	t.Cleanup(func() { metrics.SetGlobal(prev) })

	capture := audit.NewStreamCapture()

	// Realistic committed-then-truncated upstream body: a content
	// chunk, then EOF with no [DONE] and NO finish_reason chunk — a
	// genuine mid-answer truncation. (2026-09-23: the finish_reason=stop
	// chunk this fixture used to carry was moved to
	// TestStreamChatWithPendingCapture_BenignEOFAfterFinishReason — a
	// received finish_reason means the stream completed semantically and
	// is now benign, parity with the responses/anthropic bridges.)
	// The MiniMax truncation pattern observed in production (commit
	// 05c79fbe9 referenced provider=14/credential=21/raw_model=MiniMax-M3).
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(
			"data: {\"id\":\"chunk-1\",\"choices\":[{\"delta\":{\"content\":\"partial answer\"}}]}\n\n",
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

	// 1. Wire shape — error envelope present, content preserved, [DONE]
	// synthesized, in the correct order.
	body := writer.Body.String()
	assert.Contains(t, body, `"content":"partial answer"`,
		"§11.6 wire: previously committed content must NOT be erased — the client owns the partial response")
	assert.Contains(t, body, `"type":"upstream_incomplete"`,
		"§11.6 wire: structured error envelope MUST reach the client (NOT silent 200)")
	assert.Contains(t, body, `"code":"eof_without_done"`,
		"§11.6 wire: error code mirrors audit failure_detail_code so client/server agree on the failure class")
	assert.True(t, strings.HasSuffix(body, "data: [DONE]\n\n"),
		"§11.6 wire: synthesized [DONE] reaches the client so OpenAI-compatible parsers finalize")
	assert.Less(t, strings.Index(body, `"type":"upstream_incomplete"`), strings.Index(body, "data: [DONE]"),
		"§11.6 wire: error envelope MUST precede synthesized [DONE] so SDKs observe the failure BEFORE finalization")
	assert.Greater(t, strings.Index(body, `"content":"partial answer"`), -1, "sanity: content chunk preserved on wire")

	// 2. HTTP status — the gateway has already committed 200 + SSE
	// headers by the time the EOF branch fires. The §11.6 fix does not
	// (and cannot) rewrite the status line mid-stream; the
	// "structured error" path IS the in-stream error envelope above.
	// httptest.NewRecorder's default status is 200, so the assertion is
	// that no escalation happened.
	assert.Equal(t, 200, writer.Code,
		"§11.6 wire: HTTP status stays 200 — gateway cannot rewrite status mid-stream; structured error is the in-stream envelope above")

	// 3. Audit capture — stream_interrupted=true + failure_detail_code
	// is populated from isInterruptionCode("eof_without_done").
	summary := capture.SummaryAsMap()
	assert.True(t, summary["stream_interrupted"].(bool),
		"§11.6 audit: capture MUST be marked interrupted so handler.go:5438 writes Success=false")
	assert.Equal(t, "eof_without_done", summary["failure_detail_code"],
		"§11.6 audit: failure_detail_code column matches reason literal so SQL filter catches the row")

	// 4. Executor classification — Kind=KindUpstreamDown + Resumable=false
	// + Interrupted=true.
	assert.True(t, outcome.Interrupted, "§11.6 executor: outcome must be Interrupted=true")
	assert.Equal(t, "eof_without_done", outcome.Reason, "§11.6 executor: reason literal in audit.isInterruptionCode list")
	assert.Equal(t, errorsx.KindUpstreamDown, outcome.Kind,
		"§11.6 executor: Kind=KindUpstreamDown so circuit-breaker + credential-health demote the truncating credential")
	assert.False(t, outcome.Resumable,
		"§11.6 executor: Resumable=false (committed bytes cannot be transparently retried by another candidate)")

	// 5. No silent 200 — the synthesized [DONE] is preceded by the error
	// envelope, so a strict "does the body look like a clean 200-only
	// completion" check fails. This catches a future refactor that
	// accidentally drops the error envelope.
	if !strings.Contains(body, `"type":"upstream_incomplete"`) {
		t.Fatalf("§11.6 VIOLATION: wire shape is silent 200 with content + [DONE] only — no structured error envelope")
	}

	// Metric still fires — the synthesized terminator IS on the wire.
	assert.Equal(t, 1, counter.synth,
		"RecordStreamSynthesizedDone keeps firing because the synthesized terminator IS injected on the wire (observability signal for upstream non-compliance)")
}

// TestStreamChatWithPendingCapture_BenignEOFAfterFinishReason (added
// 2026-09-23) is the benign-EOF parity guard for the OpenAI chat bridge,
// matching the responses and anthropic bridges (2026-09-13).
//
// Live evidence (request df60575b / 51da1dd4, build 2235, 2026-09-23
// 20:11 +08): MiniMax-M3 streamed 86 chunks of a tool_calls turn, sent
// the finish_reason chunk, then closed the connection WITHOUT [DONE].
// The gateway's §11.6 guard classified the semantically-complete stream
// as eof_without_done/retryable=false; the client (whose turn layer
// treats the interruption as fatal) hard-failed the turn and the tool
// never executed — the exact "无法正确使用 tools" report.
//
// Contract: when a finish_reason chunk was already received, the EOF
// branch synthesizes [DONE] and records a CLEAN completion:
//   - outcome.Interrupted=false, Reason="" (audit success=true)
//   - wire shape: committed content + synthesized [DONE] (NO error
//     envelope — a strict reader must see a normal completion)
//   - RecordStreamSynthesizedDone still fires (the terminator IS
//     synthesized on the wire — operators still see which upstreams
//     omit [DONE])
//   - capture is NOT marked interrupted
//
// EOF with NO finish_reason remains a §11.6 structured-error failure —
// pinned by TestStreamChatWithPendingCapture_Section11_6_PseudoSuccessGuard.
func TestStreamChatWithPendingCapture_BenignEOFAfterFinishReason(t *testing.T) {
	counter := &countingRecorder{delegate: metrics.NewNoopRecorder()}
	prev := metrics.Global()
	metrics.SetGlobal(counter)
	t.Cleanup(func() { metrics.SetGlobal(prev) })

	capture := audit.NewStreamCapture()

	// The df60575b shape: content/tool_call deltas, then the
	// finish_reason chunk (semantically complete), then EOF with no
	// [DONE] — the minimax-style relay close.
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(
			"data: {\"id\":\"chunk-1\",\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}\n\n" +
				"data: {\"id\":\"chunk-2\",\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n",
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

	// Outcome: clean completion — the turn reaches the client's tool
	// executor instead of dying as a retryable=false failure.
	assert.False(t, outcome.Interrupted,
		"a finish_reason already received means the stream completed semantically — EOF without [DONE] must NOT fail the turn (df60575b)")
	assert.Empty(t, outcome.Reason,
		"no interruption reason: audit success=true so the tool_calls turn is not recorded as a failure")
	assert.Empty(t, outcome.Kind)
	assert.Greater(t, outcome.ChunkCount, 0)

	// Wire shape: content + synthesized [DONE], NO error envelope.
	body := writer.Body.String()
	assert.Contains(t, body, `"content":"answer"`,
		"committed content stays on the wire")
	assert.NotContains(t, body, `"type":"upstream_incomplete"`,
		"benign close must NOT carry the error envelope — the client must see a normal completion")
	assert.True(t, strings.HasSuffix(body, "data: [DONE]\n\n"),
		"synthesized [DONE] finalizes the client's stream parser")

	// Metric: still fires — the terminator was synthesized.
	assert.Equal(t, 1, counter.synth,
		"RecordStreamSynthesizedDone keeps firing so operators still see which upstreams omit [DONE]")

	// Capture: NOT interrupted — audit success=true.
	summary := capture.SummaryAsMap()
	assert.False(t, summary["stream_interrupted"].(bool),
		"capture must NOT be marked interrupted: the turn is semantically complete and must not be recorded as a failure")
}

// TestStreamChatWithPendingCapture_BenignEOFAfterFinishReason_MiniMaxProductionShape
// is the 2026-09-24 r0924 regression钉桩 reproducing the EXACT chunk
// sequence that shipped over the wire from api.minimaxi.com for
// minimax-m3 tool-calling turns:
//   1. role-only delta (first frame announces the assistant role)
//   2. finish_reason:"tool_calls" delta with a fully-formed tool_calls[]
//      carrying the actual function name + JSON arguments
//   3. usage-only frame with empty choices (mirrors MiniMax's
//      post-finish_reason accounting emission)
//   4. EOF — no `data: [DONE]` sentinel (MiniMax relay omits it)
//
// Field-evidence raw frames (request c96df1a8a50154667e8f1d40fa64b2bd,
// 2026-09-23T17:21):
//   https://raw-logs/...: 3 upstream_response chunks, then EOF.
//   Audit row recorded success=false, reason="eof_without_done",
//   kind="upstream_down", chunk_count=6, resumable=false, which means
//   the §11.6-pseudo-success branch ran instead of the benign-completion
//   branch. The failing chunk boundary is the open question this test
//   pins — if THIS test fails, the production path is missing a fix
//   vs. the df60575b shape used in TestStreamChatWithPendingCapture_BenignEOFAfterFinishReason.
func TestStreamChatWithPendingCapture_BenignEOFAfterFinishReason_MiniMaxProductionShape(t *testing.T) {
	counter := &countingRecorder{delegate: metrics.NewNoopRecorder()}
	prev := metrics.Global()
	metrics.SetGlobal(counter)
	t.Cleanup(func() { metrics.SetGlobal(prev) })

	capture := audit.NewStreamCapture()

	// Three upstream chunks + EOF — the exact minimax-m3 wire shape.
	// Truncated tool_calls argument (file_path) for readability; the IR
	// parser doesn't care about argument length.
	body := "" +
		"data: {\"id\":\"07033db16ab3a2ed677a4223558f03ad\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}],\"created\":1790184114,\"model\":\"MiniMax-M3\",\"object\":\"chat.completion.chunk\"}\n\n" +
		"data: {\"id\":\"07033db16ab3a2ed677a4223558f03ad\",\"choices\":[{\"finish_reason\":\"tool_calls\",\"index\":0,\"delta\":{\"content\":\"\",\"role\":\"assistant\",\"tool_calls\":[{\"id\":\"call_test\",\"type\":\"function\",\"function\":{\"name\":\"Read\",\"arguments\":\"{\\\"file_path\\\":\\\"/etc/hostname\\\"}\"},\"index\":0}]}}],\"created\":1790184114,\"model\":\"MiniMax-M3\",\"object\":\"chat.completion.chunk\"}\n\n" +
		"data: {\"id\":\"07033db16ab3a2ed677a4223558f03ad\",\"choices\":[],\"created\":1790184113,\"model\":\"MiniMax-M3\",\"object\":\"chat.completion.chunk\",\"usage\":{\"total_tokens\":220,\"prompt_tokens\":178,\"completion_tokens\":42}}\n\n"

	resp := &http.Response{
		Body:    io.NopCloser(strings.NewReader(body)),
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
		false, // toolsRequested=false — the test focuses on the EOF path
		nil,
		nil,
	)

	// Mirror TestStreamChatWithPendingCapture_BenignEOFAfterFinishReason
	// assertions — the production chunk shape must take the same path.
	assert.False(t, outcome.Interrupted,
		"production chunk shape (role → finish_reason+tool_calls → usage → EOF) must be classified as benign completion, NOT §11.6-pseudo-success")
	assert.Empty(t, outcome.Reason)
	assert.Empty(t, outcome.Kind)
	assert.Greater(t, outcome.ChunkCount, 0,
		"chunkCount > 0: at least one committed frame (the tool_calls payload reached the wire)")

	wire := writer.Body.String()
	assert.Contains(t, wire, `"name":"Read"`,
		"committed tool_calls payload stays on the wire — the client must receive the function name + arguments")
	assert.NotContains(t, wire, `"type":"upstream_incomplete"`,
		"production-shape benign close must NOT carry the §11.6 error envelope")
	assert.True(t, strings.HasSuffix(wire, "data: [DONE]\n\n"),
		"synthesized [DONE] finalizes the client's stream parser")

	assert.Equal(t, 1, counter.synth,
		"RecordStreamSynthesizedDone fires once so operators see the upstream is omitting [DONE]")

	summary := capture.SummaryAsMap()
	assert.False(t, summary["stream_interrupted"].(bool),
		"capture must NOT mark the turn interrupted: the tool_calls turn is semantically complete and reaches the client's tool executor")
}

// TestStreamChatWithPendingCapture_BenignEOFZeroSemanticContentIsStructuredError
// (r0924b 2026-09-24) pins the OTHER edge of the benign-EOF promotion: a
// stream with ZERO client-semantic output (pure usage / role / keepalive
// frames + a trailing finish_reason) must NOT be promoted to a benign
// completion just because finish_reason was seen. Per the emptyoutcome
// semantic table, usage-only = EMPTY with or without finish_reason.
//
// Fixture shape (usage-diluted): the empty-stream gate's early-empty counter
// resets on every choices:[] usage frame, and the buffer cap
// (emptyGateMaxChunks=8) flushes the zero-content frames write-through once
// eight accumulate — so the frames DO reach the client (chunkCount > 0) and
// the EOF branch sees finalFinishReason="stop". Pre-r0924b this pinned the
// pseudo-success: benign completion, audit success=true. Post-r0924b the
// benign branch additionally requires sawSemanticOutput, so this stream
// falls through to the existing §11.6 committed-truncation structured-error
// path (usage frames commit the attempt gate via their Terminal frame
// class): Interrupted=true, Reason="eof_without_done", wire carries the
// upstream_incomplete envelope + synthesized [DONE], Resumable=false
// (nothing semantic to duplicate, but the committed transport bytes stay
// owned by this attempt).
func TestStreamChatWithPendingCapture_BenignEOFZeroSemanticContentIsStructuredError(t *testing.T) {
	counter := &countingRecorder{delegate: metrics.NewNoopRecorder()}
	prev := metrics.Global()
	metrics.SetGlobal(counter)
	t.Cleanup(func() { metrics.SetGlobal(prev) })

	capture := audit.NewStreamCapture()

	// Eight zero-semantic frames (role-only deltas INTERLEAVED with
	// choices:[] usage frames — each usage frame resets the empty gate's
	// early-empty counter, and the buffer cap (emptyGateMaxChunks=8) then
	// flushes the zero-content frames write-through, so the frames DO reach
	// the client and the EOF branch sees finalFinishReason="stop") + a
	// finish_reason-only delta + one more usage frame, then EOF with no
	// [DONE].
	role := "data: {\"id\":\"z\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}],\"model\":\"m\",\"object\":\"chat.completion.chunk\"}\n\n"
	usage := "data: {\"id\":\"z\",\"choices\":[],\"model\":\"m\",\"object\":\"chat.completion.chunk\",\"usage\":{\"total_tokens\":1,\"prompt_tokens\":1,\"completion_tokens\":0}}\n\n"
	body := role + usage + role + usage + role + usage + role + usage +
		"data: {\"id\":\"z\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"model\":\"m\",\"object\":\"chat.completion.chunk\"}\n\n" +
		usage

	resp := &http.Response{
		Body:    io.NopCloser(strings.NewReader(body)),
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
		"zero-semantic stream + finish_reason + EOF must NOT be a benign completion (usage-only = EMPTY per emptyoutcome, finish_reason or not)")
	assert.Equal(t, "eof_without_done", outcome.Reason,
		"falls through to the existing eof_without_done structured-error path")
	assert.Equal(t, errorsx.KindUpstreamDown, outcome.Kind)
	assert.False(t, outcome.Resumable)
	assert.Greater(t, outcome.ChunkCount, 0,
		"the diluted frames DID reach the wire (buffer-cap flush + write-through) — that is exactly why the pre-fix branch misfired")

	wire := writer.Body.String()
	assert.Contains(t, wire, `"type":"upstream_incomplete"`,
		"structured SSE error envelope must reach the client — no pseudo-success on a zero-semantic stream")
	assert.True(t, strings.HasSuffix(wire, "data: [DONE]\n\n"),
		"synthesized [DONE] finalizes the client's stream parser")
	assert.NotContains(t, wire, `"content":"`,
		"sanity: the fixture carries no semantic content, so none may appear on the wire")

	assert.Equal(t, 1, counter.synth,
		"RecordStreamSynthesizedDone still fires — the terminator IS synthesized on the wire")

	summary := capture.SummaryAsMap()
	assert.True(t, summary["stream_interrupted"].(bool),
		"capture must be marked interrupted so audit writes Success=false (the pre-fix row looked like a success)")
}

// TestStreamChatWithPendingCapture_BenignEOFAfterFinishReason_ToolsRequested
// (r0924b 2026-09-24) is the toolsRequested=true twin of
// TestStreamChatWithPendingCapture_BenignEOFAfterFinishReason_MiniMaxProductionShape:
// on a tool-bearing turn (tools requested upstream) the XML tool-call
// coercer is active in the relay. The MiniMax production shape carries a
// NATIVE delta.tool_calls payload (the coercer passes frames that already
// have tool_calls through untouched), so the benign verdict must be
// identical to the toolsRequested=false pinning: finish_reason + real
// tool_calls output + EOF without [DONE] = benign completion, NOT
// eof_without_done. Guards against a future coercer change (e.g. buffering
// or rewriting the tool_calls frame) silently breaking the benign-EOF
// promotion on tool turns.
func TestStreamChatWithPendingCapture_BenignEOFAfterFinishReason_ToolsRequested(t *testing.T) {
	counter := &countingRecorder{delegate: metrics.NewNoopRecorder()}
	prev := metrics.Global()
	metrics.SetGlobal(counter)
	t.Cleanup(func() { metrics.SetGlobal(prev) })

	capture := audit.NewStreamCapture()

	// Same three-chunk MiniMax production shape as the toolsRequested=false
	// pinning (role → finish_reason+native tool_calls → usage → EOF).
	body := "" +
		"data: {\"id\":\"07033db16ab3a2ed677a4223558f03ad\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}],\"created\":1790184114,\"model\":\"MiniMax-M3\",\"object\":\"chat.completion.chunk\"}\n\n" +
		"data: {\"id\":\"07033db16ab3a2ed677a4223558f03ad\",\"choices\":[{\"finish_reason\":\"tool_calls\",\"index\":0,\"delta\":{\"content\":\"\",\"role\":\"assistant\",\"tool_calls\":[{\"id\":\"call_test\",\"type\":\"function\",\"function\":{\"name\":\"Read\",\"arguments\":\"{\\\"file_path\\\":\\\"/etc/hostname\\\"}\"},\"index\":0}]}}],\"created\":1790184114,\"model\":\"MiniMax-M3\",\"object\":\"chat.completion.chunk\"}\n\n" +
		"data: {\"id\":\"07033db16ab3a2ed677a4223558f03ad\",\"choices\":[],\"created\":1790184113,\"model\":\"MiniMax-M3\",\"object\":\"chat.completion.chunk\",\"usage\":{\"total_tokens\":220,\"prompt_tokens\":178,\"completion_tokens\":42}}\n\n"

	resp := &http.Response{
		Body:    io.NopCloser(strings.NewReader(body)),
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
		true, // toolsRequested=true — the XML tool-call coercer is active
		nil,
		nil,
	)

	// Identical contract to the toolsRequested=false pinning.
	assert.False(t, outcome.Interrupted,
		"toolsRequested=true must not change the benign verdict: finish_reason + native tool_calls + EOF is a semantically complete turn")
	assert.Empty(t, outcome.Reason)
	assert.Empty(t, outcome.Kind)
	assert.Greater(t, outcome.ChunkCount, 0)

	wire := writer.Body.String()
	assert.Contains(t, wire, `"name":"Read"`,
		"the coercer must leave the native tool_calls payload intact on the wire")
	assert.NotContains(t, wire, `"type":"upstream_incomplete"`,
		"benign close must NOT carry the §11.6 error envelope")
	assert.True(t, strings.HasSuffix(wire, "data: [DONE]\n\n"),
		"synthesized [DONE] finalizes the client's stream parser")

	assert.Equal(t, 1, counter.synth)

	summary := capture.SummaryAsMap()
	assert.False(t, summary["stream_interrupted"].(bool),
		"capture must NOT mark the turn interrupted")
}

// TestStreamChatSurvivalGateReuse_SingleTerminalOnCommittedBreak — b0c77269d
// field regression (154 build 2234, 2026-09-23), closed-loop at the exact
// seam the live bug lived in.
//
// Field topology: the SurvivalCoordinator pre-wraps the client writer in a
// per-attempt GateWriter; StreamChat…WithVendor then wraps that writer in a
// connection-monitor decorator BEFORE calling wrapAttemptWriter. b0c77269d's
// predecessor missed the GateWriter behind the decorator, built a second
// gate, and the §11.6 TerminalRendered latch landed on the throwaway — so
// the coordinator's renderTerminal guard never fired and the wire carried
// the §11.6 frame + [DONE] followed by the survival resume_blocked envelope
// + a second [DONE] (20/20 committed minimax-m3 breaks, 100%).
//
// The R58 handler-level e2e cannot see this seam: its mock executor writes
// straight into the GateWriter and never enters StreamChat…WithVendor.
// This test drives the REAL stream function over a coordinator-style
// GateWriter with a truncating upstream and then renders the survival
// terminal exactly as survival_coordinator.renderTerminal would:
//
//	§11.6 latch must land on the coordinator's gate, and the coordinator
//	terminal render must suppress — one error frame, one [DONE], silence
//	afterwards.
func TestStreamChatSurvivalGateReuse_SingleTerminalOnCommittedBreak(t *testing.T) {
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(
			"data: {\"id\":\"chunk-1\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n",
		)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	rec := httptest.NewRecorder()
	sw := NewSerializedStreamWriter(rec)
	gate := NewAttemptCommitGate(context.Background(), ProtocolOpenAIChat, sw, GateOptions{Mode: GateModeBuffered, RequestID: "req-survival-reuse"})
	gw := NewGateWriterWithResponse(gate, rec)

	outcome := StreamChatWithPendingCapture(context.Background(),
		gw,
		resp,
		"minimax-m3",
		"MiniMax-M3",
		NewNormalizer(),
		nil,
		false,
		nil,
		nil,
	)

	if !outcome.Interrupted || outcome.Reason != "eof_without_done" {
		t.Fatalf("outcome = interrupted=%v reason=%q, want committed-break eof_without_done", outcome.Interrupted, outcome.Reason)
	}
	// THE regression core: the §11.6 latch must be visible on the
	// coordinator's gate. Pre-b0c77269d this read false (latch landed on the
	// throwaway gate behind the monitor decorator).
	if !gate.TerminalRendered() {
		t.Fatal("§11.6 TerminalRendered latch must land on the coordinator's gate (GateWriter reuse behind the monitor wrapper)")
	}

	// renderTerminal semantics (survival_coordinator.go): guard sees the
	// latch → suppress → the survival resume_blocked envelope must NOT be
	// stacked onto the wire.
	coord := &SurvivalCoordinator{Terminal: func(decision TaskDecision, committed bool) {
		renderSurvivalTerminal(sw, ProtocolOpenAIChat, decision, committed)
	}}
	coord.renderTerminal(TaskDecision{Action: TaskActionResumeBlocked, Reason: "committed_output"}, gate, true)

	body := rec.Body.String()
	if n := strings.Count(body, `data: {"error"`); n != 1 {
		t.Fatalf("error frame count = %d, want exactly 1 (double terminal = the field bug)\nwire tail: %q", n, tailBytes(body, 400))
	}
	if n := strings.Count(body, "data: [DONE]"); n != 1 {
		t.Fatalf("data: [DONE] count = %d, want exactly 1\nwire tail: %q", n, tailBytes(body, 400))
	}
	if !strings.Contains(body, `"retryable":true`) {
		t.Fatal("committed-break envelope must carry retryable:true (2026-09-23 strategy fix)")
	}
	doneIdx := strings.LastIndex(body, "data: [DONE]")
	if strings.Contains(body[doneIdx:], `data: {"`) {
		t.Fatalf("frame stacked after terminal [DONE]: %q", body[doneIdx:])
	}
}
