// Package workers — OptimizationAdviser 的 SessionCloseHook 适配器。
//
// 将 OptimizationAdviser 包装为 SessionCloseHook，挂到 SessionSummaryWorker，
// 在会话关闭（总结生成后）自动检测优化建议。
package workers

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/domains/analysis"
)

// OptimizationCloseHook 把 OptimizationAdviser 适配为 SessionCloseHook。
type OptimizationCloseHook struct {
	adviser *analysis.OptimizationAdviser
}

// NewOptimizationCloseHook 构造适配器。
func NewOptimizationCloseHook(adviser *analysis.OptimizationAdviser) *OptimizationCloseHook {
	return &OptimizationCloseHook{adviser: adviser}
}

// OnSessionClosed 实现 SessionCloseHook。
func (h *OptimizationCloseHook) OnSessionClosed(ctx context.Context, tenantID, gwSessionID string) error {
	if h.adviser == nil {
		return nil
	}
	return h.adviser.Advise(ctx, tenantID, gwSessionID)
}
