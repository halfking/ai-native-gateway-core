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
	EffectRestoreCredential        EffectKind = "restore_credential"
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

// OutcomeReducer is safe for concurrent Reduce calls.
type OutcomeReducer struct {
	mu    sync.Mutex
	nodes map[NodeKey]*nodeState
	seen  map[eventKey]Decision
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

func NewOutcomeReducer() *OutcomeReducer {
	return &OutcomeReducer{
		nodes: make(map[NodeKey]*nodeState),
		seen:  make(map[eventKey]Decision),
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
		if observation.ErrorKind == ErrorKindRequest {
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
	r.seen[key] = decision
	return decision, nil
}

// ReduceAndApply invokes adapter hooks only for a newly accepted observation.
func (r *OutcomeReducer) ReduceAndApply(ctx context.Context, observation Observation, adapter Adapter) (Decision, error) {
	decision, err := r.Reduce(observation)
	if err != nil || !decision.Accepted || adapter == nil {
		return decision, err
	}
	if err := adapter.ApplyNodeHealthDecision(ctx, decision); err != nil {
		return decision, fmt.Errorf("nodehealth: apply decision: %w", err)
	}
	return decision, nil
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
	effects := []Effect{{Kind: EffectPersistNodeStatus}}
	if observation.Outcome == requestjourney.OutcomeSuccess {
		if observation.Phase == PhaseDirectProbe || decision.Status != requestjourney.NodeHealthHealthy {
			return effects
		}
		return append(effects,
			Effect{Kind: EffectRecoverCircuit},
			Effect{Kind: EffectRestoreBinding},
			Effect{Kind: EffectRestoreCredential},
			Effect{Kind: EffectUpdateURSM},
			Effect{Kind: EffectInvalidateCandidateCache},
			Effect{Kind: EffectCancelProbeBackoff},
		)
	}

	effects = append(effects, Effect{Kind: EffectUpdateURSM})
	if isPermanent(observation.ErrorKind) || decision.ConsecutiveFailures >= 3 {
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
