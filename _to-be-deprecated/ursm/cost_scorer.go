package ursm

// CostScorer 成本评分器（Task 3 实现）
type CostScorer struct {
	weights ScoringWeights
}

// NewCostScorer 创建成本评分器
func NewCostScorer(weights ScoringWeights) *CostScorer {
	return &CostScorer{
		weights: weights,
	}
}

// SortByCompositeScore 按综合评分排序
func (s *CostScorer) SortByCompositeScore(nodes []RouteNode) []RouteNode {
	// TODO: Task 3 - 实现成本评分排序
	return nodes
}
