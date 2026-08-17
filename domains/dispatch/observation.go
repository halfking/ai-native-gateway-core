package dispatch

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ObservationType identifies one dispatch lifecycle fact. Values intentionally
// match the persisted RequestJourney vocabulary, but dispatch owns this type.
type ObservationType string

const (
	// ObservationModelEnqueued records admission to a model queue.
	ObservationModelEnqueued ObservationType = "model_enqueued"
	// ObservationCredentialSelected records routing to a credential candidate.
	ObservationCredentialSelected ObservationType = "credential_selected"
	// ObservationNodeEnqueued records admission to a credential queue.
	ObservationNodeEnqueued ObservationType = "node_enqueued"
	// ObservationNodeSelected records governor admission for a credential.
	ObservationNodeSelected ObservationType = "node_selected"
	// ObservationAttemptStarted records the start of an upstream attempt.
	ObservationAttemptStarted ObservationType = "attempt_started"
	// ObservationFirstByte records the first semantic byte of an attempt.
	ObservationFirstByte ObservationType = "first_byte"
	// ObservationAttemptSucceeded records a successful attempt.
	ObservationAttemptSucceeded ObservationType = "attempt_succeeded"
	// ObservationAttemptFailed records a failed attempt.
	ObservationAttemptFailed ObservationType = "attempt_failed"
	// ObservationRetryScheduled records a same-credential retry.
	ObservationRetryScheduled ObservationType = "retry_scheduled"
	// ObservationNodeSwitched records a credential switch.
	ObservationNodeSwitched ObservationType = "node_switched"
	// ObservationModelSwitched records a model switch.
	ObservationModelSwitched ObservationType = "model_switched"
	// ObservationRequestSucceeded records successful request completion.
	ObservationRequestSucceeded ObservationType = "request_succeeded"
	// ObservationRequestFailed records failed request completion.
	ObservationRequestFailed ObservationType = "request_failed"
	// ObservationRequestCanceled records canceled request completion.
	ObservationRequestCanceled ObservationType = "request_canceled"
)

func (t ObservationType) valid() bool {
	switch t {
	case ObservationModelEnqueued,
		ObservationCredentialSelected,
		ObservationNodeEnqueued,
		ObservationNodeSelected,
		ObservationAttemptStarted,
		ObservationFirstByte,
		ObservationAttemptSucceeded,
		ObservationAttemptFailed,
		ObservationRetryScheduled,
		ObservationNodeSwitched,
		ObservationModelSwitched,
		ObservationRequestSucceeded,
		ObservationRequestFailed,
		ObservationRequestCanceled:
		return true
	default:
		return false
	}
}

// EventStage is dispatch's closed lifecycle stage vocabulary.
type EventStage string

const (
	// StageModelQueue represents waiting in the model queue.
	StageModelQueue EventStage = "model_queue"
	// StageCredentialQueue represents waiting in a credential queue.
	StageCredentialQueue EventStage = "credential_queue"
	// StageNodeSelection represents credential selection and governor admission.
	StageNodeSelection EventStage = "node_selection"
	// StageUpstream represents an active upstream attempt.
	StageUpstream EventStage = "upstream"
	// StageStreaming represents response streaming after first semantic byte.
	StageStreaming EventStage = "streaming"
	// StageRetrying represents retry or switch scheduling.
	StageRetrying EventStage = "retrying"
	// StageTerminal represents request completion.
	StageTerminal EventStage = "terminal"
)

func (s EventStage) valid() bool {
	switch s {
	case StageModelQueue, StageCredentialQueue, StageNodeSelection, StageUpstream, StageStreaming, StageRetrying, StageTerminal:
		return true
	default:
		return false
	}
}

// Outcome is the normalized result of an attempt or request.
type Outcome string

const (
	// OutcomeSuccess indicates successful completion.
	OutcomeSuccess Outcome = "success"
	// OutcomeFailure indicates failed completion.
	OutcomeFailure Outcome = "failure"
	// OutcomeCanceled indicates context cancellation or deadline expiry.
	OutcomeCanceled Outcome = "canceled"
)

func (o Outcome) valid() bool {
	return o == "" || o == OutcomeSuccess || o == OutcomeFailure || o == OutcomeCanceled
}

// AttemptRef identifies one immutable upstream attempt.
type AttemptRef struct {
	// AttemptID is the unique attempt UUID assigned by the dispatch pipeline.
	AttemptID string
	// AttemptNo is the one-based attempt number within a request.
	AttemptNo int
	// Model is the resolved model used by the attempt.
	Model string
	// ProviderID is the routing provider id for the attempt.
	ProviderID int64
	// Provider is the human-readable provider label for stats.
	Provider string
	// CredentialID is the upstream credential id for the attempt.
	CredentialID int64
}

// Observation is an immutable, content-free dispatch lifecycle fact.
type Observation struct {
	// TenantID identifies the tenant that owns the request.
	TenantID string
	// GatewayInstanceID is the gateway instance that produced the observation.
	GatewayInstanceID string
	// RequestID is the request id used across lifecycle events.
	RequestID string
	// Seq is the strictly increasing per-journey sequence number.
	Seq int64
	// Type names the lifecycle event.
	Type ObservationType
	// Stage names the lifecycle stage bucket.
	Stage EventStage
	// RequestedModel is the model the client requested.
	RequestedModel string
	// ResolvedModel is the routable model after auto-resolution.
	ResolvedModel string
	// Model is the model bound to the event (queue or attempt).
	Model string
	// ProviderID is the routing provider id.
	ProviderID int64
	// Provider is the human-readable provider label.
	Provider string
	// CredentialID is the upstream credential id.
	CredentialID int64
	// FromModel and ToModel are populated by model-switch events.
	FromModel string
	ToModel   string
	// FromCredentialID and ToCredentialID are populated by credential-switch events.
	FromCredentialID int64
	ToCredentialID   int64
	// Attempt is the attempt reference for attempt-scoped events.
	Attempt *AttemptRef
	// Outcome is the normalized result for terminal events.
	Outcome Outcome
	// ErrorKind is a bounded error classification for failure events.
	ErrorKind string
	// HTTPStatus is the upstream status for terminal events when known.
	HTTPStatus int
	// RetryReason is the bounded reason for retry-scheduled events.
	RetryReason string
	// SwitchReason is the bounded reason for switch events.
	SwitchReason string
	// OccurredAt is the monotonic timestamp the dispatch pipeline recorded.
	OccurredAt time.Time
}

// Validate rejects malformed observations before they cross the dispatch seam.
func (o Observation) Validate() error {
	if o.TenantID == "" || o.GatewayInstanceID == "" || o.RequestID == "" {
		return errors.New("tenant_id, gateway_instance_id, and request_id are required")
	}
	if o.Seq <= 0 {
		return errors.New("seq must be positive")
	}
	if !o.Type.valid() {
		return fmt.Errorf("invalid observation type %q", o.Type)
	}
	if !o.Stage.valid() {
		return fmt.Errorf("invalid event stage %q", o.Stage)
	}
	if !o.Outcome.valid() {
		return fmt.Errorf("invalid outcome %q", o.Outcome)
	}
	if o.HTTPStatus < 0 || o.HTTPStatus > 599 || (o.HTTPStatus > 0 && o.HTTPStatus < 100) {
		return fmt.Errorf("invalid http status %d", o.HTTPStatus)
	}
	for name, id := range map[string]int64{
		"provider_id": o.ProviderID, "credential_id": o.CredentialID,
		"from_credential_id": o.FromCredentialID, "to_credential_id": o.ToCredentialID,
	} {
		if id < 0 {
			return fmt.Errorf("%s must not be negative", name)
		}
	}
	if o.Attempt != nil {
		if o.Attempt.AttemptID == "" || o.Attempt.AttemptNo <= 0 || o.Attempt.ProviderID <= 0 || o.Attempt.CredentialID <= 0 {
			return errors.New("attempt requires id, positive number, provider, and credential")
		}
	}
	switch o.Type {
	case ObservationCredentialSelected, ObservationNodeEnqueued, ObservationNodeSelected:
		if o.CredentialID <= 0 {
			return fmt.Errorf("%s requires a positive credential id", o.Type)
		}
	case ObservationNodeSwitched:
		if o.FromCredentialID <= 0 || o.ToCredentialID <= 0 {
			return errors.New("node_switched requires positive source and target credential ids")
		}
	case ObservationModelSwitched:
		if o.FromModel == "" || o.ToModel == "" {
			return errors.New("model_switched requires source and target models")
		}
	case ObservationRequestCanceled:
		if o.Outcome != OutcomeCanceled {
			return errors.New("request_canceled requires canceled outcome")
		}
	}
	if o.OccurredAt.IsZero() {
		return errors.New("occurred_at is required")
	}
	return nil
}

// ObservationSink receives lifecycle observations. Pipeline delivery is
// serialized through a bounded asynchronous FIFO.
type ObservationSink interface {
	// ObserveDispatch delivers one immutable observation to the sink.
	ObserveDispatch(context.Context, Observation)
}

// ObservationSinkFunc adapts a function to ObservationSink.
type ObservationSinkFunc func(context.Context, Observation)

// ObserveDispatch calls the wrapped observation function.
func (f ObservationSinkFunc) ObserveDispatch(ctx context.Context, observation Observation) {
	if f != nil {
		f(ctx, observation)
	}
}
