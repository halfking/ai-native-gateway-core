// Package dispatch — pipeline aggregate queue stats type (V3.3-OBS OBS-BE3).
//
// PipelineQueueStats is carried by SnapshotView.Pipeline. Its values are
// maintained by QueueProjection observations (queue_projection.go), not by
// pulling Pipeline state or Prometheus collectors.
package dispatch

// PipelineQueueStats is the dispatch-native aggregate pipeline view carried
// by SnapshotView.Pipeline. Waiting percentiles are pointers: nil = 无样本
// （字段缺省），非 nil = 真实窗口分位数。
type PipelineQueueStats struct {
	Depth        int64  `json:"depth"`
	WaitingMsP50 *int64 `json:"waitingMsP50,omitempty"`
	WaitingMsP95 *int64 `json:"waitingMsP95,omitempty"`
	InFlight     int64  `json:"inFlight"`
	Degraded     bool   `json:"degraded"`
}

// nearestRank returns the nearest-rank percentile value of a sorted
// (ascending) non-empty slice: index ceil(pct*n/100)-1, clamped to bounds.
func nearestRank(sorted []int, pct int) int {
	n := len(sorted)
	idx := (pct*n+99)/100 - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return sorted[idx]
}
