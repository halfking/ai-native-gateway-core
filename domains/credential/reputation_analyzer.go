// Package credential - Reputation time-series analyzer.
//
// Reads aggregated rows from a ReputationStore and produces:
//   - ProviderReputation (trend view, long-term stats, uptime)
//   - []Anomaly (rule-based detectors: error-rate spike, latency spike,
//     success-rate drop)
//
// The analyzer is pure: no external state besides the store. All thresholds
// are exposed as AnomalyThresholds so callers can override per environment.
package credential

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"
)

// ReputationAnalyzer 信誉分析器
type ReputationAnalyzer struct {
	store      ReputationStore
	logger     *slog.Logger
	thresholds AnomalyThresholds
	now        func() time.Time // 测试时可注入
}

// NewReputationAnalyzer 创建分析器
func NewReputationAnalyzer(store ReputationStore, logger *slog.Logger) *ReputationAnalyzer {
	if logger == nil {
		logger = slog.Default()
	}
	return &ReputationAnalyzer{
		store:      store,
		logger:     logger,
		thresholds: DefaultAnomalyThresholds(),
		now:        time.Now,
	}
}

// WithThresholds 覆盖默认阈值（链式）
func (a *ReputationAnalyzer) WithThresholds(t AnomalyThresholds) *ReputationAnalyzer {
	if t.ErrorRateJumpAbove > 0 {
		a.thresholds.ErrorRateJumpAbove = t.ErrorRateJumpAbove
	}
	if t.ErrorRatePrevBelow > 0 {
		a.thresholds.ErrorRatePrevBelow = t.ErrorRatePrevBelow
	}
	if t.LatencyMinMs > 0 {
		a.thresholds.LatencyMinMs = t.LatencyMinMs
	}
	if t.LatencyMultiplier > 0 {
		a.thresholds.LatencyMultiplier = t.LatencyMultiplier
	}
	if t.SuccessDropBelow > 0 {
		a.thresholds.SuccessDropBelow = t.SuccessDropBelow
	}
	return a
}

// Thresholds 返回当前阈值
func (a *ReputationAnalyzer) Thresholds() AnomalyThresholds {
	return a.thresholds
}

// AnalyzeProviderReputation 分析供应商信誉
func (a *ReputationAnalyzer) AnalyzeProviderReputation(ctx context.Context, providerID, model string, days int) (*ProviderReputation, error) {
	if providerID == "" {
		return nil, fmt.Errorf("credential: AnalyzeProviderReputation requires provider_id")
	}
	if days <= 0 {
		days = 7
	}

	timeseries, err := a.store.GetTimeseries(ctx, providerID, model, days)
	if err != nil {
		return nil, fmt.Errorf("credential: get timeseries: %w", err)
	}

	rep := &ProviderReputation{
		ProviderID:  providerID,
		Model:       model,
		LastUpdated: a.now(),
	}

	for _, ts := range timeseries {
		rep.ReliabilityTrend = append(rep.ReliabilityTrend, DailyMetric{Date: ts.Date, Value: ts.ReliabilityScore})
		rep.LatencyTrend = append(rep.LatencyTrend, DailyMetric{Date: ts.Date, Value: ts.AvgLatencyMs})
		rep.ErrorRateTrend = append(rep.ErrorRateTrend, DailyMetric{Date: ts.Date, Value: ts.ErrorRate})
		rep.TotalRequests += ts.RequestCount
		rep.TotalSuccesses += ts.SuccessCount
	}

	rep.LongTermReliability = CalculateLongTermReliability(timeseries)
	rep.StabilityScore = a.calculateStabilityScore(timeseries)
	rep.AverageLatencyMs = averageFloat(timeseries, func(r TimeseriesRow) float64 { return r.AvgLatencyMs })
	rep.AverageErrorRate = averageFloat(timeseries, func(r TimeseriesRow) float64 { return r.ErrorRate })

	// 最近事件（30 天）
	rep.RecentIncidents, err = a.store.GetRecentIncidents(ctx, providerID, model, 30)
	if err != nil {
		a.logger.Warn("reputation: get recent incidents failed",
			"provider", providerID, "model", model, "error", err)
		rep.RecentIncidents = nil
	}

	unresolved, err := a.store.GetUnresolvedIncidents(ctx, providerID, model)
	if err == nil {
		rep.UnresolvedCount = len(unresolved)
	}

	rep.UptimePercentage = a.calculateUptime(timeseries, rep.RecentIncidents, days)
	return rep, nil
}

// DetectAnomalies 检测异常（默认 7 天窗口）
func (a *ReputationAnalyzer) DetectAnomalies(ctx context.Context, providerID, model string) ([]Anomaly, error) {
	return a.DetectAnomaliesFor(ctx, providerID, model, 7)
}

// DetectAnomaliesFor 在指定窗口内检测异常
func (a *ReputationAnalyzer) DetectAnomaliesFor(ctx context.Context, providerID, model string, days int) ([]Anomaly, error) {
	if providerID == "" {
		return nil, fmt.Errorf("credential: DetectAnomalies requires provider_id")
	}
	if days <= 0 {
		days = 7
	}

	timeseries, err := a.store.GetTimeseries(ctx, providerID, model, days)
	if err != nil {
		return nil, fmt.Errorf("credential: get timeseries: %w", err)
	}
	if len(timeseries) < 2 {
		return nil, nil
	}

	var anomalies []Anomaly
	t := a.thresholds

	for i := 1; i < len(timeseries); i++ {
		prev := timeseries[i-1]
		curr := timeseries[i]

		// 1. 错误率飙升：前一日 <10% 且今日 >30%
		if prev.ErrorRate < t.ErrorRatePrevBelow && curr.ErrorRate > t.ErrorRateJumpAbove {
			severity := ImpactHigh
			if curr.ErrorRate > 0.6 {
				severity = ImpactCritical
			}
			anomalies = append(anomalies, Anomaly{
				Type:      AnomalyErrorRateSpike,
				Date:      curr.Date,
				Severity:  severity,
				Value:     curr.ErrorRate,
				Threshold: t.ErrorRateJumpAbove,
				Message: fmt.Sprintf(
					"error_rate jumped from %.2f to %.2f (threshold=%.2f)",
					prev.ErrorRate, curr.ErrorRate, t.ErrorRateJumpAbove,
				),
			})
		}

		// 2. 延迟飙升：超过前一日 N 倍且绝对值 > 阈值
		if prev.AvgLatencyMs > 0 &&
			curr.AvgLatencyMs > t.LatencyMinMs &&
			curr.AvgLatencyMs > prev.AvgLatencyMs*t.LatencyMultiplier {
			severity := ImpactMedium
			if curr.AvgLatencyMs > t.LatencyMinMs*3 {
				severity = ImpactHigh
			}
			anomalies = append(anomalies, Anomaly{
				Type:      AnomalyLatencySpike,
				Date:      curr.Date,
				Severity:  severity,
				Value:     curr.AvgLatencyMs,
				Threshold: prev.AvgLatencyMs * t.LatencyMultiplier,
				Message: fmt.Sprintf(
					"avg_latency_ms jumped from %.0fms to %.0fms (%.1fx previous, threshold=%.0fms)",
					prev.AvgLatencyMs, curr.AvgLatencyMs,
					curr.AvgLatencyMs/math.Max(prev.AvgLatencyMs, 1),
					t.LatencyMinMs,
				),
			})
		}

		// 3. 成功率骤降：跌至阈值以下
		if curr.SuccessRate > 0 && curr.SuccessRate < t.SuccessDropBelow {
			severity := ImpactMedium
			if curr.SuccessRate < t.SuccessDropBelow*0.7 {
				severity = ImpactHigh
			}
			anomalies = append(anomalies, Anomaly{
				Type:      AnomalySuccessDrop,
				Date:      curr.Date,
				Severity:  severity,
				Value:     curr.SuccessRate,
				Threshold: t.SuccessDropBelow,
				Message: fmt.Sprintf(
					"success_rate dropped to %.2f (threshold=%.2f)",
					curr.SuccessRate, t.SuccessDropBelow,
				),
			})
		}
	}
	return anomalies, nil
}

// ---------------------------------------------------------------------------
// 纯函数：可单独测试
// ---------------------------------------------------------------------------

// CalculateLongTermReliability 计算长期可靠性（基于 Beta 后验）
//
// 公式：sum(success) / sum(total requests)；缺失数据不计入。
func CalculateLongTermReliability(rows []TimeseriesRow) float64 {
	var (
		totalSuccess  int64
		totalRequests int64
	)
	for _, r := range rows {
		totalSuccess += r.SuccessCount
		totalRequests += r.RequestCount
	}
	if totalRequests == 0 {
		return 0
	}
	return float64(totalSuccess) / float64(totalRequests)
}

// calculateStabilityScore 计算稳定性评分
//
// 基于 reliability 的标准差：stddev 越小稳定性越高。
//   - stddev = 0  -> score = 1.0
//   - stddev = 0.5 -> score = 0.0
//
// 计算公式：1 - clamp(stddev * 2, 0, 1)
func (a *ReputationAnalyzer) calculateStabilityScore(rows []TimeseriesRow) float64 {
	return CalculateStabilityScore(rows)
}

// CalculateStabilityScore 公开的稳定性评分（纯函数）
func CalculateStabilityScore(rows []TimeseriesRow) float64 {
	if len(rows) < 2 {
		return 1.0
	}
	// 跳过 reliability 为 0 的样本（无数据）
	var values []float64
	for _, r := range rows {
		if r.ReliabilityScore > 0 || r.RequestCount > 0 {
			values = append(values, r.ReliabilityScore)
		}
	}
	n := float64(len(values))
	if n < 2 {
		return 1.0
	}
	var sum, sumSq float64
	for _, v := range values {
		sum += v
		sumSq += v * v
	}
	mean := sum / n
	variance := (sumSq / n) - (mean * mean)
	if variance < 0 {
		variance = 0
	}
	stddev := math.Sqrt(variance)
	score := 1.0 - stddev*2
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}

// calculateUptime 计算 uptime 百分比
//
// 简化模型：
//   - 基础 uptime = mean(reliability) over 窗口
//   - 减去事件影响：每个未解决事件按 duration / window 折扣
func (a *ReputationAnalyzer) calculateUptime(rows []TimeseriesRow, incidents []Incident, days int) float64 {
	return CalculateUptime(rows, incidents, days)
}

// CalculateUptime 公开的 uptime 计算（纯函数）
func CalculateUptime(rows []TimeseriesRow, incidents []Incident, days int) float64 {
	if days <= 0 {
		days = 7
	}
	windowSeconds := float64(days) * 24 * 3600

	// 1. 基础可靠性
	var totalReq, totalSuccess int64
	for _, r := range rows {
		totalReq += r.RequestCount
		totalSuccess += r.SuccessCount
	}
	baseUptime := 1.0
	if totalReq > 0 {
		baseUptime = float64(totalSuccess) / float64(totalReq)
	}

	// 2. 事件影响（每个事件按 duration 比例扣减）
	if len(incidents) == 0 {
		return clamp01(baseUptime)
	}
	incidentPenalty := 0.0
	for _, inc := range incidents {
		dur := inc.ResolvedDuration()
		if dur <= 0 {
			dur = 5 * time.Minute // 未结束事件默认 5 分钟影响
		}
		fraction := float64(dur) / windowSeconds
		// 按影响级别加权
		weight := 1.0
		switch inc.Impact {
		case ImpactCritical:
			weight = 1.0
		case ImpactHigh:
			weight = 0.8
		case ImpactMedium:
			weight = 0.5
		case ImpactLow:
			weight = 0.2
		}
		incidentPenalty += fraction * weight
	}
	if incidentPenalty > 1 {
		incidentPenalty = 1
	}
	uptime := baseUptime * (1.0 - incidentPenalty)
	return clamp01(uptime)
}

// averageFloat 计算某字段的加权平均（按 RequestCount 加权）
func averageFloat(rows []TimeseriesRow, fn func(TimeseriesRow) float64) float64 {
	if len(rows) == 0 {
		return 0
	}
	var (
		weightedSum float64
		weight      float64
	)
	for _, r := range rows {
		w := float64(r.RequestCount)
		if w == 0 {
			continue
		}
		weightedSum += fn(r) * w
		weight += w
	}
	if weight == 0 {
		return 0
	}
	return weightedSum / weight
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
