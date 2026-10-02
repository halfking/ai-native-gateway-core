package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Output-gate observability (第三十轮, 2026-10-02；第二十八轮 §四#3 收口).
//
// 输出闸（sanitize 输出守卫 + outputcompliance 合规层 + response chain 的
// FailClosed 语义）此前的 block/mask 决策只有 slog 与终态码，没有专属
// series——拦截率、credential 键名硬阻断曲线（output_sensitive.go 的
// 无条件 Blocked）、checker 故障 fail-closed 量都不可见。sanitize 包按
// 既定决策不引 prometheus（保轻依赖），故落在根 metrics 包，由
// outputcompliance（gate 层）与 hooks/response（chain 层）两个消费方接线；
// 两包引叶 metrics 均无环。
var (
	// OutputGateDecisionsTotal counts every output-gate decision.
	// gate 闭集：output_compliance（outputcompliance 层决策点）。
	// action 闭集：block / redact / observe / allow。
	// path 闭集：body（非流式或流结束的整体验）/ stream_lane /
	//   stream_comment / stream_event（流中三 text lane）。
	OutputGateDecisionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "output_gate_decisions_total",
		Help: "Output gate decisions by gate, action and path.",
	}, []string{"gate", "action", "path"})

	// OutputGateFailClosedErrorsTotal counts fail-closed withholdings:
	// checker 错误、transform 错误、chain 层 FailClosed interceptor 错误——
	// 每一次都是"整响应/整流被扣下"的可用性事件，必须有独立曲线。
	// source 闭集：transform_error / chain_fail_closed。
	OutputGateFailClosedErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "output_gate_fail_closed_errors_total",
		Help: "Output gate fail-closed withholdings by gate and source.",
	}, []string{"gate", "source"})
)

// Gate name / action / path / source label values (closed enums).
const (
	OutputGateNameCompliance = "output_compliance"

	OutputGateActionBlock   = "block"
	OutputGateActionRedact  = "redact"
	OutputGateActionObserve = "observe"
	OutputGateActionAllow   = "allow"

	OutputGatePathBody          = "body"
	OutputGatePathStreamLane    = "stream_lane"
	OutputGatePathStreamComment = "stream_comment"
	OutputGatePathStreamEvent   = "stream_event"

	OutputGateSourceTransformError = "transform_error"
	OutputGateSourceChainFailClose = "chain_fail_closed"
)

func init() {
	// 预热全部 series：带 label 的 Vec 首次 Inc 前不产出样本，仪表盘会呈现
	// "series 不存在"而非 0（R81 empty_response 白名单漏族同型教训）。
	for _, action := range []string{
		OutputGateActionBlock, OutputGateActionRedact,
		OutputGateActionObserve, OutputGateActionAllow,
	} {
		OutputGateDecisionsTotal.WithLabelValues(OutputGateNameCompliance, action, OutputGatePathBody).Add(0)
		OutputGateDecisionsTotal.WithLabelValues(OutputGateNameCompliance, action, OutputGatePathStreamLane).Add(0)
		OutputGateDecisionsTotal.WithLabelValues(OutputGateNameCompliance, action, OutputGatePathStreamComment).Add(0)
		OutputGateDecisionsTotal.WithLabelValues(OutputGateNameCompliance, action, OutputGatePathStreamEvent).Add(0)
	}
	for _, source := range []string{
		OutputGateSourceTransformError, OutputGateSourceChainFailClose,
	} {
		OutputGateFailClosedErrorsTotal.WithLabelValues(OutputGateNameCompliance, source).Add(0)
	}
}
