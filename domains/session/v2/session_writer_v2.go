package v2

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sessiondigest"
	"github.com/kaixuan/llm-gateway-go/settings"
)

const (
	aggregateMaxAttempts = 3
	aggregateRetryDelay  = 25 * time.Millisecond
)

// sessionUpdater is the minimal surface of SessionAggregator that
// SessionWriterV2 uses. Declared as an interface so unit tests can inject a
// fake (e.g. a recording aggregator) and so the lifecycle code can be tested
// without a live PostgreSQL instance.
type sessionUpdater interface {
	UpdateSession(ctx context.Context, update SessionUpdate) error
}

// SessionWriterV2 is the main coordinator for writing session data to V2 tables
//
// It orchestrates:
//   - TurnWriter: writes turn metadata to session_turns
//   - BodiesWriter: writes incremental message deltas to session_bodies
//   - SessionAggregator: updates session snapshots in sessions table
//
// This is the entry point for shadow writes alongside request_logs.
//
// Lifecycle (spec §6.3):
//
//	The session-snapshot aggregate update runs in a goroutine tracked by
//	aggWg and bound to lifecycleCtx. Callers MUST call Stop before the
//	process exits so the goroutine is awaited (it is no longer a detached
//	fire-and-forget). Stop is idempotent and safe to call from the
//	gateway shutdown goroutine.
type SessionWriterV2 struct {
	turnWriter        *TurnWriter
	bodiesWriter      *SessionBodiesWriter
	sessionAggregator sessionUpdater
	turnLogsWriter    *TurnLogsWriter
	// memoraWriter（706，可选）：会话首 turn 时写初始环境/上下文快照。
	// nil 时跳过（旧部署/测试无需该表存在）。
	memoraWriter *SessionMemoraWriter
	// detailsWriter（733/734 会话存储解耦 v3，可选）：每 turn 特征层
	// session_turn_details(_hot) 写入。nil 或表族缺席时跳过。
	detailsWriter *SessionTurnDetailsWriter
	// bodyMirror（2026-09-24 方案 H3，可选）：热区请求侧镜像投递回调，在
	// turn bodies / final_full 写入成功后 fire-and-forget 触发。nil 时零开销
	// 跳过（未装配热区 / lite 模式 / 离线 backfill 工具均不注入）。
	bodyMirror BodyMirrorFunc

	// aggWg tracks the in-flight aggregate snapshot goroutines so Stop can
	// wait for them (spec §6.3). Each Write that reaches the aggregate step
	// does Add(1) before launching the goroutine and Done() when it returns.
	aggWg sync.WaitGroup

	// lifecycleCtx / lifecycleCancel gate the aggregate goroutine. Stop
	// cancels lifecycleCtx so a blocked/slow aggregate returns promptly,
	// then waits on aggWg. New writes after Stop will see a cancelled ctx
	// and skip the aggregate (the primary turn+bodies write still runs).
	lifecycleCtx    context.Context
	lifecycleCancel context.CancelFunc
	lifecycleInit   sync.Once
	stopOnce        sync.Once

	// lifecycleMu serializes aggregate registration with Stop. Without this
	// gate, WaitGroup.Add could race with Wait when a write finishes as shutdown
	// begins, which is unsupported and can let work escape the drain.
	lifecycleMu sync.Mutex
	stopped     bool
}

// ensureLifecycle lazily initializes lifecycleCtx / lifecycleCancel for
// SessionWriterV2 instances that were constructed via a struct literal (e.g.
// in tests) instead of NewSessionWriterV2. Idempotent.
func (w *SessionWriterV2) ensureLifecycle() {
	w.lifecycleInit.Do(func() {
		if w.lifecycleCtx != nil && w.lifecycleCancel != nil {
			return
		}
		parent := w.lifecycleCtx
		if parent == nil {
			parent = context.Background()
		}
		ctx, cancel := context.WithCancel(parent)
		w.lifecycleCtx = ctx
		w.lifecycleCancel = cancel
	})
}

// NewSessionWriterV2 creates a new SessionWriterV2 instance
func NewSessionWriterV2(tw *TurnWriter, bw *SessionBodiesWriter, sa *SessionAggregator, tlw *TurnLogsWriter) *SessionWriterV2 {
	ctx, cancel := context.WithCancel(context.Background())
	return &SessionWriterV2{
		turnWriter:        tw,
		bodiesWriter:      bw,
		sessionAggregator: sa,
		turnLogsWriter:    tlw,
		lifecycleCtx:      ctx,
		lifecycleCancel:   cancel,
	}
}

// SetMemoraWriter wires the optional session_memora snapshot writer (706).
// Call before the first Write; nil disables the snapshot write.
func (w *SessionWriterV2) SetMemoraWriter(mw *SessionMemoraWriter) {
	w.memoraWriter = mw
}

// BodyMirrorFunc 是热区请求侧镜像的投递 seam（2026-09-24 方案 H3）。由存储
// 装配层注入，底层为 storage/file.RequestMirror 的 fire-and-forget 异步写
// （失败仅计数，绝不阻断主链路）。direction 取 "req"/"resp"/"out" 字面量
// （与 storage/file 的 DirRequest/DirResponse/DirOutput 值一致）；payload 是
// 与 session_bodies_hot 落库同源换算（safeJSONMarshal）的 JSON 原文；at 是
// 镜像日期分区取样时刻（对位 rec.Ts / req.Timestamp）。
//
// 实现契约：不等待、不返回错误、不因镜像失败改变 turn+bodies 主链路结果。
type BodyMirrorFunc func(tenantID, requestID, direction string, payload json.RawMessage, at time.Time)

// SetBodyMirror wires the optional hotzone request-body mirror (H3).
// Call before the first Write; nil disables mirroring — the turn+bodies path
// is unaffected. 与 SetMemoraWriter 同契约：仅启动装配期调用，不与在途请求并发。
func (w *SessionWriterV2) SetBodyMirror(fn BodyMirrorFunc) {
	w.bodyMirror = fn
}

// mirrorTurnBodies 把 turn bodies 三件套投递给热区镜像（H3，fire-and-forget）。
// 换算与 WriteBodiesInTx 同源（safeJSONMarshal / jsonTextOrNull 的空值语义：
// len==0 → PG 存 json null）；镜像侧跳过空与字面 "null" 载荷，不产生 null
// 噪声文件。镜像失败只由底层计数（BodyMirrorFunc 契约），不影响本链路。
// mirror outbox 重放（sessionv2mirror GAP-2）经同一 Write 路径再次触发镜像：
// 同 (tenant, requestID, direction) 路径覆盖写、内容一致，幂等无害。
func (w *SessionWriterV2) mirrorTurnBodies(rec BodiesRecord) {
	if w.bodyMirror == nil {
		return
	}
	if req, err := safeJSONMarshal(rec.RequestDelta); err == nil && mirrorableTurnPayload(req) {
		w.bodyMirror(rec.TenantID, rec.RequestID, "req", req, rec.Ts)
	}
	if resp, err := safeJSONMarshal(rec.ResponseDelta); err == nil && mirrorableTurnPayload(resp) {
		w.bodyMirror(rec.TenantID, rec.RequestID, "resp", resp, rec.Ts)
	}
	if out, err := safeJSONMarshal(rec.OutboundBody); err == nil && mirrorableTurnPayload(out) {
		w.bodyMirror(rec.TenantID, rec.RequestID, "out", out, rec.Ts)
	}
}

// mirrorFinalFull 投递 final_full 终态快照镜像（H3）：requestID 与 PG 行口径
// 一致（'final_full:<session>'，WriteFinalFullInTx 同款），仅 "out" 一个方向。
func (w *SessionWriterV2) mirrorFinalFull(req *ProcessedRequest) {
	if w.bodyMirror == nil || len(req.OutboundBody) == 0 {
		return
	}
	if out, err := safeJSONMarshal(req.OutboundBody); err == nil && mirrorableTurnPayload(out) {
		w.bodyMirror(req.TenantID, "final_full:"+req.SessionID, "out", out, req.Timestamp)
	}
}

// mirrorableTurnPayload 报告序列化后的载荷是否值得镜像：空与字面 "null"
// （safeJSONMarshal(nil) 的产物）跳过——PG 侧对应 json null，镜像成文件
// 只会是对账噪声。
func mirrorableTurnPayload(data []byte) bool {
	return len(data) > 0 && string(data) != "null"
}

// SetDetailsWriter wires the optional session_turn_details feature-layer
// writer (733/734 v3). Call before the first Write; nil (or an unavailable
// probe) disables the details write — the turn+bodies path is unaffected.
func (w *SessionWriterV2) SetDetailsWriter(dw *SessionTurnDetailsWriter) {
	w.detailsWriter = dw
}

// Stop signals shutdown and waits for all in-flight aggregate goroutines to
// finish (or be cancelled). It is idempotent and safe to call multiple times
// and from multiple goroutines.
//
// Per spec §6.3, the gateway shutdown sequence must call this AFTER
// telemetryClient.Stop so the telemetry onPersisted hook (which feeds Write)
// has stopped producing new work before we drain.
func (w *SessionWriterV2) Stop(ctx context.Context) error {
	w.ensureLifecycle()
	w.stopOnce.Do(func() {
		w.lifecycleMu.Lock()
		w.stopped = true
		if w.lifecycleCancel != nil {
			w.lifecycleCancel()
		}
		w.lifecycleMu.Unlock()
	})

	done := make(chan struct{})
	go func() {
		w.aggWg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ProcessedRequest represents a request that has been processed through the pipeline
//
// This mirrors the data structure from domains/streaming but is defined here
// to avoid circular dependencies. In production, we'd use a shared interface.
type ProcessedRequest struct {
	// Session context
	SessionID       string
	TenantID        string
	RequestID       string
	Timestamp       time.Time
	ProjectID       string
	Namespace       string
	ParentRequestID string
	TaskType        string
	ClientType      string // IDE/client type extracted from headers or system prompt

	// Request content
	RequestBody  []Message // Full request body from client
	ResponseBody []Message // Response from LLM
	Attachments  []AttachmentRef

	// Compression state
	LastOutboundBody    []Message // Previous turn's outbound (for delta extraction)
	OutboundBody        []Message // Actual outbound sent to LLM (after compression)
	CompressionApplied  bool
	CompressionStrategy string
	CompressionMeta     map[string]interface{}
	TokensSaved         int

	// Submit mode detection
	SubmitModeHeader string // X-Gw-Submit-Mode header value from client

	// Governance verdicts
	InjectionVerdict string
	OutputVerdict    string

	// Routing & model
	ClientModel  string
	ProviderID   string
	CredentialID string

	// Quality override for session_turns.quality ('verified' | 'inferred' |
	// 'partial' | 'rejected'). Empty means deriveTurnQuality classifies the
	// turn from Success/ErrorKind/ResponseBody. Reserved for callers that
	// hold richer signals than the writer (e.g. the telemetry quality
	// processor behind 017_quality_fix_mode.sql); the sessionv2mirror bridge
	// does not populate it yet.
	Quality string

	// Usage & cost
	PromptTokens     int
	CompletionTokens int
	CacheReadTokens  int
	CacheWriteTokens int
	CostUSD          float64

	// Performance
	StartedAt   time.Time
	CompletedAt time.Time
	StatusCode  int
	Success     bool
	ErrorKind   string

	// RequestStatus is the three-state lifecycle label the gateway already
	// computes in telemetry (success | failure | rate_limited | in_progress),
	// verbatim from RequestLogEntry.RequestStatus.
	//
	// It is NOT derivable from Success+ErrorKind: ResolveRequestStatus only ever
	// returns success/failure/in_progress, while `rate_limited` is set explicitly
	// by the rate-limit paths (request_log_pipeline.go, embeddings.go). The
	// sessionv2mirror bridge used to drop it — the entry carried it, the mirror
	// just never copied it — so public.session_turns could not tell a
	// rate-limited rejection from a genuine upstream failure.
	//
	// Why this matters for the request_logs retirement: 394,614 rate_limited
	// turns (≈446,819 including the stop-write window, audit §9.150) are already
	// mirrored into session_turns. Once request_logs is gone, this field is the
	// only surviving way to tell model-catalog scan traffic apart from real
	// failures. See audit §9.150.4 / §9.155.
	RequestStatus string

	// V3.1 dispatch 9-stage (10 timestamps) queue timestamps (migration 513).
	// Mirrors RequestLogEntry.T0ArrivedAt..T9ResponseEndAt from migration 491.
	// Plumbed through to public.session_turns so the session timeline view
	// can answer "how long did stage X take" without joining request_logs.
	// nil-safe — callers (e.g. pipeline_hook.go) that don't have these yet
	// simply leave them nil and the columns persist as NULL.
	T0ArrivedAt       *time.Time
	T1TotalEnqueuedAt *time.Time
	T2TotalDequeuedAt *time.Time
	T3ModelEnqueuedAt *time.Time
	T4ModelDequeuedAt *time.Time
	T5CredEnqueuedAt  *time.Time
	T6CredDequeuedAt  *time.Time
	T7ForwardStartAt  *time.Time
	T8ResponseStartAt *time.Time
	T9ResponseEndAt   *time.Time

	// §9.98：session_turns.search_text 列已建、TurnRecord 字段已建、INSERT 已绑定，
	// 但 ProcessedRequest（Write 的入参）**没有这个字段** ⇒ 镜像侧即使算出来也传不进来。
	// 这是该列 0% 填充的第三个原因（另两个：entry 无源、Write 里硬写 ""）。
	SearchText string

	// ── 存储优化方案 v2 S1a（migration 706/707）：request_logs 独有数据补采。
	// 数据源是 telemetry.RequestLogEntry（mirror bridge
	// entryToProcessedRequest 逐一拷贝）；四组列全部可空、零值即 NULL。
	// 缺源字段（TraceEvents/SearchText/RequestChecksum/RawModelName 在
	// RequestLogEntry 上不存在）保留列位、暂为 NULL（方案 §9 视图 NULL
	// 补位登记）。

	// 访问维度（sessions 存首值做会话归属，turns 存每轮值做计费精确到轮）。
	APIKeyID           string
	ApplicationID      string
	EndUserID          string
	CustomerID         int64
	OwnerUser          string
	ClientIP           string
	ClientForwardedFor string
	AgentName          string
	AgentType          string
	VirtualClientID    string

	// ClientProtocol is the client protocol vocabulary
	// (openai-chat / anthropic-messages / gemini-generate, …) that telemetry
	// infers from the request path. It travels with AgentName/AgentType as part
	// of the same client-identity group.
	//
	// 2026-10-05 审计 §9.208: it was missing here even though the column exists
	// in session_turns and the admin logs list projects it — so the field was
	// always empty there. ⚠ NOT to be confused with ExecParams.ClientProtocol /
	// IR's protocol vocabulary (the same Go field name in other packages means a
	// different thing); this one is the persisted column.
	ClientProtocol string

	// 730 会话角色归因三列（R50 F15 写入方）：agent_role 取
	// ResolveAgentRoleFromHeaders 的已解析值（""=未声明，SQL 侧落 'main'
	// 列默认）；parent_session_id 来自 X-Gw-Parent-Session-Id；parent_task_id
	// 复用 X-Gw-Task-Id 关联头。三列均会话首值优先（与 706 访问维度同款）。
	AgentRole       string
	ParentSessionID string
	ParentTaskID    string

	// 计费组（credits_charged 是计费事实源，D7 双读校验前提）。
	CreditsCharged int64
	CostDisplay    float64
	CostCurrency   string
	WorkType       string
	TokenBand      string
	UsageSource    string

	// 路由组。
	IsAutoRequest   bool
	AutoDecision    string
	AutoConfidence  float64
	TaskTypeChosen  string
	RoutingAttempts json.RawMessage
	RoutingSummary  string
	CanonicalID     int64
	CanonicalModel  string
	RawModelName    string

	// 诊断组。
	TraceEvents          json.RawMessage
	FailureStage         string
	FailureDetailCode    string
	UpstreamStatusCode   int
	UpstreamFinishReason string
	StreamFirstChunkMs   int
	StreamChunkCount     int
	StreamInterrupted    bool
	StreamDoneSent       bool
	ClientRequestID      string
	ClientEndpoint       string
	ClientTimeout        bool
	EgressProtocol       string

	// 检索/完整性组。
	RequestPreview    string
	ResponsePreview   string
	TransformSummary  string
	IdentityHash      string
	RequestChecksum   string
	ResponseChecksum  string
	SystemFingerprint string
	OriginStage       string
	OriginActor       string

	// Protocol-specific extensions (from ir.TransportContext)
	ProviderExtensions map[string]interface{} // Preserves vendor-specific fields

	// Multimodal content tracking
	MultimodalTypes []string // Types present: ["image", "audio", "video", "document"]

	// Details（733/734 会话存储解耦 v3）：turn 特征层。nil = 无特征可写
	//（陈旧 bridge / 非 mirror 写方）；键（SessionID/TurnNo）由 Write 在
	// AppendTurn 返回后补齐。
	Details *DetailsRecord

	// Processing stages (for turn logs)
	ProcessingStages []ProcessingStage
}

// ProcessingStage represents one stage in the request pipeline
type ProcessingStage struct {
	Stage       string // routing | compression | injection_check | llm_call | output_check | response
	Status      string // pending | running | success | failed | skipped
	StartedAt   time.Time
	CompletedAt time.Time
	EventData   map[string]interface{}
	ErrorMsg    string
}

// Write writes a processed request to all V2 tables
//
// This is a coordinated write that ensures consistency across:
//  1. session_turns (metadata)
//  2. session_bodies (incremental deltas)  — committed atomically with (1)
//  3. session_turn_logs (processing stages) — best-effort, own connection
//  4. sessions (snapshot, async)            — best-effort, lifecycle-managed
//
// Atomicity (spec §6.2): the turn INSERT and the bodies INSERT run inside a
// SINGLE transaction. If either fails the whole tx is rolled back, so a
// bodies failure can never leave an orphan turn row. Stage logs and the
// aggregate snapshot are best-effort (their failures are logged but do not
// fail the primary write), as the spec explicitly allows.
//
// Lifecycle (spec §6.3): the aggregate snapshot update runs in a goroutine
// tracked by aggWg and bound to lifecycleCtx; Stop() awaits it.
func (w *SessionWriterV2) Write(ctx context.Context, req *ProcessedRequest) error {
	// 2026-08-08 audit fix: bound the advisory-lock + body-read critical
	// section. r.Context() in telemetry / node-probe paths can be very long,
	// so a slow DB during GetLatestBodiesInTx would hold the per-session
	// advisory lock for unbounded time, blocking every other concurrent
	// write for that session. A 5s deadline matches the per-credential
	// probe timeout (see bg/node_probe.go) — same order of magnitude, same
	// operational expectation. The outer ctx is preserved for the rest of
	// the turn (detector, marshal, WriteBodiesInTx) which has no shared
	// lock that would cascade.
	lockCtx, cancelLock := context.WithTimeout(ctx, 5*time.Second)
	defer cancelLock()

	// Begin the transaction before reading the previous body. AppendTurnInTx
	// uses the same transaction-scoped advisory lock; acquiring it here makes
	// delta derivation observe the exact state that this turn will follow.
	tx, err := w.turnWriter.BeginTx(lockCtx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(lockCtx)
		}
	}()
	// RLS Phase 2 适配 (R41 P1-5): 共享 turn 事务须设 app.current_tenant=req.TenantID
	// 才能让 EnqueueSessionAggregateOutbox 的 INSERT 命中
	// session_aggregate_outbox policy 的 tenant 分支；不设则走 GUC fallback
	// 'default'（或 42501），非 default 租户请求静默丢失聚合快照。
	if _, err := tx.Exec(lockCtx, "SELECT set_config('app.current_tenant', $1, true)", req.TenantID); err != nil {
		return fmt.Errorf("set tenant GUC for aggregate outbox: %w", err)
	}
	if err := w.turnWriter.LockSessionInTx(lockCtx, tx, req.TenantID, req.SessionID); err != nil {
		return err
	}

	// 1. Detect submit mode using the latest locked body.
	detector := NewSubmitModeDetector()
	var previousAttachments []AttachmentRef
	previousBody, err := w.bodiesWriter.GetLatestBodiesInTx(lockCtx, tx, req.TenantID, req.SessionID)
	if err != nil {
		slog.WarnContext(ctx, "failed to get previous body for submit mode detection",
			"session_id", req.SessionID, "error", err)
	} else if previousBody != nil {
		previousAttachments = previousBody.RequestAttachments
		if len(req.LastOutboundBody) == 0 {
			req.LastOutboundBody = previousBody.OutboundBody
		}
	}

	submitMode := string(detector.Detect(DetectionContext{
		SubmitModeHeader:    req.SubmitModeHeader,
		ClientMessages:      req.RequestBody,
		LastOutboundBody:    req.LastOutboundBody,
		CompressionApplied:  req.CompressionApplied,
		CurrentAttachments:  req.Attachments,
		PreviousAttachments: previousAttachments,
	}))

	// 2. Extract request delta after the locked previous-body read.
	requestDelta := extractRequestDelta(req, submitMode)

	// 3. Build the turn record and bodies record (computed before the insert so
	// marshalling errors fail fast while the lock remains held).
	digestMeta := map[string]any{
		"prompt_tokens": req.PromptTokens, "completion_tokens": req.CompletionTokens,
		"cost_usd": req.CostUSD, "latency_ms": int(req.CompletedAt.Sub(req.StartedAt).Milliseconds()),
		"status_code": req.StatusCode, "success": req.Success, "error_kind": req.ErrorKind,
		"cache_read_tokens": req.CacheReadTokens, "cache_write_tokens": req.CacheWriteTokens,
	}
	digestGovernance := map[string]any{
		"injection_verdict": req.InjectionVerdict, "output_verdict": req.OutputVerdict,
		"compression_applied": req.CompressionApplied, "compression_tokens_saved": req.TokensSaved,
	}
	digestJSON, err := sessiondigest.Marshal(sessiondigest.Build(requestDelta, req.ResponseBody, digestMeta, digestGovernance, req.Timestamp))
	if err != nil {
		return fmt.Errorf("marshal turn digest: %w", err)
	}

	requestAttachments := extractRequestAttachments(req)
	responseAttachments := extractResponseAttachments(req)
	attachmentCount := len(requestAttachments) + len(responseAttachments)
	attachmentTotalBytes := calculateTotalBytes(requestAttachments, responseAttachments)

	// Auto-populate MultimodalTypes if not provided
	if len(req.MultimodalTypes) == 0 && attachmentCount > 0 {
		allAttachments := append(requestAttachments, responseAttachments...)
		req.MultimodalTypes = ExtractMultimodalTypes(allAttachments)
	}

	turnRec := TurnRecord{
		SessionID:       req.SessionID,
		TenantID:        req.TenantID,
		RequestID:       req.RequestID,
		Ts:              req.Timestamp,
		ProjectID:       req.ProjectID,
		Namespace:       req.Namespace,
		ParentRequestID: req.ParentRequestID,
		TaskType:        req.TaskType,
		SubmitMode:      submitMode,

		CompressionApplied:  req.CompressionApplied,
		CompressionStrategy: req.CompressionStrategy,
		CompressionMeta:     req.CompressionMeta,
		TokensSaved:         req.TokensSaved,

		InjectionVerdict: req.InjectionVerdict,
		OutputVerdict:    req.OutputVerdict,

		Model:        req.ClientModel,
		Provider:     req.ProviderID,
		CredentialID: req.CredentialID,

		PromptTokens:     req.PromptTokens,
		CompletionTokens: req.CompletionTokens,
		CacheReadTokens:  req.CacheReadTokens,
		CacheWriteTokens: req.CacheWriteTokens,
		CostUSD:          req.CostUSD,

		LatencyMs:  int(req.CompletedAt.Sub(req.StartedAt).Milliseconds()),
		StatusCode: req.StatusCode,
		Success:    req.Success,
		ErrorKind:  req.ErrorKind,
		// Audit §9.150.4: the telemetry entry already carries this
		// (including `rate_limited`); the mirror used to discard it. Pass it
		// through verbatim — no derivation, no re-inference.
		RequestStatus: req.RequestStatus,

		// V3.2 dual-write (migration 513): copy the 10 dispatch queue
		// timestamps from the ProcessedRequest (populated by the
		// sessionv2mirror hook from telemetry entry) so public.session_turns
		// stays in sync with public.request_logs_hot.
		T0ArrivedAt:       req.T0ArrivedAt,
		T1TotalEnqueuedAt: req.T1TotalEnqueuedAt,
		T2TotalDequeuedAt: req.T2TotalDequeuedAt,
		T3ModelEnqueuedAt: req.T3ModelEnqueuedAt,
		T4ModelDequeuedAt: req.T4ModelDequeuedAt,
		T5CredEnqueuedAt:  req.T5CredEnqueuedAt,
		T6CredDequeuedAt:  req.T6CredDequeuedAt,
		T7ForwardStartAt:  req.T7ForwardStartAt,
		T8ResponseStartAt: req.T8ResponseStartAt,
		T9ResponseEndAt:   req.T9ResponseEndAt,

		SourceKind: "live",
		// Quality is derived from what this turn actually carries, not
		// hardcoded: the CHECK constraint on session_turns.quality
		// ('verified'|'inferred'|'partial'|'rejected') is only useful to
		// operators if it reflects the row's content. Derivation rules
		// (deriveTurnQuality):
		//   rejected — terminal failure (Success=false / ErrorKind set)
		//   partial  — success but zero captured response messages
		//   verified — success with a non-empty response body
		// Callers that DID capture richer signals (the telemetry entry
		// carries QualityFlags/QualityScore from 017_quality_fix_mode.sql)
		// cannot forward them today because the sessionv2mirror bridge
		// does not copy them onto ProcessedRequest; when that bridge is
		// extended, set req.Quality explicitly and it wins over derivation.
		Quality: deriveTurnQuality(req),

		// Attachment metadata
		AttachmentCount:      attachmentCount,
		AttachmentTotalBytes: attachmentTotalBytes,
		MultimodalTypes:      req.MultimodalTypes,

		// Turn-level title / summary (migration 456). Derive deterministic
		// previews from the new messages in this turn so the admin turns-list
		// UI shows something useful without waiting for the async LLM
		// summarizer. summarizeMessages already produces a 200-char cap.
		Title:      summarizeMessages(requestDelta),
		Summary:    summarizeMessages(req.ResponseBody),
		DigestJSON: digestJSON,

		// ── 707 补采列搬运（存储优化方案 v2 S1a/S1b）。正文列按
		// storage.session_turns_bodies_enabled 灰度写入；其余列无条件
		// 写入（全部可空，mirror bridge 已从 RequestLogEntry 填充）。
		RequestDeltaJSON:  nil,
		ResponseDeltaJSON: nil,

		APIKeyID:             req.APIKeyID,
		ApplicationID:        req.ApplicationID,
		EndUserID:            req.EndUserID,
		CustomerID:           req.CustomerID,
		CreditsCharged:       req.CreditsCharged,
		CostDisplay:          req.CostDisplay,
		CostCurrency:         req.CostCurrency,
		WorkType:             req.WorkType,
		TokenBand:            req.TokenBand,
		UsageSource:          req.UsageSource,
		IsAutoRequest:        req.IsAutoRequest,
		AutoDecision:         req.AutoDecision,
		AutoConfidence:       req.AutoConfidence,
		TaskTypeChosen:       req.TaskTypeChosen,
		RoutingAttempts:      []byte(req.RoutingAttempts),
		RoutingSummary:       req.RoutingSummary,
		CanonicalID:          req.CanonicalID,
		CanonicalModel:       req.CanonicalModel,
		RawModelName:         req.RawModelName,
		TraceEvents:          []byte(req.TraceEvents),
		FailureStage:         req.FailureStage,
		FailureDetailCode:    req.FailureDetailCode,
		UpstreamStatusCode:   req.UpstreamStatusCode,
		UpstreamFinishReason: req.UpstreamFinishReason,
		StreamFirstChunkMs:   req.StreamFirstChunkMs,
		StreamChunkCount:     req.StreamChunkCount,
		StreamInterrupted:    req.StreamInterrupted,
		StreamDoneSent:       req.StreamDoneSent,
		ClientRequestID:      req.ClientRequestID,
		ClientEndpoint:       req.ClientEndpoint,
		ClientTimeout:        req.ClientTimeout,
		EgressProtocol:       req.EgressProtocol,
		// §9.98：原先硬写 ""，把 req.SearchText 直接丢弃——即使映射补上也不会落库。
		// 这是「列存在、字段存在、INSERT 已绑定」却仍然 0% 填充的第二个原因。
		SearchText:         req.SearchText,
		RequestPreview:     req.RequestPreview,
		ResponsePreview:    req.ResponsePreview,
		TransformSummary:   req.TransformSummary,
		IdentityHash:       req.IdentityHash,
		RequestChecksum:    req.RequestChecksum,
		ResponseChecksum:   req.ResponseChecksum,
		SystemFingerprint:  req.SystemFingerprint,
		OriginStage:        req.OriginStage,
		OriginActor:        req.OriginActor,
		ClientIP:           req.ClientIP,
		ClientForwardedFor: req.ClientForwardedFor,
		AgentName:          req.AgentName,
		AgentType:          req.AgentType,
		VirtualClientID:    req.VirtualClientID,
		ClientProtocol:     req.ClientProtocol,
	}

	// S1b 灰度开关①：每轮正文同步进 session_turns（宽表路线第一步）。
	if settings.GetPlatformBool("storage.session_turns_bodies_enabled", false) {
		if requestDeltaJSON, err := safeJSONMarshal(requestDelta); err == nil {
			turnRec.RequestDeltaJSON = requestDeltaJSON
		} else {
			slog.WarnContext(ctx, "marshal turn request_delta failed; persisting NULL",
				"session_id", req.SessionID, "request_id", req.RequestID, "error", err)
		}
		if responseDeltaJSON, err := safeJSONMarshal(req.ResponseBody); err == nil {
			turnRec.ResponseDeltaJSON = responseDeltaJSON
		} else {
			slog.WarnContext(ctx, "marshal turn response_delta failed; persisting NULL",
				"session_id", req.SessionID, "request_id", req.RequestID, "error", err)
		}
	}

	// 4. Atomic turn + bodies write (spec §6.2). The transaction and lock were
	// opened before the previous-body read, so AppendTurnInTx reuses them.
	turnNo, err := w.turnWriter.appendTurnInLockedTx(lockCtx, tx, turnRec)
	if err != nil {
		return fmt.Errorf("write turn: %w", err)
	}

	bodiesRec := BodiesRecord{
		SessionID: req.SessionID,
		TurnNo:    turnNo,
		TenantID:  req.TenantID,
		RequestID: req.RequestID,
		Ts:        req.Timestamp,

		RequestDelta:  requestDelta,
		ResponseDelta: req.ResponseBody,
		OutboundBody:  req.OutboundBody,

		RequestAttachments:  requestAttachments,
		ResponseAttachments: responseAttachments,
	}
	// S1b 灰度开关②：final_full 启用时逐轮停写 outbound_body（每轮的完整
	// outbound 改由下方 final_full 行承载），避免同一份内容双份落盘
	//（方案 §6：outbound_body 停写月省 ≈1.8GB）。旧行为完全保留可回切。
	if settings.GetPlatformBool("storage.session_final_full_enabled", false) {
		bodiesRec.OutboundBody = nil
	}
	if err := w.bodiesWriter.WriteBodiesInTx(lockCtx, tx, bodiesRec); err != nil {
		return fmt.Errorf("write bodies: %w", err)
	}
	// H3 镜像的投递点在 tx.Commit 成功之后（见 committed=true 处）：镜像必须
	// 与 PG 行共存亡——commit 失败回滚时 PG 无 bodies 行，镜像若已投递就成了
	// 对账无法解释的孤儿文件。

	// 733/734 特征层：与 turn+bodies 同事务（任一失败整体回滚）。键由
	// AppendTurn 返回的 turnNo 补齐；幂等 upsert 支持晚到回填重放。
	if req.Details != nil && w.detailsWriter != nil {
		detailsRec := *req.Details
		detailsRec.SessionID = req.SessionID
		detailsRec.TenantID = req.TenantID
		detailsRec.RequestID = req.RequestID
		detailsRec.TurnNo = turnNo
		if detailsRec.Ts.IsZero() {
			detailsRec.Ts = req.Timestamp
		}
		if err := w.detailsWriter.UpsertDetailsInTx(lockCtx, tx, detailsRec); err != nil {
			return fmt.Errorf("write details: %w", err)
		}
	}

	// S1b 灰度开关②（写点）：每会话"最后完整快照"行。方案 D2 原设计为
	// 会话关闭时拼装写入；本库无自动关闭链路（CloseSession 零调用方，
	// 2026-09-14 审计），故实现为每轮 upsert 覆盖——同为"最后完整快照"
	// 终态，migration 708 注释已登记该偏差。turn_no=0 +
	// request_id='final_full:<session>' + kind='final_full'，经 708 部分唯
	// 一索引守护每会话每分区至多一行；幂等 upsert 走既有
	// (tenant_id, request_id, partition_date) 唯一约束。
	finalFullWritten := false
	if settings.GetPlatformBool("storage.session_final_full_enabled", false) && len(req.OutboundBody) > 0 {
		if err := w.bodiesWriter.WriteFinalFullInTx(lockCtx, tx, FinalFullRecord{
			SessionID:    req.SessionID,
			TenantID:     req.TenantID,
			Ts:           req.Timestamp,
			OutboundBody: req.OutboundBody,
		}); err != nil {
			return fmt.Errorf("write final_full: %w", err)
		}
		finalFullWritten = true
	}

	// 706：会话首 turn 持久化时一次写入初始环境/上下文快照（insert-only，
	// 幂等由 (tenant_id, session_id, partition_date) 唯一约束兜底）。与
	// turn+bodies 同事务（spec §6.2）；失败即整体回滚重试，无孤儿快照。
	if w.memoraWriter != nil && turnNo == 1 {
		if err := w.memoraWriter.WriteMemoraSnapshotInTx(lockCtx, tx, buildMemoraSnapshot(req)); err != nil {
			return fmt.Errorf("write memora snapshot: %w", err)
		}
	}

	// audit-data-closure-C (2026-08-31): enqueue the aggregate snapshot update
	// in the SAME transaction as the turn + bodies write so its durability
	// matches the source-of-truth. The reaper (session_aggregate_outbox_reaper)
	// drains the outbox with FOR UPDATE SKIP LOCKED and exponential backoff,
	// closing the loop the original 3-attempt in-memory retry could not
	// guarantee. partition_date uses the request's calendar date (UTC) so the
	// unique key matches the session_turns partition the row will reconcile.
	outboxUpdate := SessionUpdate{
		SessionID:           req.SessionID,
		TenantID:            req.TenantID,
		RequestID:           req.RequestID,
		LastTurnNo:          turnNo,
		LastRequestSummary:  summarizeMessages(requestDelta),
		LastResponseSummary: summarizeMessages(req.ResponseBody),
		LastModel:           req.ClientModel,
		LastProvider:        req.ProviderID,
		ClientType:          req.ClientType,
		ProjectID:           req.ProjectID,
		APIKeyID:            req.APIKeyID,
		ApplicationID:       req.ApplicationID,
		EndUserID:           req.EndUserID,
		OwnerUser:           req.OwnerUser,
		ClientIP:            req.ClientIP,
		AgentName:           req.AgentName,
		AgentRole:           req.AgentRole,
		ParentSessionID:     req.ParentSessionID,
		ParentTaskID:        req.ParentTaskID,
		TurnIncrement:       1,
		TokensIncrement:     req.PromptTokens + req.CompletionTokens,
		CostIncrement:       req.CostUSD,
		UpdatedAt:           req.Timestamp,
	}
	if err := EnqueueSessionAggregateOutbox(lockCtx, tx, outboxUpdate, calendarDate(req.Timestamp)); err != nil {
		return fmt.Errorf("enqueue session aggregate outbox: %w", err)
	}

	if err := tx.Commit(lockCtx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	committed = true

	// H3 镜像（fire-and-forget，2026-09-24 方案）：turn+bodies 事务**提交成功**
	// 后投递热区镜像；镜像与主链路完全解耦（失败仅计数，不影响已提交数据）。
	// 放在 commit 之后而非 INSERT 成功后：commit 失败回滚时 PG 无 bodies 行，
	// 先投递的镜像会成为对账无法解释的孤儿文件（2026-09-30 批判式审计 F-A）。
	// final_full 灰度开启时 per-turn outbound 已被置 nil（停写门），此处自然
	// 跳过 "out" 方向，终态由下方 final_full 镜像行承载——与 PG 侧双路径落库
	// 口径一致。mirror outbox 重放经同一 Write 路径再次镜像：同路径覆盖写、
	// 内容一致，幂等无害。
	w.mirrorTurnBodies(bodiesRec)
	if finalFullWritten {
		// final_full 行的 request_id 口径与 PG 一致
		//（'final_full:<session>'，WriteFinalFullInTx 同款）。
		w.mirrorFinalFull(req)
	}

	// 5. Write turn logs (processing stages) — best-effort, NOT in the tx.
	//
	// spec §6.2 allows turn logs to fail without failing the write; keeping
	// them out of the atomic tx means a slow/stale stage log can't hold the
	// turn+bodies transaction open.
	//
	// 2026-09-27 (12h audit round 15, D-1): one WriteStages call, not a
	// per-row loop. A single statement makes the whole turn's rows visible
	// atomically, so the 5-minute aggregator tick can never observe (and
	// flush, delete, then re-emit under the same turn_N key) a half-written
	// turn — see WriteStages' doc comment.
	if w.turnLogsWriter != nil && len(req.ProcessingStages) > 0 {
		recs := make([]TurnLogRecord, 0, len(req.ProcessingStages))
		for _, stage := range req.ProcessingStages {
			recs = append(recs, TurnLogRecord{
				SessionID: req.SessionID,
				TurnNo:    turnNo,
				TenantID:  req.TenantID,
				RequestID: req.RequestID,

				Stage:       stage.Stage,
				StageStatus: stage.Status,
				EventData:   stage.EventData,
				ErrorMsg:    stage.ErrorMsg,

				StartedAt:   stage.StartedAt,
				CompletedAt: stage.CompletedAt,
			})
		}
		if err := w.turnLogsWriter.WriteStages(ctx, recs); err != nil {
			// Log but don't fail the write (turn logs are optional)
			slog.ErrorContext(ctx, "write turn logs failed",
				"session_id", req.SessionID,
				"turn_no", turnNo,
				"error", err)
		}
	}

	// 6. Update session snapshot — best-effort, lifecycle-managed goroutine
	// (spec §6.3). The goroutine is tracked by aggWg so Stop can await it,
	// and bound to lifecycleCtx so a blocked aggregate is cancelled on
	// shutdown instead of leaking.
	//
	// audit-data-closure-C (2026-08-31): a durable copy of this update is
	// already enqueued in session_aggregate_outbox (see step above) inside
	// the same transaction as the turn+bodies write. The reaper guarantees
	// eventual delivery; this in-process call is the fast path so the
	// snapshot is current within the same request lifecycle whenever the DB
	// cooperates. On failure the outbox row is the fallback, so this
	// goroutine no longer needs to be the only line of defense.
	if w.sessionAggregator != nil {
		w.ensureLifecycle()
		// Snapshot every caller-owned field before launching the asynchronous
		// aggregate update. The telemetry pipeline may reuse or mutate req and
		// its message slices as soon as Write returns; the goroutine must only
		// capture this immutable value.
		update := SessionUpdate{
			SessionID: req.SessionID,
			TenantID:  req.TenantID,
			// 2026-07-28 request-flow Step 3 (spec §6.2): pass RequestID so
			// the aggregator can claim this turn exactly once and never
			// double-accumulate token/turn/cost on a replay.
			RequestID:           req.RequestID,
			LastTurnNo:          turnNo,
			LastRequestSummary:  summarizeMessages(requestDelta),
			LastResponseSummary: summarizeMessages(req.ResponseBody),
			LastModel:           req.ClientModel,
			LastProvider:        req.ProviderID,
			ClientType:          req.ClientType,
			ProjectID:           req.ProjectID,
			APIKeyID:            req.APIKeyID,
			ApplicationID:       req.ApplicationID,
			EndUserID:           req.EndUserID,
			OwnerUser:           req.OwnerUser,
			ClientIP:            req.ClientIP,
			AgentName:           req.AgentName,
			AgentRole:           req.AgentRole,
			ParentSessionID:     req.ParentSessionID,
			ParentTaskID:        req.ParentTaskID,
			TurnIncrement:       1,
			TokensIncrement:     req.PromptTokens + req.CompletionTokens,
			CostIncrement:       req.CostUSD,
			UpdatedAt:           req.Timestamp,
		}
		w.lifecycleMu.Lock()
		if w.stopped {
			w.lifecycleMu.Unlock()
			return nil
		}
		w.aggWg.Add(1)
		w.lifecycleMu.Unlock()
		go func(update SessionUpdate) {
			defer w.aggWg.Done()
			// lifecycleCtx gates the goroutine on shutdown. A small timeout
			// bounds it so a slow DB can't stall Stop indefinitely even if
			// the ctx isn't yet cancelled.
			aggCtx, cancel := context.WithTimeout(w.lifecycleCtx, 30*time.Second)
			defer cancel()
			if err := w.updateSessionAggregate(aggCtx, update); err != nil {
				slog.Error("update session snapshot failed after retries",
					"session_id", update.SessionID,
					"request_id", update.RequestID,
					"error", err)
			}
		}(update)
	}

	return nil
}

func (w *SessionWriterV2) updateSessionAggregate(ctx context.Context, update SessionUpdate) error {
	var lastErr error
	for attempt := 1; attempt <= aggregateMaxAttempts; attempt++ {
		if err := w.sessionAggregator.UpdateSession(ctx, update); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt == aggregateMaxAttempts {
			break
		}
		timer := time.NewTimer(time.Duration(attempt) * aggregateRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

// getPreviousBody retrieves the most recent persisted turn body.
//
// The caller uses both its outbound snapshot (for multi-turn delta detection)
// and attachment references (for attachment-only submit-mode detection).
func (w *SessionWriterV2) getPreviousBody(ctx context.Context, sessionID, tenantID string) (*BodiesRecord, error) {
	body, err := w.bodiesWriter.GetLatestBodies(ctx, tenantID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("get latest bodies: %w", err)
	}
	return body, nil
}

// extractRequestDelta extracts incremental messages that are new in this turn
//
// Logic:
//   - If submit_mode is "delta" or "snapshot": entire RequestBody is the delta
//   - If submit_mode is "full": extract messages not present in LastOutboundBody
//   - If LastOutboundBody is empty: entire RequestBody is the delta (first turn)
func extractRequestDelta(req *ProcessedRequest, submitMode string) []Message {
	// Attachment-only turns carry no new message body; persist an empty delta
	// while retaining attachment metadata on the turn row.
	if submitMode == "attachment_only" {
		return nil
	}

	// If client explicitly sent delta or snapshot, trust it
	if submitMode == "delta" || submitMode == "snapshot" || submitMode == "inferred_compressed" {
		return req.RequestBody
	}

	// If no previous outbound, everything is new
	if len(req.LastOutboundBody) == 0 {
		return req.RequestBody
	}

	// Extract delta: messages in RequestBody but not in LastOutboundBody
	var delta []Message
	lastOutboundSet := buildMessageSet(req.LastOutboundBody)

	for _, msg := range req.RequestBody {
		msgKey := messageKey(msg)
		if !lastOutboundSet[msgKey] {
			delta = append(delta, msg)
		}
	}

	// Fallback: if delta extraction found nothing new, the previous turn's
	// outbound already covers the entire client history. Returning the full
	// body here is deliberate: every reader (turn_reader.LoadChain,
	// outbound_builder.BuildFromDeltas, sessionsummary message_source_v2)
	// ACCUMULATES request_delta across turns, so persisting an empty delta
	// would silently drop this turn's user input from the reconstructed
	// conversation. Full-body fallback re-sends messages the reader may
	// already have, which the dedup-tolerant assembly paths handle, but never
	// under-reports. (2026-09 fix: the previous code returned an empty slice
	// whenever the client re-sent an identical history, e.g. a bare "retry"
	// with no new user message or an idempotent re-submit.)
	if len(delta) == 0 {
		return req.RequestBody
	}

	return delta
}

// deriveTurnQuality classifies a turn for the session_turns.quality column
// (CHECK constraint: 'verified' | 'inferred' | 'partial' | 'rejected',
// migration 526). Values must reflect the row's content rather than a
// constant, otherwise the column is dead weight for operators:
//
//	rejected — the turn records a terminal failure (Success=false, or an
//	           ErrorKind survived onto the entry).
//	partial  — the turn succeeded but we captured no response messages, so
//	           the bodies row cannot reconstruct what the model said.
//	verified — the turn succeeded and carries a response body.
//
// An explicit req.Quality (when a caller plumbs one through the mirror
// bridge) always wins, so a future caller with richer signals (telemetry
// QualityFlags/QualityScore from 017_quality_fix_mode.sql) can override
// without touching this function.
func deriveTurnQuality(req *ProcessedRequest) string {
	if req == nil {
		return "verified"
	}
	if req.Quality != "" {
		return req.Quality
	}
	if !req.Success || req.ErrorKind != "" {
		return "rejected"
	}
	if len(req.ResponseBody) == 0 {
		return "partial"
	}
	return "verified"
}

// buildMessageSet creates a set of message keys for fast lookup
func buildMessageSet(messages []Message) map[string]bool {
	set := make(map[string]bool)
	for _, msg := range messages {
		set[messageKey(msg)] = true
	}
	return set
}

// messageKey generates a unique key for a message (for delta extraction /
// deduplication).
//
// It mirrors the V1 compression fingerprint domains/hooks/compression/diff.go
// msgHash: sha256(role + \x00 + first-512-bytes-content + \x00 + toolCallID),
// truncated to 16 bytes (32 hex). Keeping the two schemes aligned is a hard
// prerequisite for unifying the V1 (LCS) and V2 (delta) read paths — see
// docs/omni-ref3/03-MULTITURN-ASSEMBLY.md A2.
//
// The previous implementation used "role:first-100-chars", which (a) collided
// on any two tool results sharing a 100-char prefix despite different
// tool_call_ids — exactly the mis-pairing SanitizeToolMessages exists to
// prevent — and (b) used a plain ":" separator that the content itself could
// contain. Both are fixed here.
func messageKey(msg Message) string {
	contentKey := msg.Content
	if len(contentKey) > 512 {
		contentKey = contentKey[:512]
	}
	if structured := structuredMessagePayload(msg); structured != "" {
		contentKey += "\x00structured:" + structured
	}
	// NUL bytes separate the fields so no legal content can spoof a different
	// (role, content, toolCallID) triple, matching the V1 scheme.
	h := sha256.Sum256([]byte(msg.Role + "\x00" + contentKey + "\x00" + msg.ToolCallID))
	return fmt.Sprintf("%x", h[:16])
}

func structuredMessagePayload(msg Message) string {
	payload := struct {
		ContentRaw json.RawMessage          `json:"content,omitempty"`
		RawContent any                      `json:"raw,omitempty"`
		ToolCalls  []map[string]interface{} `json:"tool_calls,omitempty"`
		Name       string                   `json:"name,omitempty"`
	}{ContentRaw: msg.ContentRaw, RawContent: msg.RawContent, ToolCalls: msg.ToolCalls, Name: msg.Name}
	if len(payload.ContentRaw) == 0 && payload.RawContent == nil && len(payload.ToolCalls) == 0 && payload.Name == "" {
		return ""
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(b)
}

func extractRequestAttachments(req *ProcessedRequest) []AttachmentRef {
	// Return the attachments that were already extracted and stored
	// In the full pipeline, this comes from the attachment extraction layer
	return req.Attachments
}

// extractResponseAttachments extracts attachment references from response
func extractResponseAttachments(req *ProcessedRequest) []AttachmentRef {
	// For now, responses rarely contain attachments (future: audio/image outputs)
	// This would be populated by the response parser if the LLM returns media
	// TODO: Parse response_body for attachment references when models support output media
	return []AttachmentRef{}
}

// calculateTotalBytes sums up the total bytes from all attachments
func calculateTotalBytes(requestAttachments, responseAttachments []AttachmentRef) int64 {
	var total int64
	for _, att := range requestAttachments {
		total += att.SizeBytes
	}
	for _, att := range responseAttachments {
		total += att.SizeBytes
	}
	return total
}

// stripMarkdownNoise 去除常见 markdown 装饰，供人类阅读的摘要面使用
// （R69，审计 checklist"抽取会话信息便于人类查看（去除格式）"）：
// 代码围栏整段剔除、行首标题/列表符剥除、粗体与行内代码反引号去除、
// 空行折叠为单空格。刻意不做完整 markdown 解析——摘要面只需要"大体
// 可读"，语义与换行结构不承诺保留。
func stripMarkdownNoise(s string) string {
	var b strings.Builder
	inFence := false
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		t = strings.TrimLeft(t, "#-*•► ")
		t = strings.ReplaceAll(t, "**", "")
		t = strings.ReplaceAll(t, "`", "")
		if t == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(t)
	}
	return b.String()
}

// summarizeMessages creates a brief summary of messages for session snapshot
func summarizeMessages(messages []Message) string {
	if len(messages) == 0 {
		return ""
	}

	// Take first message content, truncate to 200 runes (not bytes) to avoid
	// cutting UTF-8 sequences in the middle. PostgreSQL text columns enforce
	// valid UTF-8, so byte-slicing [:200] would panic on insert if the cut
	// lands inside a multi-byte character. R69: markdown 噪音在截断前剥除，
	// 让 title/summary/last_request_summary 面向人类阅读而非原始格式。
	// R71：剥噪后为空且原文非空时回退原文——响应整体是一个（或未闭合的）
	// 代码围栏时 stripMarkdownNoise 返回空串，摘要信息量反而从"有"变"无"。
	firstMsg := messages[0]
	content := stripMarkdownNoise(firstMsg.Content)
	if content == "" && firstMsg.Content != "" {
		content = strings.TrimSpace(firstMsg.Content)
	}
	runes := []rune(content)
	if len(runes) > 200 {
		content = string(runes[:200]) + "..."
	}

	if len(messages) > 1 {
		return fmt.Sprintf("%s (%d messages)", content, len(messages))
	}

	return content
}
