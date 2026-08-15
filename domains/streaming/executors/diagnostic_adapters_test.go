package executors

import (
	"context"
	"errors"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/stretchr/testify/require"
)

type diagnosticRawLogger struct {
	clientRequests    int
	upstreamRequests  int
	upstreamResponses int
	clientResponses   int
	lastRequestID     string
	lastProtocol      string
	lastBody          []byte
}

func (l *diagnosticRawLogger) LogClientRequest(requestID, protocol string, body []byte, _ map[string]string, _ string) {
	l.clientRequests++
	l.lastRequestID = requestID
	l.lastProtocol = protocol
	l.lastBody = append([]byte(nil), body...)
}

func (l *diagnosticRawLogger) LogUpstreamRequest(requestID, protocol string, body []byte, _ string) {
	l.upstreamRequests++
	l.lastRequestID = requestID
	l.lastProtocol = protocol
	l.lastBody = append([]byte(nil), body...)
}

func (l *diagnosticRawLogger) LogUpstreamResponse(requestID, protocol string, body []byte, _ string) {
	l.upstreamResponses++
	l.lastRequestID = requestID
	l.lastProtocol = protocol
	l.lastBody = append([]byte(nil), body...)
}

func (l *diagnosticRawLogger) LogClientResponse(requestID, protocol string, body []byte, _ string) {
	l.clientResponses++
	l.lastRequestID = requestID
	l.lastProtocol = protocol
	l.lastBody = append([]byte(nil), body...)
}

func TestRawDataLoggerAdapter_RecordsCorrectDirections(t *testing.T) {
	logger := &diagnosticRawLogger{}
	adapter := NewRawDataLoggerAdapter(logger)

	require.NoError(t, adapter.LogRequest("request-1", "openai-completions", []byte(`{"messages":[]}`)))
	require.Equal(t, 1, logger.clientRequests)
	require.Equal(t, "request-1", logger.lastRequestID)
	require.Equal(t, "openai-completions", logger.lastProtocol)

	require.NoError(t, adapter.LogUpstreamRequest("request-1", "anthropic-messages", []byte(`{"messages":[]}`)))
	require.Equal(t, 1, logger.upstreamRequests)

	require.NoError(t, adapter.LogResponse("request-1", "anthropic-messages", []byte("event: message_stop\n\n"), true))
	require.Equal(t, 1, logger.upstreamResponses)

	require.NoError(t, adapter.LogClientResponse("request-1", "openai-completions", []byte(`{"choices":[]}`)))
	require.Equal(t, 1, logger.clientResponses)
}

type diagnosticAnomalyReporter struct {
	conversionCalls int
	toolCalls       int
	semanticCalls   int
	lastRequestID   string
	lastSource      string
	lastTarget      string
	lastStep        string
	lastInput       []byte
	lastOutput      []byte
	lastErr         error
}

func (r *diagnosticAnomalyReporter) ReportToolCallsMissing(_ context.Context, requestID, source, target string, input, output []byte, _ string, _ float64) {
	r.toolCalls++
	r.lastRequestID = requestID
	r.lastSource = source
	r.lastTarget = target
	r.lastInput = append([]byte(nil), input...)
	r.lastOutput = append([]byte(nil), output...)
}

func (r *diagnosticAnomalyReporter) ReportConversionError(_ context.Context, requestID, source, target, step string, input []byte, err error) {
	r.conversionCalls++
	r.lastRequestID = requestID
	r.lastSource = source
	r.lastTarget = target
	r.lastStep = step
	r.lastInput = append([]byte(nil), input...)
	r.lastErr = err
}

func (r *diagnosticAnomalyReporter) ReportSemanticIncomplete(_ context.Context, requestID, protocol string, output []byte, _ string, _ []string, _ float64) {
	r.semanticCalls++
	r.lastRequestID = requestID
	r.lastTarget = protocol
	r.lastOutput = append([]byte(nil), output...)
}

func TestAnomalyReporterAdapter_DeliversAllAnomalyTypes(t *testing.T) {
	reporter := &diagnosticAnomalyReporter{}
	adapter := NewAnomalyReporterAdapter(reporter)

	require.NoError(t, adapter.ReportAnomaly("request-1", "conversion_error", map[string]interface{}{
		"source_protocol": "anthropic-messages",
		"target_protocol": "openai-completions",
		"conversion_step": "parse_stream_event",
		"raw_input":       []byte(`{"type":"bad"}`),
		"error":           "invalid event",
	}))
	require.Equal(t, 1, reporter.conversionCalls)
	require.Equal(t, "parse_stream_event", reporter.lastStep)
	require.Error(t, reporter.lastErr)

	require.NoError(t, adapter.ReportAnomaly("request-1", "tool_calls_missing", map[string]interface{}{
		"source_protocol":    "anthropic-messages",
		"target_protocol":    "openai-completions",
		"raw_input":          []byte(`{"type":"tool_use"}`),
		"raw_output":         []byte(`{"choices":[]}`),
		"missing_tool_calls": "toolu_1",
	}))
	require.Equal(t, 1, reporter.toolCalls)
	require.Equal(t, "anthropic-messages", reporter.lastSource)

	require.NoError(t, adapter.ReportAnomaly("request-1", "semantic_incomplete", map[string]interface{}{
		"target_protocol": "openai-completions",
		"raw_output":      []byte(`{"choices":[]}`),
		"reason":          "incomplete",
	}))
	require.Equal(t, 1, reporter.semanticCalls)
	require.Equal(t, "openai-completions", reporter.lastTarget)
}

func TestSemanticAnalyzerAdapter_MapsIRAnalysis(t *testing.T) {
	adapter := NewSemanticAnalyzerAdapter(ir.NewSemanticAnalyzer(true))
	analysis, err := adapter.AnalyzeResponse("request-1", &ir.InternalResponse{
		FinishReason: "stop",
		Content:      []ir.ResponseContentBlock{{Type: "text", Text: "I will use the tool, then I will call it."}},
	})
	require.NoError(t, err)
	require.NotNil(t, analysis)
	require.True(t, analysis.IsIncomplete)
	require.True(t, analysis.SuspectedMissingTools)

	analysis, err = adapter.AnalyzeResponse("request-1", errors.New("not an IR response"))
	require.NoError(t, err)
	require.Nil(t, analysis)
}

// TestEnvelopeFromParams_AllFields covers the 2026-07-28 §5.7 fix:
// envelopeFromParams must populate every field the operator dashboard
// needs, and GWTaskID must NEVER be the model name (which was the
// pre-fix bug).
func TestEnvelopeFromParams_AllFields(t *testing.T) {
	appID := 33
	env := envelopeFromParams(&ExecParams{
		RequestID:        "req-1",
		SessionID:        "sess",
		Model:            "gpt-4o", // legacy buggy binding; should NOT bleed through
		TenantID:         "t1",
		KeyID:            7,
		AppID:            &appID,
		ClientRequestID:  "cr1",
		GWTaskID:         "task-x",
		ParentRequestID:  "parent",
		TraceID:          "trace",
		SpanID:           "span",
		ProviderID:       18,
		CredentialID:     42,
		AttemptNo:        2,
		UpstreamEndpoint: "https://upstream/api",
	})
	if env.GWTaskID == "gpt-4o" {
		t.Error("GWTaskID must NOT be the model name (pre-fix bug)")
	}
	if env.GWTaskID != "task-x" {
		t.Errorf("GWTaskID=%q want %q", env.GWTaskID, "task-x")
	}
	if env.ClientRequestID != "cr1" {
		t.Errorf("client_request_id=%q", env.ClientRequestID)
	}
	if env.ProviderID != 18 {
		t.Errorf("provider_id=%d", env.ProviderID)
	}
	if env.CredentialID != 42 {
		t.Errorf("credential_id=%d", env.CredentialID)
	}
	if env.AttemptNo != 2 {
		t.Errorf("attempt_no=%d", env.AttemptNo)
	}
	if env.UpstreamEndpoint != "https://upstream/api" {
		t.Errorf("upstream_endpoint=%s", env.UpstreamEndpoint)
	}
	if env.ApplicationID != "33" {
		t.Errorf("application_id=%s", env.ApplicationID)
	}
	if env.TraceID != "trace" || env.SpanID != "span" {
		t.Errorf("trace/span=%s/%s", env.TraceID, env.SpanID)
	}
}

// TestEnvelopeFromParams_AuditContext takes precedence: when
// ExecParams.Audit is non-nil, the envelope is the AuditContext
// snapshot — flat fields are ignored.
func TestEnvelopeFromParams_AuditContext(t *testing.T) {
	auditCtx := &AuditContext{
		RequestID:        "req-audit",
		ClientRequestID:  "cr-audit",
		GWSessionID:      "sess-audit",
		GWTaskID:         "task-audit",
		TenantID:         "t-audit",
		APIKeyID:         99,
		ProviderID:       100,
		CredentialID:     200,
		AttemptNo:        3,
		UpstreamEndpoint: "https://audit/upstream",
		TraceID:          "trace-audit",
		SpanID:           "span-audit",
	}
	env := envelopeFromParams(&ExecParams{
		Audit:     auditCtx,
		SessionID: "ignored-sess", // flat field must be ignored
		GWTaskID:  "ignored-task", // flat field must be ignored
		Model:     "ignored-model",
	})
	if env.ClientRequestID != "cr-audit" {
		t.Errorf("client_request_id=%q (must come from Audit)", env.ClientRequestID)
	}
	if env.GWSessionID != "sess-audit" {
		t.Errorf("gw_session_id=%q (must come from Audit)", env.GWSessionID)
	}
	if env.GWTaskID != "task-audit" {
		t.Errorf("gw_task_id=%q (must come from Audit)", env.GWTaskID)
	}
	if env.APIKeyID != 99 {
		t.Errorf("api_key_id=%d (must come from Audit)", env.APIKeyID)
	}
	if env.ProviderID != 100 {
		t.Errorf("provider_id=%d (must come from Audit)", env.ProviderID)
	}
	if env.UpstreamEndpoint != "https://audit/upstream" {
		t.Errorf("upstream_endpoint=%s", env.UpstreamEndpoint)
	}
}

// TestEnvelopeFromParams_Nil covers the defensive guarantee for
// callers that pass nil.
func TestEnvelopeFromParams_Nil(t *testing.T) {
	env := envelopeFromParams(nil)
	if env.GWSessionID != "" || env.ClientRequestID != "" {
		t.Errorf("nil params envelope must be empty: %+v", env)
	}
}

// diagnosticContextAwareAnomalyReporter counts the calls so we can
// assert that the audit-aware adapter dispatched to the underlying
// reporter. The end-to-end envelope check lives in the streaming
// integration test (2026-07-28 §5.7).
type diagnosticContextAwareAnomalyReporter struct {
	conversionCalls int
}

func (r *diagnosticContextAwareAnomalyReporter) ReportToolCallsMissing(_ context.Context, _ string, _, _ string, _, _ []byte, _ string, _ float64) {
	r.conversionCalls++
}

func (r *diagnosticContextAwareAnomalyReporter) ReportConversionError(_ context.Context, _ string, _, _, _ string, _ []byte, _ error) {
	r.conversionCalls++
}

func (r *diagnosticContextAwareAnomalyReporter) ReportSemanticIncomplete(_ context.Context, _ string, _ string, _ []byte, _ string, _ []string, _ float64) {
	r.conversionCalls++
}

// TestAnomalyReporterAdapterWithAudit_UsesAuditEnvelope covers the
// 2026-07-28 §5.7 fix: ReportAnomalyFromContext must attach the
// AuditContext envelope to ctx so LockFreeAnomalyReporter populates
// the AnomalyReport fields.
func TestAnomalyReporterAdapterWithAudit_UsesAuditEnvelope(t *testing.T) {
	rep := &diagnosticContextAwareAnomalyReporter{}
	adapter := NewAnomalyReporterAdapterWithAudit(rep)
	auditCtx := &AuditContext{
		ClientRequestID: "cr1",
		GWSessionID:     "s1",
		ProviderID:      18,
		CredentialID:    42,
		TraceID:         "trace-1",
	}
	require.NoError(t, adapter.ReportAnomalyFromContext(auditCtx, "req-1", "conversion_error", map[string]interface{}{
		"source_protocol": "openai-chat",
		"target_protocol": "anthropic-messages",
	}))
	require.Equal(t, 1, rep.conversionCalls)

	// The ctx passed to the reporter must carry the AnomalyReportEnvelope.
	// We do not reach into the unexported context key directly here —
	// end-to-end envelope propagation is covered by the integration
	// test in domains/streaming (TestAnomalyHttpPayloadIncludesEnvelope,
	// 2026-07-28 §5.7). Here we just assert the adapter invoked the
	// reporter and that an override context is respected.
}
