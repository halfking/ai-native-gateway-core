package quality

import (
	"context"
	"database/sql"
	"fmt"
	"math"
)

// ReliabilityScorer L3 可信度评分器（20% 权重）
//
// 数据来源：provider_metrics_hour 表（7天历史）
// 指标：
//   - 在线率（50% 权重）：有数据的小时数 / 168 小时
//   - 一致性（30% 权重）：成功率标准差越小越好
//   - 趋势（20% 权重）：近3天 vs 前4天的错误率变化
type ReliabilityScorer struct {
	db *sql.DB
}

// NewReliabilityScorer 创建可信度评分器
func NewReliabilityScorer(db *sql.DB) *ReliabilityScorer {
	return &ReliabilityScorer{db: db}
}

// Name 返回评分器名称
func (s *ReliabilityScorer) Name() string {
	return "L3_Reliability"
}

// Calculate 计算可信度评分（0-100）
func (s *ReliabilityScorer) Calculate(ctx context.Context, providerID int64, modelName string) (float64, error) {
	// 1. 计算在线率
	uptimeScore, err := s.calculateUptimeScore(ctx, providerID, modelName)
	if err != nil {
		return 0, err
	}

	// 2. 计算一致性
	consistencyScore, err := s.calculateConsistencyScore(ctx, providerID, modelName)
	if err != nil {
		return 0, err
	}

	// 3. 计算趋势
	trendScore, err := s.calculateTrendScore(ctx, providerID, modelName)
	if err != nil {
		return 0, err
	}

	// 加权平均
	finalScore := uptimeScore*0.50 +
		consistencyScore*0.30 +
		trendScore*0.20

	return clamp(finalScore, 0, 100), nil
}

// calculateUptimeScore 在线率得分
func (s *ReliabilityScorer) calculateUptimeScore(ctx context.Context, providerID int64, modelName string) (float64, error) {
	query := `
SELECT COUNT(DISTINCT bucket) as hours_with_data
FROM provider_metrics_hour
WHERE provider_id = $1
  AND model_name = $2
  AND bucket >= NOW() - INTERVAL '7 days'
`

	var hoursWithData int
	err := s.db.QueryRowContext(ctx, query, providerID, modelName).Scan(&hoursWithData)
	if err != nil {
		return 0, fmt.Errorf("查询在线率失败: %w", err)
	}

	// 7 天 = 168 小时
	uptimeRatio := float64(hoursWithData) / 168.0
	return uptimeRatio * 100, nil
}

// calculateConsistencyScore 一致性得分（成功率标准差越小越好）
func (s *ReliabilityScorer) calculateConsistencyScore(ctx context.Context, providerID int64, modelName string) (float64, error) {
	query := `
SELECT
    AVG(COALESCE(success_rate, 0)) as mean_success_rate,
    STDDEV(COALESCE(success_rate, 0)) as stddev_success_rate
FROM provider_metrics_hour
WHERE provider_id = $1
  AND model_name = $2
  AND bucket >= NOW() - INTERVAL '7 days'
`

	var meanRate, stddevRate sql.NullFloat64
	err := s.db.QueryRowContext(ctx, query, providerID, modelName).Scan(&meanRate, &stddevRate)
	if err != nil {
		return 0, fmt.Errorf("查询一致性失败: %w", err)
	}

	if !stddevRate.Valid {
		return 100, nil // 没有数据或标准差为 0，视为完美一致
	}

	// 标准差越小越好
	// stddev < 5: 100 分
	// stddev 5-20: 线性递减
	// stddev > 20: 低分
	score := 100 - stddevRate.Float64*2
	return clamp(score, 0, 100), nil
}

// calculateTrendScore 趋势得分（近3天 vs 前4天的错误率变化）
func (s *ReliabilityScorer) calculateTrendScore(ctx context.Context, providerID int64, modelName string) (float64, error) {
	query := `
SELECT
    AVG(CASE WHEN bucket >= NOW() - INTERVAL '3 days' THEN error_rate_5xx ELSE NULL END) as recent_error_rate,
    AVG(CASE WHEN bucket < NOW() - INTERVAL '3 days' THEN error_rate_5xx ELSE NULL END) as previous_error_rate
FROM provider_metrics_hour
WHERE provider_id = $1
  AND model_name = $2
  AND bucket >= NOW() - INTERVAL '7 days'
`

	var recentErrorRate, previousErrorRate sql.NullFloat64
	err := s.db.QueryRowContext(ctx, query, providerID, modelName).Scan(&recentErrorRate, &previousErrorRate)
	if err != nil {
		return 0, fmt.Errorf("查询趋势失败: %w", err)
	}

	// 如果没有足够数据，返回中性分数
	if !recentErrorRate.Valid || !previousErrorRate.Valid {
		return 60, nil
	}

	// 计算变化率
	change := previousErrorRate.Float64 - recentErrorRate.Float64
	changeRatio := 0.0
	if previousErrorRate.Float64 > 0 {
		changeRatio = change / previousErrorRate.Float64 * 100 // 百分比
	}

	// 评分逻辑
	switch {
	case changeRatio > 20: // 错误率改善 > 20%
		return 100, nil
	case changeRatio > 0: // 改善 0-20%
		return 80 + changeRatio, nil
	case math.Abs(changeRatio) < 5: // 持平（±5%）
		return 60, nil
	case changeRatio > -20: // 恶化 < 20%
		return 40 - changeRatio, nil
	default: // 恶化 > 20%
		return clamp(20+changeRatio, 0, 40), nil
	}
}
