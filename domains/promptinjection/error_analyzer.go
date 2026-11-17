// Package promptinjection - LLM 智能分析器
// 用途：使用 LLM 分析 PI 检测错误，提供根因分析和修复建议
package promptinjection

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrorAnalyzer LLM 错误分析器
type ErrorAnalyzer struct {
	db              *sql.DB
	collector       *ErrorCollector
	llmClient       LLMClient // 抽象 LLM 客户端接口
	analyzerModel   string
	analyzerVersion string
	batchSize       int
	enabled         bool
}

// LLMClient 抽象 LLM 客户端接口
type LLMClient interface {
	// Analyze 调用 LLM 进行分析
	Analyze(ctx context.Context, prompt string) (string, int, error) // 返回：响应、token 数、错误
}

// AnalysisResult LLM 分析结果
type AnalysisResult struct {
	QueueID               int64
	AnalysisID            string
	RootCause             string
	ErrorCategory         string
	IsFalsePositive       bool
	ConfidenceScore       float64
	SuggestedFix          string
	RuleAdjustment        map[string]interface{}
	CodeChanges           map[string]interface{}
	SeverityLevel         string
	ImpactScope           string
	AffectedUsersEstimate int
	SimilarCases          []map[string]interface{}
	PatternFrequency      int
	AnalysisDetails       map[string]interface{}
	LLMResponseRaw        string
	AnalysisDurationMs    int
	LLMTokensUsed         int
}

// PromptTemplate 提示词模板
type PromptTemplate struct {
	SystemPrompt string
	UserPrompt   string
}

// NewErrorAnalyzer 创建错误分析器
func NewErrorAnalyzer(
	db *sql.DB,
	collector *ErrorCollector,
	llmClient LLMClient,
	analyzerModel string,
	enabled bool,
) *ErrorAnalyzer {
	return &ErrorAnalyzer{
		db:              db,
		collector:       collector,
		llmClient:       llmClient,
		analyzerModel:   analyzerModel,
		analyzerVersion: "1.0.0",
		batchSize:       10,
		enabled:         enabled,
	}
}

// ProcessBatch 批量处理待分析的错误
func (ea *ErrorAnalyzer) ProcessBatch(ctx context.Context) (int, error) {
	if !ea.enabled {
		return 0, nil
	}

	// 获取待处理的错误
	pendingErrors, err := ea.collector.GetPendingErrors(ctx, ea.batchSize)
	if err != nil {
		return 0, fmt.Errorf("failed to get pending errors: %w", err)
	}

	if len(pendingErrors) == 0 {
		return 0, nil // 没有待处理的错误
	}

	processed := 0
	for _, queuedError := range pendingErrors {
		// 更新状态为 processing
		if err := ea.collector.UpdateStatus(ctx, queuedError.ID, QueueStatusProcessing); err != nil {
			slog.Warn("error_analyzer: failed to update status to processing",
				"queue_id", queuedError.ID, "error", err)
			continue
		}

		// 分析错误
		result, err := ea.AnalyzeError(ctx, queuedError)
		if err != nil {
			slog.Warn("error_analyzer: failed to analyze error",
				"queue_id", queuedError.ID, "error", err)

			// 增加重试计数
			if err := ea.collector.IncrementRetry(ctx, queuedError.ID); err != nil {
				slog.Error("error_analyzer: failed to increment retry",
					"queue_id", queuedError.ID, "error", err)
			}
			continue
		}

		// 保存分析结果
		if err := ea.SaveAnalysisResult(ctx, result); err != nil {
			slog.Error("error_analyzer: failed to save analysis result",
				"queue_id", queuedError.ID, "error", err)
			// 标记为失败
			_ = ea.collector.UpdateStatus(ctx, queuedError.ID, QueueStatusFailed)
			continue
		}

		// 更新状态为 analyzed
		if err := ea.collector.UpdateStatus(ctx, queuedError.ID, QueueStatusAnalyzed); err != nil {
			slog.Warn("error_analyzer: failed to update status to analyzed",
				"queue_id", queuedError.ID, "error", err)
		}

		processed++
		slog.Info("error_analyzer: successfully analyzed error",
			"queue_id", queuedError.ID,
			"analysis_id", result.AnalysisID,
			"root_cause", result.RootCause,
			"is_false_positive", result.IsFalsePositive)
	}

	return processed, nil
}

// AnalyzeError 分析单个错误
func (ea *ErrorAnalyzer) AnalyzeError(ctx context.Context, queuedError *QueuedError) (*AnalysisResult, error) {
	startTime := time.Now()

	// 构建提示词
	prompt, err := ea.buildPrompt(queuedError)
	if err != nil {
		return nil, fmt.Errorf("failed to build prompt: %w", err)
	}

	// 调用 LLM 分析
	response, tokensUsed, err := ea.llmClient.Analyze(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("LLM analysis failed: %w", err)
	}

	duration := time.Since(startTime)

	// 解析 LLM 响应
	result, err := ea.parseResponse(response)
	if err != nil {
		return nil, fmt.Errorf("failed to parse LLM response: %w", err)
	}

	// 填充元数据
	result.QueueID = queuedError.ID
	result.AnalysisID = uuid.New().String()
	result.LLMResponseRaw = response
	result.AnalysisDurationMs = int(duration.Milliseconds())
	result.LLMTokensUsed = tokensUsed

	// 查找相似案例
	similarCases, err := ea.findSimilarCases(ctx, queuedError)
	if err != nil {
		slog.Warn("error_analyzer: failed to find similar cases", "error", err)
	} else {
		result.SimilarCases = similarCases
		result.PatternFrequency = len(similarCases)
	}

	return result, nil
}

// buildPrompt 构建分析提示词
func (ea *ErrorAnalyzer) buildPrompt(queuedError *QueuedError) (string, error) {
	template := ea.getPromptTemplate(queuedError.ErrorType)

	// 解析 JSON 字段
	var errorDetails map[string]interface{}
	if len(queuedError.ErrorDetails) > 0 {
		if err := json.Unmarshal(queuedError.ErrorDetails, &errorDetails); err != nil {
			errorDetails = map[string]interface{}{"raw": string(queuedError.ErrorDetails)}
		}
	}

	var detectionLayers map[string]bool
	if len(queuedError.DetectionLayers) > 0 {
		json.Unmarshal(queuedError.DetectionLayers, &detectionLayers)
	}

	var matchedRules []MatchedRule
	if len(queuedError.MatchedRules) > 0 {
		json.Unmarshal(queuedError.MatchedRules, &matchedRules)
	}

	// 构建上下文信息
	contextInfo := fmt.Sprintf(`
错误类型: %s
租户ID: %s
会话ID: %s
请求ID: %s
错误消息: %s

输入文本（前500字符）:
%s

错误详情:
%v

已执行的检测层:
%v

已匹配的规则:
%v

重试次数: %d
`,
		queuedError.ErrorType,
		queuedError.TenantID,
		queuedError.SessionID.String,
		queuedError.RequestID.String,
		queuedError.ErrorMessage,
		truncateString(queuedError.InputText.String, 500),
		errorDetails,
		detectionLayers,
		matchedRules,
		queuedError.RetryCount,
	)

	// 组合完整提示词
	fullPrompt := fmt.Sprintf("%s\n\n用户输入:\n%s\n\n%s",
		template.SystemPrompt,
		contextInfo,
		template.UserPrompt,
	)

	return fullPrompt, nil
}

// getPromptTemplate 根据错误类型获取提示词模板
func (ea *ErrorAnalyzer) getPromptTemplate(errorType string) PromptTemplate {
	switch errorType {
	case ErrorTypeLLMDetectionFailed:
		return ea.getLLMDetectionFailedTemplate()
	case ErrorTypeRuleFalsePositive:
		return ea.getRuleFalsePositiveTemplate()
	case ErrorTypeCanaryDetectionFailed:
		return ea.getCanaryDetectionFailedTemplate()
	case ErrorTypeVectorMatchFailed:
		return ea.getVectorMatchFailedTemplate()
	default:
		return ea.getGenericTemplate()
	}
}

// getLLMDetectionFailedTemplate LLM 检测失败模板
func (ea *ErrorAnalyzer) getLLMDetectionFailedTemplate() PromptTemplate {
	return PromptTemplate{
		SystemPrompt: `你是一个专业的 AI 安全系统分析专家，专注于提示词注入检测系统的错误分析。

你的任务是分析 LLM 检测失败的根本原因，并提供可执行的修复建议。

分析时请考虑：
1. LLM 服务的可用性和稳定性
2. 超时配置是否合理
3. 输入内容是否超过 LLM 限制
4. 是否存在配置错误
5. fail-closed 策略是否导致误拦截

请以 JSON 格式返回分析结果，包含以下字段：
- root_cause: 根本原因（简短描述）
- error_category: 错误分类（如：service_unavailable, timeout, configuration, resource_limit）
- is_false_positive: 是否误报（boolean）
- confidence_score: 置信度（0.0-1.0）
- suggested_fix: 建议的修复方案（详细说明）
- rule_adjustment: 规则调整建议（JSON 对象，如果需要）
- code_changes: 代码修改建议（JSON 对象，如果需要）
- severity_level: 严重程度（low/medium/high/critical）
- impact_scope: 影响范围（session/tenant/global）
- affected_users_estimate: 预估受影响用户数（整数）
- analysis_details: 详细分析（JSON 对象）`,

		UserPrompt: `请分析上述 LLM 检测失败的情况，并提供详细的根因分析和修复建议。

特别关注：
1. 失败是否可以通过调整超时、重试策略来避免
2. 是否需要降级到规则检测
3. fail-closed 策略是否过于严格
4. 是否存在系统性问题

请返回完整的 JSON 分析结果。`,
	}
}

// getRuleFalsePositiveTemplate 规则误报模板
func (ea *ErrorAnalyzer) getRuleFalsePositiveTemplate() PromptTemplate {
	return PromptTemplate{
		SystemPrompt: `你是一个专业的安全规则优化专家，专注于减少提示词注入检测的误报。

你的任务是分析规则误报的原因，并提供规则调整建议。

分析时请考虑：
1. 规则是否过于宽泛
2. 是否需要添加白名单例外
3. 是否需要调整严重程度
4. 输入内容的合法性
5. 业务场景的特殊性

请以 JSON 格式返回分析结果，包含所有必要字段。`,

		UserPrompt: `请分析上述规则误报的情况。

重点关注：
1. 哪些规则导致了误报
2. 误报的具体原因
3. 如何调整规则以减少误报而不降低安全性
4. 是否需要添加例外模式

请返回完整的 JSON 分析结果，包含具体的规则调整建议。`,
	}
}

// getCanaryDetectionFailedTemplate Canary 检测失败模板
func (ea *ErrorAnalyzer) getCanaryDetectionFailedTemplate() PromptTemplate {
	return PromptTemplate{
		SystemPrompt: `你是一个专业的安全系统分析专家，专注于 Canary Token 检测系统。

分析 Canary 检测失败的原因，并提供修复建议。

请以 JSON 格式返回分析结果。`,

		UserPrompt: `请分析 Canary Token 检测失败的原因，并提供修复建议。`,
	}
}

// getVectorMatchFailedTemplate 向量匹配失败模板
func (ea *ErrorAnalyzer) getVectorMatchFailedTemplate() PromptTemplate {
	return PromptTemplate{
		SystemPrompt: `你是一个专业的向量检索系统专家。

分析向量相似度匹配失败的原因。

请以 JSON 格式返回分析结果。`,

		UserPrompt: `请分析向量匹配失败的原因，并提供修复建议。`,
	}
}

// getGenericTemplate 通用模板
func (ea *ErrorAnalyzer) getGenericTemplate() PromptTemplate {
	return PromptTemplate{
		SystemPrompt: `你是一个专业的 AI 安全系统分析专家。

请分析提供的错误信息，并给出详细的根因分析和修复建议。

请以 JSON 格式返回分析结果，包含所有必要字段。`,

		UserPrompt: `请分析上述错误，并提供详细的分析和建议。`,
	}
}

// parseResponse 解析 LLM 响应
func (ea *ErrorAnalyzer) parseResponse(response string) (*AnalysisResult, error) {
	// 尝试提取 JSON（LLM 可能在 JSON 前后添加解释文本）
	jsonStr := ea.extractJSON(response)
	if jsonStr == "" {
		return nil, fmt.Errorf("no valid JSON found in response")
	}

	var parsed struct {
		RootCause             string                   `json:"root_cause"`
		ErrorCategory         string                   `json:"error_category"`
		IsFalsePositive       bool                     `json:"is_false_positive"`
		ConfidenceScore       float64                  `json:"confidence_score"`
		SuggestedFix          string                   `json:"suggested_fix"`
		RuleAdjustment        map[string]interface{}   `json:"rule_adjustment"`
		CodeChanges           map[string]interface{}   `json:"code_changes"`
		SeverityLevel         string                   `json:"severity_level"`
		ImpactScope           string                   `json:"impact_scope"`
		AffectedUsersEstimate int                      `json:"affected_users_estimate"`
		AnalysisDetails       map[string]interface{}   `json:"analysis_details"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	// 验证必填字段
	if parsed.RootCause == "" {
		return nil, fmt.Errorf("root_cause is required")
	}
	if parsed.ErrorCategory == "" {
		parsed.ErrorCategory = "unknown"
	}
	if parsed.ConfidenceScore < 0 || parsed.ConfidenceScore > 1 {
		parsed.ConfidenceScore = 0.5 // 默认中等置信度
	}
	if parsed.SeverityLevel == "" {
		parsed.SeverityLevel = "medium"
	}
	if parsed.ImpactScope == "" {
		parsed.ImpactScope = "session"
	}

	return &AnalysisResult{
		RootCause:             parsed.RootCause,
		ErrorCategory:         parsed.ErrorCategory,
		IsFalsePositive:       parsed.IsFalsePositive,
		ConfidenceScore:       parsed.ConfidenceScore,
		SuggestedFix:          parsed.SuggestedFix,
		RuleAdjustment:        parsed.RuleAdjustment,
		CodeChanges:           parsed.CodeChanges,
		SeverityLevel:         parsed.SeverityLevel,
		ImpactScope:           parsed.ImpactScope,
		AffectedUsersEstimate: parsed.AffectedUsersEstimate,
		AnalysisDetails:       parsed.AnalysisDetails,
	}, nil
}

// extractJSON 从响应中提取 JSON
func (ea *ErrorAnalyzer) extractJSON(response string) string {
	// 尝试找到 JSON 代码块
	if start := strings.Index(response, "```json"); start != -1 {
		start += 7
		if end := strings.Index(response[start:], "```"); end != -1 {
			return strings.TrimSpace(response[start : start+end])
		}
	}

	// 尝试找到裸 JSON（以 { 开始，以 } 结束）
	start := strings.Index(response, "{")
	if start == -1 {
		return ""
	}

	// 从后往前找最后一个 }
	end := strings.LastIndex(response, "}")
	if end == -1 || end <= start {
		return ""
	}

	return strings.TrimSpace(response[start : end+1])
}

// SaveAnalysisResult 保存分析结果
func (ea *ErrorAnalyzer) SaveAnalysisResult(ctx context.Context, result *AnalysisResult) error {
	ruleAdjustmentJSON, _ := json.Marshal(result.RuleAdjustment)
	codeChangesJSON, _ := json.Marshal(result.CodeChanges)
	similarCasesJSON, _ := json.Marshal(result.SimilarCases)
	analysisDetailsJSON, _ := json.Marshal(result.AnalysisDetails)

	query := `
		INSERT INTO error_analysis_results (
			queue_id, analysis_id, analyzer_model, analyzer_version,
			root_cause, error_category, is_false_positive, confidence_score,
			suggested_fix, rule_adjustment, code_changes,
			severity_level, impact_scope, affected_users_estimate,
			similar_cases, pattern_frequency,
			analysis_details, llm_response_raw,
			analysis_duration_ms, llm_tokens_used
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20
		) RETURNING id
	`

	var resultID int64
	err := ea.db.QueryRowContext(ctx, query,
		result.QueueID, result.AnalysisID, ea.analyzerModel, ea.analyzerVersion,
		result.RootCause, result.ErrorCategory, result.IsFalsePositive, result.ConfidenceScore,
		result.SuggestedFix, ruleAdjustmentJSON, codeChangesJSON,
		result.SeverityLevel, result.ImpactScope, result.AffectedUsersEstimate,
		similarCasesJSON, result.PatternFrequency,
		analysisDetailsJSON, result.LLMResponseRaw,
		result.AnalysisDurationMs, result.LLMTokensUsed,
	).Scan(&resultID)

	if err != nil {
		return fmt.Errorf("failed to insert analysis result: %w", err)
	}

	slog.Info("error_analyzer: analysis result saved",
		"result_id", resultID,
		"analysis_id", result.AnalysisID,
		"queue_id", result.QueueID)

	return nil
}

// findSimilarCases 查找相似案例
func (ea *ErrorAnalyzer) findSimilarCases(ctx context.Context, queuedError *QueuedError) ([]map[string]interface{}, error) {
	// 查找相同错误类型和租户的历史案例
	query := `
		SELECT 
			q.id, q.error_type, q.error_message, q.created_at,
			r.root_cause, r.error_category, r.is_false_positive
		FROM error_analysis_queue q
		INNER JOIN error_analysis_results r ON q.id = r.queue_id
		WHERE q.error_type = $1
		  AND q.tenant_id = $2
		  AND q.id != $3
		  AND q.status = 'analyzed'
		  AND q.created_at > NOW() - INTERVAL '30 days'
		ORDER BY q.created_at DESC
		LIMIT 5
	`

	rows, err := ea.db.QueryContext(ctx, query,
		queuedError.ErrorType,
		queuedError.TenantID,
		queuedError.ID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cases []map[string]interface{}
	for rows.Next() {
		var id int64
		var errorType, errorMessage, rootCause, errorCategory string
		var isFalsePositive bool
		var createdAt time.Time

		err := rows.Scan(&id, &errorType, &errorMessage, &createdAt,
			&rootCause, &errorCategory, &isFalsePositive)
		if err != nil {
			continue
		}

		cases = append(cases, map[string]interface{}{
			"id":                id,
			"error_type":        errorType,
			"error_message":     errorMessage,
			"created_at":        createdAt,
			"root_cause":        rootCause,
			"error_category":    errorCategory,
			"is_false_positive": isFalsePositive,
		})
	}

	return cases, nil
}
