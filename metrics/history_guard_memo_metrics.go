package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// 历史守卫判决缓存的可观测性（2026-11-23，大请求体归约提速轮）。
//
// sanitize 包按既定决策不引 prometheus ⇒ 它只维护原子计数，由装配层
// （cmd/gateway）把取值函数注册到这里。未装配时这些序列不出现（而不是恒 0）：
// 「缓存没开」和「缓存开了但没人用」在监控上必须能区分。
//
// 命中率 = (hits_local + hits_shared) / (hits_local + hits_shared + misses)。
// shared_skipped 持续增长 ⇒ 共享层触到了自我保护（写入预算 / 内存水位线）。

// HistoryGuardMemoStatsFunc 返回缓存的运行计数快照。
type HistoryGuardMemoStatsFunc func() HistoryGuardMemoCounters

// HistoryGuardMemoCounters 是缓存的运行计数。
type HistoryGuardMemoCounters struct {
	LocalHits     uint64
	SharedHits    uint64
	Misses        uint64
	SharedErrors  uint64
	SharedWrites  uint64
	SharedSkipped uint64
	// BreakerOpens 是共享层熔断打开的次数（共享层未启用时恒为 0）。
	BreakerOpens uint64
}

// RegisterHistoryGuardMemoStats 注册 6 个只读 gauge（幂等：重复注册是 no-op）。
func RegisterHistoryGuardMemoStats(stats HistoryGuardMemoStatsFunc) {
	if stats == nil || historyGuardMemoRegistered {
		return
	}
	historyGuardMemoRegistered = true
	for _, c := range []struct {
		name string
		help string
		get  func(HistoryGuardMemoCounters) uint64
	}{
		{"history_guard_memo_local_hits_total", "History guard verdicts served from the in-process cache.", func(v HistoryGuardMemoCounters) uint64 { return v.LocalHits }},
		{"history_guard_memo_shared_hits_total", "History guard verdicts served from the shared cache.", func(v HistoryGuardMemoCounters) uint64 { return v.SharedHits }},
		{"history_guard_memo_misses_total", "History guard verdicts not found in any cache (recomputed).", func(v HistoryGuardMemoCounters) uint64 { return v.Misses }},
		{"history_guard_memo_shared_errors_total", "Shared cache read/write errors (never changes a verdict).", func(v HistoryGuardMemoCounters) uint64 { return v.SharedErrors }},
		{"history_guard_memo_shared_writes_total", "Verdicts written to the shared cache.", func(v HistoryGuardMemoCounters) uint64 { return v.SharedWrites }},
		{"history_guard_memo_shared_skipped_total", "Verdicts not written to the shared cache because a self-protection limit tripped.", func(v HistoryGuardMemoCounters) uint64 { return v.SharedSkipped }},
		{"history_guard_memo_breaker_opens_total", "Times the shared verdict cache was bypassed because it kept failing or slowing down.", func(v HistoryGuardMemoCounters) uint64 { return v.BreakerOpens }},
	} {
		get := c.get
		promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: c.name, Help: c.help}, func() float64 {
			return float64(get(stats()))
		})
	}
}

var historyGuardMemoRegistered bool
