package executors

import (
	"context"
	"math"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// headroomStrategy 偏好剩余并发余量（headroom）最大的候选。
//
// 复用现有 calculateHeadroom（router_scoring.go:196）的语义：headroom 越大
// 越健康。但 headroom 不能绕过健康过滤——不健康候选已在 Router 的 state/health
// 阶段被过滤，到 strategy 步的候选都是 routable 的。本策略只在 routable 候选
// 里选余量最大的。
//
// Unknown 值语义：ConcurrencyLimit 未知（nil）→ 中性分（不奖励也不惩罚），
// 不绕过健康过滤。用 0.5（headroom 中位）作为中性分。
//
// 评分方向：lower = better，headroom 越大越好 → 返回 (1 - headroom)。
type headroomStrategy struct {
	r *Router
}

func (s headroomStrategy) Name() string { return "headroom" }

func (s headroomStrategy) Score(ctx context.Context, c provider.Candidate, _ StrategyInput) (float64, error) {
	if c.ConcurrencyLimit == nil {
		// 并发上限未知 → 中性分，不绕过健康过滤。
		return 0.5, nil
	}
	// calculateHeadroom 返回 [0,1]，越大越好。lower=better → 1-headroom。
	h := calculateHeadroom(c, s.r)
	if h < 0 {
		h = 0
	}
	if h > 1 {
		h = 1
	}
	// calculateHeadroom now uses realtime in-flight pressure from s.r.Limiter
	// (when available), consistent with calculateLoadScore / P2C.
	return 1.0 - h, nil
}

// 避免 unused import（math 用于 MaxFloat64 的对称性，若未来加惩罚用）。
var _ = math.MaxFloat64
