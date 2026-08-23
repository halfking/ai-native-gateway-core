// Command gateway - main_v32_wiring.go (2026-08-14)
//
// V3.2 backend wire helpers: SSE providers. Only `wireQueueSnapshotProvider`
// remains; the node_status provider was retired by 4478b311f+1 (2026-08-24) —
// the live cache path in main_livestream.go now owns the credential/provider
// projection, and the unused closure (which re-ran an uncached SQL on every
// SSE tick without a peak-collector input) is gone.
//
// The V3.2 StateTransitionLogger wiring was retired by B3-PR1 (2026-08-17):
// its two event producers now write through the requestjourney recorder, and
// the request_state_transitions retention duty moved to
// requestjourney.RetentionWorker (wired in main.go).

package main

import (
	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
)

// wireQueueSnapshotProvider wraps the independent QueueProjection in the
// QueueMetricsCollector and returns the closure handed to LiveStreamSSEHub
// for V3.2 BE-A1 (V32-LP3). The collector only reads the projection, so the
// admin SSE path never locks or copies from the dispatch Pipeline.
// Wired: false when projection is nil (pre-deployment / test mode).
func wireQueueSnapshotProvider(projection *dispatch.QueueProjection) func() *admin.LiveQueueSnapshot {
	// Create collector over the projection; admin/SSE never locks Pipeline.
	collector := dispatch.NewQueueMetricsCollector(projection)

	// Return closure that converts dispatch.SnapshotView → admin.LiveQueueSnapshot
	return func() *admin.LiveQueueSnapshot {
		snap := collector.Snapshot()
		if snap == nil {
			// Defensive: should never happen per collector contract
			return &admin.LiveQueueSnapshot{Enabled: false, Wired: false}
		}

		out := &admin.LiveQueueSnapshot{
			Enabled:       snap.Enabled,
			Wired:         snap.Wired,
			SourceVersion: snap.SourceVersion,
			Models:        make([]admin.LiveQueueLaneSnapshot, 0, len(snap.Models)),
			Credentials:   make([]admin.LiveQueueLaneSnapshot, 0, len(snap.Credentials)),
		}

		// V3.3-OBS OBS-BE3: pipeline aggregate view. Present only when the
		// collector produced it (wired + dispatch gate enabled); nil keeps
		// the JSON field absent instead of a zero-value placeholder.
		if snap.Pipeline != nil {
			out.Pipeline = &admin.LiveQueuePipelineStats{
				Depth:        snap.Pipeline.Depth,
				WaitingMsP50: snap.Pipeline.WaitingMsP50,
				WaitingMsP95: snap.Pipeline.WaitingMsP95,
				InFlight:     snap.Pipeline.InFlight,
				Degraded:     snap.Pipeline.Degraded,
			}
		}

		for _, m := range snap.Models {
			out.Models = append(out.Models, admin.LiveQueueLaneSnapshot{
				Model: m.Model,
				Depth: m.Depth,
			})
		}

		for _, c := range snap.Credentials {
			out.Credentials = append(out.Credentials, admin.LiveQueueLaneSnapshot{
				Credential: c.Credential,
				Mode:       c.Mode,
				Depth:      c.Depth,
			})
		}

		return out
	}
}
