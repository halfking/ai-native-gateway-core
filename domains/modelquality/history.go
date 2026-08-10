package modelquality

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"
)

// HistoryAnalyzer 历史分析器 - 分析评分历史，检测异常变化
type HistoryAnalyzer struct {
	storage MonitorStorage
}

// NewHistoryAnalyzer 创建历史分析器
func NewHistoryAnalyzer(storage MonitorStorage) *HistoryAnalyzer {
	return &HistoryAnalyzer{
		storage: storage,
	}
}

// scoresForModel 收集某 provider+modelName 跨所有凭据节点的评分（按时间倒序）。
// 2026-08-10：引入节点维度后，单节点文件(per node)不再覆盖该模型的全部历史，
// 所以历史/趋势分析改用 ListAllScores 再按模型过滤。
func (a *HistoryAnalyzer) scoresForModel(ctx context.Context, provider, modelName string, limit int) ([]*QualityScore, error) {
	all, err := a.storage.ListAllScores(ctx, 0)
	if err != nil {
		return nil, err
	}
	var out []*QualityScore
	for _, s := range all {
		if s.Provider == provider && s.ModelName == modelName {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Timestamp.After(out[j].Timestamp)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ChangeDetection 变化检测结果
type ChangeDetection struct {
	Provider        string         `json:"provider"`
	ModelName       string         `json:"model_name"`
	HasChange       bool           `json:"has_change"`       // 是否检测到变化
	ChangeType      string         `json:"change_type"`      // improvement/degradation/stable
	CurrentScore    *QualityScore  `json:"current_score"`    // 当前评分
	PreviousScore   *QualityScore  `json:"previous_score"`   // 上一次评分
	AccuracyChange  float64        `json:"accuracy_change"`  // 准确率变化
	StabilityChange float64        `json:"stability_change"` // 稳定性变化
	LatencyChange   float64        `json:"latency_change"`   // 延迟变化(ms)
	OverallChange   float64        `json:"overall_change"`   // 综合评分变化
	Severity        string         `json:"severity"`         // low/medium/high/critical
	DetectedAt      time.Time      `json:"detected_at"`
	HistoryCount    int            `json:"history_count"`   // 历史记录数
	Trend           *TrendAnalysis `json:"trend,omitempty"` // 趋势分析
}

// TrendAnalysis 趋势分析
type TrendAnalysis struct {
	Period       string  `json:"period"`        // 分析周期(如"30天")
	Direction    string  `json:"direction"`     // up/down/stable
	Volatility   float64 `json:"volatility"`    // 波动性(标准差)
	AverageScore float64 `json:"average_score"` // 平均分
	MinScore     float64 `json:"min_score"`     // 最低分
	MaxScore     float64 `json:"max_score"`     // 最高分
	ScoreRange   float64 `json:"score_range"`   // 分数范围
	IsVolatile   bool    `json:"is_volatile"`   // 是否波动剧烈
}

// ModelQualityHistory 模型质量历史摘要
type ModelQualityHistory struct {
	Provider      string            `json:"provider"`
	ModelName     string            `json:"model_name"`
	TotalRecords  int               `json:"total_records"`
	FirstTestDate time.Time         `json:"first_test_date"`
	LastTestDate  time.Time         `json:"last_test_date"`
	CurrentScore  *QualityScore     `json:"current_score"`
	AverageScore  float64           `json:"average_score"`
	BestScore     *QualityScore     `json:"best_score"`
	WorstScore    *QualityScore     `json:"worst_score"`
	Trend         *TrendAnalysis    `json:"trend"`
	RecentChanges []ChangeDetection `json:"recent_changes,omitempty"`
}

// DetectChange 检测相邻两次评分的变化
func (a *HistoryAnalyzer) DetectChange(ctx context.Context, provider string, modelName string) (*ChangeDetection, error) {
	// 获取最近两次评分
	scores, err := a.scoresForModel(ctx, provider, modelName, 2)
	if err != nil {
		return nil, fmt.Errorf("get score history: %w", err)
	}

	if len(scores) < 2 {
		// 没有足够的历史数据
		return &ChangeDetection{
			Provider:     provider,
			ModelName:    modelName,
			HasChange:    false,
			ChangeType:   "insufficient_data",
			DetectedAt:   time.Now(),
			HistoryCount: len(scores),
		}, nil
	}

	current := scores[0]
	previous := scores[1]

	detection := &ChangeDetection{
		Provider:        provider,
		ModelName:       modelName,
		CurrentScore:    current,
		PreviousScore:   previous,
		AccuracyChange:  current.Accuracy - previous.Accuracy,
		StabilityChange: current.Stability - previous.Stability,
		LatencyChange:   current.Latency - previous.Latency,
		OverallChange:   current.OverallScore - previous.OverallScore,
		DetectedAt:      time.Now(),
		HistoryCount:    len(scores),
	}

	// 判断变化类型
	if math.Abs(detection.OverallChange) < 2.0 {
		detection.HasChange = false
		detection.ChangeType = "stable"
		detection.Severity = "low"
	} else {
		detection.HasChange = true
		if detection.OverallChange > 0 {
			detection.ChangeType = "improvement"
			detection.Severity = a.calculateSeverity(detection.OverallChange, true)
		} else {
			detection.ChangeType = "degradation"
			detection.Severity = a.calculateSeverity(-detection.OverallChange, false)
		}
	}

	return detection, nil
}

// calculateSeverity 计算变化严重程度
func (a *HistoryAnalyzer) calculateSeverity(change float64, isImprovement bool) string {
	if isImprovement {
		// 改进不需要告警
		if change > 10 {
			return "high" // 显著改进
		} else if change > 5 {
			return "medium"
		}
		return "low"
	}

	// 下降需要告警
	if change > 15 {
		return "critical" // 严重下降
	} else if change > 10 {
		return "high"
	} else if change > 5 {
		return "medium"
	}
	return "low"
}

// AnalyzeTrend 分析评分趋势
func (a *HistoryAnalyzer) AnalyzeTrend(ctx context.Context, provider string, modelName string, days int) (*TrendAnalysis, error) {
	// 获取指定天数内的所有评分
	allScores, err := a.scoresForModel(ctx, provider, modelName, 0) // 0表示获取全部
	if err != nil {
		return nil, fmt.Errorf("get score history: %w", err)
	}

	if len(allScores) == 0 {
		return nil, fmt.Errorf("no score history found")
	}

	// 过滤指定天数内的评分
	cutoff := time.Now().AddDate(0, 0, -days)
	var recentScores []*QualityScore
	for _, score := range allScores {
		if score.Timestamp.After(cutoff) {
			recentScores = append(recentScores, score)
		}
	}

	if len(recentScores) < 2 {
		return &TrendAnalysis{
			Period:    fmt.Sprintf("%d天", days),
			Direction: "insufficient_data",
		}, nil
	}

	// 计算统计指标
	var sum, sumSq float64
	minScore := recentScores[0].OverallScore
	maxScore := recentScores[0].OverallScore

	for _, score := range recentScores {
		sum += score.OverallScore
		sumSq += score.OverallScore * score.OverallScore
		if score.OverallScore < minScore {
			minScore = score.OverallScore
		}
		if score.OverallScore > maxScore {
			maxScore = score.OverallScore
		}
	}

	n := float64(len(recentScores))
	mean := sum / n
	variance := (sumSq / n) - (mean * mean)
	stdDev := math.Sqrt(variance)

	// 判断趋势方向（比较最近和最早的评分）
	oldest := recentScores[len(recentScores)-1]
	newest := recentScores[0]
	scoreDiff := newest.OverallScore - oldest.OverallScore

	var direction string
	if math.Abs(scoreDiff) < 3.0 {
		direction = "stable"
	} else if scoreDiff > 0 {
		direction = "up"
	} else {
		direction = "down"
	}

	// 判断波动性（标准差大于5视为波动剧烈）
	isVolatile := stdDev > 5.0

	return &TrendAnalysis{
		Period:       fmt.Sprintf("%d天", days),
		Direction:    direction,
		Volatility:   stdDev,
		AverageScore: mean,
		MinScore:     minScore,
		MaxScore:     maxScore,
		ScoreRange:   maxScore - minScore,
		IsVolatile:   isVolatile,
	}, nil
}

// GetModelHistory 获取模型完整历史摘要
func (a *HistoryAnalyzer) GetModelHistory(ctx context.Context, provider string, modelName string) (*ModelQualityHistory, error) {
	// 获取所有历史评分
	scores, err := a.scoresForModel(ctx, provider, modelName, 0)
	if err != nil {
		return nil, fmt.Errorf("get score history: %w", err)
	}

	if len(scores) == 0 {
		return nil, fmt.Errorf("no score history found for %s:%s", provider, modelName)
	}

	history := &ModelQualityHistory{
		Provider:      provider,
		ModelName:     modelName,
		TotalRecords:  len(scores),
		CurrentScore:  scores[0],
		FirstTestDate: scores[len(scores)-1].Timestamp,
		LastTestDate:  scores[0].Timestamp,
	}

	// 计算平均分和找出最佳/最差评分
	var sum float64
	best := scores[0]
	worst := scores[0]

	for _, score := range scores {
		sum += score.OverallScore
		if score.OverallScore > best.OverallScore {
			best = score
		}
		if score.OverallScore < worst.OverallScore {
			worst = score
		}
	}

	history.AverageScore = sum / float64(len(scores))
	history.BestScore = best
	history.WorstScore = worst

	// 分析30天趋势
	trend, err := a.AnalyzeTrend(ctx, provider, modelName, 30)
	if err == nil {
		history.Trend = trend
	}

	// 检测最近的变化
	detection, err := a.DetectChange(ctx, provider, modelName)
	if err == nil && detection.HasChange {
		history.RecentChanges = []ChangeDetection{*detection}
	}

	return history, nil
}

// GetAllModelsHistory 获取所有模型的历史摘要
func (a *HistoryAnalyzer) GetAllModelsHistory(ctx context.Context, models []ModelTarget) ([]*ModelQualityHistory, error) {
	var histories []*ModelQualityHistory

	for _, model := range models {
		history, err := a.GetModelHistory(ctx, model.Provider, model.ModelName)
		if err != nil {
			// 跳过没有历史记录的模型
			continue
		}
		histories = append(histories, history)
	}

	return histories, nil
}

// DetectAllChanges 检测所有模型的变化
func (a *HistoryAnalyzer) DetectAllChanges(ctx context.Context, models []ModelTarget) ([]*ChangeDetection, error) {
	var changes []*ChangeDetection

	for _, model := range models {
		detection, err := a.DetectChange(ctx, model.Provider, model.ModelName)
		if err != nil {
			continue
		}

		// 只返回有变化的检测结果
		if detection.HasChange {
			changes = append(changes, detection)
		}
	}

	return changes, nil
}

// GenerateChangeReport 生成变化报告
func (a *HistoryAnalyzer) GenerateChangeReport(ctx context.Context, models []ModelTarget) (string, error) {
	changes, err := a.DetectAllChanges(ctx, models)
	if err != nil {
		return "", err
	}

	if len(changes) == 0 {
		return "所有模型质量稳定，未检测到显著变化。", nil
	}

	report := fmt.Sprintf("=== 模型质量变化报告 ===\n")
	report += fmt.Sprintf("检测时间: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	report += fmt.Sprintf("检测到 %d 个模型有质量变化\n\n", len(changes))

	// 按严重程度分类
	critical := []*ChangeDetection{}
	high := []*ChangeDetection{}
	medium := []*ChangeDetection{}
	low := []*ChangeDetection{}

	for _, change := range changes {
		switch change.Severity {
		case "critical":
			critical = append(critical, change)
		case "high":
			high = append(high, change)
		case "medium":
			medium = append(medium, change)
		case "low":
			low = append(low, change)
		}
	}

	// 输出各级别的变化
	if len(critical) > 0 {
		report += "🚨 严重变化 (Critical):\n"
		for _, c := range critical {
			report += a.formatChangeItem(c)
		}
		report += "\n"
	}

	if len(high) > 0 {
		report += "⚠️ 高风险变化 (High):\n"
		for _, c := range high {
			report += a.formatChangeItem(c)
		}
		report += "\n"
	}

	if len(medium) > 0 {
		report += "⚡ 中等变化 (Medium):\n"
		for _, c := range medium {
			report += a.formatChangeItem(c)
		}
		report += "\n"
	}

	if len(low) > 0 {
		report += "ℹ️ 轻微变化 (Low):\n"
		for _, c := range low {
			report += a.formatChangeItem(c)
		}
	}

	return report, nil
}

// formatChangeItem 格式化单个变化项
func (a *HistoryAnalyzer) formatChangeItem(c *ChangeDetection) string {
	changeIcon := "↓"
	if c.ChangeType == "improvement" {
		changeIcon = "↑"
	}

	return fmt.Sprintf("  %s %s:%s\n"+
		"    准确率: %.2f%% → %.2f%% (%s%.2f%%)\n"+
		"    综合评分: %.2f (%s) → %.2f (%s) (%s%.2f)\n"+
		"    延迟P95: %.0fms → %.0fms (%+.0fms)\n",
		changeIcon, c.Provider, c.ModelName,
		c.PreviousScore.Accuracy, c.CurrentScore.Accuracy, changeIcon, math.Abs(c.AccuracyChange),
		c.PreviousScore.OverallScore, c.PreviousScore.Grade,
		c.CurrentScore.OverallScore, c.CurrentScore.Grade, changeIcon, math.Abs(c.OverallChange),
		c.PreviousScore.Latency, c.CurrentScore.Latency, c.LatencyChange,
	)
}
