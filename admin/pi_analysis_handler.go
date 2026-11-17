package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/kaixuan/llm-gateway-go/domains/promptinjection"
)

// PIAnalysisHandler PI 分析管理接口
type PIAnalysisHandler struct {
	collector *promptinjection.ErrorCollector
	analyzer  *promptinjection.ErrorAnalyzer
	db        *sql.DB
}

// NewPIAnalysisHandler 创建处理器
func NewPIAnalysisHandler(
	collector *promptinjection.ErrorCollector,
	analyzer *promptinjection.ErrorAnalyzer,
	db *sql.DB,
) *PIAnalysisHandler {
	return &PIAnalysisHandler{
		collector: collector,
		analyzer:  analyzer,
		db:        db,
	}
}

// Routes 返回路由
func (h *PIAnalysisHandler) Routes() chi.Router {
	r := chi.NewRouter()
	
	r.Get("/queue", h.GetQueue)
	r.Get("/results", h.GetResults)
	r.Get("/results/{queue_id}", h.GetResult)
	r.Post("/trigger", h.TriggerAnalysis)
	r.Get("/stats", h.GetStats)
	
	return r
}

// GetQueue 获取错误队列
func (h *PIAnalysisHandler) GetQueue(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")
	
	limit := 50
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 200 {
			limit = l
		}
	}
	
	offset := 0
	if offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}
	
	// 构建查询
	query := `
		SELECT 
			id, error_type, error_hash, priority,
			tenant_id, session_id, request_id,
			error_message, status, retry_count, created_at
		FROM error_analysis_queue
		WHERE 1=1
	`
	args := []interface{}{}
	argPos := 1
	
	if status != "" {
		query += fmt.Sprintf(" AND status = $%d", argPos)
		args = append(args, status)
		argPos++
	}
	
	query += " ORDER BY priority DESC, created_at DESC"
	query += fmt.Sprintf(" LIMIT $%d OFFSET $%d", argPos, argPos+1)
	args = append(args, limit, offset)
	
	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	
	var errors []map[string]interface{}
	for rows.Next() {
		var e struct {
			ID          int64
			ErrorType   string
			ErrorHash   string
			Priority    int
			TenantID    string
			SessionID   sql.NullString
			RequestID   sql.NullString
			ErrorMsg    string
			Status      string
			RetryCount  int
			CreatedAt   string
		}
		
		if err := rows.Scan(
			&e.ID, &e.ErrorType, &e.ErrorHash, &e.Priority,
			&e.TenantID, &e.SessionID, &e.RequestID,
			&e.ErrorMsg, &e.Status, &e.RetryCount, &e.CreatedAt,
		); err != nil {
			continue
		}
		
		errors = append(errors, map[string]interface{}{
			"id":            e.ID,
			"error_type":    e.ErrorType,
			"error_hash":    e.ErrorHash,
			"priority":      e.Priority,
			"tenant_id":     e.TenantID,
			"session_id":    e.SessionID.String,
			"request_id":    e.RequestID.String,
			"error_message": e.ErrorMsg,
			"status":        e.Status,
			"retry_count":   e.RetryCount,
			"created_at":    e.CreatedAt,
		})
	}
	
	// 获取总数
	countQuery := `SELECT COUNT(*) FROM error_analysis_queue WHERE 1=1`
	if status != "" {
		countQuery += " AND status = $1"
		var total int64
		h.db.QueryRowContext(r.Context(), countQuery, status).Scan(&total)
		
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"errors": errors,
			"total":  total,
			"limit":  limit,
			"offset": offset,
		})
	} else {
		var total int64
		h.db.QueryRowContext(r.Context(), countQuery).Scan(&total)
		
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"errors": errors,
			"total":  total,
			"limit":  limit,
			"offset": offset,
		})
	}
}

// GetResults 获取分析结果列表
func (h *PIAnalysisHandler) GetResults(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")
	
	limit := 50
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 200 {
			limit = l
		}
	}
	
	offset := 0
	if offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}
	
	query := `
		SELECT 
			r.id, r.analysis_id, r.queue_id,
			r.root_cause, r.error_category, r.is_false_positive,
			r.confidence_score, r.severity_level, r.suggested_fix,
			q.error_type, q.tenant_id, r.created_at
		FROM error_analysis_results r
		JOIN error_analysis_queue q ON r.queue_id = q.id
		ORDER BY r.created_at DESC
		LIMIT $1 OFFSET $2
	`
	
	rows, err := h.db.QueryContext(r.Context(), query, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	
	var results []map[string]interface{}
	for rows.Next() {
		var r struct {
			ID              int64
			AnalysisID      string
			QueueID         int64
			RootCause       string
			ErrorCategory   string
			IsFalsePositive bool
			ConfidenceScore float64
			SeverityLevel   string
			SuggestedFix    string
			ErrorType       string
			TenantID        string
			CreatedAt       string
		}
		
		if err := rows.Scan(
			&r.ID, &r.AnalysisID, &r.QueueID,
			&r.RootCause, &r.ErrorCategory, &r.IsFalsePositive,
			&r.ConfidenceScore, &r.SeverityLevel, &r.SuggestedFix,
			&r.ErrorType, &r.TenantID, &r.CreatedAt,
		); err != nil {
			continue
		}
		
		results = append(results, map[string]interface{}{
			"id":                r.ID,
			"analysis_id":       r.AnalysisID,
			"queue_id":          r.QueueID,
			"root_cause":        r.RootCause,
			"error_category":    r.ErrorCategory,
			"is_false_positive": r.IsFalsePositive,
			"confidence_score":  r.ConfidenceScore,
			"severity_level":    r.SeverityLevel,
			"suggested_fix":     r.SuggestedFix,
			"error_type":        r.ErrorType,
			"tenant_id":         r.TenantID,
			"created_at":        r.CreatedAt,
		})
	}
	
	// 获取总数
	var total int64
	h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM error_analysis_results`).Scan(&total)
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"results": results,
		"total":   total,
		"limit":   limit,
		"offset":  offset,
	})
}

// GetResult 获取单个分析结果详情
func (h *PIAnalysisHandler) GetResult(w http.ResponseWriter, r *http.Request) {
	queueIDStr := chi.URLParam(r, "queue_id")
	queueID, err := strconv.ParseInt(queueIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid queue_id", http.StatusBadRequest)
		return
	}
	
	query := `
		SELECT 
			r.id, r.analysis_id, r.queue_id,
			r.root_cause, r.error_category, r.is_false_positive,
			r.confidence_score, r.suggested_fix,
			r.rule_adjustment, r.code_changes,
			r.severity_level, r.impact_scope, r.affected_users_estimate,
			r.similar_cases, r.pattern_frequency,
			r.analysis_duration_ms, r.llm_tokens_used,
			r.created_at,
			q.error_type, q.error_message, q.tenant_id, q.input_text
		FROM error_analysis_results r
		JOIN error_analysis_queue q ON r.queue_id = q.id
		WHERE r.queue_id = $1
	`
	
	var result struct {
		ID                    int64
		AnalysisID            string
		QueueID               int64
		RootCause             string
		ErrorCategory         string
		IsFalsePositive       bool
		ConfidenceScore       float64
		SuggestedFix          string
		RuleAdjustment        json.RawMessage
		CodeChanges           json.RawMessage
		SeverityLevel         string
		ImpactScope           string
		AffectedUsersEstimate int
		SimilarCases          json.RawMessage
		PatternFrequency      int
		AnalysisDurationMs    int
		LLMTokensUsed         int
		CreatedAt             string
		ErrorType             string
		ErrorMessage          string
		TenantID              string
		InputText             sql.NullString
	}
	
	err = h.db.QueryRowContext(r.Context(), query, queueID).Scan(
		&result.ID, &result.AnalysisID, &result.QueueID,
		&result.RootCause, &result.ErrorCategory, &result.IsFalsePositive,
		&result.ConfidenceScore, &result.SuggestedFix,
		&result.RuleAdjustment, &result.CodeChanges,
		&result.SeverityLevel, &result.ImpactScope, &result.AffectedUsersEstimate,
		&result.SimilarCases, &result.PatternFrequency,
		&result.AnalysisDurationMs, &result.LLMTokensUsed,
		&result.CreatedAt,
		&result.ErrorType, &result.ErrorMessage, &result.TenantID, &result.InputText,
	)
	
	if err == sql.ErrNoRows {
		http.Error(w, "result not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// TriggerAnalysis 手动触发分析
func (h *PIAnalysisHandler) TriggerAnalysis(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BatchSize int `json:"batch_size"`
	}
	
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		req.BatchSize = 10
	}
	
	if req.BatchSize <= 0 || req.BatchSize > 100 {
		req.BatchSize = 10
	}
	
	// 在后台执行
	go func() {
		ctx := context.Background()
		processed, err := h.analyzer.ProcessBatch(ctx)
		if err != nil {
			fmt.Printf("Manual analysis failed: %v\n", err)
		} else {
			fmt.Printf("Manual analysis completed: processed=%d\n", processed)
		}
	}()
	
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message":    "Analysis triggered",
		"batch_size": req.BatchSize,
	})
}

// GetStats 获取统计信息
func (h *PIAnalysisHandler) GetStats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.collector.GetStats(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}
