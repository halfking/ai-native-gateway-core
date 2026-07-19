package quality

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/kaixuan/llm-gateway-go/pkg/logger"
)

// AvailabilityScorer L1 可用性评分器（35% 权重）
//
// 数据来源：provider_metrics_hour 表（24小时窗口）
// 指标：
//   - 成功率（70% 权重）
//   - 5xx 错误率（20% 权重）
//   - 4xx 错误率（5% 权重）
//   - 超时率（5% 权重）
type AvailabilityScorer struct {
	db     *sql.DB
	logger logger.Logger
}

// NewAvailabilityScorer 创建可用性评分器
func NewAvailabilityScorer(db *sql.DB) *AvailabilityScorer {
	return &AvailabilityScorer{
		db:     db,
		logger: logger.NewWithComponent("quality", "availability"),
	}
}

// Name 返回评分器名称
func (s *AvailabilityScorer) Name() string {
	return "L1_Availability"
}

// Calculate 计算可用性评分（0-100）
func (s *AvailabilityScorer) Calculate(ctx context.Context, providerID int64, modelName string) (float64, error) {
	s.logger.Debug("calculating availability score",
		"provider_id", providerID,
		"model", modelName,
	)

	// 查询 24 小时数据
	query := `
SELECT
    AVG(COALESCE(success_rate, 0)) as success_rate_24h,
    AVG(COALESCE(error_rate_5xx, 0)) as error_rate_5xx_24h,
    100.0 * SUM(COALESCE(error_4xx, 0)) / NULLIF(SUM(total_requests), 0) as error_rate_4xx_24h,
    100.0 * SUM(COALESCE(error_timeout, 0)) / NULLIF(SUM(total_requests), 0) as timeout_rate_24h,
    SUM(total_requests) as total_requests_24h
FROM provider_metrics_hour
WHERE provider_id = $1
  AND model_name = $2
  AND bucket >= NOW() - INTERVAL '24 hours'
`

	var successRate, error5xxRate, error4xxRate, timeoutRate sql.NullFloat64
	var totalRequests sql.NullInt64

	err := s.db.QueryRowContext(ctx, query, providerID, modelName).Scan(
		&successRate,
		&error5xxRate,
		&error4xxRate,
		&timeoutRate,
		&totalRequests,
	)
	if err != nil {
		s.logger.Error("failed to query availability data",
			"provider_id", providerID,
			"model", modelName,
			"error", err.Error(),
		)
		return 0, fmt.Errorf("查询可用性数据失败: %w", err)
	}

	// 如果没有数据，返回 0 分
	if !totalRequests.Valid || totalRequests.Int64 == 0 {
		s.logger.Warn("no availability data",
			"provider_id", providerID,
			"model", modelName,
		)
		return 0, nil
	}

	// 计算各项得分
	successScore := s.calculateSuccessScore(successRate.Float64)
	error5xxScore := s.calculate5xxScore(error5xxRate.Float64)
	error4xxScore := s.calculate4xxScore(error4xxRate.Float64)
	timeoutScore := s.calculateTimeoutScore(timeoutRate.Float64)

	// 加权平均
	finalScore := successScore*0.70 +
		error5xxScore*0.20 +
		error4xxScore*0.05 +
		timeoutScore*0.05

	s.logger.Info("availability score calculated",
		"provider_id", providerID,
		"model", modelName,
		"score", finalScore,
		"success_rate", successRate.Float64,
		"error_5xx_rate", error5xxRate.Float64,
		"total_requests", totalRequests.Int64,
	)

	return clamp(finalScore, 0, 100), nil
}

// calculateSuccessScore 成功率得分（线性）
func (s *AvailabilityScorer) calculateSuccessScore(successRate float64) float64 {
	return successRate // 0-100 直接映射
}

// calculate5xxScore 5xx错误率得分（5xx每增加1%扣50分）
func (s *AvailabilityScorer) calculate5xxScore(error5xxRate float64) float64 {
	score := 100 - error5xxRate*5 // 每1%扣5分
	return clamp(score, 0, 100)
}

// calculate4xxScore 4xx错误率得分（4xx每增加1%扣20分）
func (s *AvailabilityScorer) calculate4xxScore(error4xxRate float64) float64 {
	score := 100 - error4xxRate*2 // 每1%扣2分
	return clamp(score, 0, 100)
}

// calculateTimeoutScore 超时率得分（超时每增加1%扣30分）
func (s *AvailabilityScorer) calculateTimeoutScore(timeoutRate float64) float64 {
	score := 100 - timeoutRate*3 // 每1%扣3分
	return clamp(score, 0, 100)
}
