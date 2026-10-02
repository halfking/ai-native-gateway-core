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
	"time"

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
	registerRefreshMetrics()
	registerRoleFallbackMetrics()
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

// 2026-08-11: autoroute index refresh observability.
//
// Operators need to detect two classes of silent degradation:
//   1. The in-memory index shrinks/grows unexpectedly (rollup gap, filter
//      regression) → index_entries gauge lets you see the candidate count
//      trend across refresh cycles.
//   2. The index contains entries that the authoritative
//      v_routable_credential_models view currently marks non-routable (for
//      example disabled credentials, exhausted quota, unhealthy credentials,
//      or node-probe backoff). index_drift is deliberately the leaked-entry
//      count, not entries - view_count, because the view includes static
//      routable bindings that may not have recent 5-minute rollup rows.
//
// refresh_total / refresh_failed_total track refresh attempt health so a
// stuck refresher (5-min cadence stalling) is visible in dashboards.

var (
	indexEntries  prometheus.Gauge
	indexDrift    prometheus.Gauge
	refreshTotal  prometheus.Counter
	refreshFailed prometheus.Counter

	// F-7: 路由决策耗时（毫秒）
	decisionLatency prometheus.Histogram

	// F-7: 缓存命中率计数器
	cacheHitTotal  prometheus.Counter
	cacheMissTotal prometheus.Counter
)

func registerRefreshMetrics() {
	indexEntries = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: routingMetricPrefix + "index_entries",
		Help: "Number of candidates in the in-memory autoroute index after the last successful refresh.",
	})
	indexDrift = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: routingMetricPrefix + "index_drift",
		Help: "Number of in-memory autoroute index entries that are not currently routable in v_routable_credential_models. " +
			"A value greater than zero indicates disabled, exhausted, unhealthy, or probe-backoff credentials leaked into the index.",
	})
	refreshTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: routingMetricPrefix + "refresh_total",
		Help: "Total autoroute index refresh attempts (success + failure).",
	})
	refreshFailed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: routingMetricPrefix + "refresh_failed_total",
		Help: "Autoroute index refresh attempts that failed.",
	})
	// F-7: 路由决策耗时直方图（毫秒）
	decisionLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: routingMetricPrefix + "decision_latency_ms",
		Help: "Routing decision latency in milliseconds (Decide/DecideV2 end-to-end). " +
			"Buckets tuned to detect P99 > 10ms (routing hot path budget).",
		Buckets: []float64{0.5, 1, 2, 5, 10, 20, 50, 100, 200},
	})
	// F-7: 缓存命中率计数器
	cacheHitTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: routingMetricPrefix + "cache_hit_total",
		Help: "Session intent cache hits (reused decision without reclassification).",
	})
	cacheMissTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: routingMetricPrefix + "cache_miss_total",
		Help: "Session intent cache misses (required fresh classification).",
	})
	prometheus.MustRegister(indexEntries, indexDrift, refreshTotal, refreshFailed,
		decisionLatency, cacheHitTotal, cacheMissTotal)
}

// recordRefreshOutcome is called at the end of Index.Refresh.
//
//   - entries: len of the refreshed candidate slice (0 on failure)
//   - leakedEntries: COUNT of refreshed entries that do not have a matching
//     is_routable row in v_routable_credential_models, or -1 when the probe
//     itself failed (so drift is not published with stale data)
//   - err: the refresh error (nil on success)
//
// All metrics are nil-safe (no-op before registration).
func recordRefreshOutcome(entries int, leakedEntries int, err error) {
	if refreshTotal != nil {
		refreshTotal.Inc()
	}
	if err != nil {
		if refreshFailed != nil {
			refreshFailed.Inc()
		}
		return
	}
	if indexEntries != nil {
		indexEntries.Set(float64(entries))
	}
	if leakedEntries >= 0 && indexDrift != nil {
		indexDrift.Set(float64(leakedEntries))
	}
}

// F-7: recordDecisionLatency 记录路由决策耗时（毫秒）。
// 在 Decide/DecideV2 入口使用 defer recordDecisionLatency(time.Now()) 调用。
func recordDecisionLatency(start time.Time) {
	if decisionLatency != nil {
		elapsed := float64(time.Since(start).Microseconds()) / 1000.0
		decisionLatency.Observe(elapsed)
	}
}

// F-7: recordCacheHit 记录缓存命中（会话缓存复用决策）。
func recordCacheHit() {
	if cacheHitTotal != nil {
		cacheHitTotal.Inc()
	}
}

// F-7: recordCacheMiss 记录缓存未命中（需要重新分类）。
func recordCacheMiss() {
	if cacheMissTotal != nil {
		cacheMissTotal.Inc()
	}
}

// R53 (2026-10-02) role 偏好层可观测性。
//
// 诉求"低价不可用要退主流"（R52 修复）本身是**静默的**：轻量池整层掉线时
// 请求照常成功，只是静默升档到重量模型。审计字段 Decision.RoleFallbackLayer
// 已经落进 request_logs.auto_decision（JSONB），但那是"事后翻库"的观测面，
// 没有任何主动告警。本指标把同一个值送进 Prometheus，让"轻量池整层掉线"
// 成为可被告警的事件。
//
// 为什么需要这个指标，而不能直接对 SQL 写告警：
//
//	request_logs.auto_decision->>'role_fallback_layer' 在父开关
//	AUTO_ROLE_ROUTING_ENABLED 关闭时**恒为 NULL**——Decision.RoleFallbackLayer
//	带 omitempty，flag-off 时 rolePrefs 为空、字段根本不进序列化。所以一条
//	"比例 = N/N" 的 SQL 告警在 flag-off 部署上返回 NULL，与"开关开着但从
//	没回退过"在监控面板上长得一模一样：都是"没数据"。运维据此无法区分
//	"轻量池健康"和"这套机制压根没开"。
//
// 本指标用两个 label 值把这两件事分开：
//
//	layer="kind"        role 路由介入且命中第 1 层（轻量池）
//	layer="mainstream"  role 路由介入且整层缺席后落第 2 层（重量池）← 要告警的
//
// 两个值在 init 时以 Add(0) 预置，因此 flag-off 部署上 series 依然存在（读数
// 0），运维能确认"机制已装载但一次都没进过 role 路由"——这是与 SQL 侧 NULL
// 最重要的区别。分母用两个值之和（role 路由实际介入的请求数），而不是
// 全量 auto 请求数：后者会把 main 会话等无关流量算进分母，让比例失去意义。
//
// 刻意不进 telemetry.AutoSelection（同 R52 的取舍）：本指标是诊断量，
// 不是 (task_type, profile) 奖励单元的输入，进选型学习表会污染学习样本。
var (
	// roleFallbackLayerTotal 只在 role 路由**介入**时递增（父开关开 +
	// roleLLMRouter 已装配）。空串/false 分支不埋点，因此两个 label 之和
	// 恰好是"role 路由实际改过 winner 的请求数"。
	roleFallbackLayerTotal *prometheus.CounterVec
	// roleRoutingActiveGauge 恒定反映 AutoRoleRoutingEnabled 开关态，
	// 供告警表达式区分"没回退"与"没开"。0/1 两值，不会引入基数问题。
	roleRoutingActiveGauge prometheus.Gauge
)

// newRoleFallbackMetrics 构造本指标族并注册进给定的 registry。
//
// 拆出独立构造函数（而不是全部写在 register 里）的唯一理由是**可测性**：
// 预置行为（两个 label 值在零事件时就出现在 /metrics 上）只能在干净
// registry 上证明——包内其他 R52 测试会调用决策路径给 DefaultRegisterer
// 里的同名 series 加值，直接查默认 registry 时"series 存在"这件事会被
// 污染证伪不了（series 早就被别的测试建出来了），门就变成恒绿。
//
// roleRoutingActive 显式作参数而不是函数内读全局 flag：包内
// SetGlobalFeatureFlagsForTest 会改全局 flag，从 flag 读会让本函数的
// 返回值依赖测试执行顺序。init() 传入的是启动期加载的 flag 值——
// 生产里 feature flag 只在启动时读一次，gauge 不随运行期变更而更新。
func newRoleFallbackMetrics(reg prometheus.Registerer, roleRoutingActive bool) (*prometheus.CounterVec, prometheus.Gauge) {
	layers := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: routingMetricPrefix + "role_fallback_layer_total",
			Help: "Role-routing preference layer hits, by layer. Counts ONLY requests where role routing " +
				"was active (AUTO_ROLE_ROUTING_ENABLED on and role router assembled). layer=kind is the " +
				"lightweight SelectLLM preference layer; layer=mainstream is the heavyweight fallback used " +
				"when the kind layer is absent. Both label values are pre-initialized to 0 so a deployment " +
				"with the feature disabled still exposes the series.",
		},
		[]string{"layer"},
	)
	// 预置两个 label 值：CounterVec 只有在被 WithLabelValues 过之后才出现在
	// /metrics 上。flag-off 部署若不预置，PromQL 求和得到空向量，告警规则
	// 拿到 no data 而非 0——"no data"和"真的是 0"在告警语义上完全不同，
	// 前者会静默吞掉一次真实的整层掉线。
	layers.WithLabelValues(RoleLayerKind).Add(0)
	layers.WithLabelValues(RoleLayerMainstream).Add(0)

	active := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: routingMetricPrefix + "role_routing_active",
		Help: "1 when AUTO_ROLE_ROUTING_ENABLED is on and the role LLM router is assembled, else 0. " +
			"Use it to tell \"no mainstream fallback happened\" (1, counter 0) apart from " +
			"\"the feature is off\" (0) — the SQL side cannot: role_fallback_layer is omitempty " +
			"and lands as NULL when the flag is off.",
	})
	if roleRoutingActive {
		active.Set(1)
	}

	reg.MustRegister(layers, active)
	return layers, active
}

func registerRoleFallbackMetrics() {
	// 启动期读一次全局 flag：生产里 feature flag 只在启动时加载一次，
	// gauge 反映的是本次启动的配置，不随后台改 flag 漂移。
	active := false
	if flags := GetFeatureFlags(); flags != nil {
		active = flags.AutoRoleRoutingEnabled
	}
	roleFallbackLayerTotal, roleRoutingActiveGauge = newRoleFallbackMetrics(prometheus.DefaultRegisterer, active)
}

// RoleLayerKind / RoleLayerMainstream 是 Decision.RoleFallbackLayer 的取值
// 常量（见 layerName）。在此导出以便告警规则与测试引用同一字面量。
const (
	RoleLayerKind       = "kind"
	RoleLayerMainstream = "mainstream"
)

// recordRoleFallbackLayer 在 role 路由介入且命中某一层时埋点。
//
// 调用契约（三条，缺一条指标就会失真）：
//  1. 只在 d.roleRoutingActive() 为真时调用——flag-off 不埋点，否则分母
//     会被从未进过 role 路由的请求污染，"主流层占比"失去意义。
//  2. 只在真正命中（hit != ""，即 winner 被偏好模型改写）时调用。
//     两层皆缺席的静默让位**不**计入：那种情况 roleFallbackLayer 为空串，
//     计入会让"让位"看起来像"命中了某一层"。
//  3. layer 必须来自 layerName(rolePlan.layerOf(hit))，不要自己拼字符串。
//
// nil-safe：注册前调用直接跳过，不 panic。
func recordRoleFallbackLayer(layer string) {
	if roleFallbackLayerTotal == nil {
		return
	}
	// 未知取值一律归入 kind 层：layerName 只产出上面两个值，这里的兜底
	// 是防御性的——若将来新增第三层，忘记改这里的后果应该是"计数落在
	// 轻量层"（让主流层告警保持保守、不误报），而不是静默丢弃埋点。
	if layer != RoleLayerMainstream {
		layer = RoleLayerKind
	}
	roleFallbackLayerTotal.WithLabelValues(layer).Inc()
}
