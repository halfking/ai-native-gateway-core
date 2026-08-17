package dispatch

// QueueMetricsCollector provides the admin SSE hook over the independent queue
// projection. It never reads Pipeline state or locks.
type QueueMetricsCollector struct {
	projection *QueueProjection
}

// NewQueueMetricsCollector constructs a collector for a QueueProjection. A nil
// projection returns degraded snapshots while preserving the dispatch gate.
func NewQueueMetricsCollector(projection *QueueProjection) *QueueMetricsCollector {
	return &QueueMetricsCollector{projection: projection}
}

// Snapshot returns the current queue snapshot for admin SSE consumption.
func (c *QueueMetricsCollector) Snapshot() *SnapshotView {
	if c == nil || c.projection == nil {
		return &SnapshotView{Enabled: IsDispatchEnabled(), Wired: false, Models: []LaneView{}, Credentials: []LaneView{}}
	}
	return c.projection.Snapshot()
}

// RecordEnqueue applies the legacy observer hook to the projection.
func (c *QueueMetricsCollector) RecordEnqueue(model string, credentialID int, mode string) {
	if c == nil || c.projection == nil {
		return
	}
	if model != "" && credentialID == 0 {
		c.projection.ObserveQueue(QueueObservation{Kind: QueueModelDepth, Model: model, Delta: 1})
		return
	}
	if credentialID > 0 {
		c.projection.ObserveQueue(QueueObservation{Kind: QueueCredentialDepth, CredentialID: credentialID, Mode: mode, Delta: 1})
	}
}

// RecordDequeue applies the legacy observer hook to the projection.
func (c *QueueMetricsCollector) RecordDequeue(model string, credentialID int) {
	if c == nil || c.projection == nil {
		return
	}
	if model != "" && credentialID == 0 {
		c.projection.ObserveQueue(QueueObservation{Kind: QueueModelDepth, Model: model, Delta: -1})
		return
	}
	if credentialID > 0 {
		c.projection.ObserveQueue(QueueObservation{Kind: QueueCredentialDepth, CredentialID: credentialID, Delta: -1})
	}
}

// SnapshotView is the dispatch-native queue snapshot shape for admin SSE.
// cmd/gateway converts it to admin.LiveQueueSnapshot at the composition seam.
type SnapshotView struct {
	Enabled       bool                `json:"enabled"`
	Wired         bool                `json:"wired"`
	SourceVersion int64               `json:"sourceVersion,omitempty"`
	Pipeline      *PipelineQueueStats `json:"pipeline,omitempty"`
	Models        []LaneView          `json:"models"`
	Credentials   []LaneView          `json:"credentials"`
}

// LaneView is one queue lane in a projection snapshot.
type LaneView struct {
	Model      string `json:"model,omitempty"`
	Credential int    `json:"credential,omitempty"`
	Mode       string `json:"mode,omitempty"`
	Depth      int64  `json:"depth"`
}
