package autoroute

// metrics.go — CHANNEL_QUALITY_ROUTING 可观测性。
//
// 注册 3 个 Prometheus 指标，命名遵循仓库惯例：llmgw_<pkg>_<metric>。
//
//   1. llmgw_autoroute_pool_decisions_total{pool, reason}
//      Counter，按 pool 标签分（preferred / fallback）累计路由决策。
//      reason 标签细分 demotion 行为：
//        - no_demotion       preferred 充足（>= topN）
//        - demotion_05       主渠道未饱和，fallback 被施加 0.5 demotion
//        - demotion_085      主渠道饱和，fallback 被放宽到 0.85 demotion
//        - empty_preferred   无 preferred 池（冷启动 / 全 fallback）
//
//   2. llmgw_autoroute_channel_quality_score
//      Histogram，被选赢家（top-1）的 ChannelQuality 分值。
//      桶设置针对 0-100 区间，按关键阈值 50（preferred/fallback 分界）、
//      90（official 类基线）分段。
//
//   3. llmgw_autoroute_demotion_events_total{factor}
//      Counter，fallback demotion 触发次数（按 factor=0.5/0.85 分）。
//      仅在 stratifyAndPickTopN 真正对 fallback 施加了 demotion 时
//      增加（与 llmgw_autoroute_pool_decisions_total{reason=...} 自洽）。
//
// 注册：使用 sync.Once 幂等；通过 init() 立即注册，让指标在 /metrics
// 上从启动起就可见（labels 在首次事件时出现）。
//
// 调用入口：stratifyAndPickTopN 返回前调用 recordRoutingDecision。

import (
	"log/slog"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

const routingMetricPrefix = "llmgw_autoroute_"

var (
	routingMetricOnce sync.Once

	// poolDecisions：路由池决策分布
	poolDecisions *prometheus.CounterVec

	// channelQualityScore：被选赢家的 ChannelQuality 分值分布
	channelQualityScore prometheus.Histogram

	// demotionEvents：fallback demotion 触发次数（按 factor 分）
	demotionEvents *prometheus.CounterVec
)

// registerRoutingMetrics registers all llmgw_autoroute_* collectors with
// the default Prometheus registry. Idempotent thanks to sync.Once;
// gateway's promhttp handler at /metrics surfaces them automatically.
//
// Operators 用这三个指标可以回答关键问题：
//   - "是否真的在 preferred > fallback 优先？" → poolDecisions 比例
//   - "demotion 命中频率如何？" → demotionEvents{factor}
//   - "赢家的通道质量分布健康吗？" → channelQualityScore 直方图
func registerRoutingMetrics() {
	routingMetricOnce.Do(func() {
		poolDecisions = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: routingMetricPrefix + "pool_decisions_total",
				Help: "Routing pool selection outcomes for the top-1 winner. " +
					"Labels: pool={preferred,fallback}, reason={no_demotion,demotion_05,demotion_085,empty_preferred}.",
			},
			[]string{"pool", "reason"},
		)
		channelQualityScore = prometheus.NewHistogram(
			prometheus.HistogramOpts{
				Name: routingMetricPrefix + "channel_quality_score",
				Help: "Distribution of ChannelQuality (0-100) for the selected top-1 winner. " +
					"Buckets tuned to threshold 50 (preferred/fallback boundary) and 90 (official baseline).",
				// 阈值分段：0/30/50（boundary）/60/80/90（official）/100
				Buckets: []float64{0, 30, 50, 60, 80, 90, 100},
			},
		)
		demotionEvents = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: routingMetricPrefix + "demotion_events_total",
				Help: "Fallback demotion events by factor. Only incremented when demotion was actually applied.",
			},
			[]string{"factor"},
		)
		prometheus.MustRegister(poolDecisions, channelQualityScore, demotionEvents)
	})
}

func init() {
	registerRoutingMetrics()
	registerLiveFilterMetrics()
}

// DecisionPoolLabel 与 DecisionReasonLabel 是 recordRoutingDecision
// 接受的 label 值常量。
const (
	PoolLabelPreferred = "preferred"
	PoolLabelFallback  = "fallback"

	ReasonLabelNoDemotion     = "no_demotion"
	ReasonLabelDemotion05     = "demotion_05"
	ReasonLabelDemotion085    = "demotion_085"
	ReasonLabelEmptyPreferred = "empty_preferred"
)

// recordRoutingDecision 是 stratifyAndPickTopN 末尾调用的埋点函数。
//
//   - pool: winner 所在池（preferred / fallback）
//   - reason: demotion 行为
//   - no_demotion: preferred 充足（>= topN），未触发 demotion
//   - demotion_05: 主渠道未饱和，fallback 被施加 0.5 demotion
//   - demotion_085: 主渠道饱和，fallback 被放宽到 0.85 demotion
//   - empty_preferred: 无 preferred 池（冷启动），未施加 demotion
//   - channelQuality: winner 的 ChannelQuality 分值（用于 histogram）
//   - demotionApplied: 真正施加到 fallback 的 demotion 系数（无 demotion 时为 1.0）
//
// 安全：所有指标为 nil 时（注册前调用）安全跳过，避免 panic。
func recordRoutingDecision(pool, reason string, channelQuality, demotionApplied float64) {
	if poolDecisions != nil {
		poolDecisions.WithLabelValues(pool, reason).Inc()
	}
	if channelQualityScore != nil && pool == PoolLabelPreferred {
		// 仅记录 preferred 赢家的 ChannelQuality 分布。fallback 胜出
		// 是异常路径，避免污染主指标。
		channelQualityScore.Observe(channelQuality)
	}
	if demotionEvents != nil && demotionApplied < 1.0 {
		demotionEvents.WithLabelValues(formatFactor(demotionApplied)).Inc()
	}
}

// formatFactor 把 0.5 / 0.85 转成字符串 label。
// 输入限定在已知的两个因子之一，避免 label 基数爆炸。
func formatFactor(f float64) string {
	switch f {
	case FallbackDemotionFactor:
		return "0.5"
	case FallbackDemotionFactorSaturated:
		return "0.85"
	default:
		return "unknown"
	}
}

// 2026-07-04 V17: live availability filter observability.
// 2026-07-05 V27: Expose as Prometheus metrics instead of in-process counters.
//
// When the DB-bound filter fails, the router falls back to a cached
// snapshot (up to 5min stale). Without these metrics, operators have
// no way to detect a backend DB blip that's silently degrading routing.

var (
	liveFilterTotal  prometheus.Counter
	liveFilterFailed prometheus.Counter
)

func registerLiveFilterMetrics() {
	liveFilterTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: routingMetricPrefix + "live_filter_total",
			Help: "Total live availability filter operations (success + failure).",
		},
	)
	liveFilterFailed = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: routingMetricPrefix + "live_filter_failed",
			Help: "Live availability filter operations that fell back to snapshot due to DB error.",
		},
	)
	prometheus.MustRegister(liveFilterTotal, liveFilterFailed)
}

func recordLiveFilterSuccess(filtered int) {
	liveFilterTotal.Inc()
	// (filtered = removed candidate count) — populated for observability.
}

func recordLiveFilterFailure(poolConfigured bool, err error) {
	liveFilterTotal.Inc()
	liveFilterFailed.Inc()
	slog.Error("recommend_v2: live availability filter failed, using snapshot",
		"error", err,
		"pool_configured", poolConfigured,
	)
}
