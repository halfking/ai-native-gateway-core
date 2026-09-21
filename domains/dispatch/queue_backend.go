package dispatch

import (
	"context"
	"sync/atomic"
	"time"
)

// QueueBackend (V6-W1.7, docs/架构优化v6/10-dual-backend-queue.md) is the
// dual-mode abstraction for the CLUSTER-side queue primitives — Tier-0
// waiting-room admission, Tier-1/Tier-2 lane capacity and due-parked
// visibility (memory | Redis). It reuses the GovernorBackend composition
// pattern: env-selected, falling back to local when Redis is unavailable.
//
// CONNECTION AFFINITY (non-negotiable): QueuedRequest carries goroutines,
// the client connection and ResultCh — it is NOT serializable. The backend
// NEVER moves execution between instances; Redis only makes admission,
// capacity and due-parking visible cluster-wide. Cross-instance work
// stealing stays with the durable lane (PG, off by default).
//
// Degradation direction (deliberately the OPPOSITE of the Governor):
// admission/capacity is fail-open — on Redis failure the backend degrades to
// per-instance local bounds so requests keep flowing; the Governor stays
// fail-closed because losing rate-limit fidelity would breach supplier
// limits. Availability over suppliers here, suppliers over availability
// there. Do not unify.

// QueueBackendKind names the concrete backend implementation.
type QueueBackendKind string

const (
	QueueBackendLocal QueueBackendKind = "local"
	QueueBackendRedis QueueBackendKind = "redis"
)

// LaneKind identifies the capacity dimension of an admission. LaneTotal is
// the Tier-0 waiting room; model/credential are the Tier-1/Tier-2 lanes.
type LaneKind string

const (
	LaneTotal      LaneKind = "total"
	LaneModel      LaneKind = "model"
	LaneCredential LaneKind = "credential"
)

// Admission is a cluster-capacity token returned by the Try* calls. The
// zero value (Kind == "") means "no cluster accounting" — the local backend
// returns it and Release is a no-op, so the in-process primitives remain the
// sole admission authority (bit-equivalence with the pre-W1.7 behavior).
type Admission struct {
	Kind LaneKind
	// ID is the lane identifier (model key / credential id); empty for total.
	ID string
	// Key is the backend-owned accounting key, opaque to the pipeline and
	// only meaningful to the backend that minted the token.
	Key string
}

// QueueBackendStats is the ops snapshot: this instance's held admissions and
// (redis) the cluster occupancy.
type QueueBackendStats struct {
	Kind         QueueBackendKind
	InstanceID   string
	TotalHeld    int64
	ModelHeld    int64
	CredHeld     int64
	ClusterTotal int64
	Degraded     bool
}

// QueueBackend is safe for concurrent use. Implementations must tolerate
// Open/Close idempotently.
type QueueBackend interface {
	// Kind is the closed-enum identifier for metrics and logs.
	Kind() QueueBackendKind
	// Open initializes the backend (redis: starts the heartbeat loop).
	Open(ctx context.Context) error
	// Close stops background work. Idempotent.
	Close() error
	// TryAdmitTotal admits one request into the cluster-wide Tier-0 waiting
	// room (cap passed by the caller from live config). false = the cluster
	// waiting room is full → the existing OverflowError path.
	TryAdmitTotal(ctx context.Context, qr *QueuedRequest, cap int) (Admission, bool)
	// TryReserveLane reserves one slot of the cluster-wide lane capacity
	// (kind=model|credential, id = lane key, cap from live config). false →
	// the existing "lane full" paths (switch credential / capacity wait).
	TryReserveLane(ctx context.Context, kind LaneKind, id string, cap int) (Admission, bool)
	// Release returns an admission. Idempotent per token; the pipeline
	// additionally guards with a take-once swap on the request.
	Release(a Admission)
	// ParkDue mirrors a due-parked request into the cluster due view
	// (redis: due ZSET). Observation only — pickup stays with the local
	// promoter (the request object lives on this instance).
	ParkDue(ctx context.Context, requestID string, dueAt time.Time) error
	// ClearDue removes the request from the cluster due view (due pickup or
	// terminal).
	ClearDue(ctx context.Context, requestID string) error
	// Heartbeat refreshes instance liveness (redis: heartbeat fields + due
	// residue sweep). Local: no-op.
	Heartbeat(ctx context.Context) error
	// Snapshot reports instance + cluster occupancy for ops.
	Snapshot(ctx context.Context) (QueueBackendStats, error)
}

// localQueueBackend is the default, zero-dependency backend: admission is a
// pass-through because the in-process primitives (totalQueue CAS, lane
// channel capacities) already ARE the local admission. Wiring it changes no
// behavior — the U1 equivalence gate (full dispatch suite, zero test
// modifications) pins this. Local occupancy stays observable through the
// queue projection (GET /api/admin/dispatch/queues), so Snapshot reports
// counters only.
type localQueueBackend struct {
	closed atomic.Bool
}

// NewLocalQueueBackend returns the pass-through local backend.
func NewLocalQueueBackend() QueueBackend { return &localQueueBackend{} }

func (b *localQueueBackend) Kind() QueueBackendKind { return QueueBackendLocal }

func (b *localQueueBackend) Open(ctx context.Context) error { return nil }
func (b *localQueueBackend) Close() error                   { b.closed.Store(true); return nil }
func (b *localQueueBackend) TryAdmitTotal(ctx context.Context, qr *QueuedRequest, cap int) (Admission, bool) {
	return Admission{}, true
}
func (b *localQueueBackend) TryReserveLane(ctx context.Context, kind LaneKind, id string, cap int) (Admission, bool) {
	return Admission{}, true
}
func (b *localQueueBackend) Release(a Admission)                                               {}
func (b *localQueueBackend) ParkDue(ctx context.Context, requestID string, dueAt time.Time) error { return nil }
func (b *localQueueBackend) ClearDue(ctx context.Context, requestID string) error                { return nil }
func (b *localQueueBackend) Heartbeat(ctx context.Context) error                                 { return nil }
func (b *localQueueBackend) Snapshot(ctx context.Context) (QueueBackendStats, error) {
	return QueueBackendStats{Kind: QueueBackendLocal}, nil
}
