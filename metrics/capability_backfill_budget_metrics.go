// Package metrics - capability 回填预算闸门可观测性（R37 遗留 #4 工程半）.
//
// 背景：capability_backfill 的每日真实出网探测预算（滚动 24h 窗口）此前
// 只有 slog——budget exhausted 抬到 Warn，但「今天花了多少」「被闸门挡了
// 多少」「这个进程配了多大预算」在面板上不可见。R37 审计遗留 #4 指出：
// 闸门无 Prometheus 指标时，多实例交接窗双花与重启清账只能靠翻日志发现。
//
// 观察面（对齐 node_state_prefetch_metrics.go 的埋点纪律）：
//
//	llmgw_capability_backfill_probe_charged_total        真实出网记账数
//	llmgw_capability_backfill_probe_budget_blocked_total 闸门拒绝出网数
//	llmgw_capability_backfill_daily_budget               本进程配置的日预算
//
// 语义约定：
//   - charged 只在 chargeProbe 记账成功时 +1（= 真实出网次数，与账单口径
//     一致；admission 拒绝/退避跳过不记——它们不出网）。
//   - blocked 只在 chargeProbe 因预算耗尽返回 false 时 +1。它与 Warn 日志
//     同源但不同层：Warn 是每轮一条，counter 是每行一次，运维面板用
//     rate(blocked) 判断「闸门开始拦人」，用日志判断「哪一轮开始拦」。
//   - daily_budget 是 Gauge 而非常量导出：预算来自 env（构造时读一次），
//     多实例配置漂移时各进程各自暴露配置值即可对出差异；<=0（不设预算）
//     暴露 0。
//
// 基数（GW-00）：三个指标都无 label，序列数恒为 3。credential_id /
// binding_id 绝不进 label（metrics/label_cardinality_guard_test.go 会拦）。
// 埋点不引入任何出网点的新往返：全部进程内采样。
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// CapabilityBackfillProbeChargedTotal 统计预算闸门记账的真实出网探测数。
var CapabilityBackfillProbeChargedTotal = promauto.NewCounter(prometheus.CounterOpts{
	Name: "llmgw_capability_backfill_probe_charged_total",
	Help: "Egress probes charged to the capability-backfill rolling daily budget (equals real upstream probes; admission rejections and backoff skips are not charged).",
})

// CapabilityBackfillProbeBudgetBlockedTotal 统计因预算耗尽被闸门拒绝的探测行数。
var CapabilityBackfillProbeBudgetBlockedTotal = promauto.NewCounter(prometheus.CounterOpts{
	Name: "llmgw_capability_backfill_probe_budget_blocked_total",
	Help: "Probe candidates refused by the capability-backfill daily budget gate. Sustained non-zero rate means capability refresh is falling behind staleAfter and the budget needs an Owner decision.",
})

// CapabilityBackfillDailyBudget 暴露本进程生效的日预算（<=0 表示不设预算，
// 暴露 0）。构造时设置一次；它让「重启后清账」与「多实例配置漂移」在
// 面板上直接可比。
var CapabilityBackfillDailyBudget = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "llmgw_capability_backfill_daily_budget",
	Help: "Configured rolling-24h egress probe budget for capability backfill in this process (0 = unbudgeted).",
})

// RecordCapabilityBackfillProbeCharged 在 chargeProbe 记账成功（即将真实
// 出网）时调用。纯指标，不改控制流。
func RecordCapabilityBackfillProbeCharged() {
	CapabilityBackfillProbeChargedTotal.Inc()
}

// RecordCapabilityBackfillProbeBudgetBlocked 在 chargeProbe 因预算耗尽拒绝
// 出网时调用。纯指标，不改控制流。
func RecordCapabilityBackfillProbeBudgetBlocked() {
	CapabilityBackfillProbeBudgetBlockedTotal.Inc()
}

// SetCapabilityBackfillDailyBudget 在 CapabilityBackfill 构造时同步配置值。
func SetCapabilityBackfillDailyBudget(budget int) {
	if budget < 0 {
		budget = 0
	}
	CapabilityBackfillDailyBudget.Set(float64(budget))
}
