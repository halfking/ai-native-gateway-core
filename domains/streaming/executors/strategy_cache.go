package executors

import (
	"context"
	"math"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// cacheOptimizedStrategy 偏好支持 prompt cache 且 cache read 价格最低的候选。
//
// Unknown 值语义：不支持 prompt cache 的候选 → 最大惩罚，排到支持 cache 的之后。
// 支持 cache 但 CacheReadPricePer1M 未知（nil）→ 用一个固定的高惩罚分（不是免费），
// 优于"不支持"但劣于"已知低价 cache"。
type cacheOptimizedStrategy struct{}

func (cacheOptimizedStrategy) Name() string { return "cache-optimized" }

func (s cacheOptimizedStrategy) Score(_ context.Context, c provider.Candidate, _ StrategyInput) (float64, error) {
	if !c.SupportsPromptCache {
		// 不支持 cache → 最大惩罚。
		return math.MaxFloat64, nil
	}
	if c.CacheReadPricePer1M == nil {
		// 支持 cache 但价格未知 → 高惩罚（不是免费），但优于不支持。
		// 选一个小于 MaxFloat64 的大数：1e6 per 1M（远高于任何真实 cache 价）。
		return 1e6, nil
	}
	return *c.CacheReadPricePer1M, nil
}
