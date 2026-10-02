package bg

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// 基线可用性的可观测出口（审计 §9.44）。
//
// # 要观测的失效形态
//
// `ComputeRoutingRewardWithWeights` 的两行：
//
//	latencyScore := 0.5
//	if in.P95BaselineMs > 0 && in.LatencyMs > 0 { … }
//
//	costScore := 0.5
//	if in.P75BaselineCost > 0 && in.CostUSD > 0 { … }
//
// 两个 0.5 不是「报错」，是**中性回落**：设计意图是「没测量就别假装知道」，
// 单独看是对的。问题在于它与「测了，结果就是中性」在输出上逐字相同。
//
// 当 `baselines[p.taskType]` 这个 map 取值 miss（或取到零值）时，**这一整条
// selection 的延迟项和成本项同时塌成 0.5**。而按 §9.43，切到会话族正是通过
// `src.TurnsTable` 换掉基线 cohort 的来源表——真库实测（24h 窗口，
// `is_auto_request = TRUE AND latency_ms IS NOT NULL`）：
//
//	v1 侧     2178 行
//	会话族侧  0 行
//
// 即在当前这份数据上切换会**立刻**得到空 map。没有报错、没有异常日志、
// 计数器照常增长，只有 reward 的分布悄悄变了：所有模型在延迟和成本上不再
// 可区分，而这恰恰是 cohort 基线存在的唯一理由。
//
// # 为什么要拆成两个 term
//
// 实测里 p95 有值而 p75 为 NULL（探针流量没有 cost_usd），所以两个项**独立**
// 塌陷：p95>0 但 p75=0 是常见形态。合成一个计数器会把这个区分抹掉，而
// 告警需要知道该查延迟采集还是成本采集。

const (
	baselineTermLatency = "latency"
	baselineTermCost    = "cost"
)

var (
	// autoRouteSettleBaselineCohortRows 是本轮基线 cohort 实际由多少行构成。
	//
	// 它是**先行指标**：missing 计数器要等 selection 被结算才动，而 cohort
	// 归零是它必然的前兆。family 维度沿用 settleSourceSpec.Family 的闭集
	// （v1 / session），刻意不带 task_type —— task_type 来自请求内容，
	// 基数无上界，进不了告警标签。
	autoRouteSettleBaselineCohortRows = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "llmgw_autoroute_settle_baseline_cohort_rows",
			Help: "Rows forming the cohort baseline (p95 latency / p75 cost) in the most recent settle sweep, by request-row family. Zero means every reward's baseline terms fall back to neutral.",
		},
		[]string{"family"},
	)

	// autoRouteSettleBaselineNeutral 统计有多少条 selection 的 reward 里，
	// latency / cost 某一项**走了中性回落**（基线缺失或为零）。
	//
	// 它统计的是「本可以区分快慢却没区分」的次数，不是错误数——回落本身是
	// 正确的降级。之所以要计数：回落与「实测结果恰好中性」不可区分，而后者
	// 是常态，只有把回落单独数出来才能看出 cohort 已经失效。
	autoRouteSettleBaselineNeutral = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_autoroute_settle_baseline_neutral_total",
			Help: "Selections whose reward scored latency or cost via the neutral 0.5 fallback (cohort baseline missing or zero) instead of the measured value, by term and request-row family.",
		},
		[]string{"term", "family"},
	)
)

func init() {
	// 预置两条序列：family 与 term 都是闭集，先建好空序列，告警表达式才不会
	// 在首次发生前因为「序列不存在」而静默。
	for _, f := range []string{settleFamilyV1, settleFamilySession} {
		autoRouteSettleBaselineCohortRows.WithLabelValues(f)
		for _, term := range []string{baselineTermLatency, baselineTermCost} {
			autoRouteSettleBaselineNeutral.WithLabelValues(term, f)
		}
	}
}
