// Package observability —— 会话存储降级指标（Subtask 4，handoff §6）。
//
// 背景：admin 的会话列表 / 详情 / 轮次三个读端点在数据库不可用时，此前把
// withTenantTx 的失败一律折叠成 HTTP 500。500 的语义是「服务端出错了」，
// 会让上游告警、值班处置和客户端重试策略全部走错分支：数据库挂掉其实是
// 「依赖降级」，应当是 503 + 明确的 storage_status，让调用方知道这是可重试
// 的、且与自己发来的请求无关。
//
// 本文件提供该降级路径的可观测性：每次降级按 component 打点一次，并在
// 降级请求在途期间抬高 pending gauge，便于判断「还在持续降级」还是
// 「偶发抖动」。注册在 prometheus.DefaultRegisterer 上，与本包既有
// MockProbe* 指标同一注册表（gateway-v2 的 /metrics 用
// promhttp.HandlerFor(prometheus.DefaultGatherer) 输出它），未发生降级时
// 两个指标都零输出、零开销。
package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// StorageDegradedComponent 是 component 标签的取值集合（低基数、固定档）。
// 新增读端点时在此登记，避免各处硬编码字符串导致标签基数失控。
const (
	StorageComponentList   = "list"
	StorageComponentDetail = "detail"
	StorageComponentTurns  = "turns"
	// StorageComponentSummary（2026-09-29 审计二十一轮）: 会话摘要/标题
	// 家族端点（/api/admin/sessions/summary、instant-summary、session_title）。
	StorageComponentSummary = "summary"
)

var (
	// SessionStorageDegradedTotal 统计按 component 归类的存储降级次数。
	SessionStorageDegradedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "session_storage_degraded_total",
			Help: "Total number of admin session read requests answered with 503 storage_unavailable.",
		},
		[]string{"component"},
	)
	// SessionStorageDegradedPending 是当前在途的降级请求数。
	// 降级判定到写响应之间 Inc、defer Dec，因此它反映的是「正在被判定为
	// 降级」的并发量，而不是累计失败量（那是 counter 的活）。
	SessionStorageDegradedPending = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "session_storage_degraded_pending",
			Help: "In-flight admin session read requests currently being answered as storage-degraded.",
		},
	)
)

// RecordStorageDegraded 在判定某个 component 的读请求因存储不可用而降级时
// 调用：抬高 pending 并给对应 component 计数一次。调用方应在写出 503 响应
// 后（或随响应一起）调用 StorageDegradedDone 回落 pending。
//
// 未知 component 不会被静默丢弃——它会照常计入自己那个标签值，方便在
// /metrics 上直接看到是谁没来登记，而不是悄悄少一个序列。
func RecordStorageDegraded(component string) {
	SessionStorageDegradedTotal.WithLabelValues(component).Inc()
	SessionStorageDegradedPending.Inc()
}

// StorageDegradedDone 与 RecordStorageDegraded 配对使用，defer 调用以确保
// 任何提前 return 的路径都不会把 pending 永久抬高（那会让 gauge 单调虚增，
// 看上去像降级从未恢复）。
func StorageDegradedDone() {
	SessionStorageDegradedPending.Dec()
}
