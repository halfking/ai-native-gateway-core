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
	"log/slog"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// ProbeEventSink is the interface a self-check SSE hub satisfies. bg cannot
// import admin (admin imports bg), so the wiring is structural: *admin.ProbeSSEHub
// implements PublishProbeEvent(bg.ProbeStreamEvent) directly, satisfying this
// interface. main.go wires the concrete hub via ActiveProbeEmitter.SetProbeSink.
// This mirrors the existing NodeProbeStateSink / ActiveProbeEmitter pattern.
type ProbeEventSink interface {
	PublishProbeEvent(task ProbeStreamEvent)
}

// ProbeStreamEvent is the bg-side projection of a probe lifecycle transition.
// It is converted to admin.ProbeStreamTask by the adapter in main.go. Keeping
// the bg-side shape free of admin imports preserves the dependency direction.
type ProbeStreamEvent struct {
	ID           string // unique run id
	TaskType     string // node_probe | integrity_verify | selfcheck
	Source       string // node_probe | integrity | selfcheck
	Status       string // pending | in-flight | ok | fail
	CredentialID int64
	ProviderID   int64
	ProviderCode string
	RawModel     string
	Attempt      int
	LatencyMs    *int
	HTTPStatus   *int
	ErrCode      string
	ErrDetail    string
	Scheduled    bool
	Reason       string
	TimestampMs  int64
}

// ActiveProbeEmitter pushes probe results into the live request stream.
type ActiveProbeEmitter struct {
	telemetry *telemetry.Client
	probeSink ProbeEventSink // optional SSE hook (2026-08-11 自检队列 SSE)
}

// NewActiveProbeEmitter constructs an emitter. Passing a nil telemetry
// client is safe (emits become no-ops).
func NewActiveProbeEmitter(tc *telemetry.Client) *ActiveProbeEmitter {
	return &ActiveProbeEmitter{telemetry: tc}
}

// SetProbeSink wires an optional SSE sink for the self-check queue stream.
// The emitter publishes a completed/failed transition on every Emit so the
// 自检 tab mirrors node-probe and integrity-probe outcomes in real time.
// Safe to call with nil (the hook becomes a no-op).
func (e *ActiveProbeEmitter) SetProbeSink(sink ProbeEventSink) {
	if e == nil {
		return
	}
	e.probeSink = sink
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
	origin string,
	parentReqID string,
	attempt int,
	result *ProbeResult,
) {
	if e == nil || result == nil {
		return
	}
	if origin == "" {
		origin = "direct"
	}

	success := result.Status == ProbeStatusSuccess
	ts := result.StartedAt
	latencyMs := result.LatencyMs
	requestID := buildProbeRequestID(credID, rawModel, attempt, success, ts)

	// 2026-08-11: mirror this probe outcome to the self-check SSE stream so
	// the 自检 tab updates in real time, INDEPENDENTLY of the telemetry path.
	// The SSE stream is a separate surface — a probe that can't reach telemetry
	// (e.g. telemetry disabled, transient DB error) must still update the
	// 自检 queue view.
	e.publishSink(requestID, credID, providerID, origin, parentReqID, attempt, rawModel, success, ts, latencyMs, result)

	if e.telemetry == nil || !e.telemetry.Enabled() {
		return
	}

	if tenantID == "" {
		tenantID = "default"
	}

	var errKind *string
	if !success {
		k := classifyProbeErrorKind(result)
		errKind = &k
	}

	// 2026-07-17: failure_stage is now derived from the real failure
	// point instead of a hardcoded "upstream". A decrypt/endpoint_build
	// failure is gateway-side and must read "gateway"; only faults that
	// reached the provider are "upstream". See classifyProbeFailureStage.
	failureStage := classifyProbeFailureStage(result)

	// 2026-07-17: token counts were previously hardcoded (1/0 fail, 1/1
	// success) as "optimistic placeholders". That misled operators into
	// reading a failed probe as "request sent, response incomplete"
	// (prompt=1, completion=0). Now: failures are 0/0 (nothing
	// billable); success attempts to parse the real usage from the
	// upstream response body, falling back to 0/0 when unparseable
	// rather than inventing a token.
	promptTokens := 0
	completionTokens := 0
	if success {
		if pt, ct, ok := parseProbeUsage(result.ResponseBody); ok {
			promptTokens = pt
			completionTokens = ct
		}
	}

	autoDecision, err := json.Marshal(map[string]any{
		"probe_attempt":       attempt,
		"probe_origin":        origin,
		"probe_trigger":       "consecutive_failures",
		"parent_request_id":   parentReqID,
		"probe_http_status":   result.HTTPStatus,
		"probe_status":        string(result.Status),
		"probe_err_code":      result.ErrCode,
		"probe_latency_ms":    result.LatencyMs,
		"probe_failure_stage": failureStage,
		"probe_via_proxy":     result.ViaProxy,
		"tenant_id":           tenantID,
	})
	if err != nil {
		// The payload is internal and should always be JSON-safe. Fail closed
		// if that invariant changes so a malformed probe row is never emitted.
		slog.Error("probe telemetry JSON encoding failed",
			"credential_id", credID,
			"model", rawModel,
			"attempt", attempt,
			"error", err)
		return
	}
	autoDecisionStr := string(autoDecision)

	entry := buildProbeRequestLogEntry(credID, providerID, tenantID, rawModel, outboundModel, origin, parentReqID, attempt, result)
	// Recompute the request_id-dependent fields that buildProbeRequestLogEntry
	// left to the caller (it stays a pure function of its inputs + ProbeResult,
	// while request_id encodes success/attempt/timestamp).
	entry.RequestID = requestID
	entry.ErrorKind = errKind
	entry.LatencyMs = &latencyMs
	entry.PromptTokens = &promptTokens
	entry.CompletionTokens = &completionTokens
	entry.FailureStage = strPtrOrNil(failureStage)
	entry.AutoDecision = &autoDecisionStr

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

// publishSink mirrors a probe outcome to the self-check SSE stream. It is
// called from Emit INDEPENDENTLY of the telemetry path so the 自检 tab stays
// live even when telemetry is disabled or transiently failing. The sink is
// optional; when unset (older wiring / tests) this is a no-op.
func (e *ActiveProbeEmitter) publishSink(
	requestID string,
	credID, providerID int,
	origin, parentReqID string,
	attempt int,
	rawModel string,
	success bool,
	ts time.Time,
	latencyMs int,
	result *ProbeResult,
) {
	if e == nil || e.probeSink == nil || result == nil {
		return
	}
	status := "ok"
	if !success {
		status = "fail"
	}
	var latPtr *int
	if latencyMs > 0 {
		latPtr = &latencyMs
	}
	var httpPtr *int
	if result.HTTPStatus != 0 {
		hs := result.HTTPStatus
		httpPtr = &hs
	}
	e.probeSink.PublishProbeEvent(ProbeStreamEvent{
		ID:           requestID,
		TaskType:     "node_probe",
		Source:       probeSourceForOrigin(origin),
		Status:       status,
		CredentialID: int64(credID),
		ProviderID:   int64(providerID),
		RawModel:     rawModel,
		Attempt:      attempt,
		LatencyMs:    latPtr,
		HTTPStatus:   httpPtr,
		ErrCode:      result.ErrCode,
		ErrDetail:    truncateErrDetail(result.ErrMsg),
		Reason:       parentReqID,
		TimestampMs:  ts.UnixMilli(),
	})
}

// probeSourceForOrigin maps the emitter's origin string to the SSE source
// badge consumed by the 自检 tab (node_probe | integrity | selfcheck).
func probeSourceForOrigin(origin string) string {
	switch origin {
	case "integrity", "integrity_verify":
		return "integrity"
	case "selfcheck", "self_check":
		return "selfcheck"
	default:
		return "node_probe"
	}
}

// truncateErrDetail caps the error detail so a verbose upstream body does not
// blow up the SSE envelope. 512 bytes is plenty for the err_code context.
func truncateErrDetail(s string) string {
	const max = 512
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// buildProbeRequestLogEntry assembles the telemetry.RequestLogEntry for a
// probe result. It is a pure function of its inputs (no telemetry client,
// no I/O) so it can be unit-tested directly — the live-stream dashboard,
// /request-logs correlation, and the chk_compression_parent_single CHECK
// constraint all depend on these fields being populated consistently, and
// a regression here silently drops probe rows (see the 2026-07-14 incident
// where every probe INSERT was rejected by SQLSTATE 23514).
//
// The caller is expected to fill in request_id, error_kind, latency_ms,
// token counts, failure_stage and auto_decision afterwards — those depend on
// values derived in Emit() (request_id encodes success/attempt/timestamp,
// tokens come from parseProbeUsage, etc.). Keeping them out of this helper
// lets the unit test assert the probe-attribution fields in isolation.
func buildProbeRequestLogEntry(
	credID, providerID int,
	tenantID, rawModel, outboundModel, origin, parentReqID string,
	attempt int,
	result *ProbeResult,
) *telemetry.RequestLogEntry {
	// A direct probe bypasses OriginMiddleware, so the row would otherwise
	// inherit whatever origin_stage/origin_actor the telemetry context
	// carries (typically "business" / empty). Label it explicitly so the
	// dashboard's probe filter (origin_stage IN node_probe/self_check/…)
	// matches these rows and operators can tell a synthetic probe apart
	// from real traffic. When the probe result did not carry an origin
	// (older callers), fall back to "node_probe" / "active-probe-worker"
	// rather than leaving the columns NULL and breaking the filter.
	stage := result.OriginStage
	if stage == "" {
		stage = "node_probe"
	}
	actor := result.OriginActor
	if actor == "" {
		actor = "active-probe-worker"
	}

	var requestBody, responseBody *string
	if result.RequestBody != "" {
		requestBody = strPtrTelemetry(result.RequestBody)
	}
	if result.ResponseBody != "" {
		responseBody = strPtrTelemetry(result.ResponseBody)
	}

	credIDCopy := credID
	providerIDCopy := providerID
	success := result.Status == ProbeStatusSuccess

	return &telemetry.RequestLogEntry{
		EventAt:       &result.CompletedAt,
		TenantID:      tenantID,
		ClientModel:   strPtrTelemetry(rawModel),
		OutboundModel: strPtrTelemetry(outboundModel),
		CredentialID:  &credIDCopy,
		ProviderID:    &providerIDCopy,
		Success:       success,
		// Default to the failure status; Emit() overrides to "success" on
		// the success path so this helper stays a pure mapping of inputs.
		RequestStatus: strPtrTelemetry(telemetry.RequestStatusFailure),
		// Explicit origin attribution for the direct-probe path.
		OriginStage: strPtrTelemetry(stage),
		OriginActor: strPtrTelemetry(actor),
		// Link back to the original failed business request so /request-logs
		// can correlate the probe row with its trigger.
		ParentRequestID: strPtrTelemetry(parentReqID),
		ClientRequestID: strPtrTelemetry(parentReqID),
		// 2026-07-14 fix: request_logs.chk_compression_parent_single is
		// `parent_request_id IS NULL OR compression_reason IS NOT NULL`.
		// The parent_request_id column was originally reserved for the
		// compression parent-child chain; we reuse it here for probe→trigger
		// correlation, so we MUST supply a non-NULL compression_reason to
		// satisfy the CHECK constraint. Without this every probe INSERT was
		// silently rejected with SQLSTATE 23514, the row never landed in
		// request_logs_hot, and the live-stream tile (pushed via onEmitted)
		// vanished on refresh / returned 404 on /api/logs/:id.
		// "probe_correlation" makes the non-compression intent explicit and
		// keeps the column self-documenting for operators reading the table.
		CompressionReason: strPtrTelemetry("probe_correlation"),
		// 2026-07-13: probe observability fields
		IsAutoRequest:  boolPtrTelemetry(true),
		TaskType:       strPtrTelemetry("probe_triggered"),
		TaskTypeChosen: strPtrTelemetry("probe_" + origin),
		QualityFlags:   buildProbeQualityFlags(result, attempt, origin),
		// 2026-07-19: Probe request/response bodies for diagnostics
		RequestBody:  requestBody,
		ResponseBody: responseBody,
	}
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
func buildProbeQualityFlags(result *ProbeResult, attempt int, origins ...string) []string {
	origin := "direct"
	if len(origins) > 0 && origins[0] != "" {
		origin = origins[0]
	}
	flags := []string{"probe", origin}
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

// parseProbeUsage extracts prompt/completion token counts from a probe
// response body. Most OpenAI-compatible providers return a `usage` object
// even for max_tokens=1 probes; Anthropic returns it at top level too.
// Returns ok=false when the body is empty, not JSON, or lacks a usage
// object — in which case the caller should report 0/0 rather than the
// old hardcoded 1/1 "optimistic" placeholder.
//
// We only parse the leading ~512 bytes of the body (RespPreview /
// ResponseBody are already truncated upstream), so this stays cheap.
func parseProbeUsage(body string) (promptTokens, completionTokens int, ok bool) {
	if len(body) == 0 {
		return 0, 0, false
	}
	var parsed struct {
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			// Anthropic shape:
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return 0, 0, false
	}
	if parsed.Usage == nil {
		return 0, 0, false
	}
	pt := parsed.Usage.PromptTokens
	ct := parsed.Usage.CompletionTokens
	if pt == 0 {
		pt = parsed.Usage.InputTokens
	}
	if ct == 0 {
		ct = parsed.Usage.OutputTokens
	}
	if pt == 0 && ct == 0 {
		return 0, 0, false
	}
	return pt, ct, true
}
