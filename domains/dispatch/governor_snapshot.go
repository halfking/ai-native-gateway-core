package dispatch

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
//   - InFlight: forwarder.CurrentDepth()-style count.
//   - QueueDepth: forwarder.CurrentDepth() snapshot.
//   - State: see SnapshotState constants.
//   - AgeMS: milliseconds since the underlying source state changed;
//     Stage C will define a max-age budget and drop stale entries.
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