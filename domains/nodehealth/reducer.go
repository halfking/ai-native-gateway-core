// Package nodehealth reduces request and probe outcomes into one normalized
// node-health decision stream. It does not write legacy health stores directly;
// callers connect those stores through an Adapter.
package nodehealth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
)

// Phase distinguishes independently observable outcomes within one attempt.
type Phase string

const (
	PhaseRequest      Phase = "request"
	PhaseDirectProbe  Phase = "direct_probe"
	PhaseGatewayProbe Phase = "gateway_probe"
)

// ErrorKind is the normalized health-relevant failure classification.
type ErrorKind string

const (
	ErrorKindNetwork      ErrorKind = "network"
	ErrorKindTimeout      ErrorKind = "timeout"
	ErrorKindUpstream     ErrorKind = "upstream"
	ErrorKindRateLimit    ErrorKind = "rate_limit"
	ErrorKindAuth         ErrorKind = "auth"
	ErrorKindQuota        ErrorKind = "quota"
	ErrorKindModelBinding ErrorKind = "model_binding"
	// ErrorKindModelUnavailable is a binding-scoped failure that is NOT
	// "the model does not exist" (which is terminal and skips cross-credential
	// failover). It means "this upstream deterministically does not serve this
	// (credential, model) pair" — e.g. a bare 404 whose body carries no
	// model-level verdict. Like ErrorKindModelBinding it invalidates only the
	// pair and never feeds the credential-wide circuit; unlike it, the
	// request may still fail over to a sibling credential.
	ErrorKindModelUnavailable ErrorKind = "model_unavailable"
	// ErrorKindNoAvailableChannel is "no backend can serve this" — but the
	// 归责 level depends on the upstream status, because the two variants
	// point at different layers:
	//
	//	4xx: the credential does not serve this (model, path) — 绑定级
	//	5xx: the relay group has no channel at all — 凭据级
	//
	// See isBindingScoped for the full rationale and the production
	// evidence (211, 2026-10-13, credential 63 / claude-haiku-5-5).
	ErrorKindNoAvailableChannel ErrorKind = "no_available_channel"
	// ErrorKindEmptyResponse is a binding-scoped quality signal. It is recorded
	// in URSM for routing penalties and cannot escalate a credential-wide circuit
	// or availability state. A SUSTAINED streak (>= the configured threshold,
	// DefaultEmptyResponseStreakThreshold) additionally demotes the exact
	// (credential, model) binding — never siblings, never the credential —
	// closing node-state chain gap #1 (2026-11-11): 48 consecutive NIM empty
	// streams in 6h previously left zero persistent trace while every turn
	// burned a full failover walk.
	ErrorKindEmptyResponse ErrorKind = "empty_response"
	// ErrorKindRequest is a terminal attempt failure caused by request shape,
	// context, or content policy. It is observable but not node-health evidence.
	ErrorKindRequest ErrorKind = "request"
)

// NodeKey identifies one routable credential/model node.
type NodeKey struct {
	TenantID     string
	ProviderID   int64
	CredentialID int64
	Model        string
}

// Observation is one terminal request or probe phase outcome.
type Observation struct {
	Node        NodeKey
	AttemptID   string
	Phase       Phase
	Outcome     requestjourney.Outcome
	ErrorKind   ErrorKind
	HTTPStatus  int
	RequestID   string
	BillingMode string
	LatencyMs   int
	ErrorDetail string
}

// EffectKind identifies a side effect for an integration adapter. The reducer
// deliberately does not import circuit, credentialstate, URSM, or queue code.
type EffectKind string

const (
	EffectPersistNodeStatus        EffectKind = "persist_node_status"
	EffectRecordCircuitFailure     EffectKind = "record_circuit_failure"
	EffectRecoverCircuit           EffectKind = "recover_circuit"
	EffectSetBindingUnavailable    EffectKind = "set_binding_unavailable"
	EffectRestoreBinding           EffectKind = "restore_binding"
	EffectSetCredentialUnavailable EffectKind = "set_credential_unavailable"
	EffectUpdateURSM               EffectKind = "update_ursm"
	EffectInvalidateCandidateCache EffectKind = "invalidate_candidate_cache"
	EffectScheduleProbe            EffectKind = "schedule_probe"
	EffectCancelProbeBackoff       EffectKind = "cancel_probe_backoff"
	EffectQuarantine               EffectKind = "quarantine"
)

// Effect is an explicit integration instruction produced by a Decision.
type Effect struct {
	Kind EffectKind
}

// Decision is the complete observable result of reducing one outcome.
type Decision struct {
	Node                NodeKey
	AttemptID           string
	Phase               Phase
	Outcome             requestjourney.Outcome
	Accepted            bool
	Duplicate           bool
	PreviousStatus      requestjourney.NodeHealthStatus
	Status              requestjourney.NodeHealthStatus
	ConsecutiveFailures int
	// EmptyResponseStreak is the node's current consecutive ErrorKindEmptyResponse
	// count (0 on success or any non-empty failure kind). At the configured
	// threshold the decision carries the binding-scoped demotion effects.
	EmptyResponseStreak int
	ErrorKind           ErrorKind
	HTTPStatus          int
	RequestID           string
	BillingMode         string
	LatencyMs           int
	ErrorDetail         string
	Effects             []Effect
}

// Adapter applies a decision to the current circuit, credentialstate, URSM,
// binding, cache, and probe-queue implementations. Duplicate decisions are not
// passed to the adapter by ReduceAndApply.
type Adapter interface {
	ApplyNodeHealthDecision(context.Context, Decision) error
}

// DefaultSeenCapacity bounds request/phase deduplication history. Node state is
// retained independently, so evicting an old event never resets health.
const DefaultSeenCapacity = 100000

// DefaultSeenTTL bounds how long one terminal outcome keeps suppressing
// replays of the same attempt/phase. Replays arrive within seconds (outbox
// retry, journey re-reading); the two-phase probe window is minutes, so
// fifteen minutes suppresses every legitimate replay while still reclaiming
// idle history without waiting for capacity pressure.
const DefaultSeenTTL = 15 * time.Minute

// DefaultEmptyResponseStreakThreshold is the consecutive empty-response count
// that escalates to a binding-scoped cooldown (node-state chain gap #1,
// 2026-11-11). Below it a burst stays URSM-only — the pre-existing soft
// semantics keep short aggregates blips (a one-off relay hiccup) from removing
// usable capacity. The executor wires the settings-backed value over this
// default; the constant pins the ladder for tests and zero-value configs.
const DefaultEmptyResponseStreakThreshold = 5

// EmptyResponseStreakWindow 是「连续空响应」的**时间窗**。
//
// ★ 2026-11-11 审计 F-B1：★ emptyStreak 被实现成**纯计数**（★ 没有任何时间维度）
// ★ ★⇒ ★★ 「连续」的语义被时间**抹平**：★ 3 个月前 4 次 + 今天 1 次 = 5
// ★ ★⇒ ★★ ⇒ 触发绑定降级（★ 而今天这条绑定可能完全健康）
// ★ ★⇒ ★★ 而仓内**自己的规格文本**写的就是「**48 起/6h**」
// ★ ★（★ settings/spec_thresholds.go 的 KeyEmptyResponseStreakThreshold 描述）
// ★ ★⇒ ★★ 即「窗口内的连续」⇒ ★★ **实现与自述不符**。
// ⇒ ★★ 修法：★ 记 `lastEmptyAt`；★ 间隔超过本窗口 ⇒ ★ streak 归零后再计数。
const EmptyResponseStreakWindow = 6 * time.Hour

// OutcomeReducer is safe for concurrent Reduce calls.
type OutcomeReducer struct {
	mu       sync.Mutex
	nodes    map[NodeKey]*nodeState
	seen     map[eventKey]Decision
	seenRing []seenEntry
	seenHead int
	maxSeen  int
	seenTTL  time.Duration
	now      func() time.Time
	// emptyStreakThreshold reports the consecutive-empty escalation count.
	// Nil selects DefaultEmptyResponseStreakThreshold; the executor injects
	// the hot-reloadable settings getter.
	emptyStreakThreshold func() int
}

type nodeState struct {
	status              requestjourney.NodeHealthStatus
	consecutiveFailures int
	emptyStreak         int
	// lastEmptyAt 是**最近一次**空响应的时间（★ 零值 = 本窗口内还没有过空响应）。
	// ★ 用途：★ 让「连续」带上时间维度（★ 见 EmptyResponseStreakWindow）。
	lastEmptyAt time.Time
}

type eventKey struct {
	node      NodeKey
	attemptID string
	phase     Phase
}

// seenEntry is one slot of the insertion-ordered deduplication ring.
type seenEntry struct {
	key    eventKey
	at     time.Time
	active bool
}

// ReducerConfig tunes the outcome reducer. Zero values select production
// defaults; Now is injectable so tests can advance time deterministically.
type ReducerConfig struct {
	SeenCapacity int
	SeenTTL      time.Duration
	Now          func() time.Time
	// EmptyResponseStreakThreshold reports the consecutive empty-response
	// count that escalates a binding-scoped cooldown. Nil selects
	// DefaultEmptyResponseStreakThreshold.
	EmptyResponseStreakThreshold func() int
}

func NewOutcomeReducer() *OutcomeReducer {
	return NewOutcomeReducerWithConfig(ReducerConfig{})
}

// NewOutcomeReducerWithSeenCapacity constructs a reducer with bounded event
// deduplication history. Values below one use the production default.
func NewOutcomeReducerWithSeenCapacity(maxSeen int) *OutcomeReducer {
	return NewOutcomeReducerWithConfig(ReducerConfig{SeenCapacity: maxSeen})
}

// NewOutcomeReducerWithConfig constructs a reducer, normalizing invalid
// configuration to the production defaults.
func NewOutcomeReducerWithConfig(cfg ReducerConfig) *OutcomeReducer {
	if cfg.SeenCapacity < 1 {
		cfg.SeenCapacity = DefaultSeenCapacity
	}
	if cfg.SeenTTL <= 0 {
		cfg.SeenTTL = DefaultSeenTTL
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &OutcomeReducer{
		nodes:                make(map[NodeKey]*nodeState),
		seen:                 make(map[eventKey]Decision),
		maxSeen:              cfg.SeenCapacity,
		seenTTL:              cfg.SeenTTL,
		now:                  cfg.Now,
		emptyStreakThreshold: cfg.EmptyResponseStreakThreshold,
	}
}

// Reduce atomically deduplicates and reduces an observation.
func (r *OutcomeReducer) Reduce(observation Observation) (Decision, error) {
	if err := observation.validate(); err != nil {
		return Decision{}, err
	}
	if r == nil {
		return Decision{}, errors.New("nodehealth: nil reducer")
	}

	// ★ 2026-11-11 审计 F-B2：★ 阈值读取**可能触发 DB 读**（★ settings 的 uncached 族
	// ★ ★⇒ ★★ 每次一条 `SELECT value::text FROM settings_kv`、★ 5s 超时）
	// ★ ★⇒ ★★ 绝不能在**进程级全局锁**（★ r.mu）内做 ⇒ ★★ 否则 DB 抖动时，
	// ★ ★⇒ ★★ 每一次 Reduce 都会把**全网关**的 nodehealth 归约**串行化**阻塞。
	// ⇒ ★★ 修法：★ **锁外**取快照（★ 锁内只做纯计算）★ ★ 配合注入端的缓存版读取器。
	threshold := r.emptyStreakThresholdSnapshot()

	r.mu.Lock()
	defer r.mu.Unlock()

	r.defaultsLocked()
	now := r.now()
	r.reclaimSeenLocked(now)

	key := eventKey{node: observation.Node, attemptID: observation.AttemptID, phase: observation.Phase}
	if previous, ok := r.seen[key]; ok {
		previous.Accepted = false
		previous.Duplicate = true
		previous.Effects = nil
		return previous, nil
	}

	state := r.nodes[observation.Node]
	if state == nil {
		state = &nodeState{status: requestjourney.NodeHealthHealthy}
		r.nodes[observation.Node] = state
	}
	decision := Decision{
		Node:           observation.Node,
		AttemptID:      observation.AttemptID,
		Phase:          observation.Phase,
		Outcome:        observation.Outcome,
		Accepted:       true,
		PreviousStatus: state.status,
		Status:         state.status,
		ErrorKind:      observation.ErrorKind,
		HTTPStatus:     observation.HTTPStatus,
		RequestID:      observation.RequestID,
		BillingMode:    observation.BillingMode,
		LatencyMs:      observation.LatencyMs,
		ErrorDetail:    observation.ErrorDetail,
	}

	switch observation.Outcome {
	case requestjourney.OutcomeSuccess:
		// Any success on the node — request or probe — is direct evidence the
		// upstream produces content again, so an empty-response streak ends.
		state.emptyStreak = 0
		state.lastEmptyAt = time.Time{}
		if observation.Phase == PhaseDirectProbe {
			state.status = requestjourney.NodeHealthRecovering
			break
		}
		if observation.Phase == PhaseGatewayProbe && !r.directProbeSucceeded(observation) {
			state.status = requestjourney.NodeHealthSuspect
			break
		}
		state.consecutiveFailures = 0
		state.status = requestjourney.NodeHealthHealthy
	case requestjourney.OutcomeFailure:
		if observation.ErrorKind == ErrorKindEmptyResponse {
			// Empty responses carry no node-health signal (a long deliberation
			// is not an outage), so consecutiveFailures stays untouched. The
			// per-node streak feeds the binding-scoped escalation below; any
			// other outcome kind breaks the "consecutive" property.
			// ★ 2026-11-11 审计 F-B1：★ 「连续」必须带**时间维度**
			// ★ ★⇒ ★★ 间隔超过窗口 ⇒ ★★ 之前的计数**不再连续** ⇒ ★★ 归零后重新计数。
			if !state.lastEmptyAt.IsZero() && now.Sub(state.lastEmptyAt) > EmptyResponseStreakWindow {
				state.emptyStreak = 0
			}
			state.emptyStreak++
			state.lastEmptyAt = now
			break
		}
		if observation.ErrorKind == ErrorKindRequest {
			break
		}
		state.emptyStreak = 0
		state.lastEmptyAt = time.Time{}
		state.consecutiveFailures++
		if isPermanent(observation.ErrorKind) {
			state.status = requestjourney.NodeHealthQuarantined
			break
		}
		if observation.Phase == PhaseGatewayProbe && state.consecutiveFailures < 2 {
			state.consecutiveFailures = 2
		}
		// R31 (audit 2026-09-16 §四#5): the Suspect→Degraded boundary follows
		// the same billing-mode threshold effectsFor uses to fire the circuit,
		// so the status ladder stays a leading indicator of breaker behaviour.
		// Previously the boundary was a flat 3: a free credential (circuit
		// threshold 6) sat labelled Degraded at failures 3-5 while its circuit
		// was still closed and traffic kept flowing — a dashboard fork with no
		// behavioural counterpart. Paid credentials are unchanged (3 = 3).
		if state.consecutiveFailures < circuitFailureThreshold(observation.BillingMode) {
			state.status = requestjourney.NodeHealthSuspect
		} else {
			state.status = requestjourney.NodeHealthDegraded
		}
	case requestjourney.OutcomeCanceled:
		// Cancellation is not evidence about upstream node health.
	}
	decision.Status = state.status
	decision.ConsecutiveFailures = state.consecutiveFailures
	decision.EmptyResponseStreak = state.emptyStreak
	decision.Effects = r.effectsFor(observation, decision, threshold)
	r.rememberLocked(key, decision, now)
	return decision, nil
}

// defaultsLocked repairs a zero-value OutcomeReducer in place so Reduce is
// safe on any constructed instance, matching the constructor defaults.
func (r *OutcomeReducer) defaultsLocked() {
	if r.nodes == nil {
		r.nodes = make(map[NodeKey]*nodeState)
	}
	if r.seen == nil {
		r.seen = make(map[eventKey]Decision)
	}
	if r.maxSeen < 1 {
		r.maxSeen = DefaultSeenCapacity
	}
	if r.seenTTL <= 0 {
		r.seenTTL = DefaultSeenTTL
	}
	if r.now == nil {
		r.now = time.Now
	}
	// ★ emptyStreakThreshold **刻意不在这里修复**：★ 它必须**只由构造器写一次**
	// ★ ★（★ Reduce 在锁外无锁读它 ⇒ ★ 若这里也写 ⇒ ★★ 无锁读与锁内写构成数据竞争）
	// ★ ★⇒ ★★ nil 的情形由 emptyStreakThresholdSnapshot() 自己兜住（★ 取默认阈值）。
}

// emptyStreakThresholdSnapshot 返回配置的空响应升级阈值（★ **锁外**调用）。
//
// ★ ★ 它**可能触发 DB 读**（★ 注入的是 settings 读取器）⇒ ★★ **必须在 r.mu 之外调用**。
// ★ ★ r.emptyStreakThreshold **只由构造器写一次**（★ defaultsLocked 刻意不碰它）
// ★ ★⇒ ★★ 这里无锁读它是安全的（★ happens-before 由 goroutine 启动保证）。
func (r *OutcomeReducer) emptyStreakThresholdSnapshot() int {
	if fn := r.emptyStreakThreshold; fn != nil {
		if n := fn(); n > 0 {
			return n
		}
	}
	return DefaultEmptyResponseStreakThreshold
}

func (r *OutcomeReducer) rememberLocked(key eventKey, decision Decision, now time.Time) {
	if len(r.seenRing)-r.seenHead >= r.maxSeen {
		oldest := r.seenRing[r.seenHead]
		delete(r.seen, oldest.key)
		r.seenHead++
		recordSeenEviction(evictionReasonCapacity)
	}
	r.seenRing = append(r.seenRing, seenEntry{key: key, at: now, active: true})
	r.seen[key] = decision
	r.compactSeenLocked()
}

// reclaimSeenLocked drops deduplication entries older than the TTL. The ring
// is insertion-ordered, so expiry scans from the head and stops at the first
// live entry; per Reduce the work is proportional to the entries reclaimed.
// Node state lives in a separate map and is never touched here.
func (r *OutcomeReducer) reclaimSeenLocked(now time.Time) {
	deadline := now.Add(-r.seenTTL)
	for r.seenHead < len(r.seenRing) && r.seenRing[r.seenHead].at.Before(deadline) {
		entry := r.seenRing[r.seenHead]
		if entry.active {
			delete(r.seen, entry.key)
			recordSeenEviction(evictionReasonTTL)
		}
		r.seenHead++
	}
	r.compactSeenLocked()
}

// compactSeenLocked reclaims ring slots the head pointer already skipped so
// the backing array does not grow without bound. Amortized O(1) per event.
func (r *OutcomeReducer) compactSeenLocked() {
	if r.seenHead == 0 {
		return
	}
	if r.seenHead >= len(r.seenRing) {
		r.seenRing = r.seenRing[:0]
		r.seenHead = 0
		return
	}
	if r.seenHead >= len(r.seenRing)/2 {
		n := copy(r.seenRing, r.seenRing[r.seenHead:])
		r.seenRing = r.seenRing[:n]
		r.seenHead = 0
	}
}

// ReduceAndApply invokes adapter hooks only for a newly accepted observation.
func (r *OutcomeReducer) ReduceAndApply(ctx context.Context, observation Observation, adapter Adapter) (Decision, error) {
	decision, err := r.Reduce(observation)
	if err != nil || !decision.Accepted || adapter == nil {
		return decision, err
	}
	if err := adapter.ApplyNodeHealthDecision(ctx, decision); err != nil {
		// Empty responses are intentionally URSM-only and do not mutate reducer
		// health state. If their sole side effect fails, allow the same attempt to
		// replay instead of suppressing its routing penalty for the seen TTL.
		if decision.ErrorKind == ErrorKindEmptyResponse {
			r.forgetSeen(observation)
		}
		return decision, fmt.Errorf("nodehealth: apply decision: %w", err)
	}
	return decision, nil
}

func (r *OutcomeReducer) forgetSeen(observation Observation) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := eventKey{node: observation.Node, attemptID: observation.AttemptID, phase: observation.Phase}
	delete(r.seen, key)
	// Keep the ring and map coherent: a later successful replay may use the
	// same key, so the failed entry must not delete that newer mapping at TTL
	// reclamation time.
	for i := len(r.seenRing) - 1; i >= r.seenHead; i-- {
		if r.seenRing[i].active && r.seenRing[i].key == key {
			r.seenRing[i].active = false
			break
		}
	}
}

func (r *OutcomeReducer) directProbeSucceeded(observation Observation) bool {
	direct, ok := r.seen[eventKey{
		node: observation.Node, attemptID: observation.AttemptID, phase: PhaseDirectProbe,
	}]
	return ok && direct.Outcome == requestjourney.OutcomeSuccess
}

func (r *OutcomeReducer) effectsFor(observation Observation, decision Decision, emptyStreakThreshold int) []Effect {
	if observation.Outcome == requestjourney.OutcomeCanceled || observation.ErrorKind == ErrorKindRequest {
		return nil
	}
	if observation.Outcome == requestjourney.OutcomeFailure && observation.ErrorKind == ErrorKindEmptyResponse {
		// Empty responses are already failover-eligible at the executor, so a
		// short burst keeps its only durable routing effect on the exact
		// tenant/credential/model node in URSM — one-off relay hiccups must
		// not remove usable capacity. A SUSTAINED streak (node-state chain
		// gap #1, 2026-11-11: 48 consecutive NIM empty streams in 6h with
		// zero persistent trace) additionally demotes the binding itself so
		// the executor stops re-walking a deterministically empty upstream.
		// Scope stays binding-level by construction: no circuit failure, no
		// credential-wide unavailability — sibling models on the same
		// credential are unaffected, mirroring ErrorKindModelUnavailable.
		if decision.EmptyResponseStreak >= emptyStreakThreshold {
			return []Effect{
				{Kind: EffectUpdateURSM},
				{Kind: EffectSetBindingUnavailable},
				{Kind: EffectInvalidateCandidateCache},
			}
		}
		return []Effect{{Kind: EffectUpdateURSM}}
	}
	effects := []Effect{{Kind: EffectPersistNodeStatus}}
	if observation.Outcome == requestjourney.OutcomeSuccess {
		if observation.Phase == PhaseDirectProbe || decision.Status != requestjourney.NodeHealthHealthy {
			return effects
		}
		return append(effects,
			Effect{Kind: EffectRecoverCircuit},
			Effect{Kind: EffectRestoreBinding},
			Effect{Kind: EffectUpdateURSM},
			Effect{Kind: EffectInvalidateCandidateCache},
			Effect{Kind: EffectCancelProbeBackoff},
		)
	}

	effects = append(effects, Effect{Kind: EffectUpdateURSM})
	if isBindingScoped(observation) {
		// A binding-scoped error invalidates only the (credential, model)
		// binding. The credential-wide circuit and availability stay
		// untouched: sibling models on the same credential keep serving.
		effects = append(effects,
			Effect{Kind: EffectSetBindingUnavailable},
			Effect{Kind: EffectInvalidateCandidateCache},
			Effect{Kind: EffectScheduleProbe},
		)
	} else if isPermanent(observation.ErrorKind) || decision.ConsecutiveFailures >= circuitFailureThreshold(observation.BillingMode) {
		effects = append(effects,
			Effect{Kind: EffectRecordCircuitFailure},
			Effect{Kind: EffectSetBindingUnavailable},
			Effect{Kind: EffectSetCredentialUnavailable},
			Effect{Kind: EffectInvalidateCandidateCache},
			Effect{Kind: EffectScheduleProbe},
		)
	}
	if decision.Status == requestjourney.NodeHealthQuarantined {
		effects = append(effects, Effect{Kind: EffectQuarantine})
	}
	return effects
}

// circuitFailureThreshold is the consecutive-failure count that escalates a
// non-permanent failure to the credential-wide circuit (paid) or its free-tier
// variant (2026-09-15, 245 free-capacity plan).
//
// Free credentials (billing_mode='free', on 245 the NVIDIA NIM pool) are
// long-term flaky but occasionally usable single-concurrency channels. At the
// paid threshold of 3, ordinary bursts of transient errors opened the circuit
// constantly (326 "circuit opened" cycles / 24h on 18/8 + 18/19) and each
// open window discarded usable free capacity while requests bounced with
// KindCircuitOpen. Free bindings therefore need 6 consecutive failures before
// the circuit effect fires — ranking is already handled by the soft-demote
// path (cmi.success_rate / URSM quality), so a merely noisy free credential
// keeps serving instead of being hard-excluded. Permanent failure kinds
// bypass this threshold entirely (isPermanent above).
func circuitFailureThreshold(billingMode string) int {
	if strings.EqualFold(strings.TrimSpace(billingMode), "free") {
		return 6
	}
	return 3
}

func isPermanent(kind ErrorKind) bool {
	return kind == ErrorKindAuth || kind == ErrorKindQuota || kind == ErrorKindModelBinding
}

// isBindingScoped reports whether an error kind only invalidates the
// (credential, model) binding rather than the credential itself. Binding
// failures are quarantined per node and never feed the credential-wide
// circuit, so model-a going missing must not affect model-b availability
// on the same credential.
//
// ★ 2026-11-02（第九轮 · F-B）：本函数**多了 HTTP 状态这一维**，因为
// 「无可用后端」有两种**证据指向不同层**的事实，必须分开：
//
//	4xx 的「无可用后端」——例如中转的
//	  "No enterprise provider is available for this request path: POST
//	   /v1/messages. Required model: claude-haiku-5-5; model series: claude."
//	  ⇒ 证据指向**这一对 (凭据,模型)**（这个凭据在这条路径上不提供这个
//	  模型系列）⇒ **绑定级**：只冷却这一对，兄弟模型照常服务，
//	  且**保留跨凭据故障转移**（兄弟凭据可能服务同一个模型）。
//	5xx 的「无可用后端」——例如中转分组的
//	  "No available channel for model X under group Y (distributor)"
//	  ⇒ 证据指向**中转整体**（这个分组暂时没有可用通道）⇒ **凭据级**：
//	  连败到阈值就熔断该凭据，这是**既有且正确**的处置。
//
// ★ 改动前两者都落 `ErrorKindUpstream` ⇒ 都不绑定级 ⇒ **4xx 那一支会
// 连坐该凭据上的全部模型**（3 连败 ⇒ 整凭据 cooling ⇒ 全租户 503）。
// 这与规则自己的注释相悖：errorsx/classify.go 的 noEnterpriseProviderRe
// 写明「KindNoAvailableChannel means fail over to a sibling
// credential/model, **not** blind same-node retries that park a healthy
// node」⇒ 它明确不要「park 一个健康节点」，而旧归责恰恰 park 了整个凭据。
//
// ★ 判据用**上游状态码**（Observation.HTTPStatus，由 executor 的
// dispatchHTTPStatus 从 *upstream.Error.StatusCode 取）⇒ 400..499 绑定级，
// 5xx / 未知（0）凭据级。
func isBindingScoped(observation Observation) bool {
	switch observation.ErrorKind {
	case ErrorKindModelBinding, ErrorKindModelUnavailable:
		return true
	case ErrorKindNoAvailableChannel:
		return observation.HTTPStatus >= 400 && observation.HTTPStatus < 500
	}
	return false
}

func (o Observation) validate() error {
	if o.Node.CredentialID <= 0 || strings.TrimSpace(o.Node.Model) == "" {
		return errors.New("nodehealth: positive credential ID and model are required")
	}
	if strings.TrimSpace(o.AttemptID) == "" || strings.TrimSpace(string(o.Phase)) == "" {
		return errors.New("nodehealth: attempt ID and phase are required")
	}
	if o.Outcome != requestjourney.OutcomeSuccess && o.Outcome != requestjourney.OutcomeFailure && o.Outcome != requestjourney.OutcomeCanceled {
		return fmt.Errorf("nodehealth: invalid outcome %q", o.Outcome)
	}
	if o.Outcome == requestjourney.OutcomeFailure && o.ErrorKind == "" {
		return errors.New("nodehealth: failure requires error kind")
	}
	return nil
}
