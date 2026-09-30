package dispatch

import "fmt"

// SnapshotState is the closed enum for GovernorSnapshot.State. Used as a
// Prometheus label in Stage C; keep the set tightly bounded and
// append-only. Renaming or removing an existing value is a coordinate
// break for downstream dashboards/alerts.
type SnapshotState string

const (
	// SnapshotStateReady indicates the governor has capacity to admit a
	// new request (Used < Limit, queue not pinned).
	SnapshotStateReady SnapshotState = "ready"
	// SnapshotStateQueueFull indicates the credential's Tier-2 queue is
	// at capacity (forwarder.depth >= forwarder.limit). Acquire would
	// either time out (queueWaitBudget) or overflow.
	SnapshotStateQueueFull SnapshotState = "queue_full"
	// SnapshotStateGovernorSaturated indicates the governor itself
	// (concurrent in-flight cap, or rpm/tpm budget) is exhausted. Queue
	// may have room, but admission will block until tokens free up.
	SnapshotStateGovernorSaturated SnapshotState = "governor_saturated"
	// SnapshotStateUnknown is the only valid state when BackendErr is
	// non-nil; it tells Stage C metrics to drop the observation and the
	// forwarder to surface ErrGovernorUnavailable to the capacity-wait
	// ladder.
	SnapshotStateUnknown SnapshotState = "unknown"
)

// GovernorSnapshot is a read-only, point-in-time projection of a single
// per-credential governor's admission state. The forwarder (or a polling
// observer owned by the management layer) fills it; Stage C metrics
// observe it; Stage D capacity-aware sort consumes it via the snap API.
//
// Field semantics:
//
//   - SpecRevision: the GovernorSpec.Revision under which the snapshot
//     was produced; consumers compare it to the live active revision to
//     drop stale observations.
//   - Backend / Mode: closed-enum strings (see GovernorBackendKind and
//     the four Mode* values in queued_request.go). Stage C label
//     allowlist must include both sets.
//   - Limit: GovernorSpec.Limit at snapshot time.
//   - Used: current consumers (in-flight for ModeConcurrency; tokens
//     floor for ModeRPM / ModeTPM).
//   - InFlight / QueueDepth: currently filled from the SAME source —
//     pipeline.snapshotForCredForwarderLocked assigns both from one
//     cf.depth.Load() read (pipeline.go, snapshot fill), so the two fields
//     are always equal; the forwarder has no separate Tier-2 wait-depth
//     counter today. Distinct semantics would require a second counter
//     (registered in R73 §5).
//   - State: see SnapshotState constants.
//   - AgeMS: currently the milliseconds elapsed since the observer's
//     PREVIOUS visit to this credential (pipeline.go: delta from the last
//     snapshotAgeMS stamp; 0 on the first visit) — so it always tracks the
//     observer tick period, not any backend state-change time. The
//     "age of the underlying source state" reading and the max-age budget
//     mentioned below are NOT implemented (registered in R73 §5 / §3 #10).
//   - BackendErr: non-nil ONLY when the snapshot could not be filled
//     from the backend (e.g. Redis DOWN). When non-nil, State MUST be
//     SnapshotStateUnknown and metric consumers MUST drop the row.
type GovernorSnapshot struct {
	SpecRevision uint64
	Backend      string
	Mode         string
	Limit        int
	Used         int
	InFlight     int
	QueueDepth   int
	State        SnapshotState
	AgeMS        int64
	BackendErr   error
}

// Validate enforces the bidirectional invariant:
//
//	State == SnapshotStateUnknown ⇔ BackendErr != nil
//
// Stage C metrics consumers rely on this to drop unknown rows and to
// trust that State=Ready/QueueFull/GovernorSaturated never masks a
// backend fault. Production call sites that fill a GovernorSnapshot MUST
// call Validate() before returning the snapshot, so a backend bug
// surfaces immediately rather than silently feeding wrong state to
// dashboards and to the Stage D capacity-aware sort.
//
// On violation Validate panics — there is no graceful path because a
// contradictory snapshot has no safe consumer behavior.
func (s GovernorSnapshot) Validate() error {
	if s.State == SnapshotStateUnknown && s.BackendErr == nil {
		panic("dispatch: GovernorSnapshot.State=SnapshotStateUnknown without BackendErr")
	}
	if s.State != SnapshotStateUnknown && s.BackendErr != nil {
		panic(fmt.Sprintf("dispatch: GovernorSnapshot.State=%q with non-nil BackendErr=%v", s.State, s.BackendErr))
	}
	return nil
}
