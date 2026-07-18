package quality

import (
	"context"
	"database/sql"
	"fmt"
)

// StabilityScorer L4 稳定性评分器（15% 权重）
//
// 数据来源：provider_metrics_minute 表（最近 5 分钟）
// 指标：
//   - 延迟抖动（40% 权重）：P95 的变异系数
//   - 流量波动（30% 权重）：请求量的变异系数
//   - 错误突增（30% 权重）：错误率突增次数
type StabilityScorer struct {
	db *sql.DB
}

// NewStabilityScorer 创建稳定性评分器
func NewStabilityScorer(db *sql.DB) *StabilityScorer {
	return &StabilityScorer{db: db}
}

// Name 返回评分器名称
func (s *StabilityScorer) Name() string {
	return "L4_Stability"
}

// Calculate 计算稳定性评分（0-100）
func (s *StabilityScorer) Calculate(ctx context.Context, providerID int64, modelName string) (float64, error) {
	// 查询最近 5 分钟数据
	query := `
SELECT
    AVG(COALESCE(latency_p95, 0)) as mean_latency,
    STDDEV(COALESCE(latency_p95, 0)) as stddev_latency,
    AVG(total_requests) as mean_requests,
    STDDEV(total_requests) as stddev_requests,
    COUNT(*) as sample_count
FROM provider_metrics_minute
WHERE provider_id = $1
  AND model_name = $2
  AND bucket >= NOW() - INTERVAL '5 minutes'
`

	var meanLatency, stddevLatency, meanRequests, stddevRequests sql.NullFloat64
	var sampleCount int

	err := s.db.QueryRowContext(ctx, query, providerID, modelName).Scan(
		&meanLatency,
		&stddevLatency,
		&meanRequests,
		&stddevRequests,
		&sampleCount,
	)
	if err != nil {
		return 0, fmt.Errorf("查询稳定性数据失败: %w", err)
	}

	// 如果样本不足，返回中性分数
	if sampleCount < 2 {
		return 60, nil
	}

	// 1. 延迟抖动得分
	jitterScore := s.calculateJitterScore(meanLatency.Float64, stddevLatency.Float64)

	// 2. 流量波动得分
	trafficScore := s.calculateTrafficScore(meanRequests.Float64, stddevRequests.Float64)

	// 3. 错误突增得分
	spikeScore, err := s.calculateSpikeScore(ctx, providerID, modelName)
	if err != nil {
		return 0, err
	}

	// 加权平均
	finalScore := jitterScore*0.40 +
		trafficScore*0.30 +
		spikeScore*0.30

	return clamp(finalScore, 0, 100), nil
}

// calculateJitterScore 延迟抖动得分（变异系数）
func (s *StabilityScorer) calculateJitterScore(mean, stddev float64) float64 {
	if mean == 0 {
		return 100
	}

	cv := stddev / mean // 变异系数

	switch {
	case cv < 0.1:
		return 100
	case cv < 0.3:
		return 100 - cv*200
	default:
		score := 40 - (cv-0.3)*100
		return clamp(score, 0, 100)
	}
}

// calculateTrafficScore 流量波动得分（变异系数）
func (s *StabilityScorer) calculateTrafficScore(mean, stddev float64) float64 {
	if mean == 0 {
		return 100
	}

	cv := stddev / mean

	switch {
	case cv < 0.2:
		return 100
	case cv < 0.5:
		return 100 - cv*150
	default:
		score := 50 - (cv-0.5)*100
		return clamp(score, 0, 100)
	}
}

// calculateSpikeScore 错误突增得分
func (s *StabilityScorer) calculateSpikeScore(ctx context.Context, providerID int64, modelName string) (float64, error) {
	query := `
WITH stats AS (
    SELECT
        AVG(100.0 * error_5xx / NULLIF(total_requests, 0)) as mean_error_rate
    FROM provider_metrics_minute
    WHERE provider_id = $1
      AND model_name = $2
      AND bucket >= NOW() - INTERVAL '5 minutes'
)
SELECT
    COUNT(*) FILTER (
        WHERE (100.0 * m.error_5xx / NULLIF(m.total_requests, 0)) > s.mean_error_rate * 2
    ) as spike_count
FROM provider_metrics_minute m, stats s
WHERE m.provider_id = $1
  AND m.model_name = $2
  AND m.bucket >= NOW() - INTERVAL '5 minutes'
`

	var spikeCount int
	err := s.db.QueryRowContext(ctx, query, providerID, modelName).Scan(&spikeCount)
	if err != nil {
		return 0, fmt.Errorf("查询错误突增失败: %w", err)
	}

	// 评分逻辑
	switch spikeCount {
	case 0:
		return 100, nil
	case 1:
		return 80, nil
	case 2:
		return 60, nil
	default:
		return 40, nil
	}
}
