package dispatch

import (
	"sync"
	"sync/atomic"
)

// QueueMetricsCollector provides non-blocking queue snapshot collection for the
// admin SSE live-stream. It hooks into the dispatch pipeline via the
// QueueMetricsHook interface and maintains thread-safe in-memory state.
//
// Design principles (V3.2-LP3):
//   - Non-blocking: hooks must not slow down relay path
//   - Thread-safe: RWMutex for snapshot read/write
//   - Degraded mode: returns Wired=false on initialization failure
//   - Zero allocation on hot path: atomic counters, pre-allocated maps
//
// Wire flow:
//  1. cmd/gateway creates collector via NewQueueMetricsCollector(pipeline)
//  2. cmd/gateway injects collector.Snapshot into hub.SetQueueSnapshotProvider
//  3. Pipeline calls collector.RecordEnqueue/RecordDequeue on queue operations
//  4. Admin SSE reads collector.Snapshot() for periodic queue_snapshot events
type QueueMetricsCollector struct {
	// pipeline is the dispatch.Pipeline instance being monitored.
	// Used to read live queue depths via Pipeline.Snapshot().
	pipeline *Pipeline

	// wired indicates whether the collector successfully initialized.
	// false means degraded mode (returns empty snapshot with Wired=false).
	wired atomic.Bool

	// enabled mirrors dispatch.IsDispatchEnabled() at snapshot time.
	// Cached to avoid redundant gate reads in hot path.
	enabled atomic.Bool

	// sourceVersion (V3.3-OBS OBS-BE3) increments on every wired+enabled
	// snapshot so SSE clients can detect stale/out-of-order queue_snapshot
	// envelopes. Monotonic per collector; omitted (0) while degraded or
	// disabled.
	sourceVersion atomic.Int64

	// lastOverflowTotal is the last-seen cumulative dispatch_overflow_total.
	// Diffing against the current reading yields the "overflow since last
	// snapshot" degraded flag (queue_metrics_pipeline.go). Overflow counters
	// are Inc-only, so the diff is monotone-safe. The first tick of a fresh
	// collector only latches the baseline (the counter is process-global and
	// may already be non-zero from earlier traffic) and never reports
	// degraded from pre-existing history.
	lastOverflowTotal    atomic.Int64
	overflowBaselineSeen atomic.Bool

	// mu protects the lane maps during concurrent updates from hooks.
	mu sync.RWMutex

	// modelLanes tracks Tier-1 model queue lanes.
	// Key: model name, Value: snapshot state.
	modelLanes map[string]*laneState

	// credLanes tracks Tier-2 credential queue lanes.
	// Key: credential ID, Value: snapshot state.
	credLanes map[int]*laneState
}

// laneState holds the mutable state for one queue lane (model or credential).
// Protected by QueueMetricsCollector.mu.
type laneState struct {
	// For model lanes: model name
	model string
	// For credential lanes: credential ID
	credentialID int
	// Concurrency mode (e.g., "concurrency", "rpm", "tpm", "disabled")
	mode string
	// Current depth (populated from Pipeline.Snapshot())
	depth int64
}

// NewQueueMetricsCollector constructs a collector for the given pipeline.
// Returns a collector in degraded mode (Wired=false) if pipeline is nil.
func NewQueueMetricsCollector(pipeline *Pipeline) *QueueMetricsCollector {
	c := &QueueMetricsCollector{
		pipeline:   pipeline,
		modelLanes: make(map[string]*laneState),
		credLanes:  make(map[int]*laneState),
	}
	if pipeline == nil {
		// Degraded: cannot collect without pipeline reference
		c.wired.Store(false)
		return c
	}
	c.wired.Store(true)
	c.enabled.Store(IsDispatchEnabled())

	// Register a transition handler to track dispatch gate changes
	RegisterTransitionHandler(func(enabled bool) {
		c.enabled.Store(enabled)
	})

	return c
}

// Snapshot returns the current queue snapshot for admin SSE consumption.
// This is the func() *SnapshotView that gets injected into hub.
//
// Returns:
//   - Enabled: current dispatch gate state
//   - Wired: whether collector initialized successfully
//   - Models: Tier-1 model queue lanes (model name + depth)
//   - Credentials: Tier-2 credential queue lanes (cred ID + mode + depth)
//
// Thread-safe: reads under RWMutex.RLock(); does not block enqueue/dequeue.
func (c *QueueMetricsCollector) Snapshot() *SnapshotView {
	// Return degraded snapshot if not wired
	if !c.wired.Load() {
		return &SnapshotView{
			Enabled: false,
			Wired:   false,
		}
	}

	// Capture current gate state
	enabled := c.enabled.Load()

	// Read live queue depths from pipeline
	modelSnaps, credSnaps := c.pipeline.Snapshot()

	// Build model lanes slice
	c.mu.RLock()
	models := make([]LaneView, 0, len(modelSnaps))
	for _, ms := range modelSnaps {
		models = append(models, LaneView{
			Model: ms.Model,
			Depth: ms.Depth,
		})
	}

	// Build credential lanes slice
	credentials := make([]LaneView, 0, len(credSnaps))
	for _, cs := range credSnaps {
		credentials = append(credentials, LaneView{
			Credential: cs.Credential,
			Mode:       cs.Mode,
			Depth:      cs.Depth,
		})
	}
	c.mu.RUnlock()

	view := &SnapshotView{
		Enabled:     enabled,
		Wired:       true,
		Models:      models,
		Credentials: credentials,
	}

	// V3.3-OBS OBS-BE3: pipeline aggregate view + monotonic sourceVersion.
	// Only when the dispatch gate is enabled — a disabled pipeline carries
	// no live traffic and its queue stats would be stale, so the fields stay
	// absent (omitempty) instead of masquerading as current values.
	if enabled {
		overflowNow := int64(overflowVecTotal())
		overflowSince := false
		if c.overflowBaselineSeen.CompareAndSwap(false, true) {
			// First tick: latch the baseline only.
			c.lastOverflowTotal.Store(overflowNow)
		} else {
			overflowSince = overflowNow > c.lastOverflowTotal.Load()
			c.lastOverflowTotal.Store(overflowNow)
		}
		view.SourceVersion = c.sourceVersion.Add(1)
		view.Pipeline = c.pipeline.pipelineQueueStats(overflowSince)
	}

	return view
}

// RecordEnqueue is a hook called when a request enters a queue (model or credential).
// Non-blocking: updates lane state under write lock.
//
// Parameters:
//   - model: model name (empty for credential-only lanes)
//   - credentialID: credential ID (0 for model-only lanes)
//   - mode: concurrency mode (empty for model lanes)
func (c *QueueMetricsCollector) RecordEnqueue(model string, credentialID int, mode string) {
	if !c.wired.Load() {
		return // Degraded mode: no-op
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if model != "" && credentialID == 0 {
		// Model lane enqueue
		if _, exists := c.modelLanes[model]; !exists {
			c.modelLanes[model] = &laneState{model: model}
		}
	} else if credentialID > 0 {
		// Credential lane enqueue
		if _, exists := c.credLanes[credentialID]; !exists {
			c.credLanes[credentialID] = &laneState{
				credentialID: credentialID,
				mode:         mode,
			}
		}
	}
}

// RecordDequeue is a hook called when a request leaves a queue.
// Non-blocking: currently a no-op (depth is read from Pipeline.Snapshot()).
//
// Future: could track wait time percentiles here if needed.
func (c *QueueMetricsCollector) RecordDequeue(model string, credentialID int) {
	// No-op: depth is authoritative from Pipeline.Snapshot()
	// Wait time tracking (p50/p95) would go here in future iterations
}

// SnapshotView is the dispatch-native queue snapshot shape for admin SSE.
// Kept in dispatch package to avoid admin → dispatch import cycle.
// cmd/gateway converts SnapshotView → admin.LiveQueueSnapshot at the boundary.
//
// Pipeline (V3.3-OBS OBS-BE3) and SourceVersion are optional: present only
// when the collector is wired AND the dispatch gate is enabled. A present
// Depth/InFlight of 0 is a real zero (queues empty), never a placeholder.
type SnapshotView struct {
	Enabled       bool                `json:"enabled"`
	Wired         bool                `json:"wired"`
	SourceVersion int64               `json:"sourceVersion,omitempty"`
	Pipeline      *PipelineQueueStats `json:"pipeline,omitempty"`
	Models        []LaneView          `json:"models"`
	Credentials   []LaneView          `json:"credentials"`
}

// LaneView is one queue lane (model or credential).
type LaneView struct {
	Model      string `json:"model,omitempty"`
	Credential int    `json:"credential,omitempty"`
	Mode       string `json:"mode,omitempty"`
	Depth      int64  `json:"depth"`
}
