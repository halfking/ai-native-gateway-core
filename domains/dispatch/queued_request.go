package dispatch

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Concurrency modes (mirror credentials.concurrency_mode CHECK, migration 479).
const (
	ModeConcurrency = "concurrency" // in-flight hard cap (DeepSeek/智谱/Kimi)
	ModeRPM         = "rpm"         // requests-per-minute token bucket (OpenAI/Gemini/Groq)
	ModeTPM         = "tpm"         // tokens-per-minute token bucket (OpenAI/Anthropic/Azure)
	ModeDisabled    = "disabled"    // no limiting
)

// CredentialRef is dispatch's minimal, decoupled view of a credential. The
// executor adapts provider.Candidate → CredentialRef. dispatch never imports
// the provider package (avoids the executors↔dispatch cycle).
type CredentialRef struct {
	CredentialID     int
	ProviderID       int
	ConcurrencyMode  string
	ConcurrencyLimit int // in-flight cap (concurrency mode); 0 = unlimited
	RPMLimit         int // req/min (rpm mode); 0 = unlimited
	TPMLimit         int // tokens/min (tpm mode); 0 = unlimited
	MaxQueueDepth    int // 0 = use global Config.MaxQueueDepth
	MaxQueueWaitMS   int // 0 = use global Config.MaxQueueWaitMS
	// PriorityCluster is a closed, caller-derived rank. Lower clusters are
	// preferred before capacity-aware soft ranking is applied.
	PriorityCluster int
	Vendor          string // 原厂/供应商, for stats labels
}

// ForwardOutcome is what ForwardFunc returns.
type ForwardOutcome struct {
	// Result is the opaque success payload (e.g. *executors.ExecuteResult).
	// dispatch never inspects it; it is forwarded to the Submit caller.
	Result any
	// BytesSent is true if any byte reached the client (first-byte
	// boundary). When true, a subsequent error MUST NOT trigger a
	// cross-credential switch — the request is completed with the error
	// (ADR-Disp-003).
	BytesSent bool
	// FatalCredential is true when Err is a credential-fatal upstream
	// failure (quota exhausted / auth revoked). The mover skips the
	// same-credential retry ladder for such errors: retrying a dead
	// credential only re-yields the same upstream rejection, wastes a
	// concurrency slot, and delays the switch to a healthy sibling
	// (incident afd75c81…). The executor pre-computes this flag from
	// errorsx.IsCredentialFatal so the dispatch package stays decoupled
	// from errorsx (see the import-cycle guard on CredentialRef).
	FatalCredential bool
	// ErrorKind and HTTPStatus are optional bounded diagnostics supplied by the
	// executor adapter. Dispatch falls back to a coarse local classification.
	ErrorKind string
	// RetryAfter is the upstream Retry-After hint (R2.4: upstream hints take
	// priority over the dispatch backoff ladder, clamped to [2s, 120s]).
	// 0 = no hint. Populated by the executor adapter when the upstream
	// supplies one; dispatch never parses headers itself.
	RetryAfter time.Duration
	// HTTPStatus is the upstream status for terminal events when known.
	HTTPStatus int
	Err        error
}

// QueuedRequest is the unit of work flowing through the pipeline.
//
// INVARIANT (single-owner): at any instant exactly one executor goroutine
// owns a *QueuedRequest (hand-off, never shared). The mutable failover
// fields below are therefore safe to mutate WITHOUT a mutex — the single
// current owner is the only writer.
type QueuedRequest struct {
	ID       string // request_id
	TenantID string
	// GatewayInstanceID completes the stable lifecycle observation identity. Callers
	// wiring an ObservationSink must populate it before Submit.
	GatewayInstanceID string
	// SessionID is the Sessions V2 textual identifier (public.sessions.session_id).
	// It is distinct from the numeric public.sessions.id surrogate key used by
	// admin session_pk resolution. Populated by the executor from ExecParams.SessionID
	// so admin /sessions/{id}/timeline can group in-flight + completed requests by
	// session. Empty for one-shot traffic (probe / health checks).
	SessionID      string
	RequestedModel string // client model (may be "auto")
	ResolvedModel  string // set by the dispatcher after auto-resolution
	modelMu        sync.RWMutex

	// Ctx is the request context. Used by governors/forwarders for pacing
	// waits and to observe client disconnect.
	Ctx context.Context

	// Payload is opaque to dispatch. RouteFunc/ForwardFunc interpret it
	// (e.g. *executors.ExecParams).
	Payload any

	// Request-level failover policy. The Pipeline-wide switches are kill
	// switches; these fields are the per-request authority derived by the caller
	// from the original client model and routing constraints.
	AllowModelChange    bool
	AllowProviderChange bool
	// RetryPerCredential overrides Config.RetryPerCredential when >= 0. This
	// keeps policy.RetryPerCredential=0 meaningful for requests that must
	// switch immediately after a pre-firstbyte failure.
	RetryPerCredential int
	ModelAlternatives  []string
	InitialProviderID  int

	// EstimatedTokens is the pre-send token estimate used by the tpm
	// governor. 0 = unknown (the tpm governor uses a conservative fixed cost).
	EstimatedTokens int

	// SelectedCred is the credential the dispatcher/mover chose for the
	// current attempt. Set before enqueueing into a Tier-2 credential queue.
	SelectedCred   CredentialRef
	selectedCredMu sync.RWMutex

	// OnNodeSwitchSummary reports a provider-neutral failover summary to the
	// request's transport. It is called only after a sibling credential has
	// been selected and successfully re-enqueued.
	OnNodeSwitchSummary func(message string)

	// OnDispatchNotice delivers structured requeue/wait/switch notices to the
	// request's transport (v6 G-Ⅲ). Unlike OnNodeSwitchSummary it fires on
	// EVERY requeue site (same-cred retry, credential switch, model switch,
	// capacity wait, scheduled park/due). The executor bridges it to the
	// handler's `: thinking:` SSE comment writer so the client sees progress
	// without the frames entering the conversation. Callbacks must be
	// non-blocking; dispatch wraps them with recover.
	OnDispatchNotice func(notice DispatchNotice)

	// DueAt schedules future execution (定时请求, v6 G-Ⅱ). Zero = immediate.
	// A future DueAt parks the request in the pipeline's due heap when the
	// total drainer pops it; the promoter re-admits it into Tier-0 at the due
	// time. Capped by maxScheduleAhead at Submit.
	DueAt time.Time

	// LastFailover is the 回队打标 (v6 G-Ⅴ): the previous round's error kind,
	// model/credential executed, and the decided next action. Written under
	// the single-owner invariant at every requeue site. Since V6-W1.6 (R9)
	// it is a PROJECTION of the AttemptJournal tail, refreshed by
	// recordDecision — the journal is the authority.
	LastFailover FailoverMarker

	// AttemptJournal is the per-request execution trace (V6-W1.6 R9):
	// one entry per requeue/terminal decision, bounded at journalCapacity.
	// Written only via recordDecision under the single-owner invariant.
	AttemptJournal []JournalEntry
	// Counts is the cumulative per-action view of the journal (R12: the
	// attempt limit authority remains AttemptCount).
	Counts ActionCounts
	// RequestClass mirrors ir.RequestClass as a plain string ("immediate" |
	// "scheduled") so dispatch stays decoupled from internal/ir (E14).
	// Empty is derived from DueAt via requestClass().
	RequestClass string
	// journalSeq is the last allocated journal sequence number.
	journalSeq int

	// noticeSeq orders DispatchNotice delivery per request (single owner).
	noticeSeq int

	// ResultCh signals completion to the Submit caller. Capacity 1; sent on
	// exactly once (guarded by the completed atomic).
	ResultCh chan ForwardOutcome

	// Failover tracking — single-owner mutation.
	TriedCredentials map[int]struct{}
	TriedModels      map[string]struct{}
	// CredRetryCount is the same-credential error retry count (upstream 5xx/timeout).
	// Used for exponential backoff on actual failures.
	CredRetryCount int
	// CapacityRetryCount is the same-model capacity wait count.
	// Used for capacity exhaustion fixed 5s retry, does not trigger exponential backoff.
	CapacityRetryCount int
	// AttemptCount is the total number of forward attempts across all
	// credentials. Bounded by maxAttempts to prevent a request from looping
	// through an unbounded candidate set under pathological conditions.
	AttemptCount int

	// Dispatch observation sequencing and attempt state are separately synchronized:
	// streaming can report first semantic byte while the forward goroutine is
	// returning, and sinks must still observe strictly increasing seq order.
	journeyMu sync.Mutex
	// JourneySeq is the last allocated lifecycle observation sequence. Producers
	// that emit request_received/route_resolved before dispatch may seed it before
	// Submit; dispatch then continues monotonically from that value.
	JourneySeq atomic.Int64
	// JourneySharedSeq and JourneyTerminal are populated by request handlers.
	// They let handler and dispatch events share one sequence/terminal owner.
	JourneySharedSeq *atomic.Int64
	JourneyTerminal  *atomic.Bool
	observationEmit  observationEmitter
	attemptMu        sync.Mutex
	attempts         []*dispatchAttempt
	currentAttempt   *dispatchAttempt

	// ===== 10-Stage Queue Timestamps (V3.1 Enhancement) =====
	// These timestamps enable precise performance analysis and bottleneck diagnosis.
	// See: docs/会话优化v3/01-需求分析与架构设计.md §2.2.6
	//
	// stages replaces the former T0..T9 pointer fields: one inline array plus
	// a set bitmask instead of nine per-stage heap allocations. Writes follow
	// the single-owner invariant; the forward-start and response-start slots
	// are additionally written under attemptMu (journey.go). Cross-package
	// consumers must take detached copies via StageTimestamps — never alias
	// the array, because failover re-enqueue rewrites earlier slots.
	stages   [reqStageCount]time.Time
	stageSet uint16
	stageMu  sync.RWMutex

	// Legacy fields (kept for backward compatibility, map to new timestamps)
	EnqueuedAt     time.Time // Deprecated: use ReqStageTotalEnqueued
	CredEnqueuedAt time.Time // Deprecated: use ReqStageCredEnqueued
	DequeuedAt     time.Time // Deprecated: use ReqStageCredDequeued

	// completed guarantees ResultCh is sent on exactly once even if a bug
	// would otherwise double-complete.
	completed atomic.Bool
	// totalQueueDone makes total execution admission release exactly once when
	// cancellation races with the FIFO drainer.
	totalQueueDone atomic.Bool

	// V6-W1.7 cluster queue-backend admission tokens. Stored by the owner
	// goroutine BEFORE the queue handoff; released via take-once swap at the
	// leave points (drainers / cancel / complete sweep), so racing releasers
	// return exactly one token each.
	clusterTotal atomic.Pointer[Admission]
	clusterModel atomic.Pointer[Admission]
	clusterCred  atomic.Pointer[Admission]

	// abandoned is set by Submit when the caller's ctx expired before the
	// pipeline finished. complete() then drops the result (no reader left).
	abandoned atomic.Bool

	// vendor snapshot for stats labels (from SelectedCred at enqueue time).
	vendor   string
	vendorMu sync.RWMutex
}

// NewQueuedRequest constructs a QueuedRequest ready for Pipeline.Submit.
func NewQueuedRequest(id, tenantID, model string, ctx context.Context, payload any) *QueuedRequest {
	now := time.Now()
	qr := &QueuedRequest{
		ID:                 id,
		TenantID:           tenantID,
		RequestedModel:     model,
		Ctx:                ctx,
		Payload:            payload,
		ResultCh:           make(chan ForwardOutcome, 1),
		TriedCredentials:   make(map[int]struct{}),
		TriedModels:        make(map[string]struct{}),
		RetryPerCredential: -1,

		EnqueuedAt: now, // Legacy: backward compatibility
	}
	// V3.1: initialize Stage 1 (arrival)
	qr.setStage(ReqStageArrived, now)
	return qr
}

func (qr *QueuedRequest) resolvedModel() string {
	qr.modelMu.RLock()
	model := qr.ResolvedModel
	qr.modelMu.RUnlock()
	return model
}

// ResolvedModelSnapshot is the cross-package read path for ResolvedModel.
// Model changes during failover (tryModelChange) happen under modelMu, so
// readers outside domains/dispatch (e.g. the executor RouteFunc/ForwardFunc
// adapters) must not read the bare field.
func (qr *QueuedRequest) ResolvedModelSnapshot() string {
	return qr.resolvedModel()
}

func (qr *QueuedRequest) setResolvedModel(model string) {
	qr.modelMu.Lock()
	qr.ResolvedModel = model
	qr.modelMu.Unlock()
}

func (qr *QueuedRequest) selectedCredential() CredentialRef {
	qr.selectedCredMu.RLock()
	ref := qr.SelectedCred
	qr.selectedCredMu.RUnlock()
	return ref
}

func (qr *QueuedRequest) setSelectedCredential(ref CredentialRef) {
	qr.selectedCredMu.Lock()
	qr.SelectedCred = ref
	qr.selectedCredMu.Unlock()
}

func (qr *QueuedRequest) selectedVendor() string {
	qr.vendorMu.RLock()
	vendor := qr.vendor
	qr.vendorMu.RUnlock()
	return vendor
}

func (qr *QueuedRequest) setVendor(vendor string) {
	qr.vendorMu.Lock()
	qr.vendor = vendor
	qr.vendorMu.Unlock()
}

func (qr *QueuedRequest) markTriedCredential(id int) {
	qr.TriedCredentials[id] = struct{}{}
}

// markTriedModel records a model as exhausted for this request.
func (qr *QueuedRequest) markTriedModel(model string) {
	if model == "" {
		return
	}
	qr.TriedModels[model] = struct{}{}
}

// HasTriedCredential reports whether the credential was already exhausted.
// Exported so the executor-supplied RouteFunc can filter tried credentials.
func (qr *QueuedRequest) HasTriedCredential(id int) bool {
	_, ok := qr.TriedCredentials[id]
	return ok
}

// hasTriedCredential is the internal alias.
func (qr *QueuedRequest) hasTriedCredential(id int) bool { return qr.HasTriedCredential(id) }

// ===== V3.1: 10-Stage Timestamp Slots =====

// ReqStage identifies one of the ten lifecycle timestamp slots.
type ReqStage uint8

const (
	ReqStageArrived       ReqStage = iota // Stage 1: Request arrival
	ReqStageTotalEnqueued                 // Stage 2: Total queue enqueue
	ReqStageTotalDequeued                 // Stage 3: Total queue dequeue (model routing start)
	ReqStageModelEnqueued                 // Stage 4: Model queue enqueue
	ReqStageModelDequeued                 // Stage 5: Model queue dequeue (credential selection)
	ReqStageCredEnqueued                  // Stage 6: Credential queue enqueue
	ReqStageCredDequeued                  // Stage 7: Credential queue dequeue (governor acquired)
	ReqStageForwardStart                  // Stage 8: Request forwarding to upstream
	ReqStageResponseStart                 // Stage 9: First byte received from upstream
	ReqStageResponseEnd                   // Stage 10: Response stream completed
	reqStageCount
)

// stageSetBit asserts at compile time that the stage bitmask (uint16) can
// hold every stage flag; adding an 11th..16th stage stays safe, a 17th
// fails the build instead of silently wrapping.
const stageSetBit = 1 << (reqStageCount - 1)

var _ [0]struct{} = [stageSetBit >> 16]struct{}{}

// setStage records t for the stage. Callers must hold the single-owner
// invariant (ReqStageForwardStart / ReqStageResponseStart are written under
// attemptMu by journey.go).
func (qr *QueuedRequest) setStage(s ReqStage, t time.Time) {
	qr.stageMu.Lock()
	qr.stages[s] = t
	qr.stageSet |= 1 << s
	qr.stageMu.Unlock()
}

// ReqStageTime returns the recorded timestamp for the stage; the zero time
// when the stage has not been recorded yet.
func (qr *QueuedRequest) ReqStageTime(s ReqStage) time.Time {
	qr.stageMu.RLock()
	t := qr.stages[s]
	qr.stageMu.RUnlock()
	return t
}

// SetReqStageTime installs an explicit timestamp for the stage. Production
// code should prefer the stage-specific SetTn_* helpers (time.Now());
// SetReqStageTime serves journey paths that already hold the event time and
// tests seeding deterministic values.
func (qr *QueuedRequest) SetReqStageTime(s ReqStage, t time.Time) {
	qr.setStage(s, t)
}

// StageTimestamps returns detached copies of the ten lifecycle timestamps:
// exactly one heap allocation per call, nil for stages never recorded.
// Returned pointers never alias request state, so later rewrites (failover
// re-enqueue) cannot mutate previously extracted values.
func (qr *QueuedRequest) StageTimestamps() (t0, t1, t2, t3, t4, t5, t6, t7, t8, t9 *time.Time) {
	qr.stageMu.RLock()
	stageSet := qr.stageSet
	box := qr.stages
	qr.stageMu.RUnlock()
	if stageSet == 0 {
		return
	}
	if stageSet&(1<<ReqStageArrived) != 0 {
		t0 = &box[ReqStageArrived]
	}
	if stageSet&(1<<ReqStageTotalEnqueued) != 0 {
		t1 = &box[ReqStageTotalEnqueued]
	}
	if stageSet&(1<<ReqStageTotalDequeued) != 0 {
		t2 = &box[ReqStageTotalDequeued]
	}
	if stageSet&(1<<ReqStageModelEnqueued) != 0 {
		t3 = &box[ReqStageModelEnqueued]
	}
	if stageSet&(1<<ReqStageModelDequeued) != 0 {
		t4 = &box[ReqStageModelDequeued]
	}
	if stageSet&(1<<ReqStageCredEnqueued) != 0 {
		t5 = &box[ReqStageCredEnqueued]
	}
	if stageSet&(1<<ReqStageCredDequeued) != 0 {
		t6 = &box[ReqStageCredDequeued]
	}
	if stageSet&(1<<ReqStageForwardStart) != 0 {
		t7 = &box[ReqStageForwardStart]
	}
	if stageSet&(1<<ReqStageResponseStart) != 0 {
		t8 = &box[ReqStageResponseStart]
	}
	if stageSet&(1<<ReqStageResponseEnd) != 0 {
		t9 = &box[ReqStageResponseEnd]
	}
	return
}

// ===== V3.1: 9-Stage Timestamp Setters =====
// These methods are called at each pipeline stage to record timing for performance analysis.

// SetT1_TotalEnqueued records Stage 2: Total queue enqueue
func (qr *QueuedRequest) SetT1_TotalEnqueued() {
	qr.setStage(ReqStageTotalEnqueued, time.Now())
}

// SetT2_TotalDequeued records Stage 3: Total queue dequeue (model routing start)
func (qr *QueuedRequest) SetT2_TotalDequeued() {
	qr.setStage(ReqStageTotalDequeued, time.Now())
}

// SetT3_ModelEnqueued records Stage 4: Model queue enqueue
func (qr *QueuedRequest) SetT3_ModelEnqueued() {
	qr.setStage(ReqStageModelEnqueued, time.Now())
}

// SetT4_ModelDequeued records Stage 5: Model queue dequeue (credential selection)
func (qr *QueuedRequest) SetT4_ModelDequeued() {
	qr.setStage(ReqStageModelDequeued, time.Now())
}

// SetT5_CredEnqueued records Stage 6: Credential queue enqueue
func (qr *QueuedRequest) SetT5_CredEnqueued() {
	now := time.Now()
	qr.setStage(ReqStageCredEnqueued, now)
	qr.CredEnqueuedAt = now // Legacy: backward compatibility
}

// SetT6_CredDequeued records Stage 7: Credential queue dequeue (governor acquired)
func (qr *QueuedRequest) SetT6_CredDequeued() {
	now := time.Now()
	qr.setStage(ReqStageCredDequeued, now)
	qr.DequeuedAt = now // Legacy: backward compatibility
}

// SetT7_ForwardStart records Stage 8: Request forwarding to upstream
func (qr *QueuedRequest) SetT7_ForwardStart() {
	qr.setStage(ReqStageForwardStart, time.Now())
}

// SetT8_ResponseStart is the compatibility alias for MarkFirstSemanticByte.
// New streaming integrations should call MarkFirstSemanticByte directly.
func (qr *QueuedRequest) SetT8_ResponseStart() {
	qr.MarkFirstSemanticByte()
}

// SetT9_ResponseEnd records Stage 10: Response stream completed
func (qr *QueuedRequest) SetT9_ResponseEnd() {
	qr.setStage(ReqStageResponseEnd, time.Now())
}

// GetQueueWaitDuration returns total time spent waiting in queues (T0→T6)
func (qr *QueuedRequest) GetQueueWaitDuration() time.Duration {
	qr.stageMu.RLock()
	t0, t6 := qr.stages[ReqStageArrived], qr.stages[ReqStageCredDequeued]
	qr.stageMu.RUnlock()
	if t6.IsZero() {
		return 0
	}
	return t6.Sub(t0)
}

// GetUpstreamLatency returns time from forward start to first byte (T7→T8)
func (qr *QueuedRequest) GetUpstreamLatency() time.Duration {
	qr.stageMu.RLock()
	t7, t8 := qr.stages[ReqStageForwardStart], qr.stages[ReqStageResponseStart]
	qr.stageMu.RUnlock()
	if t7.IsZero() || t8.IsZero() {
		return 0
	}
	return t8.Sub(t7)
}

// GetStreamingDuration returns response body transfer time (T8→T9)
func (qr *QueuedRequest) GetStreamingDuration() time.Duration {
	qr.stageMu.RLock()
	t8, t9 := qr.stages[ReqStageResponseStart], qr.stages[ReqStageResponseEnd]
	qr.stageMu.RUnlock()
	if t8.IsZero() || t9.IsZero() {
		return 0
	}
	return t9.Sub(t8)
}

// GetTotalDuration returns end-to-end time (T0→T9)
func (qr *QueuedRequest) GetTotalDuration() time.Duration {
	qr.stageMu.RLock()
	t0, t9 := qr.stages[ReqStageArrived], qr.stages[ReqStageResponseEnd]
	qr.stageMu.RUnlock()
	if t9.IsZero() {
		return 0
	}
	return t9.Sub(t0)
}

// stageSeconds returns end.Sub(start) in seconds when both timestamps are
// recorded and end >= start. Used by Prometheus stage histograms.
func stageSeconds(start, end time.Time) (float64, bool) {
	if start.IsZero() || end.IsZero() {
		return 0, false
	}
	d := end.Sub(start)
	if d < 0 {
		return 0, false
	}
	return d.Seconds(), true
}
