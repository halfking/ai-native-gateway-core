package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/attachments"                   //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/authentication"                //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"                   //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/session"                       //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"           //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	agenttelemetry "github.com/kaixuan/llm-gateway-go/telemetry"              //nolint:depguard // aliased: system-prompt extractor for agent fallback (avoids clash with /domains/hooks/observability/telemetry)
)

// jsonMarshal is a local alias used by auto_route.go to avoid pulling
// encoding/json into the test hot path. (encoding/json is already imported
// transitively through other helpers.)
func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

// RequestLogContext caches request facts across handler lifecycle stages
// (auth, body read, routing, upstream, response) so every exit path emits
// a complete request_logs row with user/application correlation.
type RequestLogContext struct {
	handler *ChatHandler
	RequestID string

	// terminalKind guards the captured kind for the lifetime of the
	// request once a winner has claimed the terminal transition.
	// Read via TerminalKind() while holding terminalMu (RLock).
	// 2026-07-28 §10 Step 2: support richer terminal classification
	// (success/failure/disconnect) for downstream logs/audit.
	terminalMu     sync.RWMutex
	terminalKind   string
	terminalEntry  *telemetry.RequestLogEntry
	// ClientRequestID is the X-Request-Id the client supplied, if any.
	// Persisted into request_logs.client_request_id for debug /
	// cross-system tracing. Distinct from RequestID (the server-generated
	// UUID that is the primary audit key) so client retries that reuse
	// the same id do not collapse into one row.
	ClientRequestID string
	StartTime       time.Time
	Request         *http.Request
	Session         *session.Session

	// ProvisionalSessionID is the auto-generated session id that the
	// handler attaches to early-failure branches via
	// applyProvisionalGatewaySessionHeader. It is recorded so that
	// sub-functions (notably serveWithExecutor) can recover the same
	// id without re-running ensureSessionID, which would otherwise
	// leak a different id on every helper invocation.
	ProvisionalSessionID string

	KeyInfo       *authentication.KeyInfo
	Body          []byte
	ClientModel   string
	OutboundModel string
	EndUser       string
	ProviderID    *int
	CredentialID  *int
	ResponseBody  []byte

	// v2.0 auto-route fields (populated when model="auto" was used)
	IsAutoRequest  bool
	TaskType       string
	WorkType       string // X-Gw-Work-Type header (ACC work type key)
	AutoProfile    string
	AutoDecision   []byte // serialised autoRouteDecision JSON
	AutoConfidence float64

	// D5: model-level fallback list. The canonical model names from the
	// auto-route CandidatesTop3, EXCLUDING the already-chosen winner. Used by
	// the handler's Exhausted path to retry a non-streaming request against the
	// next-best model when every credential for the chosen model is exhausted.
	// Kept as a plain []string (not the full decision) so the hot path doesn't
	// need to unmarshal the JSONB decision to read it.
	AutoFallbackModels []string

	// v3 (2026-06-19) session-level outbound body fields.
	// Populated by SessionCompressor.Prepare when it rewrites bodyBytes.
	// All nil when the session compressor was not active.
	OutboundBody            []byte
	OutboundMsgCount        *int
	OutboundTokenEst        *int
	OutboundMsgHashes       []byte // JSON [{index, sha256}]
	OutboundStrategy        string // compression_strategy value (e.g. "delta_append")
	OutboundSummaryMarker   string
	OutboundWindowTriggered string

	ErrCode string
	ErrMsg  string

	// 2026-07-15: 请求性质维度（侧表 request_context_attrs 消费）。
	// 这些字段是最佳努力存储，权威 turn_no 用 ROW_NUMBER 派生查询。
	AttemptNo   int    // 网关 failover 轮次（≥1）
	IsRetry     bool   // 客户端重试 / follow-up（FollowUpDepth > 0 或 client_request_id 重复）
	TurnNo      int    // 会话内轮次（best-effort，由 session 已知请求计数取）
	OriginStage string // self_check/node_probe/system_health/business/probe_*

	// 2026-08-06: 父子关联维度。Set from the X-Gw-Parent-Request-Id and
	// X-Gw-Source-Actor headers at handler entry (admin/auto_title_generator.go
	// forwards them on its loopback LLM call). Flow into
	// request_logs_hot.parent_request_id / origin_actor so operators can SQL
	// JOIN child ↔ parent and filter "all rows emitted by the auto-title
	// generator".
	ParentRequestID string // user request_id that triggered this loopback
	OriginActor     string // emitting component, e.g. "auto-title-generator"

	// 2026-07-17: 同步探测 hold 维度。executor 进入 no_candidate 分支时
	// 通过 OnProbeHoldStart/End 回调更新这三个字段；request_logs 写入时
	// 可序列化进 attachment JSON 用于运维追查。
	ProbeHoldStartedAt  *time.Time // hold 开始时刻（探针进入）
	ProbeHoldDurationMs int        // hold 总毫秒（OnProbeHoldEnd 计算）
	ProbeHoldRecovered  bool       // hold 结束时是否至少一个候选恢复

	// 2026-06-19 quality fix mode (017_quality_fix_mode.sql).
	// QualityFlags accumulates detected issues across the response
	// post-processing pass. QualityFixActions is the JSON-encoded
	// per-flag action summary written by relay/tool_call_quality.go.
	// QualityScore is the 0..1 score from computeScore; nil when the
	// provider is in 'off' mode. Streaming path appends to QualityFlags
	// incrementally as each delta chunk is processed.
	QualityFlags      []string
	QualityFixActions []byte
	QualityScore      *float64

	// 2026-06-30: 上游错误诊断字段 (migration 320)
	UpstreamStatusCode *int
	ClientTimeout      bool
	ClientEndpoint     string
	StreamChunkErrors  int
	StreamChunksSent   int
	streamChunkErrors  atomic.Int64
	streamChunksSent   atomic.Int64

	// 2026-07-01: 附件元数据字段 (migration 325)
	// 存储从请求体中提取的 base64/data-URI 附件元数据，写入 request_logs.attachments JSONB。
	Attachments []attachments.AttachmentMetadata

	// RoutingTracker (2026-07-20) 记录所有路由轮次，供 buildEntry 写入
	// request_logs.routing_attempts。nil 表示不追踪（旧路径/降级）。
	RoutingTracker *executors.RoutingAttemptsTracker

	// 2026-07-25: 请求/响应体大小（用于 Redis 实时统计和看板展示）
	RequestBodySize  int
	ResponseBodySize int

	// StreamCapture (2026-07-28 §5.5) is the single source of truth
	// for stream_chunks_sent / stream_chunk_errors. The executor
	// stores it on the log context so BuildFailureEntry and the
	// success-path emit read the same numbers; StreamCapture pins the
	// counters at MarkDone / MarkInterrupted, so concurrent emit
	// calls cannot observe a torn read.
	StreamCapture *audit.StreamCapture

	meta     requestAttemptMeta
	logged   bool
	terminal atomic.Bool
}

func (c *RequestLogContext) SetError(code, msg string) {
	if c == nil {
		return
	}
	c.ErrCode = code
	c.ErrMsg = msg
}

// SetUpstreamStatus records the upstream HTTP status code extracted from upstream.Error.
func (c *RequestLogContext) SetUpstreamStatus(statusCode int) {
	if c == nil || statusCode <= 0 {
		return
	}
	c.UpstreamStatusCode = &statusCode
}

// SetClientTimeout marks that the client disconnected or timed out.
func (c *RequestLogContext) SetClientTimeout(timeout bool) {
	if c == nil {
		return
	}
	c.ClientTimeout = timeout
}

// SetClientEndpoint records the client request endpoint path (e.g., /v1/chat/completions).
func (c *RequestLogContext) SetClientEndpoint(endpoint string) {
	if c == nil || endpoint == "" {
		return
	}
	c.ClientEndpoint = endpoint
}

// IncrementStreamChunkErrors increments the count of stream chunk
// errors. The counter is mirrored onto an atomic int so streaming
// bridges and the safety net can race freely on the same request
// without a mutex. The legacy int field is kept in sync for tests
// that read or write it directly.
func (c *RequestLogContext) IncrementStreamChunkErrors() {
	if c == nil {
		return
	}
	c.streamChunkErrors.Add(1)
	c.StreamChunkErrors = int(c.streamChunkErrors.Load())
}

// IncrementStreamChunksSent increments the count of successfully sent
// stream chunks. See IncrementStreamChunkErrors for the rationale.
func (c *RequestLogContext) IncrementStreamChunksSent() {
	if c == nil {
		return
	}
	c.streamChunksSent.Add(1)
	c.StreamChunksSent = int(c.streamChunksSent.Load())
}

// StreamChunkErrors returns the current value of the atomic counter in
// a way that callers (e.g. emitTelemetry) can copy into the immutable
// request_logs row.
func (c *RequestLogContext) StreamChunkErrorsValue() int {
	if c == nil {
		return 0
	}
	return int(c.streamChunkErrors.Load())
}

// StreamChunksSentValue returns the current value of the atomic counter.
func (c *RequestLogContext) StreamChunksSentValue() int {
	if c == nil {
		return 0
	}
	return int(c.streamChunksSent.Load())
}

// SetStreamChunkCounters is the bulk setter used by legacy tests and
// by handler paths that compute the final count before persisting. It
// keeps the int fields (which the schema layer still reads) in sync
// with the atomic counters, so a torn read cannot produce a mismatch
// between BuildFailureEntry and emitTelemetry.
func (c *RequestLogContext) SetStreamChunkCounters(chunkErrors, chunksSent int) {
	if c == nil {
		return
	}
	if chunkErrors < 0 {
		chunkErrors = 0
	}
	if chunksSent < 0 {
		chunksSent = 0
	}
	c.streamChunkErrors.Store(int64(chunkErrors))
	c.streamChunksSent.Store(int64(chunksSent))
	c.StreamChunkErrors = chunkErrors
	c.StreamChunksSent = chunksSent
}

// AddQualityFlag appends a single detected issue tag, deduplicating
// against the running slice. Safe to call from both non-stream
// (once) and streaming (per chunk) paths — dedup is required because
// the same flag will fire on every chunk that contains an empty
// name, and we don't want 50 rows of `empty_tool_name` in the array.
func (c *RequestLogContext) AddQualityFlag(flag string) {
	if c == nil || flag == "" {
		return
	}
	for _, s := range c.QualityFlags {
		if s == flag {
			return
		}
	}
	c.QualityFlags = append(c.QualityFlags, flag)
}

// SetQualityScore is called once per request after the quality pass
// has run. nil clears the score (off-mode / unevaluated).
func (c *RequestLogContext) SetQualityScore(score *float64) {
	if c == nil {
		return
	}
	c.QualityScore = score
}

// RecordFix merges a single {flag: {detected, renamed, dropped}} entry
// into the running QualityFixActions JSONB blob. JSONB merge is shallow:
// later keys with the same name overwrite earlier ones. Callers should
// only call this when the body was actually rewritten (mode == 'fix').
func (c *RequestLogContext) RecordFix(actions map[string]any) {
	if c == nil || len(actions) == 0 {
		return
	}
	var existing map[string]any
	if len(c.QualityFixActions) > 0 {
		// A decode failure here would leave `existing` nil and silently
		// overwrite every previously recorded fix action.
		if err := json.Unmarshal(c.QualityFixActions, &existing); err != nil {
			c.recordMetadataLoss("quality_fix_actions", err)
		}
	}
	if existing == nil {
		existing = map[string]any{}
	}
	for k, v := range actions {
		existing[k] = v
	}
	out, err := json.Marshal(existing)
	if err != nil {
		c.recordMetadataLoss("quality_fix_actions", err)
		return
	}
	c.QualityFixActions = out
}

// NewRequestLogContext starts a per-request log cache. Call EnsureCaptured early.
func (h *ChatHandler) NewRequestLogContext(r *http.Request, requestID string, start time.Time) *RequestLogContext {
	return &RequestLogContext{
		handler:   h,
		RequestID: requestID,
		StartTime: start,
		Request:   r,
	}
}

func (c *RequestLogContext) SetSession(session *session.Session) {
	c.Session = session
}

func (c *RequestLogContext) SetKey(keyInfo *authentication.KeyInfo) {
	c.KeyInfo = keyInfo
	c.refreshMeta()
}

func (c *RequestLogContext) SetClientModel(model string) {
	if strings.TrimSpace(model) != "" {
		c.ClientModel = strings.TrimSpace(model)
	}
}

// SetWorkType stores the X-Gw-Work-Type header for request_logs.work_type.
func (c *RequestLogContext) SetWorkType(wt string) {
	c.WorkType = strings.TrimSpace(wt)
}

func (c *RequestLogContext) SetOutboundModel(model string) {
	if strings.TrimSpace(model) != "" {
		c.OutboundModel = strings.TrimSpace(model)
	}
}

func (c *RequestLogContext) SetRoute(providerID, credentialID *int) {
	c.ProviderID = providerID
	c.CredentialID = credentialID
}

func applyWorkTypeField(entry *telemetry.RequestLogEntry, c *RequestLogContext) {
	if entry == nil || c == nil || c.WorkType == "" {
		return
	}
	entry.WorkType = strPtr(c.WorkType)
}

// applyParentCorrelationFields (2026-08-06) flows the X-Gw-Parent-Request-Id
// and X-Gw-Source-Actor headers (read at handler entry into logCtx.ParentRequestID
// / logCtx.OriginActor) into the persisted RequestLogEntry.
//
// This is what makes the auto-title loopback SQL-joinable to its parent user
// request:
//
//   SELECT child.request_id, child.parent_request_id, child.origin_actor
//   FROM request_logs_hot child
//   WHERE child.origin_actor = 'auto-title-generator'
//     AND child.ts > now() - interval '1 hour';
//
// Without this, request_logs_hot.parent_request_id is always NULL on title
// rows and operators have no way to correlate "08aa2a8a → 3a03f7db".
func applyParentCorrelationFields(entry *telemetry.RequestLogEntry, c *RequestLogContext) {
	if entry == nil || c == nil {
		return
	}
	if c.ParentRequestID != "" {
		entry.ParentRequestID = strPtr(c.ParentRequestID)
	}
	if c.OriginActor != "" {
		entry.OriginActor = strPtr(c.OriginActor)
	}
}

func applyAutoRouteFields(entry *telemetry.RequestLogEntry, c *RequestLogContext) {
	if entry == nil || c == nil {
		return
	}
	applyWorkTypeField(entry, c)
	if !c.IsAutoRequest {
		return
	}
	entry.IsAutoRequest = boolPtr(true)
	if c.TaskType != "" {
		entry.TaskType = strPtr(c.TaskType)
	}
	if c.AutoProfile != "" {
		entry.AutoProfile = strPtr(c.AutoProfile)
	}
	if len(c.AutoDecision) > 0 {
		v := string(c.AutoDecision)
		entry.AutoDecision = &v

		// P7.2: also populate the 4 promoted columns from the
		// JSON payload. Parses the same wire format that
		// relay/auto_route.go emits (autoRouteDecision) so the
		// columns track the JSONB field exactly. Tolerates partial
		// JSON or unexpected shapes — missing fields stay NULL.
		var wire autoRouteDecision
		if err := json.Unmarshal(c.AutoDecision, &wire); err == nil {
			if wire.TaskType != "" {
				entry.TaskTypeChosen = strPtr(wire.TaskType)
			}
			if wire.Confidence > 0 {
				c := wire.Confidence
				entry.ConfidenceNum = &c
			}
			if wire.ChosenModel != "" {
				entry.ModelChosen = strPtr(wire.ChosenModel)
			}
		}
	}
	if c.AutoConfidence > 0 {
		conf := c.AutoConfidence
		entry.AutoConfidence = &conf
	}
}

func (c *RequestLogContext) SetResponseBody(body []byte) {
	if len(body) > 0 {
		c.ResponseBody = body
	}
}

// preferCapturedBody keeps the request-context snapshot authoritative when a
// downstream result omitted a body. Executor results may be minimal on async
// or stream paths, while the context retains the original client payload.
func preferCapturedBody(primary, fallback []byte) []byte {
	if len(primary) > 0 {
		return primary
	}
	return fallback
}

// EnsureCaptured buffers the JSON body (restores r.Body) and fills key/identity meta.
//
// A read failure here is NOT benign: readRequestBody returns whatever it read
// before the timeout or size limit, and bufferRequestBody installs that partial
// payload as r.Body. The truncated prompt is therefore both persisted AND
// forwarded upstream, while the row still looks like an ordinary short request.
// The loss is recorded so it stops being invisible.
func (c *RequestLogContext) EnsureCaptured() {
	if c == nil || c.Request == nil {
		return
	}
	if err := ensureRequestBodyBuffered(c.Request, &c.Body, &c.ClientModel); err != nil {
		c.recordBodyCaptureFailure(err)
	}
	// 2026-07-27: 智能体兜底识别 — 从已缓冲 body 抽出 system prompt,
	// 让 fillAttemptMeta 能在 AgentName == "unknown" 时调用语义匹配。
	// 只在第一次捕获后填一次,避免每次 refresh 都重新解析。
	if c.meta.SystemPrompt == "" && len(c.Body) > 0 {
		c.meta.SystemPrompt = agenttelemetry.ExtractSystemPromptFromBody(c.Body, c.Request.URL.Path)
	}
	c.refreshMeta()
}

// recordMetadataLoss reports request metadata dropped by a marshal/unmarshal
// failure. Metadata loss degrades diagnosis rather than the request itself, so
// it is recorded at medium severity; field name and reason only, never content.
func (c *RequestLogContext) recordMetadataLoss(field string, err error) {
	if c == nil {
		return
	}
	if c.handler == nil {
		slog.Warn("data loss: "+AnomalyMetadataDropped,
			"request_id", c.RequestID, "field", field, "error", err)
		return
	}
	ctx := context.Background()
	if c.Request != nil {
		ctx = c.Request.Context()
	}
	c.handler.recordDataLoss(ctx, AnomalyMetadataDropped, string(SeverityMedium), c.RequestID,
		field+" dropped: "+err.Error(),
		map[string]any{"field": field})
}

// recordBodyCaptureFailure reports a truncated or unreadable request body.
// Sizes and the reason only — the body itself is a user prompt.
func (c *RequestLogContext) recordBodyCaptureFailure(err error) {
	reason := "read_error"
	severity := SeverityHigh
	switch {
	case errors.Is(err, errBodyTooLarge):
		reason = "body_too_large"
	case errors.Is(err, context.DeadlineExceeded):
		reason = "read_timeout"
	case errors.Is(err, context.Canceled):
		// Client went away mid-upload: the partial body is expected rather than
		// a gateway defect, so it is recorded at a lower severity.
		reason = "client_canceled"
		severity = SeverityMedium
	}
	if c.handler == nil {
		slog.Warn("data loss: "+AnomalyRequestBodyTruncated,
			"request_id", c.RequestID, "reason", reason,
			"captured_bytes", len(c.Body), "error", err)
		return
	}
	c.handler.recordDataLoss(c.Request.Context(), AnomalyRequestBodyTruncated, string(severity), c.RequestID,
		"request body capture failed ("+reason+"): "+err.Error(),
		map[string]any{
			"reason":         reason,
			"captured_bytes": len(c.Body),
			"path":           c.Request.URL.Path,
		})
}

func (c *RequestLogContext) CapturePartialBody(body []byte) {
	capturePartialBodyOnReadError(body, &c.Body, &c.ClientModel)
}

func (c *RequestLogContext) refreshMeta() {
	if c == nil || c.handler == nil || c.Request == nil {
		return
	}
	c.handler.fillAttemptMeta(c.Request, c.KeyInfo, &c.meta)
}

func (c *RequestLogContext) SessionTask() (sessionID, taskID string) {
	return gwSessionTaskFromRequest(c.Request, c.Session)
}

func (c *RequestLogContext) LatencyMs() int {
	if c == nil || c.StartTime.IsZero() {
		return 0
	}
	return int(time.Since(c.StartTime).Milliseconds())
}

func (c *RequestLogContext) RequestMode() string {
	if c.meta.RequestMode != "" {
		return c.meta.RequestMode
	}
	if c.Request != nil {
		return requestModeFromPath(c.Request.URL.Path)
	}
	return "chat"
}

// SetTerminal atomically claims the request terminal transition.
// kind is the high-level outcome (success/failure/disconnect/...).
// payload is the entry the caller intends to persist; it is captured
// for downstream logs+audit consumers but does not replace the
// per-handler builder. Returns true exactly once per request lifetime;
// subsequent callers (including MarkLogged) observe won=false.
//
// 2026-07-28 §10 Step 2: replace the previous single-arg variant so
// the gate can carry the snapshot of the entry chosen by the winner.
func (c *RequestLogContext) SetTerminal(kind string, payload *telemetry.RequestLogEntry) bool {
	if c == nil || kind == "" {
		return false
	}
	if !c.terminal.CompareAndSwap(false, true) {
		return false
	}
	c.terminalMu.Lock()
	c.terminalKind = kind
	c.terminalEntry = payload
	c.terminalMu.Unlock()
	return true
}

// TerminalKind returns the kind captured by the winning SetTerminal
// caller. Returns "" when no winner has claimed the terminal yet or
// the receiver is nil. Safe for concurrent reads.
func (c *RequestLogContext) TerminalKind() string {
	if c == nil {
		return ""
	}
	c.terminalMu.RLock()
	defer c.terminalMu.RUnlock()
	return c.terminalKind
}

// TerminalEntry returns the entry captured by the winning SetTerminal
// caller (if any). Returns nil when no winner has claimed the terminal
// yet or the receiver is nil. Safe for concurrent reads.
func (c *RequestLogContext) TerminalEntry() *telemetry.RequestLogEntry {
	if c == nil {
		return nil
	}
	c.terminalMu.RLock()
	defer c.terminalMu.RUnlock()
	return c.terminalEntry
}

func (c *RequestLogContext) IsTerminal() bool {
	return c != nil && c.terminal.Load()
}

func (c *RequestLogContext) MarkLogged() {
	if c != nil {
		c.logged = true
		c.terminal.CompareAndSwap(false, true)
	}
}
func (c *RequestLogContext) IsLogged() bool {
	return c != nil && (c.logged || c.IsTerminal())
}

// MarkProbeHoldStart is invoked by the executor when it enters the
// synchronous no-candidate probe hold. The timestamp is later used
// by MarkProbeHoldEnd to compute ProbeHoldDurationMs for trace /
// request_logs correlation. Idempotent.
func (c *RequestLogContext) MarkProbeHoldStart() {
	if c == nil {
		return
	}
	now := time.Now()
	c.ProbeHoldStartedAt = &now
}

// MarkProbeHoldEnd is invoked by the executor when the synchronous
// probe finishes. recovered is true iff at least one (cred,model)
// pair recovered and the request was retried. If MarkProbeHoldStart
// was not called first the duration is recorded as 0.
func (c *RequestLogContext) MarkProbeHoldEnd(recovered bool) {
	if c == nil {
		return
	}
	c.ProbeHoldRecovered = recovered
	if c.ProbeHoldStartedAt != nil {
		c.ProbeHoldDurationMs = int(time.Since(*c.ProbeHoldStartedAt).Milliseconds())
	}
}

// SetAutoDecision stores the auto-route decision for persistence in
// request_logs.auto_decision JSONB. Called from relay/handler.go when
// model="auto" was resolved.
func (c *RequestLogContext) SetAutoDecision(wire *autoRouteDecision) {
	if c == nil {
		return
	}
	c.IsAutoRequest = true
	if wire == nil {
		return
	}
	c.TaskType = wire.TaskType
	c.AutoProfile = wire.Profile
	c.AutoConfidence = wire.Confidence
	// D5: extract fallback model names (all top-3 except the chosen winner).
	if len(wire.CandidatesTop3) > 1 {
		fallbacks := make([]string, 0, len(wire.CandidatesTop3)-1)
		for _, cand := range wire.CandidatesTop3 {
			if cand.Model == "" || cand.Model == wire.ChosenModel {
				continue
			}
			fallbacks = append(fallbacks, cand.Model)
		}
		if len(fallbacks) > 0 {
			c.AutoFallbackModels = fallbacks
		}
	}
	b, err := jsonMarshal(wire)
	if err == nil {
		c.AutoDecision = b
	} else {
		// Without this the row shows is_auto_request=true with a NULL
		// auto_decision — indistinguishable from a row written before the
		// column existed.
		c.recordMetadataLoss("auto_decision", err)
	}
}

// BuildFailureEntry assembles a failure row from cached context + exit metadata.
func (c *RequestLogContext) BuildFailureEntry(errCode, errMessage string, providerID, credentialID *int) *telemetry.RequestLogEntry {
	return c.buildEntry(errCode, errMessage, providerID, credentialID, telemetry.RequestStatusFailure)
}

// buildEntry is the shared builder for non-success exits. status distinguishes
// a genuine system error ("failure") from a client-side rejection
// ("rate_limited"). Success is false in both cases — the request did not
// complete — but request_status lets dashboards count rate-limit rejections
// out of the error numerator while still keeping them in the denominator.
func (c *RequestLogContext) buildEntry(errCode, errMessage string, providerID, credentialID *int, status string) *telemetry.RequestLogEntry {
	if c == nil {
		return nil
	}
	if providerID != nil {
		c.ProviderID = providerID
	}
	if credentialID != nil {
		c.CredentialID = credentialID
	}
	c.refreshMeta()

	clientModel := c.ClientModel
	outboundModel := c.OutboundModel
	if clientModel == "" && len(c.Body) > 0 {
		clientModel = extractModelFromBody(c.Body)
	}

	gwSessionID, gwTaskID := c.SessionTask()
	clientProfile := c.meta.ClientProfile
	identityHash := c.meta.IdentityHash
	if c.Request != nil && (clientProfile == "" || identityHash == "") {
		cp, ih := failedRequestIdentity(c.Request, c.KeyInfo)
		if clientProfile == "" {
			clientProfile = cp
		}
		if identityHash == "" {
			identityHash = ih
		}
	}

	var requestBodyText *string
	if len(c.Body) > 0 {
		v := string(redactAttachmentBodyIfEnabled(c.Body))
		requestBodyText = &v
	}
	var responseBodyText *string
	if len(c.ResponseBody) > 0 {
		v := string(c.ResponseBody)
		responseBodyText = &v
	}

	latency := c.LatencyMs()
	eventAt := c.StartTime
	if latency > 0 {
		eventAt = c.StartTime.Add(time.Duration(latency) * time.Millisecond)
	}
	tenantID := "default"
	apiKeyID := apiKeyIDForLog(c.KeyInfo, &c.meta)
	var applicationID *int
	if c.KeyInfo != nil {
		tenantID = c.KeyInfo.TenantID
		applicationID = appID(c.KeyInfo)
	}

	var requestPreviewPtr *string
	if preview := requestPreview(redactAttachmentBodyIfEnabled(c.Body)); preview != "" {
		requestPreviewPtr = strPtr(preview)
	}
	var responsePreviewPtr *string
	if preview := responsePreview(c.ResponseBody); preview != "" {
		responsePreviewPtr = strPtr(preview)
	}

	detailCode := mapGatewayErrorToDetail(errCode)
	failureStage := classifyFailureStage(errCode)

	var clientRequestIDPtr *string
	if c.ClientRequestID != "" {
		v := c.ClientRequestID
		clientRequestIDPtr = &v
	}

	// 2026-06-30: 准备上游诊断字段
	var clientEndpointPtr *string
	if c.ClientEndpoint != "" {
		clientEndpointPtr = &c.ClientEndpoint
	}

	var clientTimeoutPtr *bool
	if c.ClientTimeout {
		v := true
		clientTimeoutPtr = &v
	}

	// 2026-07-28 §5.5: prefer StreamCapture.Snapshot() over the
	// RequestLogContext atomics when the capture is attached. The
	// capture is the single source of truth and is pinned at
	// MarkDone / MarkInterrupted, so success + failure emit calls
	// read the same numbers. Fall back to the atomics when no
	// capture is attached (legacy test paths).
	sent, errs := streamCountersFromContext(c)
	var streamChunkErrorsPtr *int
	if errs > 0 {
		streamChunkErrorsPtr = &errs
	}

	// 2026-07-01 P0 fix: stream_chunks_sent is NOT NULL (migration 320).
	// Always provide a value (0 for uninitialized/negative sentinel) so
	// the INSERT/UPDATE never violates the NOT NULL constraint. This
	// mirrors the fix in BuildSuccessEntry (handler.go:2212) and ensures
	// both success and failure paths honour the schema contract.
	var streamChunksSentPtr *int
	if sent < 0 {
		sent = 0
	}
	streamChunksSentPtr = &sent

	// 2026-07-01: 序列化附件元数据为 JSONB
	attachmentsJSON, attachmentsErr := attachmentsJSON(c.Attachments)
	if attachmentsErr != nil {
		c.recordMetadataLoss("attachments", attachmentsErr)
	}

	// 2026-08-06: populate end_user_id from the unified resolver so
	// failure-path rows (auth_unavailable / invalid_key / model_forbidden /
	// session_forbidden / etc.) carry the same end-user identity as the
	// success-path emits. Previously this column was NULL on every
	// failure-row because buildEntry had no EndUserID field — the
	// dc767386f... incident is the user-facing symptom. See resolveEndUser
	// in handler.go for the full priority chain.
	//
	// Audit fix: also stash the resolved value into c.EndUser so the
	// client-disconnect probe and request_context_attrs side-table
	// (which both read c.EndUser) match the main request row. Previously
	// c.EndUser was a dead field (no production assignment), causing
	// parity divergence between the main row and side paths.
	endUser := resolveEndUser("", c.Request, c.Body)
	c.EndUser = endUser
	var endUserPtr *string
	if endUser != "" {
		endUserPtr = &endUser
	}

	reqLog := &telemetry.RequestLogEntry{
		RequestID:         c.RequestID,
		EventAt:           &eventAt,
		TenantID:          tenantID,
		ApplicationID:     applicationID,
		APIKeyID:          apiKeyID,
		EndUserID:         endUserPtr,
		ClientModel:       strPtr(clientModel),
		OutboundModel:     strPtr(outboundModel),
		ProviderID:        c.ProviderID,
		CredentialID:      c.CredentialID,
		ClientProfile:     strPtr(clientProfile),
		IdentityHash:      strPtr(identityHash),
		RequestMode:       strPtr(c.RequestMode()),
		GwSessionID:       strPtr(gwSessionID),
		GwTaskID:          strPtr(gwTaskID),
		LatencyMs:         &latency,
		Success:           false,
		RequestStatus:     strPtr(status),
		ErrorKind:         strPtr(errCode),
		FailureStage:      strPtr(failureStage),
		FailureDetailCode: strPtr(detailCode),
		RequestBody:       requestBodyText,
		RequestPreview:    requestPreviewPtr,
		ResponseBody:      responseBodyText,
		ResponsePreview:   responsePreviewPtr,
		ClientRequestID:   clientRequestIDPtr,
		// 2026-06-30: 上游诊断字段
		UpstreamStatusCode: c.UpstreamStatusCode,
		ClientTimeout:      clientTimeoutPtr,
		ClientEndpoint:     clientEndpointPtr,
		StreamChunkErrors:  streamChunkErrorsPtr,
		StreamChunksSent:   streamChunksSentPtr,
		// 2026-07-01: 附件元数据
		Attachments: attachmentsJSON,
	}
	enrichRequestLogFromMeta(reqLog, c.KeyInfo, &c.meta)
	applyAutoRouteFields(reqLog, c)
	// 2026-08-06: flow X-Gw-Parent-Request-Id / X-Gw-Source-Actor into the
	// persisted row so operators can SQL JOIN auto-title rows back to their
	// parent user request.
	applyParentCorrelationFields(reqLog, c)
	if len(c.OutboundBody) > 0 {
		reqLog.OutboundBody = json.RawMessage(c.OutboundBody)
	}

	// 2026-07-20: serialize routing attempts into request_logs
	if c.RoutingTracker != nil {
		jsonBytes, err := c.RoutingTracker.ToJSONBytes()
		switch {
		case err != nil:
			// The failover chain is exactly the data needed to debug the
			// failure this row records. ToJSONBytes also legitimately returns
			// (nil, nil), so without this the error case is fully masked.
			c.recordMetadataLoss("routing_attempts", err)
		case jsonBytes != nil:
			reqLog.RoutingAttempts = jsonBytes
		}
		if summary := c.RoutingTracker.Summary(); summary != "" {
			reqLog.RoutingSummary = &summary
		}
	}

	return reqLog
}

// EmitFailure writes/updates request_logs for a non-success exit.
func (c *RequestLogContext) EmitFailure(errCode, errMessage string, providerID, credentialID *int) {
	if c == nil || c.handler == nil {
		return
	}
	// 2026-08-02 (GAP 2): Use SetTerminal CAS so that success/failure/
	// disconnect three-way race has a single in-process winner. If
	// another path already claimed the terminal transition, skip the
	// emit entirely (the DB-level L-2 guard still prevents terminal
	// regression, but this avoids a double telemetry emit in-process).
	if !c.SetTerminal("failure", nil) {
		return
	}
	reqLog := c.BuildFailureEntry(errCode, errMessage, providerID, credentialID)
	if reqLog == nil {
		return
	}
	if c.handler.requestLogHook != nil {
		c.handler.requestLogHook(reqLog)
	}
	if c.handler.telemetryClient != nil && c.handler.telemetryClient.Enabled() {
		c.handler.telemetryClient.EmitRequestLogUpdate(reqLog)
	}
	// 2026-07-15: 侧表 request_context_attrs（best-effort）。
	if c.handler.telemetryClient != nil {
		if attrs := BuildContextAttrsEntry(c, c.KeyInfo, &c.meta, c.Request.Context()); attrs != nil {
			c.handler.telemetryClient.EmitContextAttrs(attrs)
		}
	}
	c.logged = true
}

// EmitRateLimited records a gateway-side rate-limit rejection (RPM/concurrent
// cap or a throttled key) as request_status="rate_limited" rather than
// "failure". Rate limiting is an expected, client-caused outcome — not a
// system error — so it must not inflate provider error counts, yet it stays
// in the dashboard denominator so success-rate is not artificially inflated.
// The provider/credential are always nil because the request never reached
// the executor or any upstream.
func (c *RequestLogContext) EmitRateLimited(errCode, errMessage string, providerID, credentialID *int) {
	if c == nil || c.handler == nil {
		return
	}
	// 2026-08-02 (GAP 2): Use SetTerminal CAS so that success/failure/
	// disconnect three-way race has a single in-process winner.
	if !c.SetTerminal("rate_limited", nil) {
		return
	}
	reqLog := c.buildEntry(errCode, errMessage, providerID, credentialID, telemetry.RequestStatusRateLimited)
	if reqLog == nil {
		return
	}
	if c.handler.requestLogHook != nil {
		c.handler.requestLogHook(reqLog)
	}
	if c.handler.telemetryClient != nil && c.handler.telemetryClient.Enabled() {
		c.handler.telemetryClient.EmitRequestLogUpdate(reqLog)
	}
	if c.handler.telemetryClient != nil {
		if attrs := BuildContextAttrsEntry(c, c.KeyInfo, &c.meta, c.Request.Context()); attrs != nil {
			c.handler.telemetryClient.EmitContextAttrs(attrs)
		}
	}
	c.logged = true
}

// failAndMark emits a failure row immediately (explicit exit paths).
func (c *RequestLogContext) failAndMark(errCode, errMsg string, providerID, credentialID *int) {
	c.EmitFailure(errCode, errMsg, providerID, credentialID)
}

// applySessionCompressorFields copies v3 session compressor outbound fields
// from RequestLogContext into a RequestLogEntry. Call this after building
// the entry so the outbound_body / outbound_msg_count / outbound_token_est /
// outbound_msg_hashes columns are populated.
//
// Also merges window_triggered and summary_marker into compression_meta JSONB
// (adds keys without overwriting existing v7 fields) and sets
// compression_strategy to the session compressor strategy when v7 left it
// empty.
func applySessionCompressorFields(entry *telemetry.RequestLogEntry, c *RequestLogContext) {
	if entry == nil || c == nil {
		return
	}

	// outbound_msg_count / outbound_token_est are always populated
	// (even when no compression strategy was applied) so the columns
	// in request_logs.*_hot are non-NULL for session-scoped requests.
	entry.OutboundMsgCount = c.OutboundMsgCount
	entry.OutboundTokenEst = c.OutboundTokenEst

	// 2026-07-27 (bugfix: outbound_body NULL on delta-only / fresh-session
	// requests): the handler now populates c.OutboundBody from
	// executor's result.RequestBody for every request path, not only when
	// compression fired. Persist the body unconditionally so the admin UI's
	// v3 转发体 tab always reflects what was forwarded upstream.
	if len(c.OutboundBody) > 0 {
		entry.OutboundBody = json.RawMessage(c.OutboundBody)
	}
	if c.OutboundStrategy == "" {
		return // compression_meta fields only when compression actually fired
	}
	if len(c.OutboundMsgHashes) > 0 {
		entry.OutboundMsgHashes = json.RawMessage(c.OutboundMsgHashes)
	}

	// compression_strategy: prefer v7 value if set, else use v3 strategy
	if entry.CompressionStrategy == nil || *entry.CompressionStrategy == "" {
		entry.CompressionStrategy = strPtr(c.OutboundStrategy)
	}

	// Merge window_triggered + summary_marker into compression_meta JSONB.
	if c.OutboundWindowTriggered != "" || c.OutboundSummaryMarker != "" {
		merged, err := mergeCompressionMetaV3(entry.CompressionMeta,
			c.OutboundWindowTriggered, c.OutboundSummaryMarker)
		if err != nil {
			c.recordMetadataLoss("compression_meta", err)
		}
		if len(merged) > 0 {
			entry.CompressionMeta = merged
		}
	}
}

// submitModeHeaderName is the client-supplied header the SubmitModeDetector
// treats as its P0 (authoritative) signal. Distinct from SessionHeadersPriority
// (which the gateway strips + reassigns); this one is read-only passthrough.
const submitModeHeaderName = "X-Gw-Submit-Mode"

// applySubmitModeHeader copies the X-Gw-Submit-Mode request header onto the
// telemetry entry so the v2 mirror can forward it to the SubmitModeDetector.
// Best-effort: absent header leaves the field nil and the detector falls back
// to LCS inference. Only the recognised values are forwarded so a malformed
// header cannot poison the turn's submit_mode.
func applySubmitModeHeader(entry *telemetry.RequestLogEntry, c *RequestLogContext) {
	if entry == nil || c == nil || c.Request == nil {
		return
	}
	v := strings.ToLower(strings.TrimSpace(c.Request.Header.Get(submitModeHeaderName)))
	switch v {
	case "delta", "snapshot", "full", "attachment_only":
		entry.SubmitModeHeader = strPtr(v)
	}
}

// mergeCompressionMetaV3 adds window_triggered and summary_marker to the
// existing compression_meta JSONB without clobbering v7 fields.
//
// Returns (result, droppedErr). A decode failure must NOT be merged into an
// empty map: doing so returns a blob containing only the two new keys and
// silently deletes the pre-existing v7 compression fields. On decode failure
// the existing blob is preserved untouched and the error is reported so the
// caller can record it.
func mergeCompressionMetaV3(existing json.RawMessage, windowTriggered, summaryMarker string) (json.RawMessage, error) {
	m := make(map[string]any)
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &m); err != nil {
			return existing, err
		}
	}
	if windowTriggered != "" {
		m["window_triggered"] = windowTriggered
	}
	if summaryMarker != "" {
		m["summary_marker"] = summaryMarker
	}
	if len(m) == 0 {
		return existing, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return existing, err
	}
	return b, nil
}

// applyRoutingMetadata (spec §12 GAP 3) merges the per-request
// routing_state_source and conversion_path into the request_logs
// metadata so each row can be attributed to its routing decision
// (authoritative / fallback / canary / off) and its transport path
// (ir / legacy). Until a dedicated routing_state_source column is
// migrated (TODO migration), the values land in the existing
// compression_meta JSONB — a generic metadata map that
// applySessionCompressorFields already merges additively, so this is
// purely additive and avoids a schema change in the observability-only
// GAP.
//
// Best-effort: RoutingSourceForRequest returns empty values when the
// entry was never recorded or was evicted (bounded map); in that case
// nothing is merged and the row keeps its pre-GAP-3 metadata, never a
// failure.
func applyRoutingMetadata(entry *telemetry.RequestLogEntry, requestID string) {
	if entry == nil || requestID == "" {
		return
	}
	source, conversionPath := executors.RoutingSourceForRequest(requestID)
	if source == "" && conversionPath == "" {
		return
	}
	m := make(map[string]any)
	if len(entry.CompressionMeta) > 0 {
		// Decode failure: preserve the existing blob untouched rather
		// than clobbering pre-existing compression fields (same contract
		// as mergeCompressionMetaV3). Record the loss for diagnosis.
		if err := json.Unmarshal(entry.CompressionMeta, &m); err != nil {
			return
		}
	}
	changed := false
	if source != "" {
		m["routing_state_source"] = string(source)
		changed = true
	}
	if conversionPath != "" {
		m["conversion_path"] = conversionPath
		changed = true
	}
	if !changed {
		return
	}
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	entry.CompressionMeta = b
}

// ─── 2026-07-15: 请求性质维度 setter ───

// SetAttemptNo 记录网关 failover 轮次（从候选执行器取，≥1）。
func (c *RequestLogContext) SetAttemptNo(n int) {
	if c == nil || n <= 0 {
		return
	}
	if n > c.AttemptNo {
		c.AttemptNo = n
	}
}

// MarkRetry 标记为重试请求（客户端重试或 follow-up）。
func (c *RequestLogContext) MarkRetry() {
	if c == nil {
		return
	}
	c.IsRetry = true
}

// SetTurnNo 显式设置轮次号（best-effort；权威口径见 ROW_NUMBER 派生）。
func (c *RequestLogContext) SetTurnNo(n int) {
	if c == nil || n <= 0 {
		return
	}
	c.TurnNo = n
}

// SetOriginStage 设置 origin_stage（business/self_check/node_probe/system_health/...）。
func (c *RequestLogContext) SetOriginStage(stage string) {
	if c == nil {
		return
	}
	c.OriginStage = strings.TrimSpace(stage)
}

// streamCountersFromContext (2026-07-28 §5.5) returns the
// (chunksSent, chunkErrors) counters, preferring the per-request
// StreamCapture (single source of truth, pinned at finalisation)
// over the legacy RequestLogContext atomics. When the capture is
// attached we also refresh the log-context atomics so other readers
// stay in sync.
func streamCountersFromContext(c *RequestLogContext) (sent, errs int) {
	if c == nil {
		return 0, 0
	}
	if c.StreamCapture != nil {
		sent, errs = c.StreamCapture.ChunkCountersSnapshot()
		if sent < 0 {
			sent = 0
		}
		if errs < 0 {
			errs = 0
		}
		c.SetStreamChunkCounters(errs, sent)
		return sent, errs
	}
	return c.StreamChunksSentValue(), c.StreamChunkErrorsValue()
}
