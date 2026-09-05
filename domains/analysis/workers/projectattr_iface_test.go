package workers

import (
	"github.com/kaixuan/llm-gateway-go/domains/analysis/projectattr"
)

// projectattr.CloseHook 通过 AddCloseHook 挂到 SessionSummaryWorker 上，
// 与 OptimizationCloseHook 同一挂载点。这条编译期断言保证接口签名漂移时
// 立刻失败，而不是等到 main.go 接线时才发现。
var _ SessionCloseHook = (*projectattr.CloseHook)(nil)
