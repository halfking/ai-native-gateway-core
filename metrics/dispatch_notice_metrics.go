// Package metrics - DispatchNotice 静默丢弃可观测性指标.
//
// 背景（v6 G-Ⅲ）：dispatch 在回队/切换时发出结构化 DispatchNotice
// （retry / node_switch / model_switch / queued / scheduled），投递链路为
// QueuedRequest.OnDispatchNotice → executor 桥（executor_dispatch.go）→
// params.OnNodeJump → preStream `: thinking:` SSE 注释通道。链路末端存在
// 两个此前完全无信号的静默丢弃点：
//
//  1. 非流式响应没有 SSE 思考通道，notice 无处投递（executor 桥上
//     OnNodeJump 为 nil，或 handler 侧闭包因 preStream == nil 直接吞掉）；
//  2. 流式请求的 preStream 未初始化（keepalive 功能关闭、
//     startPreStreamKeepalive 失败等），handler 闭包的
//     `if preStream != nil` 守卫静默丢弃。
//
// domains/dispatch 的 dispatch_notice_total 只统计"已送达 executor 桥"的
// notice，看不到桥之后的丢弃。本计数器补齐分子，丢弃率可按 kind 计算：
//
//	rate(llmgw_dispatch_notice_dropped_total[5m])
//	  / (rate(dispatch_notice_total[5m])
//	     + rate(llmgw_dispatch_notice_dropped_total[5m]))
//
// 命名遵循 llmgw_ 前缀（项目惯例）和 GW-00 低基数规范：
//
//	notice_kind 是 DispatchNoticeKind 闭集（retry|node_switch|model_switch|
//	queued|scheduled，未知归一为 unknown）；
//	reason 是双值闭集（non_streaming|prestream_uninit，未知归一为 unknown）。
//	序列数上界 6×3=18，与请求/租户/凭据/模型维度完全无关；
//	request_id / model / tenant_id 等高基数值绝不进入 label。
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// DispatchNotice 丢弃原因闭集。
const (
	// DispatchNoticeDropReasonNonStreaming: 请求是非流式响应，没有
	// `: thinking:` SSE 通道，notice 按设计无法呈现。
	DispatchNoticeDropReasonNonStreaming = "non_streaming"
	// DispatchNoticeDropReasonPreStreamUninit: 流式请求的 preStream
	// keepalive 未初始化（功能关闭 / 启动失败 / 未桥接 OnNodeJump），
	// notice 在 handler 闭包内部被吞掉。
	DispatchNoticeDropReasonPreStreamUninit = "prestream_uninit"
)

// dispatchNoticeDropKindAllowlist / dispatchNoticeDropReasonAllowlist 是
// label 值白名单：任何不在闭集内的输入一律归一为 "unknown"，保证 label
// 值域不随外部输入增长（基数硬上界）。
var dispatchNoticeDropKindAllowlist = map[string]bool{
	"retry":        true,
	"node_switch":  true,
	"model_switch": true,
	"queued":       true,
	"scheduled":    true,
}

var dispatchNoticeDropReasonAllowlist = map[string]bool{
	DispatchNoticeDropReasonNonStreaming:    true,
	DispatchNoticeDropReasonPreStreamUninit: true,
}

// DispatchNoticeDroppedTotal 统计到达 executor 传输桥后被静默丢弃的
// DispatchNotice。导出 Vec 供测试与旁路读取；生产写入一律走
// RecordDispatchNoticeDropped，以强制 label 归一化。
var DispatchNoticeDroppedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "llmgw_dispatch_notice_dropped_total",
	Help: "Dispatch notices silently dropped at the executor transport bridge, by notice kind and drop reason.",
}, []string{"notice_kind", "reason"})

func init() {
	// 预热全部闭集序列（0 值）：丢弃率面板从进程启动起就有完整分子，
	// 不依赖第一次丢弃事件才把序列拉出来（与 live_stream_overlay 同款）。
	for kind := range dispatchNoticeDropKindAllowlist {
		for reason := range dispatchNoticeDropReasonAllowlist {
			DispatchNoticeDroppedTotal.WithLabelValues(kind, reason).Add(0)
		}
	}
}

// RecordDispatchNoticeDropped 在 executor 桥的丢弃点调用（纯指标，不改
// 控制流）。kind 对应 dispatch.DispatchNoticeKind；两个参数都会被归一到
// 闭集，任何未知值（包括误传的 requestID / model 等）落入 "unknown" 桶。
func RecordDispatchNoticeDropped(kind, reason string) {
	DispatchNoticeDroppedTotal.WithLabelValues(
		normalizeDispatchNoticeDropLabel(kind, dispatchNoticeDropKindAllowlist),
		normalizeDispatchNoticeDropLabel(reason, dispatchNoticeDropReasonAllowlist),
	).Inc()
}

func normalizeDispatchNoticeDropLabel(value string, allowlist map[string]bool) string {
	if allowlist[value] {
		return value
	}
	return "unknown"
}
