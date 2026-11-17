// Package promptinjection - 错误收集器
// 用途：收集 PI 检测过程中的错误，入队等待智能分析
package promptinjection

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// ErrorCollector PI 检测错误收集器
type ErrorCollector struct {
	db      *sql.DB
	enabled bool
}

// ErrorType 错误类型常量
const (
	ErrorTypeLLMDetectionFailed    = "llm_detection_failed"     // LLM 判官调用失败
	ErrorTypeRuleFalsePositive     = "rule_false_positive"      // 规则误报
	ErrorTypeCanaryDetectionFailed = "canary_detection_failed"  // Canary 检测失败
	ErrorTypeVectorMatchFailed     = "vector_match_failed"      // 向量匹配失败
	ErrorTypeHeuristicMisfire      = "heuristic_misfire"        // 启发式误触发
	ErrorTypePolicyLoadFailed      = "policy_load_failed"       // 策略加载失败
	ErrorTypeUnexpectedBehavior    = "unexpected_behavior"      // 意外行为
	ErrorTypePerformanceIssue      = "performance_issue"        // 性能问题
)

// ErrorQueueStatus 队列状态
const (
	QueueStatusPending    = "pending"
	QueueStatusProcessing = "processing"
	QueueStatusAnalyzed   = "analyzed"
	QueueStatusFailed     = "failed"
	QueueStatusSkipped    = "skipped"
)

// CollectedError 收集的错误信息
type CollectedError struct {
	ErrorType    string                 `json:"error_type"`
	TenantID     string                 `json:"tenant_id"`
	SessionID    string                 `json:"session_id,omitempty"`
	RequestID    string                 `json:"request_id,omitempty"`
	InputText    string                 `json:"input_text,omitempty"`
	ErrorMessage string                 `json:"error_message"`
	ErrorDetails map[string]interface{} `json:"error_details,omitempty"`
	Priority     int                    `json:"priority"` // 1-10, 10=highest
	
	// 检测上下文
	DetectionLayers map[string]bool        `json:"detection_layers,omitempty"`
	MatchedRules    []MatchedRule          `json:"matched_rules,omitempty"`
	PolicyConfig    map[string]interface{} `json:"policy_config,omitempty"`
	
	// 元数据
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// QueuedError 队列中的错误项
type QueuedError struct {
	ID             int64
	ErrorType      string
	ErrorHash      string
	Priority       int
	TenantID       string
	SessionID      sql.NullString
	RequestID      sql.NullString
	InputText      sql.NullString
	InputHash      sql.NullString
	ErrorMessage   string
	ErrorDetails   json.RawMessage
	DetectionLayers json.RawMessage
	MatchedRules   json.RawMessage
	PolicyConfig   json.RawMessage
	Status         string
	RetryCount     int
	MaxRetries     int
	CreatedAt      time.Time
	StartedAt      sql.NullTime
	CompletedAt    sql.NullTime
	Metadata       json.RawMessage
}

// NewErrorCollector 创建错误收集器
func NewErrorCollector(db *sql.DB, enabled bool) *ErrorCollector {
	return &ErrorCollector{
		db:      db,
		enabled: enabled,
	}
}

// Collect 收集错误并入队
func (ec *ErrorCollector) Collect(ctx context.Context, collectedErr *CollectedError) error {
	if !ec.enabled {
		return nil // 收集器未启用，静默跳过
	}
	
	if collectedErr == nil {
		return fmt.Errorf("collected error is nil")
	}
	
	// 验证必填字段
	if collectedErr.ErrorType == "" {
		return fmt.Errorf("error_type is required")
	}
	if collectedErr.TenantID == "" {
		return fmt.Errorf("tenant_id is required")
	}
	
	// 计算错误指纹（用于去重）
	errorHash := ec.calculateErrorHash(collectedErr)
	
	// 检查是否已存在相同错误（最近 24 小时内）
	exists, existingID, err := ec.checkDuplicate(ctx, errorHash, 24*time.Hour)
	if err != nil {
		slog.Warn("error_collector: failed to check duplicate", "error", err)
		// 继续插入，不因去重检查失败而阻断
	}
	
	if exists {
		slog.Debug("error_collector: duplicate error skipped", 
			"error_hash", errorHash, 
			"existing_id", existingID)
		return nil // 重复错误，跳过
	}
	
	// 计算输入 hash（如果有输入文本）
	var inputHash sql.NullString
	if collectedErr.InputText != "" {
		hash := sha256.Sum256([]byte(collectedErr.InputText))
		inputHash = sql.NullString{
			String: hex.EncodeToString(hash[:]),
			Valid:  true,
		}
	}
	
	// 序列化 JSON 字段
	errorDetailsJSON, err := json.Marshal(collectedErr.ErrorDetails)
	if err != nil {
		return fmt.Errorf("failed to marshal error_details: %w", err)
	}
	
	detectionLayersJSON, err := json.Marshal(collectedErr.DetectionLayers)
	if err != nil {
		return fmt.Errorf("failed to marshal detection_layers: %w", err)
	}
	
	matchedRulesJSON, err := json.Marshal(collectedErr.MatchedRules)
	if err != nil {
		return fmt.Errorf("failed to marshal matched_rules: %w", err)
	}
	
	policyConfigJSON, err := json.Marshal(collectedErr.PolicyConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal policy_config: %w", err)
	}
	
	metadataJSON, err := json.Marshal(collectedErr.Metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}
	
	// 确定优先级（默认 5）
	priority := collectedErr.Priority
	if priority < 1 || priority > 10 {
		priority = 5
	}
	
	// 截断输入文本（保留前 10000 字符）
	inputText := collectedErr.InputText
	if len(inputText) > 10000 {
		inputText = inputText[:10000]
	}
	
	// 插入队列
	query := `
		INSERT INTO error_analysis_queue (
			error_type, error_hash, priority,
			tenant_id, session_id, request_id,
			input_text, input_hash, error_message,
			error_details, detection_layers, matched_rules, policy_config,
			metadata, status, retry_count, max_retries
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		RETURNING id
	`
	
	var queueID int64
	err = ec.db.QueryRowContext(ctx, query,
		collectedErr.ErrorType, errorHash, priority,
		collectedErr.TenantID, 
		toNullString(collectedErr.SessionID),
		toNullString(collectedErr.RequestID),
		toNullString(inputText),
		inputHash,
		collectedErr.ErrorMessage,
		errorDetailsJSON,
		detectionLayersJSON,
		matchedRulesJSON,
		policyConfigJSON,
		metadataJSON,
		QueueStatusPending,
		0, // retry_count
		3, // max_retries
	).Scan(&queueID)
	
	if err != nil {
		return fmt.Errorf("failed to insert error into queue: %w", err)
	}
	
	slog.Info("error_collector: error queued for analysis",
		"queue_id", queueID,
		"error_type", collectedErr.ErrorType,
		"error_hash", errorHash,
		"priority", priority)
	
	return nil
}

// CollectLLMDetectionFailure 收集 LLM 检测失败错误
func (ec *ErrorCollector) CollectLLMDetectionFailure(
	ctx context.Context,
	tenantID, sessionID, requestID string,
	input string,
	llmError error,
	policy *Policy,
) error {
	if !ec.enabled {
		return nil
	}
	
	err := &CollectedError{
		ErrorType:    ErrorTypeLLMDetectionFailed,
		TenantID:     tenantID,
		SessionID:    sessionID,
		RequestID:    requestID,
		InputText:    input,
		ErrorMessage: llmError.Error(),
		Priority:     7, // 高优先级
		ErrorDetails: map[string]interface{}{
			"llm_engine_id":       policy.LLMEngineID,
			"detection_timeout":   policy.LLMDetectionTimeoutSec,
			"fail_closed_enabled": policy.LLMDetectionFailClosed,
		},
		Metadata: map[string]interface{}{
			"timestamp": time.Now().Unix(),
		},
	}
	
	return ec.Collect(ctx, err)
}

// CollectRuleFalsePositive 收集规则误报
func (ec *ErrorCollector) CollectRuleFalsePositive(
	ctx context.Context,
	tenantID, sessionID, requestID string,
	input string,
	matchedRules []MatchedRule,
	detectionLayers map[string]bool,
) error {
	if !ec.enabled {
		return nil
	}
	
	err := &CollectedError{
		ErrorType:       ErrorTypeRuleFalsePositive,
		TenantID:        tenantID,
		SessionID:       sessionID,
		RequestID:       requestID,
		InputText:       input,
		ErrorMessage:    "Rule matched but likely false positive",
		Priority:        6,
		MatchedRules:    matchedRules,
		DetectionLayers: detectionLayers,
		Metadata: map[string]interface{}{
			"timestamp":          time.Now().Unix(),
			"matched_rule_count": len(matchedRules),
		},
	}
	
	return ec.Collect(ctx, err)
}

// GetPendingErrors 获取待处理的错误（按优先级排序）
func (ec *ErrorCollector) GetPendingErrors(ctx context.Context, limit int) ([]*QueuedError, error) {
	query := `
		SELECT 
			id, error_type, error_hash, priority,
			tenant_id, session_id, request_id,
			input_text, input_hash, error_message,
			error_details, detection_layers, matched_rules, policy_config,
			status, retry_count, max_retries,
			created_at, started_at, completed_at, metadata
		FROM error_analysis_queue
		WHERE status = $1
		ORDER BY priority DESC, created_at ASC
		LIMIT $2
	`
	
	rows, err := ec.db.QueryContext(ctx, query, QueueStatusPending, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query pending errors: %w", err)
	}
	defer rows.Close()
	
	var errors []*QueuedError
	for rows.Next() {
		var qe QueuedError
		err := rows.Scan(
			&qe.ID, &qe.ErrorType, &qe.ErrorHash, &qe.Priority,
			&qe.TenantID, &qe.SessionID, &qe.RequestID,
			&qe.InputText, &qe.InputHash, &qe.ErrorMessage,
			&qe.ErrorDetails, &qe.DetectionLayers, &qe.MatchedRules, &qe.PolicyConfig,
			&qe.Status, &qe.RetryCount, &qe.MaxRetries,
			&qe.CreatedAt, &qe.StartedAt, &qe.CompletedAt, &qe.Metadata,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan queued error: %w", err)
		}
		errors = append(errors, &qe)
	}
	
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %w", err)
	}
	
	return errors, nil
}

// UpdateStatus 更新错误状态
func (ec *ErrorCollector) UpdateStatus(ctx context.Context, queueID int64, status string) error {
	query := `
		UPDATE error_analysis_queue
		SET status = $1,
		    started_at = CASE WHEN $1 = 'processing' THEN NOW() ELSE started_at END,
		    completed_at = CASE WHEN $1 IN ('analyzed', 'failed', 'skipped') THEN NOW() ELSE completed_at END
		WHERE id = $2
	`
	
	_, err := ec.db.ExecContext(ctx, query, status, queueID)
	if err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}
	
	return nil
}

// IncrementRetry 增加重试计数
func (ec *ErrorCollector) IncrementRetry(ctx context.Context, queueID int64) error {
	query := `
		UPDATE error_analysis_queue
		SET retry_count = retry_count + 1,
		    status = CASE 
		        WHEN retry_count + 1 >= max_retries THEN 'failed'
		        ELSE 'pending'
		    END
		WHERE id = $1
	`
	
	_, err := ec.db.ExecContext(ctx, query, queueID)
	if err != nil {
		return fmt.Errorf("failed to increment retry: %w", err)
	}
	
	return nil
}

// calculateErrorHash 计算错误指纹
func (ec *ErrorCollector) calculateErrorHash(collectedErr *CollectedError) string {
	// 基于核心特征生成 hash，用于去重
	// 包含：错误类型、租户、输入 hash（如果有）、错误消息前 100 字符
	hashInput := fmt.Sprintf("%s|%s|%s",
		collectedErr.ErrorType,
		collectedErr.TenantID,
		truncateString(collectedErr.ErrorMessage, 100),
	)
	
	if collectedErr.InputText != "" {
		inputHash := sha256.Sum256([]byte(collectedErr.InputText))
		hashInput += "|" + hex.EncodeToString(inputHash[:8]) // 前 8 字节
	}
	
	hash := sha256.Sum256([]byte(hashInput))
	return hex.EncodeToString(hash[:])
}

// checkDuplicate 检查是否存在重复错误
func (ec *ErrorCollector) checkDuplicate(ctx context.Context, errorHash string, within time.Duration) (bool, int64, error) {
	query := `
		SELECT id 
		FROM error_analysis_queue
		WHERE error_hash = $1
		  AND created_at > NOW() - $2::INTERVAL
		ORDER BY created_at DESC
		LIMIT 1
	`
	
	var existingID int64
	err := ec.db.QueryRowContext(ctx, query, errorHash, fmt.Sprintf("%d seconds", int(within.Seconds()))).Scan(&existingID)
	
	if err == sql.ErrNoRows {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	
	return true, existingID, nil
}

// GetStats 获取错误统计
func (ec *ErrorCollector) GetStats(ctx context.Context) (map[string]interface{}, error) {
	query := `
		SELECT 
			error_type,
			COUNT(*) as total,
			COUNT(CASE WHEN status = 'pending' THEN 1 END) as pending,
			COUNT(CASE WHEN status = 'analyzed' THEN 1 END) as analyzed,
			MAX(created_at) as last_error_at
		FROM error_analysis_queue
		WHERE created_at > NOW() - INTERVAL '7 days'
		GROUP BY error_type
	`
	
	rows, err := ec.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get stats: %w", err)
	}
	defer rows.Close()
	
	stats := make(map[string]interface{})
	var typeStats []map[string]interface{}
	
	for rows.Next() {
		var errorType string
		var total, pending, analyzed int
		var lastErrorAt time.Time
		
		err := rows.Scan(&errorType, &total, &pending, &analyzed, &lastErrorAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan stats: %w", err)
		}
		
		typeStats = append(typeStats, map[string]interface{}{
			"error_type":    errorType,
			"total":         total,
			"pending":       pending,
			"analyzed":      analyzed,
			"last_error_at": lastErrorAt,
		})
	}
	
	stats["by_type"] = typeStats
	stats["timestamp"] = time.Now()
	
	return stats, nil
}

// Helper functions

func toNullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{Valid: false}
	}
	return sql.NullString{String: s, Valid: true}
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
