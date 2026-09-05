package streaming

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRawLogger records LogRequest/LogResponse calls from the stream bridges.
// errOnResponse lets a test assert that a failing logger stays invisible to the
// client.
type fakeRawLogger struct {
	mu            sync.Mutex
	responses     [][]byte
	protocols     []string
	streamFlags   []bool
	requests      [][]byte
	errOnResponse error
}

func (l *fakeRawLogger) LogRequest(_ string, protocol string, body []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requests = append(l.requests, append([]byte(nil), body...))
	l.protocols = append(l.protocols, protocol)
	return nil
}

func (l *fakeRawLogger) LogResponse(_ string, protocol string, body []byte, isStream bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.responses = append(l.responses, append([]byte(nil), body...))
	l.protocols = append(l.protocols, protocol)
	l.streamFlags = append(l.streamFlags, isStream)
	return l.errOnResponse
}

func (l *fakeRawLogger) frames() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, len(l.responses))
	for _, frame := range l.responses {
		out = append(out, string(frame))
	}
	return out
}

// panickingRawLogger models a diagnostic component that fails catastrophically
// rather than returning an error.
type panickingRawLogger struct{}

func (l *panickingRawLogger) LogRequest(_ string, _ string, _ []byte) error {
	panic("raw logger exploded")
}

func (l *panickingRawLogger) LogResponse(_ string, _ string, _ []byte, _ bool) error {
	panic("raw logger exploded")
}

type recordedAnomaly struct {
	anomalyType string
	details     map[string]interface{}
}

// fakeAnomalyReporter records anomalies. panicOnReport covers the case where a
// diagnostic component misbehaves badly rather than merely erroring.
type fakeAnomalyReporter struct {
	mu            sync.Mutex
	anomalies     []recordedAnomaly
	errOnReport   error
	panicOnReport bool
}

func (r *fakeAnomalyReporter) ReportAnomaly(_ string, anomalyType string, details map[string]interface{}) error {
	if r.panicOnReport {
		panic("diagnostic component exploded")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.anomalies = append(r.anomalies, recordedAnomaly{anomalyType: anomalyType, details: details})
	return r.errOnReport
}

func (r *fakeAnomalyReporter) types() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.anomalies))
	for _, a := range r.anomalies {
		out = append(out, a.anomalyType)
	}
	return out
}

func (r *fakeAnomalyReporter) find(anomalyType string) (recordedAnomaly, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.anomalies {
		if a.anomalyType == anomalyType {
			return a, true
		}
	}
	return recordedAnomaly{}, false
}

// fakeSemanticAnalyzer returns a canned verdict so the bridge's reporting path
// can be exercised without depending on real analyzer heuristics.
type fakeSemanticAnalyzer struct {
	mu       sync.Mutex
	calls    int
	result   *executors.SemanticAnalysisResult
	err      error
	lastResp interface{}
}

func (a *fakeSemanticAnalyzer) AnalyzeRequest(_ string, _ interface{}) error { return nil }

func (a *fakeSemanticAnalyzer) AnalyzeResponse(_ string, irResp interface{}) (*executors.SemanticAnalysisResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	a.lastResp = irResp
	return a.result, a.err
}

func (a *fakeSemanticAnalyzer) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// textChunk builds a minimal delta chunk carrying content text.
func textChunk(content string) *ir.StreamChunk {
	return &ir.StreamChunk{
		Type:  ir.ChunkTypeDelta,
		Delta: &ir.StreamDelta{Content: content},
	}
}

// sseServer serves body as an SSE stream and returns a live response.
func sseServer(t *testing.T, body string) *http.Response {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

const anthropicToolCallStream = "event: message_start\n" +
	"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-opus-4-8\",\"role\":\"assistant\",\"content\":[],\"type\":\"message\",\"usage\":{\"input_tokens\":4,\"output_tokens\":0}}}\n" +
	"\n" +
	"event: content_block_start\n" +
	"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tu_1\",\"name\":\"get_weather\",\"input\":{}}}\n" +
	"\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\\\"SF\\\"}\"}}\n" +
	"\n" +
	"event: content_block_stop\n" +
	"data: {\"type\":\"content_block_stop\",\"index\":0}\n" +
	"\n" +
	"event: message_delta\n" +
	"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":9}}\n" +
	"\n" +
	"event: message_stop\n" +
	"data: {\"type\":\"message_stop\"}\n" +
	"\n"

// TestStreamAnthropicSSEToOpenAI_LogsRawUpstreamFrames asserts the bridge hands
// pre-conversion upstream frames to the raw logger, marked as stream data.
func TestStreamAnthropicSSEToOpenAI_LogsRawUpstreamFrames(t *testing.T) {
	resp := sseServer(t, anthropicToolCallStream)
	logger := &fakeRawLogger{}
	diagnostics := &DiagnosticContext{RawLogger: logger}

	rec := httptest.NewRecorder()
	out := StreamAnthropicSSEToOpenAIWithDiagnostics(context.Background(),
		rec, resp, "claude-opus-4-8", "claude-opus-4-8", "req-raw", nil, nil, diagnostics,
	)
	require.False(t, out.Interrupted)

	frames := logger.frames()
	require.NotEmpty(t, frames, "expected upstream frames to be logged")

	joined := strings.Join(frames, "\n")
	assert.Contains(t, joined, `"type":"message_start"`, "raw frames should be pre-conversion Anthropic events")
	assert.Contains(t, joined, `"type":"tool_use"`)

	logger.mu.Lock()
	defer logger.mu.Unlock()
	for i, isStream := range logger.streamFlags {
		assert.True(t, isStream, "frame %d should be flagged as stream data", i)
	}
	assert.Empty(t, logger.requests, "response bridge must not record request payloads")
}

// TestStreamAnthropicSSEToOpenAI_HealthyToolCallRaisesNoAnomaly guards against
// false positives end to end: a tool call that converts correctly must reach the
// client and must not be flagged. The drop case is covered by the collector
// tests below, which can force zero emitted tool calls.
func TestStreamAnthropicSSEToOpenAI_HealthyToolCallRaisesNoAnomaly(t *testing.T) {
	resp := sseServer(t, anthropicToolCallStream)
	reporter := &fakeAnomalyReporter{}
	diagnostics := &DiagnosticContext{Anomaly: reporter}

	rec := httptest.NewRecorder()
	out := StreamAnthropicSSEToOpenAIWithDiagnostics(context.Background(),
		rec, resp, "claude-opus-4-8", "claude-opus-4-8", "req-missing", nil, nil, diagnostics,
	)
	require.False(t, out.Interrupted)

	assert.NotContains(t, reporter.types(), "tool_calls_missing",
		"a correctly converted tool call must not be flagged as missing")
	assert.Contains(t, rec.Body.String(), "tool_calls",
		"sanity check: the bridge should have emitted the tool call")
}

// TestStreamDiagnosticCollector_ReportsToolCallsMissing drives the collector
// directly for the drop case: raw frames carried a tool call, none was emitted.
func TestStreamDiagnosticCollector_ReportsToolCallsMissing(t *testing.T) {
	reporter := &fakeAnomalyReporter{}
	diagnostics := &DiagnosticContext{Anomaly: reporter}

	collector := &streamDiagnosticCollector{}
	collector.observeRaw([]byte(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu_1","name":"get_weather"}}`))
	collector.report(diagnostics, "req-drop", "anthropic", "openai", false)

	anomaly, ok := reporter.find("tool_calls_missing")
	require.True(t, ok, "expected tool_calls_missing, got %v", reporter.types())
	assert.Equal(t, "anthropic", anomaly.details["source_protocol"])
	assert.Equal(t, "openai", anomaly.details["target_protocol"])
	assert.Contains(t, anomaly.details["missing_tool_calls"], "get_weather",
		"anomaly should carry the dropped tool call as evidence")
}

// TestStreamDiagnosticCollector_NoAnomalyWhenToolCallEmitted guards against
// false positives once the tool call did reach the client.
func TestStreamDiagnosticCollector_NoAnomalyWhenToolCallEmitted(t *testing.T) {
	reporter := &fakeAnomalyReporter{}
	diagnostics := &DiagnosticContext{Anomaly: reporter}

	collector := &streamDiagnosticCollector{}
	collector.observeRaw([]byte(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu_1","name":"get_weather"}}`))
	collector.observeEmittedLine(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tu_1","function":{"name":"get_weather","arguments":"{}"}}]}}]}`)
	collector.report(diagnostics, "req-ok", "anthropic", "openai", false)

	assert.Empty(t, reporter.types(), "no anomaly expected when the tool call was emitted")
}

// TestStreamAnthropicSSEToOpenAI_ErroringDiagnosticsDoNotCorruptStream is the
// isolation guarantee: a raw logger that fails on every call must not change a
// single byte the client sees.
func TestStreamAnthropicSSEToOpenAI_ErroringDiagnosticsDoNotCorruptStream(t *testing.T) {
	baseline := httptest.NewRecorder()
	baseResp := sseServer(t, anthropicToolCallStream)
	baseOut := StreamAnthropicSSEToOpenAIWithDiagnostics(context.Background(),
		baseline, baseResp, "claude-opus-4-8", "claude-opus-4-8", "req-base", nil, nil, nil,
	)
	require.False(t, baseOut.Interrupted)

	withFailures := httptest.NewRecorder()
	failResp := sseServer(t, anthropicToolCallStream)
	diagnostics := &DiagnosticContext{
		RawLogger: &fakeRawLogger{errOnResponse: errors.New("disk full")},
		Anomaly:   &fakeAnomalyReporter{errOnReport: errors.New("endpoint down")},
		Semantic:  &fakeSemanticAnalyzer{err: errors.New("analyzer broken")},
	}
	failOut := StreamAnthropicSSEToOpenAIWithDiagnostics(context.Background(),
		withFailures, failResp, "claude-opus-4-8", "claude-opus-4-8", "req-base", nil, nil, diagnostics,
	)
	require.False(t, failOut.Interrupted, "diagnostic failures must not interrupt the stream")

	assert.Equal(t, baseline.Body.String(), withFailures.Body.String(),
		"failing diagnostics must not alter client-visible output")
}

// TestStreamDiagnosticCollector_PanickingReporterIsIsolated pins the isolation
// guarantee for panics, not just errors. report runs via defer in the bridges,
// so an escaping panic either unwound into the HTTP handler (stream.go, whose
// recover defer is registered after the report defer and therefore runs first)
// or was caught by the other bridges and mismarked a delivered stream as
// Interrupted, costing a failover and a circuit-breaker penalty.
func TestStreamDiagnosticCollector_PanickingReporterIsIsolated(t *testing.T) {
	diagnostics := &DiagnosticContext{Anomaly: &fakeAnomalyReporter{panicOnReport: true}}

	collector := &streamDiagnosticCollector{}
	collector.observeRaw([]byte(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu_1","name":"get_weather"}}`))

	assert.NotPanics(t, func() {
		collector.report(diagnostics, "req-panic", "anthropic", "openai", false)
	}, "a panicking diagnostic component must not escape report")
}

// TestDiagnosticHelpers_PanicIsolation covers the two hot-path helpers, which
// run per frame inside the stream read loops.
func TestDiagnosticHelpers_PanicIsolation(t *testing.T) {
	assert.NotPanics(t, func() {
		logRawUpstreamFrame(
			&DiagnosticContext{RawLogger: &panickingRawLogger{}},
			&executors.AuditContext{RequestID: "req-panic"},
			[]byte(`{"type":"ping"}`),
		)
	}, "a panicking raw logger must not abort the stream loop")

	assert.NotPanics(t, func() {
		reportConversionAnomaly(
			&DiagnosticContext{Anomaly: &fakeAnomalyReporter{panicOnReport: true}},
			"req-panic", "anthropic", "openai", "parse", []byte("raw"), errors.New("boom"), nil,
		)
	}, "a panicking anomaly reporter must not abort the stream loop")
}

// TestStreamDiagnosticCollector_InterruptedStreamSkipsToolCallAnomaly guards the
// dominant false positive: an interrupted stream can bank tool-call evidence
// and then die before the bridge converts it. Nothing was dropped by the
// gateway, so reporting it at confidence 1.0 would bury real conversion losses.
func TestStreamDiagnosticCollector_InterruptedStreamSkipsToolCallAnomaly(t *testing.T) {
	reporter := &fakeAnomalyReporter{}
	diagnostics := &DiagnosticContext{Anomaly: reporter}

	collector := &streamDiagnosticCollector{}
	collector.observeRaw([]byte(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu_1","name":"get_weather"}}`))
	collector.report(diagnostics, "req-interrupted", "anthropic", "openai", true)

	assert.Empty(t, reporter.types(), "interrupted streams must not report tool_calls_missing")
}

// TestReportConversionAnomaly_CappedPerRequest bounds the anomaly storm from an
// upstream whose frames the IR parser rejects: one report per frame otherwise
// saturates the shared reporter queue and evicts unrelated anomalies.
func TestReportConversionAnomaly_CappedPerRequest(t *testing.T) {
	reporter := &fakeAnomalyReporter{}
	diagnostics := &DiagnosticContext{Anomaly: reporter}

	for i := 0; i < 50; i++ {
		reportConversionAnomaly(
			diagnostics, "req-storm", "anthropic", "openai", "parse_stream_chunk",
			[]byte("bad frame"), errors.New("unknown event type"), nil,
		)
	}

	assert.Len(t, reporter.types(), maxConversionReportsPerRequest,
		"conversion_error reports must be capped per request")
}

// TestStreamDiagnosticCollector_SemanticIncompleteReported covers the analyzer
// verdict path reaching the anomaly reporter.
func TestStreamDiagnosticCollector_SemanticIncompleteReported(t *testing.T) {
	reporter := &fakeAnomalyReporter{}
	analyzer := &fakeSemanticAnalyzer{
		result: &executors.SemanticAnalysisResult{
			IsIncomplete:          true,
			SuspectedMissingTools: true,
			Reason:                "text promises a tool call that never arrived",
			Confidence:            0.8,
			Indicators:            []string{"promise_without_call"},
		},
	}
	diagnostics := &DiagnosticContext{Anomaly: reporter, Semantic: analyzer}

	collector := &streamDiagnosticCollector{}
	collector.observeChunk(textChunk("let me check the weather for you"))
	collector.report(diagnostics, "req-semantic", "anthropic", "openai", false)

	require.Equal(t, 1, analyzer.callCount())
	anomaly, ok := reporter.find("semantic_incomplete")
	require.True(t, ok, "expected semantic_incomplete, got %v", reporter.types())
	assert.Equal(t, "text promises a tool call that never arrived", anomaly.details["reason"])
	assert.Equal(t, 0.8, anomaly.details["confidence"])
}

// TestStreamDiagnosticCollector_SkipsSemanticAnalysisWhenTruncated documents the
// known blind spot: responses past the text cap skip semantic analysis.
func TestStreamDiagnosticCollector_SkipsSemanticAnalysisWhenTruncated(t *testing.T) {
	analyzer := &fakeSemanticAnalyzer{
		result: &executors.SemanticAnalysisResult{IsIncomplete: true, SuspectedMissingTools: true},
	}
	reporter := &fakeAnomalyReporter{}
	diagnostics := &DiagnosticContext{Anomaly: reporter, Semantic: analyzer}

	collector := &streamDiagnosticCollector{}
	collector.observeChunk(textChunk(strings.Repeat("a", maxDiagnosticTextBytes+1)))
	require.True(t, collector.textTruncated, "expected the text cap to trip")

	collector.report(diagnostics, "req-truncated", "anthropic", "openai", false)

	assert.Zero(t, analyzer.callCount(), "truncated responses currently skip semantic analysis")
	assert.Empty(t, reporter.types())
}

// TestDiagnosticHelpers_NilSafe covers every nil combination the wiring in
// main.go can produce when diagnostics are partially or fully disabled.
func TestDiagnosticHelpers_NilSafe(t *testing.T) {
	cases := []struct {
		name        string
		diagnostics *DiagnosticContext
	}{
		{"nil context", nil},
		{"empty context", &DiagnosticContext{}},
		{"only raw logger", &DiagnosticContext{RawLogger: &fakeRawLogger{}}},
		{"only anomaly", &DiagnosticContext{Anomaly: &fakeAnomalyReporter{}}},
		{"semantic without anomaly", &DiagnosticContext{Semantic: &fakeSemanticAnalyzer{}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				audit := &executors.AuditContext{RequestID: "req"}
				logRawUpstreamFrame(tc.diagnostics, audit, []byte(`{"type":"ping"}`))
				logRawUpstreamFrame(tc.diagnostics, audit, nil)
				reportConversionAnomaly(tc.diagnostics, "req", "anthropic", "openai", "step",
					[]byte("raw"), errors.New("boom"), nil)
				reportConversionAnomaly(tc.diagnostics, "req", "anthropic", "openai", "step",
					[]byte("raw"), nil, nil)

				collector := &streamDiagnosticCollector{}
				collector.observeRaw([]byte(`{"content":[{"type":"tool_use"}]}`))
				collector.observeChunk(nil)
				collector.observeEmittedChunk(nil)
				collector.report(tc.diagnostics, "req", "anthropic", "openai", false)
			})
		})
	}
}
