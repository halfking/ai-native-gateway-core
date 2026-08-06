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
	TaskID     string            `json:"task_id"`
	ProjectID  *string           `json:"project_id,omitempty"`
	Summary    TaskSummary       `json:"summary"`
	Sessions   []TaskSessionItem `json:"sessions"`
	DailyCosts []DailyCostItem   `json:"daily_costs,omitempty"`
}

// TaskSummary 任务汇总统计
type TaskSummary struct {
	SessionCount    int       `json:"session_count"`
	TotalCostUSD    float64   `json:"total_cost_usd"`
	TotalTokens     int64     `json:"total_tokens"`
	TotalRequests   int       `json:"total_requests"`
	TotalSuccess    int       `json:"total_success"`
	TotalErrors     int       `json:"total_errors"`
	StartedAt       time.Time `json:"started_at"`
	LastActivityAt  time.Time `json:"last_activity_at"`
	DurationSeconds int       `json:"duration_seconds"`
	Status          string    `json:"status"`
	ModelsUsed      []string  `json:"models_used"`
	AllUserTags     []string  `json:"all_user_tags"`
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
	Date          string  `json:"date"`
	SessionCount  int     `json:"session_count"`
	TotalCostUSD  float64 `json:"total_cost_usd"`
	TotalTokens   int64   `json:"total_tokens"`
	TotalRequests int     `json:"total_requests"`
}

// ProjectCostResponse 项目成本汇总响应
type ProjectCostResponse struct {
	ProjectID  string            `json:"project_id"`
	Summary    ProjectSummary    `json:"summary"`
	Tasks      []ProjectTaskItem `json:"tasks"`
	DailyCosts []DailyCostItem   `json:"daily_costs"`
}

// ProjectSummary 项目汇总统计
type ProjectSummary struct {
	TaskCount       int       `json:"task_count"`
	SessionCount    int       `json:"session_count"`
	TotalCostUSD    float64   `json:"total_cost_usd"`
	TotalTokens     int64     `json:"total_tokens"`
	TotalRequests   int       `json:"total_requests"`
	StartedAt       time.Time `json:"started_at"`
	LastActivityAt  time.Time `json:"last_activity_at"`
	DurationSeconds int       `json:"duration_seconds"`
	Status          string    `json:"status"`
}

// ProjectTaskItem 项目中的任务项
type ProjectTaskItem struct {
	TaskID       string    `json:"task_id"`
	SessionCount int       `json:"session_count"`
	CostUSD      float64   `json:"cost_usd"`
	Tokens       int64     `json:"tokens"`
	Status       string    `json:"status"`
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

	summary, err := h.queryTaskSummary(ctx, r, taskID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to query task summary: %v", err), http.StatusInternalServerError)
		return
	}
	if summary.Summary.SessionCount == 0 {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}

	// 查询任务中的所有会话（按时间排序）
	sessions, err := h.queryTaskSessions(ctx, r, taskID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to query task sessions: %v", err), http.StatusInternalServerError)
		return
	}

	// 查询每日成本
	dailyCosts, err := h.queryTaskDailyCosts(ctx, r, taskID)
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

	summary, err := h.queryProjectSummary(ctx, r, projectID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to query project summary: %v", err), http.StatusInternalServerError)
		return
	}
	if summary.SessionCount == 0 {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	// 查询项目中的所有任务
	tasks, err := h.queryProjectTasks(ctx, r, projectID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to query project tasks: %v", err), http.StatusInternalServerError)
		return
	}

	// 查询每日成本
	dailyCosts, err := h.queryProjectDailyCosts(ctx, r, projectID)
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
func (h *Handler) queryTaskSummary(ctx context.Context, r *http.Request, taskID string) (struct {
	ProjectID *string
	Summary   TaskSummary
}, error) {
	result := struct {
		ProjectID *string
		Summary   TaskSummary
	}{}

	where, args := scopedDimensionWhere(r, "sd.task_id", taskID)
	query := fmt.Sprintf(`
		SELECT
			MIN(ss.gw_project_id), COUNT(*)::int, COALESCE(SUM(ss.total_cost_usd), 0),
			COALESCE(SUM(ss.total_tokens), 0), COALESCE(SUM(ss.request_count), 0)::int,
			COALESCE(SUM(ss.success_count), 0)::int, COALESCE(SUM(ss.error_count), 0)::int,
			MIN(ss.first_request_at), MAX(ss.last_request_at),
			COALESCE(MAX(ss.duration_seconds), 0)::int,
			COALESCE((array_agg(ss.session_status ORDER BY ss.last_request_at DESC))[1], '')
		FROM session_summaries ss
		JOIN session_dim sd ON sd.gw_session_id = ss.session_key AND sd.tenant_id = ss.tenant_id
		WHERE %s`, strings.Join(where, " AND "))

	var projectID sql.NullString
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
func (h *Handler) queryTaskSessions(ctx context.Context, r *http.Request, taskID string) ([]TaskSessionItem, error) {
	where, args := scopedDimensionWhere(r, "sd.task_id", taskID)
	query := fmt.Sprintf(`
		SELECT
			ss.session_key, ss.title, COALESCE(ss.summary, ''), ss.user_intent, ss.session_status,
			ROW_NUMBER() OVER (ORDER BY ss.first_request_at ASC)::int,
			ss.first_request_at, ss.last_request_at, ss.duration_seconds,
			ss.request_count, ss.total_cost_usd, ss.total_tokens, ss.key_topics
		FROM session_summaries ss
		JOIN session_dim sd ON sd.gw_session_id = ss.session_key AND sd.tenant_id = ss.tenant_id
		WHERE %s
		ORDER BY ss.first_request_at ASC`, strings.Join(where, " AND "))

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sessions := make([]TaskSessionItem, 0)
	for rows.Next() {
		var item TaskSessionItem
		var userIntent sql.NullString
		if err := rows.Scan(
			&item.SessionKey, &item.Title, &item.Summary, &userIntent, &item.Status,
			&item.Order, &item.StartedAt, &item.LastActivityAt, &item.DurationSeconds,
			&item.RequestCount, &item.CostUSD, &item.Tokens, &item.KeyTopics,
		); err != nil {
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
func (h *Handler) queryTaskDailyCosts(ctx context.Context, r *http.Request, taskID string) ([]DailyCostItem, error) {
	where, args := scopedDimensionWhere(r, "sd.task_id", taskID)
	query := fmt.Sprintf(`
		SELECT DATE(ss.first_request_at), COUNT(*)::int, COALESCE(SUM(ss.total_cost_usd), 0),
		       COALESCE(SUM(ss.total_tokens), 0), COALESCE(SUM(ss.request_count), 0)::int
		FROM session_summaries ss
		JOIN session_dim sd ON sd.gw_session_id = ss.session_key AND sd.tenant_id = ss.tenant_id
		WHERE %s
		GROUP BY DATE(ss.first_request_at)
		ORDER BY DATE(ss.first_request_at) DESC LIMIT 30`, strings.Join(where, " AND "))

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	costs := make([]DailyCostItem, 0)
	for rows.Next() {
		var item DailyCostItem
		var date time.Time
		if err := rows.Scan(&date, &item.SessionCount, &item.TotalCostUSD, &item.TotalTokens, &item.TotalRequests); err != nil {
			return nil, err
		}
		item.Date = date.Format("2006-01-02")
		costs = append(costs, item)
	}
	return costs, rows.Err()
}

// queryProjectSummary 查询项目汇总统计（使用 v_project_summary 视图）
func (h *Handler) queryProjectSummary(ctx context.Context, r *http.Request, projectID string) (ProjectSummary, error) {
	var summary ProjectSummary
	where, args := scopedDimensionWhere(r, "ss.gw_project_id", projectID)
	query := fmt.Sprintf(`
		SELECT COUNT(DISTINCT sd.task_id)::int, COUNT(*)::int, COALESCE(SUM(ss.total_cost_usd), 0),
		       COALESCE(SUM(ss.total_tokens), 0), COALESCE(SUM(ss.request_count), 0)::int,
		       MIN(ss.first_request_at), MAX(ss.last_request_at), COALESCE(MAX(ss.duration_seconds), 0)::int,
		       COALESCE((array_agg(ss.session_status ORDER BY ss.last_request_at DESC))[1], '')
		FROM session_summaries ss
		JOIN session_dim sd ON sd.gw_session_id = ss.session_key AND sd.tenant_id = ss.tenant_id
		WHERE %s`, strings.Join(where, " AND "))
	err := h.db.QueryRow(ctx, query, args...).Scan(
		&summary.TaskCount, &summary.SessionCount, &summary.TotalCostUSD, &summary.TotalTokens,
		&summary.TotalRequests, &summary.StartedAt, &summary.LastActivityAt, &summary.DurationSeconds, &summary.Status,
	)
	return summary, err
}

// queryProjectTasks 查询项目中的所有任务
func (h *Handler) queryProjectTasks(ctx context.Context, r *http.Request, projectID string) ([]ProjectTaskItem, error) {
	where, args := scopedDimensionWhere(r, "ss.gw_project_id", projectID)
	query := fmt.Sprintf(`
		SELECT sd.task_id, COUNT(*)::int, COALESCE(SUM(ss.total_cost_usd), 0), COALESCE(SUM(ss.total_tokens), 0),
		       COALESCE((array_agg(ss.session_status ORDER BY ss.last_request_at DESC))[1], ''),
		       MIN(ss.first_request_at), MAX(ss.last_request_at)
		FROM session_summaries ss
		JOIN session_dim sd ON sd.gw_session_id = ss.session_key AND sd.tenant_id = ss.tenant_id
		WHERE %s
		GROUP BY sd.task_id ORDER BY MIN(ss.first_request_at) DESC`, strings.Join(where, " AND "))
	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := make([]ProjectTaskItem, 0)
	for rows.Next() {
		var item ProjectTaskItem
		if err := rows.Scan(&item.TaskID, &item.SessionCount, &item.CostUSD, &item.Tokens, &item.Status, &item.StartedAt, &item.LastActivity); err != nil {
			return nil, err
		}
		tasks = append(tasks, item)
	}
	return tasks, rows.Err()
}

// queryProjectDailyCosts 查询项目的每日成本
func (h *Handler) queryProjectDailyCosts(ctx context.Context, r *http.Request, projectID string) ([]DailyCostItem, error) {
	where, args := scopedDimensionWhere(r, "ss.gw_project_id", projectID)
	query := fmt.Sprintf(`
		SELECT DATE(ss.first_request_at), COUNT(*)::int, COALESCE(SUM(ss.total_cost_usd), 0),
		       COALESCE(SUM(ss.total_tokens), 0), COALESCE(SUM(ss.request_count), 0)::int
		FROM session_summaries ss
		JOIN session_dim sd ON sd.gw_session_id = ss.session_key AND sd.tenant_id = ss.tenant_id
		WHERE %s
		GROUP BY DATE(ss.first_request_at) ORDER BY DATE(ss.first_request_at) DESC LIMIT 30`, strings.Join(where, " AND "))
	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	costs := make([]DailyCostItem, 0)
	for rows.Next() {
		var item DailyCostItem
		var date time.Time
		if err := rows.Scan(&date, &item.SessionCount, &item.TotalCostUSD, &item.TotalTokens, &item.TotalRequests); err != nil {
			return nil, err
		}
		item.Date = date.Format("2006-01-02")
		costs = append(costs, item)
	}
	return costs, rows.Err()
}

func scopedDimensionWhere(r *http.Request, column, value string) ([]string, []interface{}) {
	where := []string{fmt.Sprintf("%s = $1", column)}
	args := []interface{}{value}
	argIdx := 2
	if tenantID := effectiveScopeTenant(r); tenantID != "" {
		where = append(where, fmt.Sprintf("ss.tenant_id = $%d", argIdx))
		args = append(args, tenantID)
		argIdx++
	}
	ownerFrag, ownerArgs, _ := ownerScopeClause(r, "sd.owner_user", argIdx)
	if ownerFrag != "" {
		where = append(where, strings.TrimPrefix(ownerFrag, " AND "))
		args = append(args, ownerArgs...)
	}
	return where, args
}
