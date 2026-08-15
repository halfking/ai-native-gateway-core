// Package dispatch — pipeline aggregate queue stats (V3.3-OBS OBS-BE3,
// 2026-08-15).
//
// queue_snapshot 契约（docs/会话优化v3/13 号 §4）中 pipeline 层从 TARGET
// 升级为 CURRENT 的数据源。全部为 pull 式只读聚合，不修改
// pipeline.go/forwarder.go 的热路径：
//
//   - depth     = Σ Tier-1 (model) queue depth + Σ Tier-2 (credential)
//     queue depth —— 已被 pipeline 接纳、尚未开始转发的等待请求数。
//     有数据时 0 是真实 0（队列确实空）。
//   - waitingMsP50/P95 = 最近 ≤200 个已完成请求 T0→T6 总排队等待
//     （waterfall 环形窗口，nearest-rank 分位数）。窗口内没有
//     有效样本时字段缺省（nil），禁止用 0 冒充。
//   - inFlight  = Σ dispatch_in_flight gauge —— 已通过 governor 准入、
//     正在向上游转发的请求数。
//   - degraded  = 任一 forwarder 的 governor 为 disabled（noop），
//     或本快照周期内发生过 overflow（队列满/pace 超时）。
package dispatch

import (
	"sort"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// PipelineQueueStats is the dispatch-native aggregate pipeline view carried
// by SnapshotView.Pipeline (V3.3-OBS OBS-BE3). Waiting percentiles are
// pointers: nil = 无样本（字段缺省），非 nil = 真实窗口分位数。
type PipelineQueueStats struct {
	Depth        int64  `json:"depth"`
	WaitingMsP50 *int64 `json:"waitingMsP50,omitempty"`
	WaitingMsP95 *int64 `json:"waitingMsP95,omitempty"`
	InFlight     int64  `json:"inFlight"`
	Degraded     bool   `json:"degraded"`
}

// pipelineQueueStats aggregates the pipeline-level view for one SSE tick.
// overflowSinceLast reports whether dispatch_overflow_total increased since
// the previous snapshot (computed by the caller, which owns the last-seen
// value). Returns nil on a nil pipeline (cannot masquerade stats).
func (p *Pipeline) pipelineQueueStats(overflowSinceLast bool) *PipelineQueueStats {
	if p == nil {
		return nil
	}

	// depth: Tier-1 + Tier-2 queued requests (authoritative from
	// Pipeline.Snapshot()'s atomic per-lane counters).
	var depth int64
	models, creds := p.Snapshot()
	for _, m := range models {
		depth += m.Depth
	}
	for _, c := range creds {
		depth += c.Depth
	}

	stats := &PipelineQueueStats{
		Depth:    depth,
		InFlight: int64(gaugeVecSum(metricInFlight)),
		Degraded: overflowSinceLast || p.anyGovernorDisabled(),
	}
	stats.WaitingMsP50, stats.WaitingMsP95 = p.waitingPercentiles()
	return stats
}

// waitingPercentiles computes nearest-rank p50/p95 of T0→T6 queue wait over
// the waterfall ring window (most recent ≤200 completed requests). Samples
// without both T0 and T6 timestamps (early shutdown/error completes) are
// excluded — they carry no real wait signal and must not deflate the
// percentiles. Returns (nil, nil) when the window has no valid sample.
func (p *Pipeline) waitingPercentiles() (p50, p95 *int64) {
	if p == nil {
		return nil, nil
	}
	reqs := p.ensureWaterfallRing().snapshot(200, "", 0)
	waits := make([]int, 0, len(reqs))
	for _, r := range reqs {
		// QueueWaitMS is only set when both T0 and T6 exist; a 0 value is
		// ambiguous (sub-ms wait vs. missing timestamps), so keep only
		// strictly-positive real samples.
		if r.ArrivedAt == "" || r.CredDequeuedAt == "" || r.QueueWaitMS <= 0 {
			continue
		}
		waits = append(waits, r.QueueWaitMS)
	}
	if len(waits) == 0 {
		return nil, nil
	}
	sort.Ints(waits)
	p50v := int64(nearestRank(waits, 50))
	p95v := int64(nearestRank(waits, 95))
	return &p50v, &p95v
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

// anyGovernorDisabled reports whether any live Tier-2 forwarder runs a
// no-op governor (credential disabled, or a rate/concurrency mode whose
// limit is 0/unlimited and therefore degraded to noopGovernor). Such a
// credential is un-paced, which marks the pipeline view degraded.
func (p *Pipeline) anyGovernorDisabled() bool {
	if p == nil {
		return false
	}
	p.credMu.Lock()
	defer p.credMu.Unlock()
	for _, cf := range p.forwarders {
		if cf.gov != nil && cf.gov.Mode() == ModeDisabled {
			return true
		}
	}
	return false
}

// overflowVecTotal returns the cumulative dispatch_overflow_total across all
// reasons. The caller diffs two consecutive readings to derive the
// "overflow since last snapshot" flag.
func overflowVecTotal() float64 {
	return counterVecTotal(metricOverflow)
}

// counterVecTotal sums all children of a CounterVec via the prometheus
// collect protocol.
func counterVecTotal(v *prometheus.CounterVec) float64 {
	if v == nil {
		return 0
	}
	var total float64
	ch := make(chan prometheus.Metric)
	go func() {
		v.Collect(ch)
		close(ch)
	}()
	for m := range ch {
		var d dto.Metric
		if err := m.Write(&d); err == nil {
			total += d.GetCounter().GetValue()
		}
	}
	return total
}

// gaugeVecSum sums all children of a GaugeVec via the prometheus collect
// protocol.
func gaugeVecSum(v *prometheus.GaugeVec) float64 {
	if v == nil {
		return 0
	}
	var total float64
	ch := make(chan prometheus.Metric)
	go func() {
		v.Collect(ch)
		close(ch)
	}()
	for m := range ch {
		var d dto.Metric
		if err := m.Write(&d); err == nil {
			total += d.GetGauge().GetValue()
		}
	}
	return total
}
