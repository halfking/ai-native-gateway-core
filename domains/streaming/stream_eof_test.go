package streaming

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/kaixuan/llm-gateway-go/metrics"
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
	assert.Equal(t, "eof_without_done", streamErrorKindForDetailCode(nil, outcome.Reason))
	// Synthesised [DONE] must still be appended so clients don't hang.
	assert.True(t, strings.HasSuffix(writer.Body.String(), "data: [DONE]\n\n"))
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

func (c *countingRecorder) RecordCircuitRequest(_ string)                            {}
func (c *countingRecorder) RecordCircuitSuccess()                                    {}
func (c *countingRecorder) RecordCircuitFailure()                                    {}
func (c *countingRecorder) RecordCircuitStateChange(_, _ string)                     {}
func (c *countingRecorder) RecordCircuitTrip()                                       {}
func (c *countingRecorder) ObserveCircuitLatency(_ time.Duration)                    {}
func (c *countingRecorder) SetCircuitState(_ string)                                 {}
func (c *countingRecorder) SetCircuitErrorRate(_ float64)                            {}
func (c *countingRecorder) RecordAdapterConversion(_, _ string, _ time.Duration)     {}
func (c *countingRecorder) RecordAdapterFailure(_, _ string)                         {}
func (c *countingRecorder) RecordAdapterTokens(_, _ string, _ int)                   {}
func (c *countingRecorder) SetAdapterActive(_ string, _ bool)                        {}
func (c *countingRecorder) RecordSchedulerSelection(_ string, _ time.Duration)       {}
func (c *countingRecorder) UpdateSchedulerWeight(_ string, _ int)                    {}
func (c *countingRecorder) UpdateSchedulerCurrentWeight(_ string, _ int)             {}
func (c *countingRecorder) UpdateSchedulerEffectiveWeight(_ string, _ int)           {}
func (c *countingRecorder) SetSchedulerAvailableCredentials(_ int)                   {}
func (c *countingRecorder) RecordSafetyCheck(_ string, _ time.Duration)              {}
func (c *countingRecorder) RecordSafetyAction(_, _ string)                           {}
func (c *countingRecorder) RecordSafetyRuleHit(_, _, _ string)                       {}
func (c *countingRecorder) RecordSafetyWhitelistHit()                                {}
func (c *countingRecorder) SetSafetyRulesCount(_ bool, _ int)                        {}
func (c *countingRecorder) UpdatePoolUtilization(_ string, _ float64)                {}
func (c *countingRecorder) RecordPoolRequest(_, _ string)                            {}
func (c *countingRecorder) SetPoolCapacity(_ string, _ int)                          {}
func (c *countingRecorder) SetPoolActiveCredentials(_ string, _ int)                 {}
func (c *countingRecorder) SetPoolHealthyCredentials(_ string, _ int)                {}
func (c *countingRecorder) RecordShadowWriteFailure(_ string)                        {}
func (c *countingRecorder) RecordRingBufferDropped(_ uint64)                         {}
func (c *countingRecorder) RecordRawAuditWriteFailure()                              {}
func (c *countingRecorder) RecordURSMv2ShadowResult(_ string)                        {}
func (c *countingRecorder) RecordStreamSynthesizedDone()                             { c.synth++ }

// Compile-time check that countingRecorder satisfies metrics.Recorder.
var _ metrics.Recorder = (*countingRecorder)(nil)

// TestStreamChatWithPendingCapture_SynthesizedDoneIncrementsMetric
// (P1 hot-patch 2026-08-06) asserts the EOF branch in stream.go calls
// RecordStreamSynthesizedDone at least once when the upstream closes
// without [DONE]. Uses an inline counter Recorder wrapper.
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
	assert.GreaterOrEqual(t, counter.synth, 1, "RecordStreamSynthesizedDone must be called at least once when upstream closes without [DONE]")
	// Also verify the unmodified classification contract still holds.
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

	_ = StreamChatWithPendingCapture(
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
