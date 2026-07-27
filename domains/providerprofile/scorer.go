package providerprofile

import "math"

// Scorer 评分器接口
type Scorer interface {
	// CalculateDimensionScores 计算各维度分数
	CalculateDimensionScores(snapshots []*MetricSnapshot, weights ProfileWeights) *DimensionScores
	
	// CalculateTotalScore 计算总分
	CalculateTotalScore(scores *DimensionScores, weights ProfileWeights) float64
}

// DimensionScores 各维度分数
type DimensionScores struct {
	NetworkScore      float64
	AvailabilityScore float64
	StabilityScore    float64
	ScaleScore        float64
	
	// 以下维度暂时设为0，后续阶段实现
	CredibilityScore  float64
	CostAccuracyScore float64
	PriceScore        float64
}

// DefaultScorer 默认评分器实现
type DefaultScorer struct{}

// NewDefaultScorer 创建默认评分器
func NewDefaultScorer() *DefaultScorer {
	return &DefaultScorer{}
}

// CalculateDimensionScores 计算各维度分数
func (s *DefaultScorer) CalculateDimensionScores(snapshots []*MetricSnapshot, weights ProfileWeights) *DimensionScores {
	if len(snapshots) == 0 {
		return &DimensionScores{}
	}

	return &DimensionScores{
		NetworkScore:      s.calculateNetworkScore(snapshots),
		AvailabilityScore: s.calculateAvailabilityScore(snapshots),
		StabilityScore:    s.calculateStabilityScore(snapshots),
		ScaleScore:        s.calculateScaleScore(snapshots),
	}
}

// CalculateTotalScore 计算总分（加权平均）
func (s *DefaultScorer) CalculateTotalScore(scores *DimensionScores, weights ProfileWeights) float64 {
	totalWeight := 0.0
	weightedSum := 0.0

	// 网络延迟
	if scores.NetworkScore > 0 {
		weightedSum += scores.NetworkScore * weights.Network
		totalWeight += weights.Network
	}

	// 可用性
	if scores.AvailabilityScore > 0 {
		weightedSum += scores.AvailabilityScore * weights.Availability
		totalWeight += weights.Availability
	}

	// 稳定性
	if scores.StabilityScore > 0 {
		weightedSum += scores.StabilityScore * weights.Stability
		totalWeight += weights.Stability
	}

	// 规模
	if scores.ScaleScore > 0 {
		weightedSum += scores.ScaleScore * weights.Scale
		totalWeight += weights.Scale
	}

	// 模型可信度（暂未实现）
	if scores.CredibilityScore > 0 {
		weightedSum += scores.CredibilityScore * weights.Credibility
		totalWeight += weights.Credibility
	}

	// 费用准确性（暂未实现，缺失按0分计算）
	if scores.CostAccuracyScore > 0 {
		weightedSum += scores.CostAccuracyScore * weights.CostAccuracy
		totalWeight += weights.CostAccuracy
	}

	// 价格（暂未实现）
	if scores.PriceScore > 0 {
		weightedSum += scores.PriceScore * weights.Price
		totalWeight += weights.Price
	}

	if totalWeight == 0 {
		return 0
	}

	return weightedSum / totalWeight
}

// calculateNetworkScore 计算网络延迟评分
// 基于P95延迟：≤100ms=100分, 100-300ms=100-80分, 300-1000ms=80-50分, 1000-3000ms=50-0分, >3000ms=0分
func (s *DefaultScorer) calculateNetworkScore(snapshots []*MetricSnapshot) float64 {
	if len(snapshots) == 0 {
		return 0
	}

	// 计算所有快照的P95平均值
	var totalP95 int
	count := 0
	for _, snap := range snapshots {
		if snap.NetworkMetrics != nil && snap.NetworkMetrics.P95 > 0 {
			totalP95 += snap.NetworkMetrics.P95
			count++
		}
	}

	if count == 0 {
		return 0
	}

	avgP95 := float64(totalP95) / float64(count)

	switch {
	case avgP95 <= 100:
		return 100
	case avgP95 <= 300:
		// 100-300ms: 100-80分
		return 100 - (avgP95-100)/200*20
	case avgP95 <= 1000:
		// 300-1000ms: 80-50分
		return 80 - (avgP95-300)/700*30
	case avgP95 <= 3000:
		// 1000-3000ms: 50-0分
		return 50 - (avgP95-1000)/2000*50
	default:
		return 0
	}
}

// calculateAvailabilityScore 计算可用性评分
// 公式: 成功率×70% + TTFT评分×15% + 完成时长评分×15%
func (s *DefaultScorer) calculateAvailabilityScore(snapshots []*MetricSnapshot) float64 {
	if len(snapshots) == 0 {
		return 0
	}

	totalRequests := 0
	successRequests := 0
	var avgTTFT, avgDuration float64
	count := 0

	for _, snap := range snapshots {
		if snap.AvailabilityMetrics != nil {
			totalRequests += snap.AvailabilityMetrics.TotalRequests
			successRequests += snap.AvailabilityMetrics.SuccessRequests
			if snap.AvailabilityMetrics.AvgTTFTMs > 0 {
				avgTTFT += float64(snap.AvailabilityMetrics.AvgTTFTMs)
				avgDuration += float64(snap.AvailabilityMetrics.AvgDurationMs)
				count++
			}
		}
	}

	if totalRequests == 0 {
		return 0
	}

	// 成功率分数
	successRate := float64(successRequests) / float64(totalRequests) * 100

	// 2026-07-27 (audit): guard against count==0. count only increments when a
	// snapshot carries AvgTTFTMs>0, but totalRequests can be >0 while every
	// snapshot has AvgTTFTMs==0 (e.g. all probes timed out before first
	// token). Dividing by zero here yields +Inf/NaN, which silently corrupts
	// the availability score and can trip a false auto-disable alert. When
	// there is no TTFT signal, score on success rate alone (the 0.7 weight)
	// rather than poisoning the dimension. Commit 006a8ce3 fixed the sibling
	// site in aggregator.go but missed this scorer path.
	if count == 0 {
		return successRate
	}

	// TTFT评分
	avgTTFT = avgTTFT / float64(count)
	ttftScore := s.scoreTTFT(avgTTFT)

	// 完成时长评分
	avgDuration = avgDuration / float64(count)
	durationScore := s.scoreDuration(avgDuration)

	return successRate*0.7 + ttftScore*0.15 + durationScore*0.15
}

// scoreTTFT TTFT评分：≤500ms=100分, 500-2000ms=100-50分, 2000-5000ms=50-0分, >5000ms=0分
func (s *DefaultScorer) scoreTTFT(ttftMs float64) float64 {
	switch {
	case ttftMs <= 500:
		return 100
	case ttftMs <= 2000:
		return 100 - (ttftMs-500)/1500*50
	case ttftMs <= 5000:
		return 50 - (ttftMs-2000)/3000*50
	default:
		return 0
	}
}

// scoreDuration 完成时长评分：≤5s=100分, 5-15s=100-50分, 15-30s=50-0分, >30s=0分
func (s *DefaultScorer) scoreDuration(durationMs float64) float64 {
	durationS := durationMs / 1000
	switch {
	case durationS <= 5:
		return 100
	case durationS <= 15:
		return 100 - (durationS-5)/10*50
	case durationS <= 30:
		return 50 - (durationS-15)/15*50
	default:
		return 0
	}
}

// calculateStabilityScore 计算稳定性评分
// 基于错误率：≤1%=100分, 1-5%=100-80分, 5-15%=80-40分, 15-30%=40-0分, >30%=0分
// 5xx错误每1%扣10分
func (s *DefaultScorer) calculateStabilityScore(snapshots []*MetricSnapshot) float64 {
	if len(snapshots) == 0 {
		return 0
	}

	totalRequests := 0
	totalErrors := 0
	serverErrors := 0

	for _, snap := range snapshots {
		if snap.AvailabilityMetrics != nil {
			totalRequests += snap.AvailabilityMetrics.TotalRequests
		}
		if snap.StabilityMetrics != nil {
			totalErrors += snap.StabilityMetrics.ErrorCount
			// 统计5xx错误
			for errorType, count := range snap.StabilityMetrics.ErrorTypes {
				if len(errorType) > 0 && errorType[0] == '5' {
					serverErrors += count
				}
			}
		}
	}

	if totalRequests == 0 {
		return 100 // 无请求默认满分
	}

	errorRate := float64(totalErrors) / float64(totalRequests) * 100
	serverErrorRate := float64(serverErrors) / float64(totalRequests) * 100

	// 基础分
	var baseScore float64
	switch {
	case errorRate <= 1:
		baseScore = 100
	case errorRate <= 5:
		baseScore = 100 - (errorRate-1)/4*20
	case errorRate <= 15:
		baseScore = 80 - (errorRate-5)/10*40
	case errorRate <= 30:
		baseScore = 40 - (errorRate-15)/15*40
	default:
		baseScore = 0
	}

	// 严重错误惩罚：5xx错误每1%扣10分
	penalty := serverErrorRate * 10

	return math.Max(0, baseScore-penalty)
}

// calculateScaleScore 计算规模评分
// 公式: 可用率×60% + 模型数量评分×40%
// 模型数量评分：≥50个=100分, 20-50=80-100分, 10-20=60-80分, 5-10=40-60分, <5=0-40分
func (s *DefaultScorer) calculateScaleScore(snapshots []*MetricSnapshot) float64 {
	if len(snapshots) == 0 {
		return 0
	}

	// 取最新快照的规模数据
	var latestSnap *MetricSnapshot
	for i := len(snapshots) - 1; i >= 0; i-- {
		if snapshots[i].ScaleMetrics != nil {
			latestSnap = snapshots[i]
			break
		}
	}

	if latestSnap == nil || latestSnap.ScaleMetrics == nil {
		return 0
	}

	totalModels := latestSnap.ScaleMetrics.TotalModels
	availableModels := latestSnap.ScaleMetrics.AvailableModels

	if totalModels == 0 {
		return 0
	}

	// 可用率
	availabilityRate := float64(availableModels) / float64(totalModels) * 100

	// 模型数量评分
	var countScore float64
	switch {
	case totalModels >= 50:
		countScore = 100
	case totalModels >= 20:
		countScore = 80 + float64(totalModels-20)/30*20
	case totalModels >= 10:
		countScore = 60 + float64(totalModels-10)/10*20
	case totalModels >= 5:
		countScore = 40 + float64(totalModels-5)/5*20
	default:
		countScore = float64(totalModels) / 5 * 40
	}

	return availabilityRate*0.6 + countScore*0.4
}
