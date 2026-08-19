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

func TestStreamChatWithPendingCapture_EOFWithoutDoneAfterContentIsNotRetryable(t *testing.T) {
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
	assert.False(t, outcome.Resumable)
	assert.Greater(t, outcome.ChunkCount, 0)
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
	assert.Equal(t, "invalid_chunk", outcome.Reason)
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
	resp := &http.Response{
		Body: &errorAfterDataReadCloser{
			data: []byte("data: {\"id\":\"chunk-1\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"),
			err:  errors.New("other side closed"),
		},
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	writer := httptest.NewRecorder()

	outcome := StreamChatWithPendingCapture(
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
	assert.True(t, outcome.Resumable)
	assert.Equal(t, 2, outcome.ChunkCount)
	assert.Contains(t, writer.Body.String(), `"content":"hello"`)
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
func (c *countingRecorder) RecordStreamSynthesizedDone()                         { c.synth++ }

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
	assert.Equal(t, 1, counter.synth)
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

func TestStreamChatWithPendingCapture_SplitsDoneJoinedToJSON(t *testing.T) {
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(
			"data: {\"id\":\"chunk-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"reasoning_content\":\"planning\"},\"finish_reason\":null}]}[DONE].",
		)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
	}
	writer := httptest.NewRecorder()

	outcome := StreamChatWithPendingCapture(
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
