package clientprofile

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"
)

// Aggregator 客户端行为聚合器
type Aggregator struct {
	store  Store
	logger *slog.Logger
}

// NewAggregator 创建聚合器
func NewAggregator(store Store, logger *slog.Logger) *Aggregator {
	if logger == nil {
		logger = slog.Default()
	}
	return &Aggregator{
		store:  store,
		logger: logger,
	}
}

// UpdateProfile 根据事件更新客户端画像
func (a *Aggregator) UpdateProfile(ctx context.Context, event *ClientBehaviorEvent) error {
	// 先保存事件
	if err := a.store.SaveEvent(ctx, event); err != nil {
		return fmt.Errorf("save event: %w", err)
	}

	// 获取现有画像
	profile, err := a.store.GetProfile(ctx, event.IdentityHash)
	if err != nil {
		return fmt.Errorf("get profile: %w", err)
	}

	// 如果画像不存在，创建新画像
	if profile == nil {
		profile = &ClientProfile{
			IdentityHash:     event.IdentityHash,
			TenantID:         event.TenantID,
			VirtualClientID:  "vc-" + event.IdentityHash[:16],
			FirstSeenAt:      event.Timestamp,
			LastSeenAt:       event.Timestamp,
			PreferredModels:  []ModelPreference{},
			TaskDistribution: make(map[string]int64),
			ActiveHours:      []int{},
		}
	}

	// 更新基础统计
	profile.LastSeenAt = event.Timestamp
	if event.EventType == EventTypeSessionStart {
		profile.TotalSessions++
	}
	if event.EventType == EventTypeRequestCompleted {
		profile.TotalRequests++
	}

	// 更新模型偏好
	if event.Model != "" {
		a.updateModelPreference(profile, event)
	}

	// 更新任务类型分布
	if event.TaskType != "" {
		profile.TaskDistribution[event.TaskType]++
	}

	// 更新活跃时段
	hour := event.Timestamp.UTC().Hour()
	a.addActiveHour(profile, hour)

	// 更新错误率（简化版，基于最近事件）
	if event.EventType == EventTypeError {
		// 增量更新错误率
		totalEvents := float64(profile.TotalRequests)
		if totalEvents > 0 {
			errorCount := profile.ErrorRate * totalEvents
			profile.ErrorRate = (errorCount + 1) / (totalEvents + 1)
		}
	} else if event.EventType == EventTypeRequestCompleted {
		totalEvents := float64(profile.TotalRequests)
		if totalEvents > 0 {
			errorCount := profile.ErrorRate * (totalEvents - 1)
			if !event.Success {
				errorCount++
			}
			profile.ErrorRate = errorCount / totalEvents
		}
	}

	// 更新审批率
	if event.EventType == EventTypeApprovalRequired {
		totalRequests := float64(profile.TotalRequests)
		if totalRequests > 0 {
			approvalCount := profile.ApprovalRate * (totalRequests - 1) + 1
			profile.ApprovalRate = approvalCount / totalRequests
		}
	}

	// 更新平均Token数（简化增量计算）
	if event.TokensUsed > 0 {
		totalRequests := float64(profile.TotalRequests)
		if totalRequests > 1 {
			profile.AvgTokensPerTurn = (profile.AvgTokensPerTurn*(totalRequests-1) + float64(event.TokensUsed)) / totalRequests
		} else {
			profile.AvgTokensPerTurn = float64(event.TokensUsed)
		}
	}

	profile.UpdatedAt = time.Now()

	// 保存更新后的画像
	if err := a.store.UpsertProfile(ctx, profile); err != nil {
		return fmt.Errorf("upsert profile: %w", err)
	}

	a.logger.Debug("profile updated",
		"identity_hash", event.IdentityHash[:16],
		"event_type", event.EventType,
		"total_sessions", profile.TotalSessions,
		"total_requests", profile.TotalRequests,
	)

	return nil
}

// updateModelPreference 更新模型偏好
func (a *Aggregator) updateModelPreference(profile *ClientProfile, event *ClientBehaviorEvent) {
	// 查找现有模型
	found := false
	for i := range profile.PreferredModels {
		if profile.PreferredModels[i].ModelName == event.Model {
			profile.PreferredModels[i].UsageCount++
			// 增量更新成功率和延迟
			if event.Success {
				successCount := profile.PreferredModels[i].SuccessRate * float64(profile.PreferredModels[i].UsageCount-1)
				profile.PreferredModels[i].SuccessRate = (successCount + 1) / float64(profile.PreferredModels[i].UsageCount)
			} else {
				successCount := profile.PreferredModels[i].SuccessRate * float64(profile.PreferredModels[i].UsageCount-1)
				profile.PreferredModels[i].SuccessRate = successCount / float64(profile.PreferredModels[i].UsageCount)
			}
			if event.LatencyMs > 0 {
				avgLatency := profile.PreferredModels[i].AvgLatencyMs
				count := float64(profile.PreferredModels[i].UsageCount)
				profile.PreferredModels[i].AvgLatencyMs = (avgLatency*(count-1) + float64(event.LatencyMs)) / count
			}
			found = true
			break
		}
	}

	// 如果是新模型，添加到列表
	if !found {
		successRate := 0.0
		if event.Success {
			successRate = 1.0
		}
		profile.PreferredModels = append(profile.PreferredModels, ModelPreference{
			ModelName:    event.Model,
			UsageCount:   1,
			SuccessRate:  successRate,
			AvgLatencyMs: float64(event.LatencyMs),
		})
	}

	// 按使用次数排序
	sort.Slice(profile.PreferredModels, func(i, j int) bool {
		return profile.PreferredModels[i].UsageCount > profile.PreferredModels[j].UsageCount
	})

	// 只保留前10个模型
	if len(profile.PreferredModels) > 10 {
		profile.PreferredModels = profile.PreferredModels[:10]
	}
}

// addActiveHour 添加活跃时段
func (a *Aggregator) addActiveHour(profile *ClientProfile, hour int) {
	// 检查是否已存在
	for _, h := range profile.ActiveHours {
		if h == hour {
			return
		}
	}
	profile.ActiveHours = append(profile.ActiveHours, hour)
	sort.Ints(profile.ActiveHours)
}

// GetProfile 获取客户端画像
func (a *Aggregator) GetProfile(ctx context.Context, identityHash string) (*ClientProfile, error) {
	return a.store.GetProfile(ctx, identityHash)
}

// AnalyzeTrends 分析客户端行为趋势
func (a *Aggregator) AnalyzeTrends(ctx context.Context, identityHash string, days int) (*TrendAnalysis, error) {
	endTime := time.Now()
	startTime := endTime.AddDate(0, 0, -days)

	// 获取时间范围内的事件
	events, err := a.store.GetEvents(ctx, identityHash, startTime, endTime)
	if err != nil {
		return nil, fmt.Errorf("get events: %w", err)
	}

	analysis := &TrendAnalysis{
		IdentityHash:   identityHash,
		StartDate:      startTime,
		EndDate:        endTime,
		Days:           days,
		DailyRequests:  []DailyMetric{},
		DailySessions:  []DailyMetric{},
		ErrorRateTrend: []DailyMetric{},
		LatencyTrend:   []DailyMetric{},
		ModelShifts:    []ModelShift{},
		Anomalies:      []Anomaly{},
	}

	// 按日期聚合指标
	dailyStats := make(map[string]*dailyAggregation)
	for _, event := range events {
		dateKey := event.Timestamp.Format("2006-01-02")
		if _, exists := dailyStats[dateKey]; !exists {
			dailyStats[dateKey] = &dailyAggregation{
				date:         event.Timestamp.Truncate(24 * time.Hour),
				requestCount: 0,
				sessionCount: 0,
				errorCount:   0,
				totalLatency: 0,
				latencyCount: 0,
			}
		}

		stats := dailyStats[dateKey]
		if event.EventType == EventTypeRequestCompleted {
			stats.requestCount++
			if !event.Success {
				stats.errorCount++
			}
		}
		if event.EventType == EventTypeSessionStart {
			stats.sessionCount++
		}
		if event.LatencyMs > 0 {
			stats.totalLatency += event.LatencyMs
			stats.latencyCount++
		}
	}

	// 构建趋势数据
	for _, stats := range dailyStats {
		analysis.DailyRequests = append(analysis.DailyRequests, DailyMetric{
			Date:  stats.date,
			Value: float64(stats.requestCount),
		})
		analysis.DailySessions = append(analysis.DailySessions, DailyMetric{
			Date:  stats.date,
			Value: float64(stats.sessionCount),
		})

		errorRate := 0.0
		if stats.requestCount > 0 {
			errorRate = float64(stats.errorCount) / float64(stats.requestCount)
		}
		analysis.ErrorRateTrend = append(analysis.ErrorRateTrend, DailyMetric{
			Date:  stats.date,
			Value: errorRate,
		})

		avgLatency := 0.0
		if stats.latencyCount > 0 {
			avgLatency = float64(stats.totalLatency) / float64(stats.latencyCount)
		}
		analysis.LatencyTrend = append(analysis.LatencyTrend, DailyMetric{
			Date:  stats.date,
			Value: avgLatency,
		})
	}

	// 排序
	sort.Slice(analysis.DailyRequests, func(i, j int) bool {
		return analysis.DailyRequests[i].Date.Before(analysis.DailyRequests[j].Date)
	})
	sort.Slice(analysis.DailySessions, func(i, j int) bool {
		return analysis.DailySessions[i].Date.Before(analysis.DailySessions[j].Date)
	})
	sort.Slice(analysis.ErrorRateTrend, func(i, j int) bool {
		return analysis.ErrorRateTrend[i].Date.Before(analysis.ErrorRateTrend[j].Date)
	})
	sort.Slice(analysis.LatencyTrend, func(i, j int) bool {
		return analysis.LatencyTrend[i].Date.Before(analysis.LatencyTrend[j].Date)
	})

	// 简单的异常检测（基于阈值）
	a.detectAnomalies(analysis)

	return analysis, nil
}

// dailyAggregation 每日聚合数据
type dailyAggregation struct {
	date         time.Time
	requestCount int64
	sessionCount int64
	errorCount   int64
	totalLatency int64
	latencyCount int64
}

// detectAnomalies 检测异常
func (a *Aggregator) detectAnomalies(analysis *TrendAnalysis) {
	// 检测错误率飙升
	if len(analysis.ErrorRateTrend) >= 2 {
		for i := 1; i < len(analysis.ErrorRateTrend); i++ {
			prev := analysis.ErrorRateTrend[i-1].Value
			curr := analysis.ErrorRateTrend[i].Value
			// 如果错误率从<10%跳到>30%
			if prev < 0.1 && curr > 0.3 {
				analysis.Anomalies = append(analysis.Anomalies, Anomaly{
					Date:        analysis.ErrorRateTrend[i].Date,
					Type:        "error_spike",
					Severity:    "high",
					Description: fmt.Sprintf("Error rate jumped from %.1f%% to %.1f%%", prev*100, curr*100),
					Value:       curr,
					Threshold:   0.3,
				})
			}
		}
	}

	// 检测延迟飙升
	if len(analysis.LatencyTrend) >= 3 {
		// 计算平均延迟
		var sum float64
		for _, metric := range analysis.LatencyTrend {
			sum += metric.Value
		}
		avgLatency := sum / float64(len(analysis.LatencyTrend))

		// 检测超过平均值2倍的情况
		for _, metric := range analysis.LatencyTrend {
			if metric.Value > avgLatency*2 && metric.Value > 1000 {
				analysis.Anomalies = append(analysis.Anomalies, Anomaly{
					Date:        metric.Date,
					Type:        "latency_spike",
					Severity:    "medium",
					Description: fmt.Sprintf("Latency %.0fms exceeds average %.0fms", metric.Value, avgLatency),
					Value:       metric.Value,
					Threshold:   avgLatency * 2,
				})
			}
		}
	}
}
