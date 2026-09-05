// Unified Auto-Orchestration Plugin — Workflow C (Observability & Audit Log).
//
// audit.go implements the audit-event primitive described in the unified
// design contract §9.2:
//
//	"Every decision and action records: event_id, type, timestamp, tenant,
//	 goal_run/session/request/task, causation/correlation, attempt/fencing
//	 token, policy version, reason code, redacted payload hash."
//
// Contract guarantees enforced here:
//   - Append-only, ordered. The MemorySink preserves insertion order and is
//     safe for concurrent appenders so the audit log can be a shared
//     coordination point between request-path and worker-path writers.
//   - No raw payloads. AuditEvent carries PayloadHash only; reflective tests
//     forbid adding fields whose names imply raw credentials or tokens.
//   - Sink errors surface. A failing sink returns an error to the caller;
//     callers decide whether to mark `audit_degraded` (design §9.2).
package observ

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// Audit event type constants. Kept narrow: the §9.2 design surfaces these
// categories through reason codes, but the Type field is a coarse classifier
// for dashboards and routing.
const (
	EventDecision        = "decision"
	EventAction          = "action"
	EventStateTransition = "state_transition"
	EventAuditDegraded   = "audit_degraded"
)

// AuditEvent is the canonical record shape for every orchestrator decision
// and action. Fields mirror design §9.2. IDs (EventID, GoalRun, Session,
// Request, Task, CausationID, CorrelationID, TraceID, FencingToken) are kept
// out of high-cardinality Prometheus labels — see metrics.go.
type AuditEvent struct {
	EventID       string    `json:"event_id"`
	Type          string    `json:"type"`
	Timestamp     time.Time `json:"timestamp"`
	Tenant        string    `json:"tenant"`
	GoalRun       string    `json:"goal_run,omitempty"`
	Session       string    `json:"session,omitempty"`
	Request       string    `json:"request,omitempty"`
	Task          string    `json:"task,omitempty"`
	CausationID   string    `json:"causation_id,omitempty"`
	CorrelationID string    `json:"correlation_id,omitempty"`
	TraceID       string    `json:"trace_id,omitempty"`
	Attempt       int       `json:"attempt,omitempty"`
	FencingToken  string    `json:"fencing_token,omitempty"`
	PolicyVersion string    `json:"policy_version,omitempty"`
	ReasonCode    string    `json:"reason_code,omitempty"`
	PayloadHash   string    `json:"payload_hash,omitempty"`
}

// Sink is the persistence boundary for audit events. Implementations MUST be
// append-only and MUST return any write error so callers can mark the run
// audit_degraded per design §9.2.
type Sink interface {
	Append(ctx context.Context, e AuditEvent) error
}

// MemorySink is the in-process Sink used by tests and the local
// LOCAL_VERIFIED tier. It is concurrency-safe and preserves insertion order.
// It is NOT durable; production deployments wire a Sink that forwards to
// analysis_events or equivalent durable storage (deferred to Wave 6).
type MemorySink struct {
	mu     sync.Mutex
	events []AuditEvent
}

// NewMemorySink returns an empty MemorySink.
func NewMemorySink() *MemorySink {
	return &MemorySink{}
}

// Append records e in arrival order. Returns nil for the in-memory sink;
// real sinks should surface persistence errors.
func (m *MemorySink) Append(_ context.Context, e AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
	return nil
}

// Snapshot returns a copy of all recorded events. Safe to call concurrently.
func (m *MemorySink) Snapshot() []AuditEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AuditEvent, len(m.events))
	copy(out, m.events)
	return out
}

// Len reports the number of recorded events without copying.
func (m *MemorySink) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.events)
}

// Auditor is the orchestration-side façade that producers (plugin bindings,
// GoalRun schedulers, recovery workers, etc.) call to emit audit events.
// It is intentionally tiny: it owns no policy and makes no decision about
// whether an event "matters" — that judgement belongs to the caller, which
// fills the §9.2 fields itself.
type Auditor struct {
	sink Sink
}

// NewAuditor wires an Auditor to a Sink. The Sink is the only state; passing
// a fresh MemorySink per test keeps tests hermetic.
func NewAuditor(s Sink) *Auditor {
	return &Auditor{sink: s}
}

// Record forwards e to the configured Sink. Errors are returned so callers
// can emit an `audit_degraded` follow-up event (per design §9.2 audit
// degraded handling).
func (a *Auditor) Record(ctx context.Context, e AuditEvent) error {
	if e.EventID == "" {
		return errMissingEventID
	}
	if e.Type == "" {
		return errMissingType
	}
	return a.sink.Append(ctx, e)
}

// HashPayload computes the sha256 hex digest used as PayloadHash. Callers
// MUST hash any sensitive decision/action body before storing; raw bytes
// must never reach the AuditEvent struct.
func HashPayload(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// NewEventID returns a 32-character hex event id (128 bits of crypto-random
// entropy). Collisions are statistically negligible at the scale of any
// single orchestrator run, and the test suite guards against degenerate
// RNG via TestNewEventID_UniqueAndNonEmpty.
func NewEventID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is exceptional on Linux/macOS; fall back to a
		// time-derived id so the call site still gets a non-empty value and
		// surfaces the issue via the returned id rather than a panic.
		return "fallback-" + timeNowNano()
	}
	return hex.EncodeToString(b[:])
}

// Sentinel errors returned by Auditor.Record when mandatory fields are
// missing. They are unexported because the only consumer is the caller of
// Record, which is expected to validate inputs at the construction site.
var (
	errMissingEventID = errAudit("event_id is required")
	errMissingType    = errAudit("type is required")
)

type errAudit string

func (e errAudit) Error() string { return string(e) }

// timeNowNano returns a fallback id suffix using the wall clock. crypto/rand
// failures are exceptional on Linux/macOS, but if they happen we still want
// a non-empty id so the caller surfaces the issue via the id rather than a
// panic.
func timeNowNano() string {
	return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
}
