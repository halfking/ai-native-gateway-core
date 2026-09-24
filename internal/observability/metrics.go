// Package observability —— mock probe 指标注册（2026-09-24，
// docs/design/2026-09-23-mock-probe-channel §3.3）。
//
// 所有 mock probe 指标强制携带 scope=ScopeMockProbe 标签，与真实请求
// 指标（requests_total 等自研 Registry）区分。注册在
// prometheus.DefaultRegisterer 上：gateway-v2 的 /metrics 已用
// promhttp.HandlerFor(prometheus.DefaultGatherer) 输出该注册表，
// 因此 CounterVec/HistogramVec 在首次打点后自动出现在 /metrics，
// 未启用子系统时（无子序列）零输出、零开销。
package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ScopeMockProbe 是 scope 标签的统一常量；所有 mock probe 指标写入时
// 必须用它，避免误用其它 scope 名。
const ScopeMockProbe = "mock_probe"

// MockProbeMetric Labels（固定维度，低基数）：
//   - supplier: "mock-fast" | "mock-slow"
//   - stream:   "true" | "false"
//   - status:   "ok" | "error"（明细进 mock_probe_history.error_code）
var (
	MockProbeRequestTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mock_probe_request_total",
			Help: "Total number of mock probe requests dispatched.",
		},
		[]string{"scope", "supplier", "stream", "status"},
	)
	// 注意 Name 不带 _bucket 后缀：client_golang 会自动为 histogram 的
	// 桶序列追加 _bucket，最终暴露 mock_probe_latency_seconds_bucket
	// {scope="mock_probe",...}（与设计 §一 图中序列名一致）。
	MockProbeLatencySeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "mock_probe_latency_seconds",
			Help:    "Mock probe request latency in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"scope", "supplier", "stream"},
	)
)
