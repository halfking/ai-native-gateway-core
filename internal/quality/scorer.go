package quality

import (
	"context"
	"database/sql"
)

// Scorer 评分器接口
//
// 每个维度实现一个 Scorer，输出 0-100 分。
type Scorer interface {
	// Calculate 计算指定供应商和模型的评分
	Calculate(ctx context.Context, providerID int64, modelName string) (float64, error)

	// Name 返回评分器名称（用于日志）
	Name() string
}

// ScoreResult 评分结果
type ScoreResult struct {
	ProviderID int64
	ModelName  string

	// 五个维度评分（0-100）
	AvailabilityScore   float64 // L1 可用性（35% 权重）
	PerformanceScore    float64 // L2 性能（25% 权重）
	ReliabilityScore    float64 // L3 可信度（20% 权重）
	StabilityScore      float64 // L4 稳定性（15% 权重）
	CostEfficiencyScore float64 // L5 成本效益（5% 权重）

	// 综合质量分（加权平均）
	QualityScore float64

	// 等级（S/A/B/C/D）
	Grade string
}

// CalculateQualityScore 计算综合质量分（加权平均）
func (r *ScoreResult) CalculateQualityScore() {
	r.QualityScore = r.AvailabilityScore*0.35 +
		r.PerformanceScore*0.25 +
		r.ReliabilityScore*0.20 +
		r.StabilityScore*0.15 +
		r.CostEfficiencyScore*0.05

	// 计算等级
	switch {
	case r.QualityScore >= 90:
		r.Grade = "S"
	case r.QualityScore >= 80:
		r.Grade = "A"
	case r.QualityScore >= 70:
		r.Grade = "B"
	case r.QualityScore >= 60:
		r.Grade = "C"
	default:
		r.Grade = "D"
	}
}

// ProfileCalculator 质量画像计算器
type ProfileCalculator struct {
	db *sql.DB

	// 五个维度的评分器
	availabilityScorer   Scorer
	performanceScorer    Scorer
	reliabilityScorer    Scorer
	stabilityScorer      Scorer
	costEfficiencyScorer Scorer
}

// NewProfileCalculator 创建质量画像计算器
func NewProfileCalculator(db *sql.DB) *ProfileCalculator {
	return &ProfileCalculator{
		db:                   db,
		availabilityScorer:   NewAvailabilityScorer(db),
		performanceScorer:    NewPerformanceScorer(db),
		reliabilityScorer:    NewReliabilityScorer(db),
		stabilityScorer:      NewReliabilityScorer(db), // TODO: 实现 StabilityScorer
		costEfficiencyScorer: NewReliabilityScorer(db), // TODO: 实现 CostEfficiencyScorer
	}
}

// Calculate 计算质量画像
func (c *ProfileCalculator) Calculate(ctx context.Context, providerID int64, modelName string) (*ScoreResult, error) {
	result := &ScoreResult{
		ProviderID: providerID,
		ModelName:  modelName,
	}

	var err error

	// L1 可用性评分
	result.AvailabilityScore, err = c.availabilityScorer.Calculate(ctx, providerID, modelName)
	if err != nil {
		return nil, err
	}

	// L2 性能评分
	result.PerformanceScore, err = c.performanceScorer.Calculate(ctx, providerID, modelName)
	if err != nil {
		return nil, err
	}

	// L3 可信度评分
	result.ReliabilityScore, err = c.reliabilityScorer.Calculate(ctx, providerID, modelName)
	if err != nil {
		return nil, err
	}

	// L4 稳定性评分
	result.StabilityScore, err = c.stabilityScorer.Calculate(ctx, providerID, modelName)
	if err != nil {
		return nil, err
	}

	// L5 成本效益评分
	result.CostEfficiencyScore, err = c.costEfficiencyScorer.Calculate(ctx, providerID, modelName)
	if err != nil {
		return nil, err
	}

	// 计算综合质量分和等级
	result.CalculateQualityScore()

	return result, nil
}

// clamp 限制值在 [min, max] 范围内
func clamp(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
