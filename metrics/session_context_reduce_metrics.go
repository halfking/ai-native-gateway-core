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
