package intentconfig

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Analyzer 多轮意图分析器
type Analyzer struct {
	configMgr  *Manager
	store      EvolutionStore
	classifier *EnhancedClassifier
	logger     *slog.Logger
}

// NewAnalyzer 创建意图分析器
func NewAnalyzer(configMgr *Manager, store EvolutionStore, logger *slog.Logger) *Analyzer {
	if logger == nil {
		logger = slog.Default()
	}

	return &Analyzer{
		configMgr:  configMgr,
		store:      store,
		logger:     logger,
	}
}

// AnalysisRequest 分析请求
type AnalysisRequest struct {
	SessionID     string
	RequestID     string
	TenantID      string
	UserContent   string
	ContextLength int
	HasImages     bool
	ToolCount     int
	StoreContent  bool // 是否存储用户内容（默认只存哈希）
}

// AnalysisResult 分析结果
type AnalysisResult struct {
	// 主意图
	PrimaryIntent     IntentCandidate
	PrimaryConfidence float64
	ConfidenceLevel   string // high/medium/low/very_low

	// 候选意图（降序排序）
	Candidates []IntentCandidate

	// 演化分析
	IntentDriftScore    float64
	IsIntentChanged     bool
	PreviousIntent      string
	IntentStability     float64
	IntentShiftType     string // sudden/oscillating/stable/no_history
	PredictedNextIntent string
	PredictedConfidence float64

	// 元数据
	TurnNumber          int
	ClassifierVersion   string
	ClassificationLatency time.Duration

	// 推荐
	Recommendation string
}

// Analyze 执行多轮意图分析
func (a *Analyzer) Analyze(ctx context.Context, req AnalysisRequest) (*AnalysisResult, error) {
	startTime := time.Now()

	// 1. 加载租户配置
	cfg := a.configMgr.GetConfig(req.TenantID)
	if cfg == nil {
		return nil, fmt.Errorf("intentconfig: no config for tenant %s", req.TenantID)
	}

	// 2. 创建分类器（每次使用最新配置）
	classifier := NewEnhancedClassifier(cfg)

	// 3. 获取会话历史意图
	history, err := a.store.GetSessionHistory(ctx, req.SessionID, cfg.MultiTurnMemory)
	if err != nil {
		a.logger.Warn("intentconfig: get history failed, continue without history",
			"session_id", req.SessionID, "error", err)
		history = []IntentEvolution{} // 继续处理，但无历史
	}

	// 4. 多层分类
	candidates := classifier.ClassifyWithCandidates(
		req.UserContent,
		req.ContextLength,
		req.HasImages,
		req.ToolCount,
	)

	// 5. 选择主意图
	primary := classifier.GetPrimaryIntent(candidates)

	// 6. 计算意图漂移
	driftScore := calculateIntentDrift(history, candidates)

	// 7. 检测意图切换
	var previousIntent *string
	var isIntentChanged bool
	var shiftType string

	if len(history) > 0 {
		prevIntent := history[0].PrimaryIntent
		previousIntent = &prevIntent
		isIntentChanged, shiftType = detectIntentShift(history, string(primary.Kind))
	} else {
		shiftType = "no_history"
	}

	// 8. 计算意图稳定性
	stability := calculateIntentStability(history, cfg.MultiTurnMemory)

	// 9. 预测下一轮意图
	predictedIntent, predictedConf := predictNextIntent(history)

	// 10. 计算轮次
	turnNumber := len(history) + 1

	// 11. 持久化演化记录
	var userContentPtr *string
	if req.StoreContent {
		userContentPtr = &req.UserContent
	}

	evolution := &IntentEvolution{
		SessionID:              req.SessionID,
		TenantID:               req.TenantID,
		RequestID:              req.RequestID,
		TurnNumber:             turnNumber,
		IntentCandidates:       candidates,
		PrimaryIntent:          string(primary.Kind),
		PrimaryConfidence:      primary.Confidence,
		PreviousPrimaryIntent:  previousIntent,
		IntentDriftScore:       &driftScore,
		IsIntentChanged:        isIntentChanged,
		ClassifierVersion:      string(cfg.Strategy),
		ClassificationLatencyMs: int(time.Since(startTime).Milliseconds()),
		UserContent:            userContentPtr,
		ContextLength:          req.ContextLength,
		HasImages:              req.HasImages,
		ToolCount:              req.ToolCount,
		ClassifiedAt:           time.Now(),
	}

	if err := a.store.Save(ctx, evolution); err != nil {
		// 保存失败不影响分析结果返回，但记录日志
		a.logger.Error("intentconfig: save evolution failed",
			"session_id", req.SessionID,
			"request_id", req.RequestID,
			"error", err)
	}

	// 12. 生成推荐
	recommendation := a.generateRecommendation(primary, driftScore, isIntentChanged, shiftType, cfg)

	// 13. 构造结果
	result := &AnalysisResult{
		PrimaryIntent:         primary,
		PrimaryConfidence:     primary.Confidence,
		ConfidenceLevel:       classifier.GetConfidenceLevel(primary.Confidence),
		Candidates:            candidates,
		IntentDriftScore:      driftScore,
		IsIntentChanged:       isIntentChanged,
		PreviousIntent:        "",
		IntentStability:       stability,
		IntentShiftType:       shiftType,
		PredictedNextIntent:   predictedIntent,
		PredictedConfidence:   predictedConf,
		TurnNumber:            turnNumber,
		ClassifierVersion:     string(cfg.Strategy),
		ClassificationLatency: time.Since(startTime),
		Recommendation:        recommendation,
	}

	if previousIntent != nil {
		result.PreviousIntent = *previousIntent
	}

	a.logger.Debug("intentconfig: analysis completed",
		"session_id", req.SessionID,
		"turn", turnNumber,
		"intent", primary.Kind,
		"confidence", primary.Confidence,
		"drift", driftScore,
		"latency_ms", result.ClassificationLatency.Milliseconds())

	return result, nil
}

// generateRecommendation 生成推荐建议
func (a *Analyzer) generateRecommendation(
	primary IntentCandidate,
	driftScore float64,
	isIntentChanged bool,
	shiftType string,
	cfg *ClassifierConfig,
) string {
	recommendations := []string{}

	// 推荐1: 低置信度建议
	if primary.Confidence < cfg.ConfidenceThresholds.Low {
		recommendations = append(recommendations,
			fmt.Sprintf("置信度较低(%.2f)，建议收集更多上下文或使用通用模型", primary.Confidence))
	}

	// 推荐2: 意图漂移建议
	if driftScore > cfg.DriftThreshold {
		recommendations = append(recommendations,
			fmt.Sprintf("意图漂移明显(%.2f)，建议重新评估模型选择", driftScore))
	}

	// 推荐3: 意图切换建议
	if isIntentChanged {
		switch shiftType {
		case "sudden":
			recommendations = append(recommendations, "检测到意图突然切换，建议切换到新意图对应的模型")
		case "oscillating":
			recommendations = append(recommendations, "检测到意图来回摇摆，建议使用通用模型或等待用户明确意图")
		}
	}

	// 推荐4: 高置信度建议
	if primary.Confidence >= cfg.ConfidenceThresholds.High {
		recommendations = append(recommendations,
			fmt.Sprintf("意图明确(%s, 置信度%.2f)，建议使用专用模型以获得最佳效果", primary.Kind, primary.Confidence))
	}

	// 推荐5: 特定意图建议
	switch primary.Kind {
	case IntentCode:
		recommendations = append(recommendations, "建议使用代码专用模型（如Claude-3.5-Sonnet、GPT-4等）")
	case IntentReasoning:
		recommendations = append(recommendations, "建议使用推理能力强的模型（如o1、Claude-3-Opus等）")
	case IntentSummary:
		recommendations = append(recommendations, "建议使用长上下文模型（如Claude-3-Haiku、Gemini-1.5-Pro等）")
	}

	if len(recommendations) == 0 {
		return "意图分析正常，继续使用当前策略"
	}

	// 合并推荐（最多3条）
	if len(recommendations) > 3 {
		recommendations = recommendations[:3]
	}

	result := ""
	for i, rec := range recommendations {
		if i > 0 {
			result += "; "
		}
		result += rec
	}

	return result
}

// AnalyzeBatch 批量分析（用于异步处理）
func (a *Analyzer) AnalyzeBatch(ctx context.Context, requests []AnalysisRequest) ([]*AnalysisResult, error) {
	results := make([]*AnalysisResult, len(requests))
	errors := make([]error, len(requests))

	for i, req := range requests {
		result, err := a.Analyze(ctx, req)
		results[i] = result
		errors[i] = err
	}

	// 返回第一个错误（如果有）
	for _, err := range errors {
		if err != nil {
			return results, err
		}
	}

	return results, nil
}

// GetSessionSummary 获取会话意图摘要
func (a *Analyzer) GetSessionSummary(ctx context.Context, sessionID string, tenantID string) (*SessionSummary, error) {
	cfg := a.configMgr.GetConfig(tenantID)
	history, err := a.store.GetSessionHistory(ctx, sessionID, cfg.MultiTurnMemory*2) // 获取更多历史
	if err != nil {
		return nil, fmt.Errorf("get history: %w", err)
	}

	if len(history) == 0 {
		return nil, fmt.Errorf("no history for session %s", sessionID)
	}

	// 统计意图分布
	intentDist := make(map[string]int)
	totalConfidence := 0.0
	switchCount := 0

	for i, evo := range history {
		intentDist[evo.PrimaryIntent]++
		totalConfidence += evo.PrimaryConfidence

		if i > 0 && history[i-1].PrimaryIntent != evo.PrimaryIntent {
			switchCount++
		}
	}

	// 找出主导意图
	dominantIntent := ""
	maxCount := 0
	for intent, count := range intentDist {
		if count > maxCount {
			maxCount = count
			dominantIntent = intent
		}
	}

	// 计算平均置信度
	avgConfidence := totalConfidence / float64(len(history))

	// 计算稳定性
	stability := calculateIntentStability(history, len(history))

	return &SessionSummary{
		SessionID:       sessionID,
		TotalTurns:      len(history),
		DominantIntent:  dominantIntent,
		IntentDistribution: intentDist,
		AvgConfidence:   avgConfidence,
		Stability:       stability,
		SwitchCount:     switchCount,
		LatestIntent:    history[0].PrimaryIntent,
	}, nil
}

// SessionSummary 会话摘要
type SessionSummary struct {
	SessionID          string
	TotalTurns         int
	DominantIntent     string
	IntentDistribution map[string]int
	AvgConfidence      float64
	Stability          float64
	SwitchCount        int
	LatestIntent       string
}
