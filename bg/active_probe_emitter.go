// bg/active_probe_emitter.go — translate a ProbeResult into a
// request_logs row, so the probe shows up in the realtime request stream.
//
// Each emission:
//  1. builds a telemetry.RequestLogEntry with task_type='probe_triggered'
//     and task_type_chosen='probe_direct' (room for future 'probe_gateway')
//  2. sets quality_flags=['probe','direct', ...] so the live-stream
//     dashboard can filter on these rows
//  3. sets parent_request_id = the original failed business request_id
//     so operators can correlate "client retry caused this probe" ↔ probe
//  4. calls telemetryClient.EmitRequestLogInsert which both persists to
//     request_logs_hot AND fires the onEmitted hook (live-stream SSE hub)
//     from the same goroutine — the dashboard sees the row immediately
//
// No new database table is introduced; we re-use request_logs + its
// auto-route observability columns (task_type, task_type_chosen, is_auto_request,
// quality_flags, auto_decision JSONB).
package bg

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// ActiveProbeEmitter pushes probe results into the live request stream.
type ActiveProbeEmitter struct {
	telemetry *telemetry.Client
}

// NewActiveProbeEmitter constructs an emitter. Passing a nil telemetry
// client is safe (emits become no-ops).
func NewActiveProbeEmitter(tc *telemetry.Client) *ActiveProbeEmitter {
	return &ActiveProbeEmitter{telemetry: tc}
}

// Emit builds a RequestLogEntry for the probe result and feeds it to the
// telemetry client. Safe to call with a nil emitter (returns immediately).
func (e *ActiveProbeEmitter) Emit(
	ctx context.Context,
	credID int,
	providerID int,
	tenantID string,
	rawModel string,
	outboundModel string,
	parentReqID string,
	attempt int,
	result *ProbeResult,
) {
	if e == nil || e.telemetry == nil || !e.telemetry.Enabled() {
		return
	}

	if tenantID == "" {
		tenantID = "default"
	}

	success := result.Status == ProbeStatusSuccess
	ts := result.StartedAt
	latencyMs := result.LatencyMs

	var errKind *string
	if !success {
		k := classifyProbeErrorKind(result)
		errKind = &k
	}

	requestID := buildProbeRequestID(credID, rawModel, attempt, success, ts)
	failureStage := "upstream"
	if success {
		failureStage = ""
		// keep empty so request_logs.failure_stage stays NULL for ok rows
	}

	promptTokens := 1
	completionTokens := 0
	if success {
		// optimistic — the model at least echoed the prompt and gave us
		// back a finish_reason, so 1 completion token is a fair upper bound.
		completionTokens = 1
	}

	autoDecision, err := json.Marshal(map[string]any{
		"probe_attempt":     attempt,
		"probe_origin":      "direct",
		"probe_trigger":     "consecutive_failures",
		"parent_request_id": parentReqID,
		"probe_http_status": result.HTTPStatus,
		"probe_status":      string(result.Status),
		"probe_err_code":    result.ErrCode,
		"probe_latency_ms":  result.LatencyMs,
		"tenant_id":         tenantID,
	})
	if err != nil {
		autoDecision = []byte(`{}`)
	}
	autoDecisionStr := string(autoDecision)

	providerIDCopy := providerID
	credIDCopy := credID

	entry := &telemetry.RequestLogEntry{
		RequestID:        requestID,
		TenantID:         tenantID,
		ClientModel:      strPtrTelemetry(rawModel),
		OutboundModel:    strPtrTelemetry(outboundModel),
		CredentialID:     &credIDCopy,
		ProviderID:       &providerIDCopy,
		Success:          success,
		RequestStatus:    strPtrTelemetry(telemetry.RequestStatusFailure),
		ErrorKind:        errKind,
		LatencyMs:        &latencyMs,
		PromptTokens:     &promptTokens,
		CompletionTokens: &completionTokens,
		FailureStage:     strPtrOrNil(failureStage),
		// Link back to the original failed business request so /request-logs
		// can correlate the probe row with its trigger.
		ParentRequestID: strPtrTelemetry(parentReqID),
		ClientRequestID: strPtrTelemetry(parentReqID),
		// 2026-07-13: probe observability fields
		IsAutoRequest:  boolPtrTelemetry(true),
		TaskType:       strPtrTelemetry("probe_triggered"),
		TaskTypeChosen: strPtrTelemetry("probe_direct"),
		QualityFlags:   buildProbeQualityFlags(result, attempt),
		AutoDecision:   &autoDecisionStr,
	}
	// Override the success-path request_status so the row reads
	// "success" (the default fallback writes "failure" for !success
	// rows; for success rows we explicitly set it here).
	if success {
		entry.RequestStatus = strPtrTelemetry(telemetry.RequestStatusSuccess)
		entry.ErrorKind = nil
		entry.FailureStage = nil
	}

	e.telemetry.EmitRequestLogInsert(entry)
}

// buildProbeRequestID returns the unique request_id for a probe row.
// Format: "probe-direct-c{cred}-m{model}-a{attempt}-{ok|fail}-{unix_nano}"
//
// The "probe-direct-" prefix matches the dashboard filter convention so
// operators can grep request_logs by request_id LIKE 'probe-direct-%'.
// All non-alphanumeric model characters are sanitised to '_' so the id
// is safe to use in URLs, log fields, and the SSE JSON envelope.
func buildProbeRequestID(credID int, model string, attempt int, success bool, ts time.Time) string {
	suffix := "fail"
	if success {
		suffix = "ok"
	}
	safe := sanitizeModelForID(model)
	return fmt.Sprintf("probe-direct-c%d-m%s-a%d-%s-%d", credID, safe, attempt, suffix, ts.UnixNano())
}

// buildProbeQualityFlags returns the array stored in request_logs.quality_flags.
// The dashboard uses these flags to:
//
//  1. recognise the row as a probe (presence of "probe")
//  2. identify the origin (presence of "direct" or "gateway")
//  3. surface timeout/final-attempt flags as pills in the UI
func buildProbeQualityFlags(result *ProbeResult, attempt int) []string {
	flags := []string{"probe", "direct"}
	if result.Status == ProbeStatusTimeout {
		flags = append(flags, "probe_timeout")
	}
	if result.Status == ProbeStatusAuth {
		flags = append(flags, "probe_auth_failed")
	}
	if result.Status == ProbeStatusRate {
		flags = append(flags, "probe_rate_limited")
	}
	if result.Status == ProbeStatusHTTP5xx || result.Status == ProbeStatusHTTP4xx {
		flags = append(flags, "probe_http_error")
	}
	if attempt >= 5 {
		flags = append(flags, "probe_final_attempt")
	}
	return flags
}

// sanitizeModelForID strips characters that would break URL / log parsing.
func sanitizeModelForID(s string) string {
	if s == "" {
		return "unknown"
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '-', c == '_', c == '.':
			b.WriteRune(c)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// strPtrTelemetry / boolPtrTelemetry — local helpers to avoid importing
// domains/streaming (which would create an import cycle bg → streaming → bg).
func strPtrTelemetry(s string) *string { return &s }
func boolPtrTelemetry(b bool) *bool    { return &b }

// strPtrOrNil returns nil for the empty string so that request_logs
// columns like failure_stage stay NULL rather than being stored as "".
func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
