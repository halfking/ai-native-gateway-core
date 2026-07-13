// Package routeincident — actions.go
//
// Phase 2 mutating actions and diagnostic runs. Every action in
// this file is gated by the same infrastructure (see
// `action_infra.go`):
//   - super-admin authorization
//   - explicit reason (length-bounded)
//   - confirmation token (SHA-256 hashed on disk)
//   - idempotency key (unique index in `routing_audit_log`)
//   - stale-state / version check on the incident
//   - pre-action snapshot
//   - post-action verification
//   - immutable `routing_audit_log` row committed in the SAME tx
//
// No action accepts an arbitrary URL, raw request body, shell
// command, or SQL. Every action's upstream URL is re-derived from
// stored provider / credential configuration, and the request
// body is a server-owned safe prompt.

package routeincident

import "time"

// ActionKind is the closed set of mutating actions and tests.
// Mirrors the CHECK constraint in `routing_audit_log.action` and
// the action CHECK in `diagnostic_runs.kind`.
type ActionKind string

const (
	ActionDirectUpstreamTest ActionKind = "direct_upstream_test"
	ActionThroughGatewayTest ActionKind = "through_gateway_test"
	ActionReprobe            ActionKind = "reprobe"
	ActionReleaseSlot        ActionKind = "release_slot"
	ActionResetSlots         ActionKind = "reset_slots"
	ActionResetAvailability  ActionKind = "reset_availability"
	ActionRecover            ActionKind = "recover"
	ActionEvidenceExport     ActionKind = "evidence_export"
)

// AllActionKinds returns the closed set. Used by the API layer to
// validate a caller-supplied action before dispatch.
func AllActionKinds() []ActionKind {
	return []ActionKind{
		ActionDirectUpstreamTest,
		ActionThroughGatewayTest,
		ActionReprobe,
		ActionReleaseSlot,
		ActionResetSlots,
		ActionResetAvailability,
		ActionRecover,
		ActionEvidenceExport,
	}
}

// IsMutating reports whether the action is a state change (versus
// `evidence_export`, which is a read). Mutating actions always
// require a confirmation token; evidence_export does not.
func (a ActionKind) IsMutating() bool {
	return a != ActionEvidenceExport
}

// ActionOutcome is the closed set of audit row outcomes.
type ActionOutcome string

const (
	OutcomeSuccess ActionOutcome = "success"
	OutcomeNoop    ActionOutcome = "noop"
	OutcomeFailed  ActionOutcome = "failed"
)

// DiagnosticRunState is the lifecycle of a single diagnostic test
// run. Final states are immutable.
type DiagnosticRunState string

const (
	RunPending   DiagnosticRunState = "pending"
	RunRunning   DiagnosticRunState = "running"
	RunSucceeded DiagnosticRunState = "succeeded"
	RunFailed    DiagnosticRunState = "failed"
	RunCancelled DiagnosticRunState = "cancelled"
)

// IsTerminal reports whether the run is in a final state.
func (s DiagnosticRunState) IsTerminal() bool {
	switch s {
	case RunSucceeded, RunFailed, RunCancelled:
		return true
	}
	return false
}

// DiagnosticRun is the public projection of `diagnostic_runs`.
// Only sanitized, dashboard-safe fields are exposed.
type DiagnosticRun struct {
	ID         string             `json:"id"`
	IncidentID string             `json:"incident_id"`
	TenantID   string             `json:"tenant_id"`
	Kind       ActionKind         `json:"kind"`
	State      DiagnosticRunState `json:"state"`
	RouteKey   map[string]any     `json:"route_key"`
	Parameters map[string]any     `json:"parameters"`
	StartedAt  time.Time          `json:"started_at"`
	FinishedAt *time.Time         `json:"finished_at,omitempty"`
	Result     map[string]any     `json:"result"`
	AuditLogID *int64             `json:"audit_log_id,omitempty"`
	CreatedAt  time.Time          `json:"created_at"`
	UpdatedAt  time.Time          `json:"updated_at"`
}

// AuditLogEntry is the public projection of `routing_audit_log`.
// It is also the response shape for `GET /route-incidents/{id}/audit`.
type AuditLogEntry struct {
	ID              int64          `json:"id"`
	IncidentID      *string        `json:"incident_id,omitempty"`
	TenantID        string         `json:"tenant_id"`
	Action          ActionKind     `json:"action"`
	Actor           string         `json:"actor"`
	Reason          string         `json:"reason"`
	IdempotencyKey  string         `json:"idempotency_key"`
	Outcome         ActionOutcome  `json:"outcome"`
	FailureReason   *string        `json:"failure_reason,omitempty"`
	DiagnosticRunID *string        `json:"diagnostic_run_id,omitempty"`
	PreSnapshot     map[string]any `json:"pre_snapshot"`
	PostSnapshot    map[string]any `json:"post_snapshot"`
	ResponsePayload map[string]any `json:"response_payload"`
	CreatedAt       time.Time      `json:"created_at"`
}

// ActionRequest is the body of every mutating POST endpoint.
// The shape is identical for all 5 actions + 2 tests so the
// dashboard's confirm-modal can be reused.
type ActionRequest struct {
	// Reason is operator-supplied free text, length-bounded in the
	// store layer (max 256 chars). Required for mutating actions.
	Reason string `json:"reason"`

	// ConfirmationToken is a short-lived (≤5 min) one-time token
	// the operator retrieves from the confirm modal. The store
	// hashes it with SHA-256 and the original is never written to
	// disk. Required for mutating actions; ignored for
	// `evidence_export`.
	ConfirmationToken string `json:"confirmation_token,omitempty"`

	// IdempotencyKey is unique per logical action execution. The
	// caller SHOULD generate this client-side (UUID v4). The store
	// rejects duplicates on the unique index.
	IdempotencyKey string `json:"idempotency_key"`

	// Action-specific parameters, allow-listed in the action
	// dispatcher. The dispatcher drops unknown keys.
	Parameters map[string]any `json:"parameters,omitempty"`
}

// ActionResponse is the body of every action endpoint.
type ActionResponse struct {
	AuditID       int64          `json:"audit_id"`
	Outcome       ActionOutcome  `json:"outcome"`
	FailureReason *string        `json:"failure_reason,omitempty"`
	DiagnosticRun *DiagnosticRun `json:"diagnostic_run,omitempty"`
	Incident      *Incident      `json:"incident,omitempty"`
	Response      map[string]any `json:"response,omitempty"`
	Idempotent    bool           `json:"idempotent"` // true when the request was a retry of an already-executed key
}

// EvidenceExport is the response shape for `GET .../export?run_id=...`.
// It is the union of (a) the completed diagnostic run and (b) a
// small set of fixed, allow-listed fields from the incident,
// route_incident_events, and request_logs.
type EvidenceExport struct {
	Run         DiagnosticRun           `json:"run"`
	Incident    Incident                `json:"incident"`
	Events      []IncidentEvent         `json:"events"`
	Timeline    []IncidentTimelinePoint `json:"timeline"`
	Integrity   IntegrityChecksum       `json:"integrity"`
	GeneratedAt time.Time               `json:"generated_at"`
	Exporter    string                  `json:"exporter"` // super-admin user id
}

// IntegrityChecksum is a SHA-256 over the canonicalized
// (Run, Incident, Events, Timeline) JSON. Lets the consumer
// detect tampering after download.
type IntegrityChecksum struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
}
