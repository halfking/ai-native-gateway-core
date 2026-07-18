package quality

import (
	"context"
	"database/sql"
	"fmt"
)

// CostEfficiencyScorer L5 成本效益评分器（5% 权重）
//
// 数据来源：provider_metrics_hour 表（24小时窗口）
// 指标：
//   - 绝对成本（60% 权重）：每千 Token 成本
//   - 相对成本（40% 权重）：与基准模型对比
type CostEfficiencyScorer struct {
	db *sql.DB
}

// NewCostEfficiencyScorer 创建成本效益评分器
func NewCostEfficiencyScorer(db *sql.DB) *CostEfficiencyScorer {
	return &CostEfficiencyScorer{db: db}
}

// Name 返回评分器名称
func (s *CostEfficiencyScorer) Name() string {
	return "L5_CostEfficiency"
}

// Calculate 计算成本效益评分（0-100）
func (s *CostEfficiencyScorer) Calculate(ctx context.Context, providerID int64, modelName string) (float64, error) {
	// 查询 24 小时成本数据
	query := `
SELECT
    SUM(total_cost) as total_cost_24h,
    SUM(total_input_tokens + total_output_tokens) as total_tokens_24h
FROM provider_metrics_hour
WHERE provider_id = $1
  AND model_name = $2
  AND bucket >= NOW() - INTERVAL '24 hours'
`

	var totalCost, totalTokens sql.NullFloat64
	err := s.db.QueryRowContext(ctx, query, providerID, modelName).Scan(&totalCost, &totalTokens)
	if err != nil {
		return 0, fmt.Errorf("查询成本数据失败: %w", err)
	}

	// 如果没有数据，返回中性分数
	if !totalCost.Valid || !totalTokens.Valid || totalTokens.Float64 == 0 {
		return 60, nil
	}

	// 计算每千 Token 成本
	costPer1kTokens := (totalCost.Float64 / totalTokens.Float64) * 1000

	// 1. 绝对成本得分
	absoluteScore := s.calculateAbsoluteCostScore(costPer1kTokens)

	// 2. 相对成本得分（与基准对比）
	relativeScore, err := s.calculateRelativeCostScore(ctx, providerID, modelName, costPer1kTokens)
	if err != nil {
		// 如果没有基准数据，只用绝对成本
		return absoluteScore, nil
	}

	// 加权平均
	finalScore := absoluteScore*0.60 + relativeScore*0.40

	return clamp(finalScore, 0, 100), nil
}

// calculateAbsoluteCostScore 绝对成本得分
func (s *CostEfficiencyScorer) calculateAbsoluteCostScore(costPer1kTokens float64) float64 {
	switch {
	case costPer1kTokens <= 0.01: // ≤ $0.01/1k
		return 100
	case costPer1kTokens <= 0.05: // $0.01-0.05
		return 100 - (costPer1kTokens-0.01)*1000
	case costPer1kTokens <= 0.1: // $0.05-0.1
		return 90 - (costPer1kTokens-0.05)*800
	default: // > $0.1
		score := 50 - (costPer1kTokens-0.1)*500
		return clamp(score, 0, 100)
	}
}

// calculateRelativeCostScore 相对成本得分（与所有供应商的中位数对比）
func (s *CostEfficiencyScorer) calculateRelativeCostScore(ctx context.Context, providerID int64, modelName string, currentCost float64) (float64, error) {
	// 查询同类模型的中位数成本（作为基准）
	query := `
WITH costs AS (
    SELECT
        provider_id,
        model_name,
        SUM(total_cost) / NULLIF(SUM(total_input_tokens + total_output_tokens), 0) * 1000 as cost_per_1k
    FROM provider_metrics_hour
    WHERE bucket >= NOW() - INTERVAL '24 hours'
    GROUP BY provider_id, model_name
    HAVING SUM(total_input_tokens + total_output_tokens) > 0
)
SELECT PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY cost_per_1k) as median_cost
FROM costs
WHERE model_name = $1
`

	var medianCost sql.NullFloat64
	err := s.db.QueryRowContext(ctx, query, modelName).Scan(&medianCost)
	if err != nil || !medianCost.Valid || medianCost.Float64 == 0 {
		return 0, fmt.Errorf("查询基准成本失败: %w", err)
	}

	// 相对得分：成本比基准低 → 高分；成本比基准高 → 低分
	ratio := medianCost.Float64 / currentCost
	score := 100 * ratio

	// 封顶 120，保底 0
	return clamp(score, 0, 120), nil
}
