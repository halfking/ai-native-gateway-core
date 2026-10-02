// Package metrics - 能力位快照复用（vapeur 遗留 #3）可观测性指标.
//
// 背景：路由在候选筛选阶段用一次 MGET 批量读 node state，并把结果作为
// `cand.RoutedNodeState` 透传给 executor 的协议能力闸门
// （executor_chat → Manager.GetSupportsResponses），从而省掉热路径上对同一
// key 的第二次 GET。代价是引入一个**无上界**的陈旧窗口：请求在
// dispatchPipeline.Submit 上同步阻塞等 worker，队列等待没有代码上界。
//
// 因此闸门侧有年龄护栏 `prefetchMaxAgeSec`，超龄的快照被丢弃并重读。
// **护栏是否合理无法靠推理判断**——它是本项目此前唯一未经实测校准的参数。
// 没有埋点就只能看到「优化还在不在」，看不到「优化在什么条件下失效」。
//
// 本文件补上那个观察面：
//
//	llmgw_node_state_prefetch_dropped_total{reason}  护栏丢弃的快照数
//	llmgw_node_state_prefetch_age_seconds           被采信快照的实际年龄分布
//
// 为什么 age 直方图与 dropped 计数器必须同时存在：**只有计数器时，
// 「从不触发」与「触发率 0.1%」在面板上难以区分，而后者恰恰是要调参的信号。
// 直方图直接给出 dispatch 排队的真实分布——护栏应当按它校准，而不是按
// 拍脑袋的整数。
//
// 埋点不引入新的热路径往返：全部是进程内 Prometheus 采样，age 由已存在的
// 快照时间戳算出，不额外访问 Redis。⚠️ 若将来有人想「顺便记一下 Redis 侧
// 的什么」，那会让这次观测反过来变成热路径的一次额外往返——禁止。
//
// 命名与基数（GW-00）：`llmgw_` 前缀；reason 是双值闭集
// （stale|unstamped），序列数上界 2，与请求/凭据/模型维度无关。
// credential_id / model 等绝不进入 label
// （metrics/label_cardinality_guard_test.go 会拦）。
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// 快照被年龄护栏丢弃的原因闭集。
const (
	// PrefetchDropReasonStale: 快照确实读过（有读取时刻），但年龄超过
	// prefetchMaxAgeSec。正常情况下这个计数应接近 0；若它在涨，说明
	// dispatch 排队超过了护栏上限，优化在尾部失效（功能仍正确，收益减少）。
	// 这正是「5s 是否合理」要回答的问题。
	PrefetchDropReasonStale = "stale"
	// PrefetchDropReasonUnstamped: 快照没有读取时刻（未经过本进程的读路径
	// 构造，例如手工构造的 NodeState 或 pre-migration 形状），年龄不可知
	// ⇒ 不得当作新鲜。持续非零说明有构造点绕过了读路径。
	PrefetchDropReasonUnstamped = "unstamped"
)

var prefetchDropReasonAllowlist = map[string]bool{
	PrefetchDropReasonStale:     true,
	PrefetchDropReasonUnstamped: true,
}

// NodeStatePrefetchDroppedTotal 统计因年龄护栏被丢弃的透传快照数。
var NodeStatePrefetchDroppedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "llmgw_node_state_prefetch_dropped_total",
	Help: "Router-prefetched node-state snapshots discarded by the capability-read age guard, by reason.",
}, []string{"reason"})

// NodeStatePrefetchAgeSeconds 观测**被采信**快照的实际年龄分布。
//
// Buckets 刻意从 1ms 起：路由 MGET 与闸门在常见路径上只差几毫秒
// （审计 2026-10-02 实测 ~6ms），若桶从 1s 起，稳态样本会全部落进
// 第一个桶，看不出分布。
//
// ⚠️ 顶档必须**超过**当前护栏上限（5s），否则直方图看不到「样本正在逼近
// 护栏」这个恰恰是要用来调参的信号。⚠️ ExponentialBuckets(start,factor,count)
// 的第 count 档是 start×factor^(count-1)：count=13 只到 4.096s，**低于**护栏。
// 这是 TestNodeStatePrefetch_BucketsCoverTheGuard 判红后修的——第一次按
// "0.001×2^13=8.192" 心算，忘了公式里那个 -1，档数少了一档。
var NodeStatePrefetchAgeSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
	Name: "llmgw_node_state_prefetch_age_seconds",
	Help: "Age of router-prefetched node-state snapshots at the capability gate, when trusted.",
	// 1ms .. ~8.2s, 2× 步进，共 14 档
	Buckets: prometheus.ExponentialBuckets(0.001, 2, 14),
})

func init() {
	for reason := range prefetchDropReasonAllowlist {
		NodeStatePrefetchDroppedTotal.WithLabelValues(reason).Add(0)
	}
}

// RecordNodeStatePrefetchDropped 在年龄护栏丢弃快照时调用。
// 纯指标，不改控制流；reason 会被归一到闭集，未知值落入 "unknown" 桶。
func RecordNodeStatePrefetchDropped(reason string) {
	if !prefetchDropReasonAllowlist[reason] {
		reason = "unknown"
	}
	NodeStatePrefetchDroppedTotal.WithLabelValues(reason).Inc()
}

// ObserveNodeStatePrefetchAge 在快照被采信时记录它的年龄。
//
// ⚠️ 与 RecordNodeStatePrefetchDropped 互斥：被丢弃的快照不记 age
// （它们没被采信，记进去会让分布偏向尾部、与护栏语义不符）。
func ObserveNodeStatePrefetchAge(seconds float64) {
	if seconds < 0 {
		return
	}
	NodeStatePrefetchAgeSeconds.Observe(seconds)
}
