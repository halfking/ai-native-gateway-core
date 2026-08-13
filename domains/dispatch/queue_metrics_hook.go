// Package dispatch — queue metrics hook (V3.2 BE-A1 / V32-LP3).
//
// This file defines the two hook surfaces the queue metrics collector
// exposes to its consumers:
//
//  1. QueueObserver — optional pipeline-side middleware hook that observes
//     enqueue/dequeue events on Tier-1 (model) and Tier-2 (credential)
//     queues. Defined for forward-compatibility; the current Pipeline does
//     NOT wire it (it uses the pull-based Pipeline.Snapshot() pattern in
//     snapshot.go, which is sufficient for V32-LP3's depth-only contract).
//     Tests and observability tools can subscribe via QueueObserver without
//     modifying the pipeline.
//
//  2. NewQueueSnapshotHook — the SSE-side producer closure that the admin
//     live-stream hub consumes (via LiveStreamSSEHub.SetQueueSnapshotProvider
//     in admin/live_stream_sse.go:419). The closure returns the collector's
//     dispatch-native SnapshotView; the cmd/gateway wiring in
//     main_v32_wiring.go converts SnapshotView → admin.LiveQueueSnapshot
//     at the package boundary (dispatch cannot import admin — see
//     live_stream_sse.go:92 comment for the cycle rationale).
//
// Splitting the "observer interface" and "SSE hook closure" into one file
// keeps all V3.2 producer-side hook concerns together and reviewable
// without touching the unrelated pipeline enqueue/dequeue hot paths.
package dispatch

// QueueObserver is the optional pipeline-side middleware hook that observes
// dispatch's Tier-1 (model) and Tier-2 (credential) queue events. The
// Pipeline does not currently wire it (see snapshot.go for the pull-based
// Snapshot() pattern that V32-LP3 uses); the interface is exported for
// forward-compatibility and so observability tooling can subscribe to
// events without polling.
//
// Future wiring (rule 09 §5.2 — comment before extending): when richer
// metrics (waiting_ms_p50/p95, in_flight — see docs/会话优化v3/13-V3.2
// 实际API与SSE契约.md §4) need push semantics, add a one-line
// `pipeline.observer = observer` field to pipeline.go and call observer
// methods at the existing enqueue/dequeue sites. The current
// depth-snapshot contract does NOT require it.
type QueueObserver interface {
	OnModelEnqueue(model string)
	OnModelDequeue(model string)
	OnCredEnqueue(credID int, mode string)
	OnCredDequeue(credID int, mode string)
}

// NoopObserver is the safe default no-op observer. Use when a hook is
// required but the consumer has no interest in event-level detail (e.g.
// background telemetry that only cares about periodic snapshots).
type NoopObserver struct{}

// OnModelEnqueue is a no-op (see NoopObserver doc).
func (NoopObserver) OnModelEnqueue(string) {}

// OnModelDequeue is a no-op (see NoopObserver doc).
func (NoopObserver) OnModelDequeue(string) {}

// OnCredEnqueue is a no-op (see NoopObserver doc).
func (NoopObserver) OnCredEnqueue(int, string) {}

// OnCredDequeue is a no-op (see NoopObserver doc).
func (NoopObserver) OnCredDequeue(int, string) {}

// QueueSnapshotHook is the SSE-side producer hook the admin live-stream
// hub consumes. The return type uses dispatch-native SnapshotView (not
// admin.LiveQueueSnapshot) because dispatch cannot import admin — the
// conversion happens in cmd/gateway/main_v32_wiring.go, which can import
// both packages.
//
// The returned SnapshotView is a fresh pointer on every call; callers
// (admin's hub) treat it as ephemeral and may mutate/scratch it without
// affecting subsequent ticks.
type QueueSnapshotHook func() *SnapshotView

// NewQueueSnapshotHook returns the SSE-side hook closure backed by the
// given collector. Pass nil to get a permanently-degraded hook that
// returns Wired=false on every call (matches the pre-LP3 wireQueueSnapshot
// Provider nil-pipeline behavior at main_v32_wiring.go:45).
//
// The returned closure is safe to call from multiple SSE-tick goroutines
// concurrently; the underlying collector uses atomic.Pointer and atomic.
// Int64 throughout (no shared RWMutex, no map writes).
func NewQueueSnapshotHook(c *QueueMetricsCollector) QueueSnapshotHook {
	if c == nil {
		return func() *SnapshotView {
			return &SnapshotView{Wired: false}
		}
	}
	return func() *SnapshotView {
		return c.Snapshot()
	}
}
