package quality

import (
	"context"
	"database/sql"
	"fmt"
)

// PerformanceScorer L2 性能评分器（25% 权重）
//
// 数据来源：provider_metrics_hour 表（最近 1 小时）
// 指标：
//   - P95 延迟（50% 权重）
//   - P99 延迟（30% 权重）
//   - TTFT P95（20% 权重）
type PerformanceScorer struct {
	db *sql.DB
}

// NewPerformanceScorer 创建性能评分器
func NewPerformanceScorer(db *sql.DB) *PerformanceScorer {
	return &PerformanceScorer{db: db}
}

// Name 返回评分器名称
func (s *PerformanceScorer) Name() string {
	return "L2_Performance"
}

// Calculate 计算性能评分（0-100）
func (s *PerformanceScorer) Calculate(ctx context.Context, providerID int64, modelName string) (float64, error) {
	// 查询最近 1 小时数据
	query := `
SELECT
    latency_p95,
    latency_p99,
    ttft_p95
FROM provider_metrics_hour
WHERE provider_id = $1
  AND model_name = $2
  AND bucket >= NOW() - INTERVAL '1 hour'
ORDER BY bucket DESC
LIMIT 1
`

	var latencyP95, latencyP99, ttftP95 sql.NullFloat64

	err := s.db.QueryRowContext(ctx, query, providerID, modelName).Scan(
		&latencyP95,
		&latencyP99,
		&ttftP95,
	)
	if err == sql.ErrNoRows {
		// 没有数据，返回 0 分
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("查询性能数据失败: %w", err)
	}

	// 计算各项得分
	p95Score := s.calculateLatencyScore(latencyP95.Float64, 1.0)
	p99Score := s.calculateLatencyScore(latencyP99.Float64, 1.5) // P99 阈值放宽 1.5x
	ttftScore := s.calculateTTFTScore(ttftP95.Float64)

	// 加权平均
	finalScore := p95Score*0.50 +
		p99Score*0.30 +
		ttftScore*0.20

	return clamp(finalScore, 0, 100), nil
}

// calculateLatencyScore 延迟得分（分段线性）
//
// 参数：
//   - latency: 延迟（毫秒）
//   - factor: 阈值因子（P99 用 1.5）
func (s *PerformanceScorer) calculateLatencyScore(latency float64, factor float64) float64 {
	// 阈值
	t1 := 500 * factor   // 优秀阈值
	t2 := 1000 * factor  // 良好阈值
	t3 := 3000 * factor  // 及格阈值

	switch {
	case latency <= t1:
		return 100
	case latency <= t2:
		// 500-1000: 每增加100ms扣20分
		return 100 - (latency-t1)/5
	case latency <= t3:
		// 1000-3000: 每增加1s扣25分
		return 90 - (latency-t2)/40
	default:
		// > 3000: 每增加1s再扣10分
		score := 40 - (latency-t3)/100
		return clamp(score, 0, 100)
	}
}

// calculateTTFTScore TTFT 得分（分段线性）
func (s *PerformanceScorer) calculateTTFTScore(ttft float64) float64 {
	switch {
	case ttft <= 200:
		return 100
	case ttft <= 500:
		// 200-500: 每增加100ms扣33分
		return 100 - (ttft-200)/3
	case ttft <= 1000:
		// 500-1000: 每增加100ms扣10分
		return 90 - (ttft-500)/10
	default:
		// > 1000: 每增加100ms扣8分
		score := 40 - (ttft-1000)/50
		return clamp(score, 0, 100)
	}
}
