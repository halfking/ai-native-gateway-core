// Package bg — s4_scan_skip_metrics.go
//
// 「本轮未执行」的可观测出口（2026-10-02 审计 §9.38）。
//
// 问题：S4 停写门控让两个后台扫描在**发出 SQL 之前**短路，
//
//	ledger_reconciliation.checkUsageCredit — 两侧不同爆炸半径（v1 冻结 /
//	  credit_ledger 继续增长），继续比对会把停写本身记成账务差异。
//	credential_recovery.scanLookbackRecoveries — 证据源冻结，窗口内不会有新行，
//	  继续扫描会把「不再具备判定能力」读成「没有需要恢复的绑定」。
//
// 两者都 `return 0`。而 0 与「扫了没发现差异」在计数上**完全不可区分**：
// 停写期间只看 findings 计数，运维看到的是「账务无差异 / 无恢复候选」，
// 而真相是这两个问题**已经不再可判定**。两处都已有机器可读的
// SkippedChecks()，但全仓 grep 确认**零生产消费者**（只有两个 s4_gate_test.go 在读）
// —— 一条只被测试读到的通道，在事实层面等于没有。
//
// 既有计数器也救不了这个洞：credential_recovery 的
// llmgw_recovery_lookback_triggers_total{outcome="skipped_s4_stop_write"} 是
// **累计**计数器，停写生效后它停止增长 —— 而「计数器不再增长」与「worker 卡死」
// 在告警侧长得一模一样。所以这里补的是**当前状态**而不是累计次数。
//
// 三个指标，每个都有 deploy/prometheus/rules/s4-scan-skip.yml 的告警在消费
// （按 §9.37 的纪律：不新增没人读的字段；本文件刻意**不**加累计型
// llm_gateway_bg_s4_scan_skip_total —— 当前状态已由 gauge 表达，
// 加一个没有告警消费者的计数器就是装饰）：
//
//	llm_gateway_bg_s4_scan_skipped_last_run{worker,reason}  gauge  1=最近一轮跳过
//	llm_gateway_bg_s4_scan_last_run_unix{worker}           gauge  最近一轮完成时刻
//	llm_gateway_bg_s4_scan_unregistered_skip_total{...}     counter 未登记 reason
//
// 标签纪律（GW-00）：worker / reason 都是编译期字面量闭集，见下方枚举。
// worker 只有 2 个、reason 只有 1 个，基数恒定。
package bg

import (
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// s4ScanSkipWorker* 是 worker 维度的闭集。新增扫描源必须同时登记到这里、
// s4ScanSkipWorkers、以及 deploy/prometheus/rules/s4-scan-skip.yml 的告警覆盖。
const (
	s4ScanWorkerLedgerReconciliation = "ledger_reconciliation"
	s4ScanWorkerCredentialRecovery   = "credential_recovery"
)

// s4ScanSkipReason* 是 reason 维度的闭集。
//
// 目前只有一个：S4 停写让「两侧同期」这个前提消失。
const s4ScanSkipReasonStopWrite = "s4_stop_write"

// s4ScanSkipWorkers / s4ScanSkipReasons 是 init() 预初始化用的闭集副本，
// 同时是 recordS4ScanSkipState 显式归零的遍历范围 —— 归零范围必须等于
// 闭集，否则「上一轮跳过、这一轮真跑了」会留下一个永远为 1 的序列。
var (
	s4ScanSkipWorkers = []string{
		s4ScanWorkerLedgerReconciliation,
		s4ScanWorkerCredentialRecovery,
	}
	s4ScanSkipReasons = []string{
		s4ScanSkipReasonStopWrite,
	}
)

var (
	// s4ScanSkippedLastRun 是本文件的核心：1 = 最近一轮**没有执行**该扫描。
	//
	// 为什么必须是 gauge 而不是 counter：告警要回答的是「现在还在跳过吗」，
	// 而累计计数器只能回答「曾经跳过过」。停写生效后累计值停止增长，
	// 这与 worker 卡死无法区分（见包注释）。0 是有信息的值：本轮真的执行了，
	// 「没发现差异」这句话此时才成立。
	s4ScanSkippedLastRun = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_gateway_bg_s4_scan_skipped_last_run",
		Help: "1 if the most recent run of this S4-gated bg scan did NOT execute (0 = it ran). Distinguishes 'skipped, undecidable' from 'scanned, no differences found'.",
	}, []string{"worker", "reason"})

	// s4ScanLastRunUnix 让告警能区分三件事：本轮跳过（gauge=1）、
	// 本轮真跑了（gauge=0 且时间戳新鲜）、worker 卡住/已死（时间戳陈旧）。
	// 没有它，gauge=0 与「进程没起来」是同一个值。
	s4ScanLastRunUnix = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_gateway_bg_s4_scan_last_run_unix",
		Help: "Unix timestamp of the most recent completed run of this S4-gated bg scan. Distinguishes 'ran and skipped' from 'ran clean' from 'wedged or dead'.",
	}, []string{"worker"})

	// s4ScanUnregisteredSkipTotal 是**默认拒绝**的运行时兜底。
	//
	// 若某个 reason 不在闭集内，它**不会**被写进 skipped_last_run（那个指标的
	// 标签空间必须严格等于闭集），否则告警表达式会依赖一个未登记的标签值 ——
	// 而更糟的是 skipped_last_run 会停在 0，把「跳过了」报成「跑过了」。
	// 未登记的 reason 走这里，并打 error 日志：宁可吵闹，不可静默谎报。
	//
	// 基数：reason 是代码里的字符串字面量，数量等于源码里的 reason 个数，
	// 不随运行时间或请求量增长。
	s4ScanUnregisteredSkipTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_gateway_bg_s4_scan_unregistered_skip_total",
		Help: "Total skip events whose reason key is not in the registered closed enum — a new skip source was deployed without registering its metric label. Never silently dropped.",
	}, []string{"worker", "reason"})
)

func init() {
	// 预初始化，使序列在首轮运行前就出现在 /metrics 里（对齐
	// metrics/routing_analytics_mv_drift.go 的做法）。skipped=0 表示
	// 「还没跑过但状态是良性的」；last_run_unix=0 与之配合，让停滞告警能区分
	// 「从未运行」与「运行过」。
	for _, w := range s4ScanSkipWorkers {
		for _, r := range s4ScanSkipReasons {
			s4ScanSkippedLastRun.WithLabelValues(w, r).Set(0)
		}
		s4ScanLastRunUnix.WithLabelValues(w).Set(0)
	}
}

// s4ScanSkipReasonRegistered 报告 reason 是否属于闭集。
func s4ScanSkipReasonRegistered(reason string) bool {
	for _, r := range s4ScanSkipReasons {
		if r == reason {
			return true
		}
	}
	return false
}

// recordS4ScanSkipState 把一轮运行的 skip 列表翻译成指标。由 RunOnce /
// scanLookbackRecoveries 在**退出时**调用（defer），因此覆盖所有 return 分支，
// 包括未来新增的分支 —— 逐个 return 手写一遍是 §9.35 之前那种「只补了已知路径」的老形状。
//
// 归零是显式的：先把闭集里每个 reason 写成 0，再把本轮真跳过的写成 1。
// 这一步是 §9.35 M3 那个 bug 的正面回应（悲观初始化后忘了在正常分支设回 false
// ⇒ 字段恒为 true）。少了它，「跳过一轮、之后恢复」会永远显示为跳过。
func recordS4ScanSkipState(worker string, skipped []string, at time.Time) {
	skippedSet := make(map[string]struct{}, len(skipped))
	for _, reason := range skipped {
		skippedSet[reason] = struct{}{}
	}

	// 显式归零整个闭集（包括本轮没跳过的那些）。
	for _, reason := range s4ScanSkipReasons {
		value := 0.0
		if _, ok := skippedSet[reason]; ok {
			value = 1
		}
		s4ScanSkippedLastRun.WithLabelValues(worker, reason).Set(value)
	}

	for _, reason := range skipped {
		if !s4ScanSkipReasonRegistered(reason) {
			s4ScanUnregisteredSkipTotal.WithLabelValues(worker, reason).Inc()
			slog.Error("bg: S4 扫描跳过原因未登记到闭集，skipped_last_run 不会反映它",
				"worker", worker, "reason", reason,
				"detail", "该 reason 不在 s4ScanSkipReasons 内；本轮在指标上会被读成"+
					"「已执行」——请把它登记进闭集与告警规则，否则这是谎报")
			continue
		}
	}

	s4ScanLastRunUnix.WithLabelValues(worker).Set(float64(at.Unix()))
}
