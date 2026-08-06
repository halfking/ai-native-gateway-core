package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// TaskFlowAPI 提供任务脉络相关功能
// 2026-08-06: 新增任务脉络展示，显示同一任务下所有会话的流程和关联

// TaskFlowResponse 任务脉络响应
type TaskFlowResponse struct {
	TaskID      string              `json:"task_id"`
	ProjectID   *string             `json:"project_id,omitempty"`
	Summary     TaskSummary         `json:"summary"`
	Sessions    []TaskSessionItem   `json:"sessions"`
	DailyCosts  []DailyCostItem     `json:"daily_costs,omitempty"`
}

// TaskSummary 任务汇总统计
type TaskSummary struct {
	SessionCount       int       `json:"session_count"`
	TotalCostUSD       float64   `json:"total_cost_usd"`
	TotalTokens        int64     `json:"total_tokens"`
	TotalRequests      int       `json:"total_requests"`
	TotalSuccess       int       `json:"total_success"`
	TotalErrors        int       `json:"total_errors"`
	StartedAt          time.Time `json:"started_at"`
	LastActivityAt     time.Time `json:"last_activity_at"`
	DurationSeconds    int       `json:"duration_seconds"`
	Status             string    `json:"status"`
	ModelsUsed         []string  `json:"models_used"`
	AllUserTags        []string  `json:"all_user_tags"`
}

// TaskSessionItem 任务中的会话项（带顺序和关系）
type TaskSessionItem struct {
	SessionKey      string    `json:"session_key"`
	Title           string    `json:"title"`
	Summary         string    `json:"summary,omitempty"`
	UserIntent      *string   `json:"user_intent,omitempty"`
	Status          string    `json:"status"`
	Order           int       `json:"order"` // 在任务中的顺序
	StartedAt       time.Time `json:"started_at"`
	LastActivityAt  time.Time `json:"last_activity_at"`
	DurationSeconds int       `json:"duration_seconds"`
	RequestCount    int       `json:"request_count"`
	CostUSD         float64   `json:"cost_usd"`
	Tokens          int64     `json:"tokens"`
	KeyTopics       []string  `json:"key_topics,omitempty"`
}

// DailyCostItem 每日成本项
type DailyCostItem struct {
	Date         string  `json:"date"`
	SessionCount int     `json:"session_count"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	TotalTokens  int64   `json:"total_tokens"`
	TotalRequests int    `json:"total_requests"`
}

// ProjectCostResponse 项目成本汇总响应
type ProjectCostResponse struct {
	ProjectID   string              `json:"project_id"`
	Summary     ProjectSummary      `json:"summary"`
	Tasks       []ProjectTaskItem   `json:"tasks"`
	DailyCosts  []DailyCostItem     `json:"daily_costs"`
}

// ProjectSummary 项目汇总统计
type ProjectSummary struct {
	TaskCount          int       `json:"task_count"`
	SessionCount       int       `json:"session_count"`
	TotalCostUSD       float64   `json:"total_cost_usd"`
	TotalTokens        int64     `json:"total_tokens"`
	TotalRequests      int       `json:"total_requests"`
	StartedAt          time.Time `json:"started_at"`
	LastActivityAt     time.Time `json:"last_activity_at"`
	DurationSeconds    int       `json:"duration_seconds"`
	Status             string    `json:"status"`
}

// ProjectTaskItem 项目中的任务项
type ProjectTaskItem struct {
	TaskID       string  `json:"task_id"`
	SessionCount int     `json:"session_count"`
	CostUSD      float64 `json:"cost_usd"`
	Tokens       int64   `json:"tokens"`
	Status       string  `json:"status"`
	StartedAt    time.Time `json:"started_at"`
	LastActivity time.Time `json:"last_activity"`
}

// HandleTaskFlow 处理任务脉络请求
// GET /api/sessions/task-flow/{task_id}
func (h *Handler) handleTaskFlow(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	taskID := strings.TrimPrefix(r.URL.Path, "/api/sessions/task-flow/")
	if taskID == "" {
		http.Error(w, "task_id required", http.StatusBadRequest)
		return
	}
	
	tenantID := r.URL.Query().Get("tenant_id")
	
	// 查询任务汇总
	summary, err := h.queryTaskSummary(ctx, taskID, tenantID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to query task summary: %v", err), http.StatusInternalServerError)
		return
	}
	
	// 查询任务中的所有会话（按时间排序）
	sessions, err := h.queryTaskSessions(ctx, taskID, tenantID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to query task sessions: %v", err), http.StatusInternalServerError)
		return
	}
	
	// 查询每日成本
	dailyCosts, err := h.queryTaskDailyCosts(ctx, taskID, tenantID)
	if err != nil {
		// 非关键错误，继续返回
		dailyCosts = []DailyCostItem{}
	}
	
	response := TaskFlowResponse{
		TaskID:     taskID,
		ProjectID:  summary.ProjectID,
		Summary:    summary.Summary,
		Sessions:   sessions,
		DailyCosts: dailyCosts,
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// HandleProjectCosts 处理项目成本汇总请求
// GET /api/sessions/project-costs/{project_id}
func (h *Handler) handleProjectCosts(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	projectID := strings.TrimPrefix(r.URL.Path, "/api/sessions/project-costs/")
	if projectID == "" {
		http.Error(w, "project_id required", http.StatusBadRequest)
		return
	}
	
	tenantID := r.URL.Query().Get("tenant_id")
	
	// 查询项目汇总
	summary, err := h.queryProjectSummary(ctx, projectID, tenantID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to query project summary: %v", err), http.StatusInternalServerError)
		return
	}
	
	// 查询项目中的所有任务
	tasks, err := h.queryProjectTasks(ctx, projectID, tenantID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to query project tasks: %v", err), http.StatusInternalServerError)
		return
	}
	
	// 查询每日成本
	dailyCosts, err := h.queryProjectDailyCosts(ctx, projectID, tenantID)
	if err != nil {
		dailyCosts = []DailyCostItem{}
	}
	
	response := ProjectCostResponse{
		ProjectID:  projectID,
		Summary:    summary,
		Tasks:      tasks,
		DailyCosts: dailyCosts,
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// queryTaskSummary 查询任务汇总统计（使用 v_task_summary 视图）
func (h *Handler) queryTaskSummary(ctx context.Context, taskID, tenantID string) (struct {
	ProjectID *string
	Summary   TaskSummary
}, error) {
	result := struct {
		ProjectID *string
		Summary   TaskSummary
	}{}
	
	var projectID sql.NullString
query := `
			SELECT 
				gw_project_id, session_count, total_cost_usd, total_tokens,
				total_requests, total_success, total_errors,
				task_started_at, task_last_activity_at, task_duration_seconds,
				task_status
			FROM v_task_summary
			WHERE gw_task_id = $1
		`
		
		args := []interface{}{taskID}
		if tenantID != "" {
			query += " AND tenant_id = $2"
			args = append(args, tenantID)
		}
		
		err := h.db.QueryRow(ctx, query, args...).Scan(
			&projectID,
			&result.Summary.SessionCount,
			&result.Summary.TotalCostUSD,
			&result.Summary.TotalTokens,
			&result.Summary.TotalRequests,
			&result.Summary.TotalSuccess,
			&result.Summary.TotalErrors,
			&result.Summary.StartedAt,
			&result.Summary.LastActivityAt,
			&result.Summary.DurationSeconds,
			&result.Summary.Status,
		)
	if err != nil {
		return result, err
	}
	
	if projectID.Valid {
		result.ProjectID = &projectID.String
	}
	
	return result, nil
}

// queryTaskSessions 查询任务中的所有会话
func (h *Handler) queryTaskSessions(ctx context.Context, taskID, tenantID string) ([]TaskSessionItem, error) {
	query := `
		SELECT 
			session_key, title, COALESCE(summary, ''), user_intent, session_status,
			session_order_in_task, first_request_at, last_request_at,
			duration_seconds, request_count, total_cost_usd, total_tokens,
			key_topics
		FROM v_session_flow
		WHERE gw_task_id = $1
	`
	
	args := []interface{}{taskID}
	if tenantID != "" {
		query += " AND tenant_id = $2"
		args = append(args, tenantID)
	}
	query += " ORDER BY session_order_in_task ASC"
	
	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	
	sessions := make([]TaskSessionItem, 0)
	for rows.Next() {
		var item TaskSessionItem
		var userIntent sql.NullString
		
		err := rows.Scan(
			&item.SessionKey,
			&item.Title,
			&item.Summary,
			&userIntent,
			&item.Status,
			&item.Order,
			&item.StartedAt,
			&item.LastActivityAt,
			&item.DurationSeconds,
			&item.RequestCount,
			&item.CostUSD,
			&item.Tokens,
			&item.KeyTopics,
		)
		if err != nil {
			return nil, err
		}
		
		if userIntent.Valid {
			item.UserIntent = &userIntent.String
		}
		
		sessions = append(sessions, item)
	}
	
	return sessions, rows.Err()
}

// queryTaskDailyCosts 查询任务的每日成本
func (h *Handler) queryTaskDailyCosts(ctx context.Context, taskID, tenantID string) ([]DailyCostItem, error) {
	query := `
		SELECT 
			date, session_count, total_cost_usd, total_tokens, total_requests
		FROM v_daily_session_costs
		WHERE gw_task_id = $1
	`
	
	args := []interface{}{taskID}
	if tenantID != "" {
		query += " AND tenant_id = $2"
		args = append(args, tenantID)
	}
	query += " ORDER BY date DESC LIMIT 30"
	
	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	
	costs := make([]DailyCostItem, 0)
	for rows.Next() {
		var item DailyCostItem
		var date time.Time
		
		err := rows.Scan(
			&date,
			&item.SessionCount,
			&item.TotalCostUSD,
			&item.TotalTokens,
			&item.TotalRequests,
		)
		if err != nil {
			return nil, err
		}
		
		item.Date = date.Format("2006-01-02")
		costs = append(costs, item)
	}
	
	return costs, rows.Err()
}

// queryProjectSummary 查询项目汇总统计（使用 v_project_summary 视图）
func (h *Handler) queryProjectSummary(ctx context.Context, projectID, tenantID string) (ProjectSummary, error) {
	var summary ProjectSummary
	
	query := `
		SELECT 
			task_count, session_count, total_cost_usd, total_tokens,
			total_requests, project_started_at, project_last_activity_at,
			project_duration_seconds, project_status
		FROM v_project_summary
		WHERE gw_project_id = $1
	`
	
	args := []interface{}{projectID}
	if tenantID != "" {
		query += " AND tenant_id = $2"
		args = append(args, tenantID)
	}
	
	err := h.db.QueryRow(ctx, query, args...).Scan(
		&summary.TaskCount,
		&summary.SessionCount,
		&summary.TotalCostUSD,
		&summary.TotalTokens,
		&summary.TotalRequests,
		&summary.StartedAt,
		&summary.LastActivityAt,
		&summary.DurationSeconds,
		&summary.Status,
	)
	
	return summary, err
}

// queryProjectTasks 查询项目中的所有任务
func (h *Handler) queryProjectTasks(ctx context.Context, projectID, tenantID string) ([]ProjectTaskItem, error) {
	query := `
		SELECT 
			gw_task_id, session_count, total_cost_usd, total_tokens,
			task_status, task_started_at, task_last_activity_at
		FROM v_task_summary
		WHERE gw_project_id = $1
	`
	
	args := []interface{}{projectID}
	if tenantID != "" {
		query += " AND tenant_id = $2"
		args = append(args, tenantID)
	}
	query += " ORDER BY task_started_at DESC"
	
	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	
	tasks := make([]ProjectTaskItem, 0)
	for rows.Next() {
		var item ProjectTaskItem
		
		err := rows.Scan(
			&item.TaskID,
			&item.SessionCount,
			&item.CostUSD,
			&item.Tokens,
			&item.Status,
			&item.StartedAt,
			&item.LastActivity,
		)
		if err != nil {
			return nil, err
		}
		
		tasks = append(tasks, item)
	}
	
	return tasks, rows.Err()
}

// queryProjectDailyCosts 查询项目的每日成本
func (h *Handler) queryProjectDailyCosts(ctx context.Context, projectID, tenantID string) ([]DailyCostItem, error) {
	query := `
		SELECT 
			date, 
			SUM(session_count)::int as session_count,
			SUM(total_cost_usd) as total_cost_usd,
			SUM(total_tokens)::bigint as total_tokens,
			SUM(total_requests)::int as total_requests
		FROM v_daily_session_costs
		WHERE gw_project_id = $1
	`
	
	args := []interface{}{projectID}
	if tenantID != "" {
		query += " AND tenant_id = $2"
		args = append(args, tenantID)
	}
	query += " GROUP BY date ORDER BY date DESC LIMIT 30"
	
	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	
	costs := make([]DailyCostItem, 0)
	for rows.Next() {
		var item DailyCostItem
		var date time.Time
		
		err := rows.Scan(
			&date,
			&item.SessionCount,
			&item.TotalCostUSD,
			&item.TotalTokens,
			&item.TotalRequests,
		)
		if err != nil {
			return nil, err
		}
		
		item.Date = date.Format("2006-01-02")
		costs = append(costs, item)
	}
	
	return costs, rows.Err()
}
