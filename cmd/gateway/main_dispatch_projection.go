package main

import (
	"sync"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
)

type queueProjectionHolder struct {
	mu         sync.RWMutex
	projection *dispatch.QueueProjection
}

func (h *queueProjectionHolder) Store(projection *dispatch.QueueProjection) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.projection = projection
	h.mu.Unlock()
}

func (h *queueProjectionHolder) Load() *dispatch.QueueProjection {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	projection := h.projection
	h.mu.RUnlock()
	return projection
}

func (h *queueProjectionHolder) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	projection := h.projection
	h.projection = nil
	h.mu.Unlock()
	if projection != nil {
		projection.Close()
	}
}

func (h *queueProjectionHolder) Snapshot() *dispatch.SnapshotView {
	projection := h.Load()
	if projection == nil {
		return unwiredQueueProjectionSnapshot()
	}
	return projection.Snapshot()
}

func (h *queueProjectionHolder) SnapshotWaterfall(limit int, model string, credentialID int, tenantID string) dispatch.WaterfallSnapshot {
	projection := h.Load()
	if projection == nil {
		return unwiredQueueWaterfallSnapshot()
	}
	return projection.SnapshotWaterfall(limit, model, credentialID, tenantID)
}

func unwiredQueueProjectionSnapshot() *dispatch.SnapshotView {
	return &dispatch.SnapshotView{Enabled: true, Wired: false, Models: []dispatch.LaneView{}, Credentials: []dispatch.LaneView{}}
}

func unwiredQueueWaterfallSnapshot() dispatch.WaterfallSnapshot {
	return dispatch.WaterfallSnapshot{
		Requests: []dispatch.WaterfallRequest{},
		Enabled:  true,
		Wired:    false,
		BottleneckDiagnosis: dispatch.BottleneckDiagnosis{
			Bottleneck: "none",
			Message:    "dispatch queue projection not wired",
		},
	}
}

func gatewayQueueProjectionSnapshot() *dispatch.SnapshotView {
	return gatewayQueueProjection.Snapshot()
}
