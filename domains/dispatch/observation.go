package dispatch

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrJournalNotFound is returned when a requested journal snapshot does not
// exist or the caller is not authorized to access it. This sentinel follows
// the not-found-shaped error pattern used by requestjourney.Detail to avoid
// cross-tenant existence leaks (ADR 2026-08-28 §Decision point 4).
var ErrJournalNotFound = errors.New("journal snapshot not found")

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
	// RetryAt, when set on ObservationRetryScheduled (v4 R1.1/T3-8), is the
	// time at which the timed retry will be picked back up. nil means an
	// immediate retry (legacy failover behavior).
	RetryAt *time.Time
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
	if o.RetryAt != nil && o.RetryAt.IsZero() {
		return errors.New("retry_at, when present, must not be zero")
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

// maxJournalSnapshotEvents bounds the number of journal entries delivered
// to a JournalSink consumer (ADR 2026-08-28-requestjourney-journal-snapshot.md
// §Decision point 3). When a journal exceeds this limit, the oldest entries
// are dropped and the snapshot's Truncated field is set to true. This defends
// consumers from unbounded event streams while preserving the most recent
// execution context (the terminal entry and its immediate predecessors).
//
// The bound is intentionally below journalCapacity (128) so a full-capacity
// journal still truncates to a manageable consumer payload.
const maxJournalSnapshotEvents = 50

// JournalSnapshot is a detached, immutable view of a QueuedRequest's full
// attempt journal. It is delivered to the optional JournalSink exactly once
// at terminal time (Pipeline.complete, CAS-guarded). The terminal entry is
// already in Entries by the time the snapshot is taken, so consumers see the
// complete trace.
//
// JournalSnapshot is a detached, immutable view of a QueuedRequest's full
// attempt journal. It is delivered to the optional JournalSink exactly once
// at terminal time (Pipeline.complete, CAS-guarded). The terminal entry is
// already in Entries by the time the snapshot is taken, so consumers see the
// complete trace.
//
// Per ADR 2026-08-28-requestjourney-journal-snapshot.md §Decision point 3,
// snapshots are bounded by a maximum event count. When the journal exceeds
// this limit, the oldest entries are dropped and Truncated is set to true.
//
// §Decision point 4 / §6 extensions (audit-24h-20260829-r5 §5.5 merged):
//   - SnapshotVersion   — qr.journalSeq at terminal time; consumers may
//     short-circuit duplicate retries by comparing it to the recorder's
//     MaxSeq for (tenant, request).
//   - CallerTenantID / CallerAuthorized — auth context required by sinks
//     that gate on tenant or operator identity; an unauthorized snapshot
//     must be rejected, not silently dropped.
type JournalSnapshot struct {
	TenantID  string
	RequestID string
	Entries   []JournalEntry
	// Truncated is true when the journal exceeded the max event count and
	// the oldest entries were dropped. TruncatedCount is the number of
	// entries dropped.
	Truncated      bool
	TruncatedCount int
	// SnapshotVersion is the monotonic journal seq observed at terminal time.
	// Sinks that ship via the recorder's MaxSeq can short-circuit retries by
	// comparing it against the live recorder state. See ADR §6.
	SnapshotVersion int64
	// CallerTenantID / CallerAuthorized carry the trusted caller's tenant
	// identity at the terminal-time wiring boundary. Sinks must reject
	// mismatched or unauthorized snapshots rather than silently drop them.
	// See ADR §4.
	CallerTenantID   string
	CallerAuthorized bool
}

// JournalSink consumes the per-request attempt journal at terminal time.
// The dispatch lifecycle owns ordering and exactly-once delivery (the same
// CAS guard as ObservationSink); the sink only translates values into its
// own persistence contract. nil sinks disable the path (no goroutine spawned,
// no metric emission). Mirrors ObservationSink — keeps the journal bridge to
// requestjourney independently wired from the observation bridge.
type JournalSink interface {
	// ApplyJournalSnapshot delivers one detached journal snapshot to the sink.
	ApplyJournalSnapshot(context.Context, JournalSnapshot)
}

// JournalSinkFunc adapts a function to JournalSink.
type JournalSinkFunc func(context.Context, JournalSnapshot)

// ApplyJournalSnapshot calls the wrapped snapshot function.
func (f JournalSinkFunc) ApplyJournalSnapshot(ctx context.Context, snapshot JournalSnapshot) {
	if f != nil {
		f(ctx, snapshot)
	}
}

// AuthorizedJournalConsumer provides a pull-based query interface for journal
// snapshots with caller authorization. This interface satisfies ADR 2026-08-28
// §Decision point 1 ("consumer requests a snapshot ... through the existing
// requestjourney service boundary") and §Decision point 4 (tenant/authorization
// context verification with not-found-shaped errors).
//
// Unlike the push-based JournalSink (which delivers snapshots from the pipeline's
// terminal completion), AuthorizedJournalConsumer is designed for external query
// paths (e.g., admin APIs, diagnostic tools) where the caller's tenant must be
// verified before snapshot access is granted.
type AuthorizedJournalConsumer interface {
	// ConsumeSnapshot retrieves a journal snapshot for the specified tenant and
	// request ID, verifying that callerTenant matches the snapshot's TenantID.
	//
	// Returns ErrJournalNotFound when:
	//   - The snapshot does not exist
	//   - callerTenant does not match the snapshot's TenantID
	//   - callerTenant is empty and the caller is not a super-admin bypass
	//
	// This not-found-shaped error prevents cross-tenant existence leaks: an
	// unauthorized caller cannot distinguish "does not exist" from "exists but
	// you cannot access it."
	ConsumeSnapshot(ctx context.Context, callerTenant, requestID string) (JournalSnapshot, error)
}
