// metrics.go — P2.2 Track C: routingopt Prometheus 可观测性。
//
// 指标命名遵循仓库惯例 llmgw_<pkg>_<metric>（参见 autoroute/metrics.go）：
//
//  1. llmgw_routingopt_preclassify_ms / llmgw_routingopt_postclassify_ms /
//     llmgw_routingopt_recommend_ms
//     Histogram，三个 hook 的耗时（毫秒）。桶覆盖 0.5~100ms，
//     10ms 桶对应设计目标 P99 ≤ 10ms（ROUTING_OPT_MAX_PLUGIN_LATENCY_MS）。
//
//  2. llmgw_routingopt_feedback_writes_total{result=ok|error|dropped}
//     反馈写入计数。实际数据源是 feedback_batch.go 的原子计数器
//     （enqueued / dropped / flushed），本轨道只定义指标并提供
//     AttachFeedbackCounters(fn) 注入口；由编排者在 Wave 2 把 fn 接到
//     batch writer 上。映射关系：
//     ok      → Flushed（成功写入 routing_feedback_log）
//     dropped → Dropped（入队前丢弃：队列满 / 溢出）
//     error   → Enqueued - Flushed - Dropped（下溢饱和为 0）
//
//  3. llmgw_routingopt_exploration_requests_total
//     ε-greedy 探索计数（recommender.Recommend 走 exploreRandomly 分支）。
//
//  4. llmgw_routingopt_cache_hits_total / llmgw_routingopt_cache_miss_total
//     缓存命中计数。本轨道只定义指标 + RecordCacheHit/RecordCacheMiss
//     累加接口；Track A 的分类缓存回填时调用即可。
//
//  5. llmgw_routingopt_weighted_accuracy
//     Gauge，(auto + 2×human) / (total + 2×human) 加权准确率，
//     RunAdaptiveMaintenance 每轮维护后刷新。
//
// 注册与挂载：与 autoroute/metrics.go 相同，注册进
// prometheus.DefaultRegisterer（init + sync.Once 幂等）。cmd/gateway 的
// /metrics（middleware.MetricsHandler() = promhttp.Handler()）与
// cmd/gateway-v2 的 /metrics（promhttp.HandlerFor(prometheus.DefaultGatherer)）
// 都直接暴露 DefaultGatherer，因此 main 无需任何额外接线，import 本包即见。
//
// Snapshot() 是非 Prometheus 的纯原子快照（读 prometheus 内部 atomic 状态，
// 无锁），供 admin JSON 输出。
package routingopt

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

const routingOptMetricPrefix = "llmgw_routingopt_"

// hook 直方图桶：0.5 ~ 100ms，10ms 桶 = P99 设计目标线。
var hookLatencyBuckets = []float64{0.5, 1, 2, 5, 10, 20, 50, 100}

var (
	routingOptMetricsOnce sync.Once

	preClassifyMs  prometheus.Histogram
	postClassifyMs prometheus.Histogram
	recommendMs    prometheus.Histogram

	explorationRequests prometheus.Counter

	cacheHits prometheus.Counter
	cacheMiss prometheus.Counter

	weightedAccuracy prometheus.Gauge

	// feedbackWrites 通过自定义 Collector 在 scrape 时读取注入的原子
	// 计数器快照（无轮询、无双重记账、无锁）。
	feedbackWrites *feedbackWritesCollector
)

// feedback_writes_total 的描述符独立于 CounterVec 定义，供
// feedbackWritesCollector 在 Describe/Collect 中使用。
var feedbackWritesDesc = prometheus.NewDesc(
	routingOptMetricPrefix+"feedback_writes_total",
	"Routing feedback batch writes by outcome. ok=flushed to routing_feedback_log, "+
		"dropped=discarded before flush, error=enqueued-flushed-dropped (saturated at 0). "+
		"Values are read from the batch writer's atomic counters at scrape time.",
	[]string{"result"}, nil,
)

// feedback result label 值。
const (
	feedbackResultOK      = "ok"
	feedbackResultError   = "error"
	feedbackResultDropped = "dropped"
)

// FeedbackCounters 是反馈 batch writer（feedback_batch.go）原子计数器的
// 一次快照。
type FeedbackCounters struct {
	Enqueued uint64 // 成功进入异步批量队列
	Dropped  uint64 // 入队前被丢弃（队列满 / 溢出）
	Flushed  uint64 // 成功持久化到 routing_feedback_log
}

// FeedbackCountersFunc 返回当前计数器快照。实现必须并发安全且不阻塞
// （只读原子变量）。Wave 2 由编排者接到 feedback_batch.go 的计数器上。
type FeedbackCountersFunc func() FeedbackCounters

// feedbackWritesCollector 在每次 scrape 时调用注入的 fn，把原子计数器
// 投影为 feedback_writes_total{result}。fn 未注入时不产出任何 metric。
type feedbackWritesCollector struct {
	fn atomic.Pointer[FeedbackCountersFunc]
}

func (c *feedbackWritesCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- feedbackWritesDesc
}

func (c *feedbackWritesCollector) Collect(ch chan<- prometheus.Metric) {
	fp := c.fn.Load()
	if fp == nil {
		return
	}
	snap := (*fp)()
	errCount := saturatingSub(saturatingSub(snap.Enqueued, snap.Flushed), snap.Dropped)
	ch <- prometheus.MustNewConstMetric(feedbackWritesDesc, prometheus.CounterValue, float64(snap.Flushed), feedbackResultOK)
	ch <- prometheus.MustNewConstMetric(feedbackWritesDesc, prometheus.CounterValue, float64(snap.Dropped), feedbackResultDropped)
	ch <- prometheus.MustNewConstMetric(feedbackWritesDesc, prometheus.CounterValue, float64(errCount), feedbackResultError)
}

// registerRoutingOptMetrics 把全部 collector 注册进默认 registry。
// sync.Once 幂等（init 与 AttachFeedbackCounters 都可能触发）。
func registerRoutingOptMetrics() {
	routingOptMetricsOnce.Do(func() {
		preClassifyMs = prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    routingOptMetricPrefix + "preclassify_ms",
			Help:    "PreClassify hook duration in milliseconds. Bucket at 10ms marks the P99 target.",
			Buckets: hookLatencyBuckets,
		})
		postClassifyMs = prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    routingOptMetricPrefix + "postclassify_ms",
			Help:    "PostClassify hook duration in milliseconds. Bucket at 10ms marks the P99 target.",
			Buckets: hookLatencyBuckets,
		})
		recommendMs = prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    routingOptMetricPrefix + "recommend_ms",
			Help:    "RecommendModel hook duration in milliseconds. Bucket at 10ms marks the P99 target.",
			Buckets: hookLatencyBuckets,
		})
		explorationRequests = prometheus.NewCounter(prometheus.CounterOpts{
			Name: routingOptMetricPrefix + "exploration_requests_total",
			Help: "Requests where ε-greedy exploration replaced the multi-objective ranking.",
		})
		cacheHits = prometheus.NewCounter(prometheus.CounterOpts{
			Name: routingOptMetricPrefix + "cache_hits_total",
			Help: "Routing-optimizer cache hits (populated by Track A classification cache).",
		})
		cacheMiss = prometheus.NewCounter(prometheus.CounterOpts{
			Name: routingOptMetricPrefix + "cache_miss_total",
			Help: "Routing-optimizer cache misses (populated by Track A classification cache).",
		})
		weightedAccuracy = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: routingOptMetricPrefix + "weighted_accuracy",
			Help: "Human-annotation-weighted routing accuracy (auto + 2*human) / (total + 2*human), refreshed by RunAdaptiveMaintenance.",
		})
		feedbackWrites = &feedbackWritesCollector{}

		prometheus.MustRegister(
			preClassifyMs, postClassifyMs, recommendMs,
			explorationRequests,
			cacheHits, cacheMiss,
			weightedAccuracy,
			feedbackWrites,
		)
	})
}

func init() {
	registerRoutingOptMetrics()
}

// =============================================================================
// 埋点入口（本包内 hook / 后续轨道注入）
// =============================================================================

// recordHookLatency 记录一次 hook 耗时（毫秒）。用法：
//
//	func (o *RealOptimizer) PreClassify(...) {
//	    defer recordHookLatency(hookPreClassify, time.Now())
//	    ...
//	}
//
// Prometheus Histogram.Observe 本身原子，无需额外加锁。
func recordHookLatency(hook string, start time.Time) {
	elapsedMs := float64(time.Since(start).Microseconds()) / 1000.0
	switch hook {
	case "preclassify":
		if preClassifyMs != nil {
			preClassifyMs.Observe(elapsedMs)
		}
	case "postclassify":
		if postClassifyMs != nil {
			postClassifyMs.Observe(elapsedMs)
		}
	case "recommend":
		if recommendMs != nil {
			recommendMs.Observe(elapsedMs)
		}
	}
}

// recordExplorationRequest ε-greedy 探索分支命中时 +1
// （recommender.Recommend 调用）。
func recordExplorationRequest() {
	if explorationRequests != nil {
		explorationRequests.Inc()
	}
}

// RecordCacheHit / RecordCacheMiss 是缓存命中计数的累加接口。
// Track A（分类缓存）回填时在命中/未命中路径各调用一行即可。
func RecordCacheHit() {
	if cacheHits != nil {
		cacheHits.Inc()
	}
}

func RecordCacheMiss() {
	if cacheMiss != nil {
		cacheMiss.Inc()
	}
}

// SetWeightedAccuracy 刷新加权准确率 gauge（RunAdaptiveMaintenance 每轮
// learner.GetStats 之后调用）。
func SetWeightedAccuracy(accuracy float64) {
	if weightedAccuracy != nil {
		weightedAccuracy.Set(accuracy)
	}
}

// AttachFeedbackCounters 注入反馈 batch writer 的原子计数器读取函数
// （Wave 2 接线：routingopt.AttachFeedbackCounters(feedbackBatch.Counters)）。
// 注入后每次 /metrics scrape 实时读取快照投影为
// llmgw_routingopt_feedback_writes_total{result=ok|error|dropped}；
// 未注入时不产出该指标。nil fn 是 no-op。并发安全（atomic.Pointer）。
func AttachFeedbackCounters(fn FeedbackCountersFunc) {
	if fn == nil {
		return
	}
	registerRoutingOptMetrics()
	f := fn
	feedbackWrites.fn.Store(&f)
}

// =============================================================================
// Snapshot：供 admin JSON 的非 Prometheus 纯原子快照
// =============================================================================

// HookLatencyStats 是单个 hook 耗时直方图的原子快照。
type HookLatencyStats struct {
	Count uint64  `json:"count"`
	SumMs float64 `json:"sum_ms"`
	AvgMs float64 `json:"avg_ms"`
}

// FeedbackWritesStats 是 feedback_writes_total 三个 result 标签的快照。
type FeedbackWritesStats struct {
	OK      uint64 `json:"ok"`
	Error   uint64 `json:"error"`
	Dropped uint64 `json:"dropped"`
}

// MetricsSnapshot 是 routingopt 全部指标的一次无锁快照（读 prometheus
// 内部 atomic 状态 / 注入的原子计数器），供 admin JSON 输出与测试断言。
type MetricsSnapshot struct {
	PreClassify  HookLatencyStats `json:"preclassify"`
	PostClassify HookLatencyStats `json:"postclassify"`
	Recommend    HookLatencyStats `json:"recommend"`

	FeedbackWrites      FeedbackWritesStats `json:"feedback_writes"`
	ExplorationRequests uint64              `json:"exploration_requests"`
	CacheHits           uint64              `json:"cache_hits"`
	CacheMiss           uint64              `json:"cache_miss"`
	CacheHitRate        float64             `json:"cache_hit_rate"`
	WeightedAccuracy    float64             `json:"weighted_accuracy"`
}

// Snapshot 读取当前指标快照。线程安全：全部经由 prometheus 原子语义或
// atomic 读取，无锁竞争。
func Snapshot() MetricsSnapshot {
	registerRoutingOptMetrics()

	feedback := FeedbackWritesStats{}
	if fp := feedbackWrites.fn.Load(); fp != nil {
		snap := (*fp)()
		feedback.OK = snap.Flushed
		feedback.Dropped = snap.Dropped
		feedback.Error = saturatingSub(saturatingSub(snap.Enqueued, snap.Flushed), snap.Dropped)
	}

	hits := counterValue(cacheHits)
	miss := counterValue(cacheMiss)
	hitRate := 0.0
	if total := hits + miss; total > 0 {
		hitRate = float64(hits) / float64(total)
	}

	return MetricsSnapshot{
		PreClassify:         histogramStats(preClassifyMs),
		PostClassify:        histogramStats(postClassifyMs),
		Recommend:           histogramStats(recommendMs),
		FeedbackWrites:      feedback,
		ExplorationRequests: counterValue(explorationRequests),
		CacheHits:           hits,
		CacheMiss:           miss,
		CacheHitRate:        hitRate,
		WeightedAccuracy:    gaugeValue(weightedAccuracy),
	}
}

// =============================================================================
// 内部读取工具：prometheus.Metric.Write 内部为 atomic 读取，无锁
// =============================================================================

func counterValue(c prometheus.Counter) uint64 {
	var m dto.Metric
	if c == nil || c.Write(&m) != nil {
		return 0
	}
	return uint64(m.GetCounter().GetValue())
}

func gaugeValue(g prometheus.Gauge) float64 {
	var m dto.Metric
	if g == nil || g.Write(&m) != nil {
		return 0
	}
	return m.GetGauge().GetValue()
}

func histogramStats(h prometheus.Histogram) HookLatencyStats {
	var m dto.Metric
	if h == nil || h.Write(&m) != nil {
		return HookLatencyStats{}
	}
	count := m.GetHistogram().GetSampleCount()
	sum := m.GetHistogram().GetSampleSum()
	var avg float64
	if count > 0 {
		avg = sum / float64(count)
	}
	return HookLatencyStats{Count: count, SumMs: sum, AvgMs: avg}
}

// saturatingSub 无符号饱和减法：a < b 时返回 0（error 计数不会下溢）。
func saturatingSub(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}
