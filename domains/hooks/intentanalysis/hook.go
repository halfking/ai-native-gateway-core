// Package intentanalysis 提供意图分析 Pipeline Hook。
//
// 核心功能：
//   - 多轮意图分析（基于会话历史）
//   - 意图漂移检测（KL散度）
//   - 智能模型推荐
//   - 配置热加载（30秒轮询）
//
// 集成位置：
//   Pipeline Phase: PreRouting (在路由决策前分析意图)
//   Priority: 50 (在安全检测后、路由决策前)
package intentanalysis

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"           //nolint:depguard
	"github.com/kaixuan/llm-gateway-go/domains/intentconfig" //nolint:depguard
)

// IntentAnalysisHook 意图分析 Hook
type IntentAnalysisHook struct {
	analyzer *intentconfig.Analyzer
	logger   *slog.Logger
	enabled  bool
}

// NewIntentAnalysisHook 创建意图分析 Hook
func NewIntentAnalysisHook(analyzer *intentconfig.Analyzer, logger *slog.Logger) *IntentAnalysisHook {
	if logger == nil {
		logger = slog.Default()
	}

	return &IntentAnalysisHook{
		analyzer: analyzer,
		logger:   logger,
		enabled:  analyzer != nil,
	}
}

// Name 返回 Hook 名称
func (h *IntentAnalysisHook) Name() string {
	return "intent_analysis"
}

// Execute 执行意图分析
func (h *IntentAnalysisHook) Execute(ctx context.Context, req *domain.PipelineRequest) error {
	if !h.enabled {
		h.logger.Debug("intent_analysis: disabled, skipping")
		return nil
	}

	startTime := time.Now()

	// 1. 提取请求信息
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = fmt.Sprintf("session_%d", time.Now().UnixNano())
		req.SessionID = sessionID
	}

	requestID := ""
	if req.Envelope != nil && req.Envelope.RequestID != "" {
		requestID = req.Envelope.RequestID
	} else {
		requestID = fmt.Sprintf("req_%d", time.Now().UnixNano())
	}

	tenantID := req.TenantID
	if tenantID == "" {
		tenantID = "default"
	}

	// 2. 提取用户内容
	userContent := extractUserContent(req)
	if userContent == "" {
		h.logger.Debug("intent_analysis: no user content, skipping")
		return nil
	}

	// 3. 估算上下文长度
	contextLength := estimateContextLength(req)

	// 4. 检测图片
	hasImages := detectImages(req)

	// 5. 统计工具数量
	toolCount := countTools(req)

	// 6. 执行意图分析
	result, err := h.analyzer.Analyze(ctx, intentconfig.AnalysisRequest{
		SessionID:     sessionID,
		RequestID:     requestID,
		TenantID:      tenantID,
		UserContent:   userContent,
		ContextLength: contextLength,
		HasImages:     hasImages,
		ToolCount:     toolCount,
		StoreContent:  false, // 默认不存储原文，只存哈希
	})

	if err != nil {
		h.logger.Warn("intent_analysis: analysis failed",
			"session_id", sessionID,
			"error", err)
		// 分析失败不影响主流程，继续执行
		return nil
	}

	// 7. 将分析结果写入 metadata
	if req.Metadata == nil {
		req.Metadata = make(map[string]any)
	}

	req.Metadata["intent_analysis"] = map[string]any{
		"primary_intent":       string(result.PrimaryIntent.Kind),
		"primary_confidence":   result.PrimaryConfidence,
		"confidence_level":     result.ConfidenceLevel,
		"intent_drift_score":   result.IntentDriftScore,
		"is_intent_changed":    result.IsIntentChanged,
		"intent_shift_type":    result.IntentShiftType,
		"intent_stability":     result.IntentStability,
		"turn_number":          result.TurnNumber,
		"recommendation":       result.Recommendation,
		"classifier_version":   result.ClassifierVersion,
		"analysis_latency_ms":  result.ClassificationLatency.Milliseconds(),
	}

	// 8. 如果有候选意图，也记录（最多前3个）
	if len(result.Candidates) > 0 {
		candidates := make([]map[string]any, 0, 3)
		for i, c := range result.Candidates {
			if i >= 3 {
				break
			}
			candidates = append(candidates, map[string]any{
				"kind":       string(c.Kind),
				"confidence": c.Confidence,
			})
		}
		req.Metadata["intent_candidates"] = candidates
	}

	// 9. 记录日志
	h.logger.Info("intent_analysis: completed",
		"session_id", sessionID,
		"request_id", requestID,
		"turn", result.TurnNumber,
		"intent", result.PrimaryIntent.Kind,
		"confidence", result.PrimaryConfidence,
		"drift", result.IntentDriftScore,
		"changed", result.IsIntentChanged,
		"latency_ms", time.Since(startTime).Milliseconds())

	// 10. 如果意图漂移严重，记录警告
	if result.IntentDriftScore > 0.5 {
		h.logger.Warn("intent_analysis: significant drift detected",
			"session_id", sessionID,
			"drift_score", result.IntentDriftScore,
			"previous_intent", result.PreviousIntent,
			"current_intent", result.PrimaryIntent.Kind,
			"shift_type", result.IntentShiftType,
			"recommendation", result.Recommendation)
	}

	return nil
}

// extractUserContent 从请求中提取用户内容
func extractUserContent(req *domain.PipelineRequest) string {
	// 尝试从 metadata 获取
	if req.Metadata != nil {
		if content, ok := req.Metadata["user_content"].(string); ok && content != "" {
			return content
		}
		
		// 尝试从 prompt 获取
		if prompt, ok := req.Metadata["prompt"].(string); ok && prompt != "" {
			return prompt
		}
		
		// 尝试从 messages 获取
		if messages, ok := req.Metadata["messages"].([]any); ok && len(messages) > 0 {
			// 获取最后一条 user 消息
			for i := len(messages) - 1; i >= 0; i-- {
				if msg, ok := messages[i].(map[string]any); ok {
					if role, ok := msg["role"].(string); ok && role == "user" {
						if content, ok := msg["content"].(string); ok {
							return content
						}
					}
				}
			}
		}
	}

	return ""
}

// estimateContextLength 估算上下文token数量
func estimateContextLength(req *domain.PipelineRequest) int {
	// 简单估算：英文 1 token ≈ 4 字符，中文 1 token ≈ 1.5 字符
	totalChars := 0

	if req.Metadata != nil {
		if messages, ok := req.Metadata["messages"].([]any); ok {
			for _, m := range messages {
				if msg, ok := m.(map[string]any); ok {
					if content, ok := msg["content"].(string); ok {
						totalChars += len(content)
					}
				}
			}
		}

		if prompt, ok := req.Metadata["prompt"].(string); ok {
			totalChars += len(prompt)
		}
		
		if content, ok := req.Metadata["user_content"].(string); ok {
			totalChars += len(content)
		}
	}

	// 粗略估算（偏保守）
	if totalChars == 0 {
		return 0
	}
	return totalChars / 3
}

// detectImages 检测请求中是否包含图片
func detectImages(req *domain.PipelineRequest) bool {
	if req.Metadata == nil {
		return false
	}

	if messages, ok := req.Metadata["messages"].([]any); ok {
		for _, m := range messages {
			if msg, ok := m.(map[string]any); ok {
				// OpenAI 格式：content 可以是数组
				if content, ok := msg["content"].([]any); ok {
					for _, item := range content {
						if part, ok := item.(map[string]any); ok {
							if typ, ok := part["type"].(string); ok && typ == "image_url" {
								return true
							}
						}
					}
				}
			}
		}
	}

	return false
}

// countTools 统计工具数量
func countTools(req *domain.PipelineRequest) int {
	if req.Metadata == nil {
		return 0
	}

	if tools, ok := req.Metadata["tools"].([]any); ok {
		return len(tools)
	}

	if functions, ok := req.Metadata["functions"].([]any); ok {
		return len(functions)
	}

	return 0
}
