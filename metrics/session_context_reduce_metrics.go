package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// 大请求体入口归约的可观测性（2026-11-23，大请求体归约提速轮）。
//
// 归约（LargeIngress → sessioncontext.Reduce）此前没有任何序列：7.7MB 的会话
// 归约要 128 秒还是 5 秒，只能靠翻请求日志的 duration_ms 反推。
//
// parallel：sequential（守卫未声明可并发）/ parallel（逐条并行）。
// outcome：ok / err。
var SessionContextReduceDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "session_context_reduce_duration_seconds",
	Help:    "Wall time of large-request context reduction (history guard + excerpt assembly).",
	Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120},
}, []string{"parallel", "outcome"})

// SessionContextReduceMessages 观测一次归约处理的消息数……
var SessionContextReduceMessages = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "session_context_reduce_messages",
	Help:    "Number of messages in the conversation being reduced.",
	Buckets: []float64{10, 50, 100, 300, 1000, 3000, 10000, 30000},
}, []string{"outcome"})

// SessionContextDigestSegments 计数一次归约里「直接命中段缓存的段」与「需要现算的段」。
// cache：hit / miss（未命中才要现算——这个比值就是「这一轮只处理了新增消息」的直接量具）。
var SessionContextDigestSegments = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "session_context_digest_segments_total",
	Help: "Conversation digest segments per reduce, by whether the per-segment summary came from cache.",
}, []string{"cache"})

// SessionContextDigestBytes 是最终摘要正文的大小。
var SessionContextDigestBytes = promauto.NewHistogram(prometheus.HistogramOpts{
	Name:    "session_context_digest_bytes",
	Help:    "Size of the conversation digest produced by a reduce, in bytes.",
	Buckets: []float64{1024, 2048, 4096, 8192, 16384, 32768, 65536},
})

// SessionContextReduceOmitted 累计被历史守卫判为不安全、整条省略的历史消息数。
//
// ★ 2026-11-23：这个数此前只存在于归约的内部报告里，线上看不见——守卫只看正则
// 时的误伤（「tokens」「keyboard」…，211 真实会话 57% 的历史被丢）因此藏了很久。
// 与 SessionContextReduceMessages 的总和一比就是「省略率」。
var SessionContextReduceOmitted = promauto.NewCounter(prometheus.CounterOpts{
	Name: "session_context_reduce_omitted_total",
	Help: "Historical messages omitted by the history guard (judged unsafe), summed over reduces.",
})
