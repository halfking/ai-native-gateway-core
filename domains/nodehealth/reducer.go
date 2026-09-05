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
	// ErrorKindEmptyResponse is a binding-scoped quality signal. It is recorded
	// in URSM for routing penalties but cannot escalate a credential-wide circuit
	// or availability state.
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
}

type nodeState struct {
	status              requestjourney.NodeHealthStatus
	consecutiveFailures int
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
		nodes:   make(map[NodeKey]*nodeState),
		seen:    make(map[eventKey]Decision),
		maxSeen: cfg.SeenCapacity,
		seenTTL: cfg.SeenTTL,
		now:     cfg.Now,
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
		if observation.ErrorKind == ErrorKindRequest || observation.ErrorKind == ErrorKindEmptyResponse {
			break
		}
		state.consecutiveFailures++
		if isPermanent(observation.ErrorKind) {
			state.status = requestjourney.NodeHealthQuarantined
			break
		}
		if observation.Phase == PhaseGatewayProbe && state.consecutiveFailures < 2 {
			state.consecutiveFailures = 2
		}
		if state.consecutiveFailures < 3 {
			state.status = requestjourney.NodeHealthSuspect
		} else {
			state.status = requestjourney.NodeHealthDegraded
		}
	case requestjourney.OutcomeCanceled:
		// Cancellation is not evidence about upstream node health.
	}
	decision.Status = state.status
	decision.ConsecutiveFailures = state.consecutiveFailures
	decision.Effects = effectsFor(observation, decision)
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

func effectsFor(observation Observation, decision Decision) []Effect {
	if observation.Outcome == requestjourney.OutcomeCanceled || observation.ErrorKind == ErrorKindRequest {
		return nil
	}
	if observation.Outcome == requestjourney.OutcomeFailure && observation.ErrorKind == ErrorKindEmptyResponse {
		// Empty responses are already failover-eligible at the executor. Keep
		// their only durable routing effect on the exact tenant/credential/model
		// node in URSM; do not let a short burst poison sibling models.
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
	if isBindingScoped(observation.ErrorKind) {
		// A binding-scoped error invalidates only the (credential, model)
		// binding. The credential-wide circuit and availability stay
		// untouched: sibling models on the same credential keep serving.
		effects = append(effects,
			Effect{Kind: EffectSetBindingUnavailable},
			Effect{Kind: EffectInvalidateCandidateCache},
			Effect{Kind: EffectScheduleProbe},
		)
	} else if isPermanent(observation.ErrorKind) || decision.ConsecutiveFailures >= 3 {
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

func isPermanent(kind ErrorKind) bool {
	return kind == ErrorKindAuth || kind == ErrorKindQuota || kind == ErrorKindModelBinding
}

// isBindingScoped reports whether an error kind only invalidates the
// (credential, model) binding rather than the credential itself. Binding
// failures are quarantined per node and never feed the credential-wide
// circuit, so model-a going missing must not affect model-b availability
// on the same credential.
func isBindingScoped(kind ErrorKind) bool {
	return kind == ErrorKindModelBinding
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
