package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionManagementAPI 提供会话管理的列表、详情、更新等功能
// 2026-08-06: 新增会话列表页面所需的 API，支持按项目、任务、标签等维度过滤和组织会话
type SessionManagementAPI struct {
	pool *pgxpool.Pool
}

// SessionManagementItem 会话列表项
type SessionManagementItem struct {
	SessionKey         string    `json:"session_key"`
	TenantID           string    `json:"tenant_id"`
	Title              string    `json:"title"`
	Summary            string    `json:"summary,omitempty"`
	ProjectID          *string   `json:"project_id,omitempty"`
	TaskID             *string   `json:"task_id,omitempty"`
	UserTags           []string  `json:"user_tags"`
	UserIntent         *string   `json:"user_intent,omitempty"`
	Status             string    `json:"status"`
	FirstRequestAt     time.Time `json:"first_request_at"`
	LastRequestAt      time.Time `json:"last_request_at"`
	DurationSeconds    int       `json:"duration_seconds"`
	RequestCount       int       `json:"request_count"`
	SuccessCount       int       `json:"success_count"`
	ErrorCount         int       `json:"error_count"`
	TotalCostUSD       float64   `json:"total_cost_usd"`
	TotalTokens        int64     `json:"total_tokens"`
	ModelsUsed         []string  `json:"models_used"`
	PrimaryModel       *string   `json:"primary_model,omitempty"`
	LastSummarizedAt   *time.Time `json:"last_summarized_at,omitempty"`
}

// SessionManagementResponse 会话列表响应
type SessionManagementResponse struct {
	Sessions []SessionManagementItem `json:"sessions"`
	Total    int               `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
}

// SessionDetailResponse 会话详情响应
type SessionDetailResponse struct {
	SessionManagementItem
	KeyTopics       []string              `json:"key_topics,omitempty"`
	QualityScore    *int                  `json:"quality_score,omitempty"`
	Requests        []SessionRequestBrief `json:"requests,omitempty"`
	RelatedSessions *RelatedSessions      `json:"related_sessions,omitempty"`
}

// SessionRequestBrief 会话中的请求简要信息
type SessionRequestBrief struct {
	RequestID      string    `json:"request_id"`
	Timestamp      time.Time `json:"ts"`
	ClientModel    *string   `json:"client_model,omitempty"`
	RequestPreview *string   `json:"request_preview,omitempty"`
	Success        bool      `json:"success"`
	Tokens         int       `json:"tokens"`
	CostUSD        float64   `json:"cost_usd"`
	LatencyMs      *int      `json:"latency_ms,omitempty"`
}

// RelatedSessions 相关会话（同任务下的前后会话）
type RelatedSessions struct {
	Prev *SessionManagementItem `json:"prev,omitempty"`
	Next *SessionManagementItem `json:"next,omitempty"`
}

// SessionUpdateRequest 会话更新请求
type SessionUpdateRequest struct {
	ProjectID *string  `json:"project_id"`
	TaskID    *string  `json:"task_id"`
	UserTags  []string `json:"user_tags"`
	Status    *string  `json:"status"`
}

// HandleSessionsList 处理会话列表请求
// GET /api/sessions/list
func (h *Handler) handleSessionsList(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	
	// 解析查询参数
	filters := parseSessionFilters(r)
	
	// 构建查询
	query, args := buildSessionListQuery(filters)
	countQuery, countArgs := buildSessionCountQuery(filters)
	
	// 获取总数
	var total int
	err := h.db.QueryRow(ctx, countQuery, countArgs...).Scan(&total)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to count sessions: %v", err), http.StatusInternalServerError)
		return
	}
	
	// 查询会话列表
	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to query sessions: %v", err), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	
	sessions := make([]SessionManagementItem, 0)
	for rows.Next() {
		var item SessionManagementItem
		var projectID, taskID, userIntent, primaryModel sql.NullString
		var lastSummarizedAt sql.NullTime
		
		err := rows.Scan(
			&item.SessionKey,
			&item.TenantID,
			&item.Title,
			&item.Summary,
			&projectID,
			&taskID,
			&item.UserTags,
			&userIntent,
			&item.Status,
			&item.FirstRequestAt,
			&item.LastRequestAt,
			&item.DurationSeconds,
			&item.RequestCount,
			&item.SuccessCount,
			&item.ErrorCount,
			&item.TotalCostUSD,
			&item.TotalTokens,
			&item.ModelsUsed,
			&primaryModel,
			&lastSummarizedAt,
		)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to scan session: %v", err), http.StatusInternalServerError)
			return
		}
		
		if projectID.Valid {
			item.ProjectID = &projectID.String
		}
		if taskID.Valid {
			item.TaskID = &taskID.String
		}
		if userIntent.Valid {
			item.UserIntent = &userIntent.String
		}
		if primaryModel.Valid {
			item.PrimaryModel = &primaryModel.String
		}
		if lastSummarizedAt.Valid {
			item.LastSummarizedAt = &lastSummarizedAt.Time
		}
		
		sessions = append(sessions, item)
	}
	
	if err := rows.Err(); err != nil {
		http.Error(w, fmt.Sprintf("error iterating sessions: %v", err), http.StatusInternalServerError)
		return
	}
	
	response := SessionManagementResponse{
		Sessions: sessions,
		Total:    total,
		Page:     filters.Page,
		PageSize: filters.PageSize,
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// HandleSessionDetail 处理会话详情请求
// GET /api/sessions/{session_key}
func (h *Handler) handleSessionDetail(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	sessionKey := strings.TrimPrefix(r.URL.Path, "/api/sessions/detail/")
	if sessionKey == "" {
		http.Error(w, "session_key required", http.StatusBadRequest)
		return
	}
	
	// 查询会话基本信息
	var detail SessionDetailResponse
	var projectID, taskID, userIntent, primaryModel sql.NullString
	var lastSummarizedAt sql.NullTime
	var qualityScore sql.NullInt32
	
	query := `
		SELECT 
			session_key, tenant_id, title, summary, gw_project_id, gw_task_id,
			user_tags, user_intent, session_status, first_request_at, last_request_at,
			duration_seconds, request_count, success_count, error_count,
			total_cost_usd, total_tokens, models_used, primary_model,
			last_summarized_at, key_topics, quality_score,
			total_prompt_tokens, total_completion_tokens
		FROM session_summaries
		WHERE session_key = $1
	`
	
	var totalPromptTokens, totalCompletionTokens int64
	err := h.db.QueryRow(ctx, query, sessionKey).Scan(
		&detail.SessionKey,
		&detail.TenantID,
		&detail.Title,
		&detail.Summary,
		&projectID,
		&taskID,
		&detail.UserTags,
		&userIntent,
		&detail.Status,
		&detail.FirstRequestAt,
		&detail.LastRequestAt,
		&detail.DurationSeconds,
		&detail.RequestCount,
		&detail.SuccessCount,
		&detail.ErrorCount,
		&detail.TotalCostUSD,
		&detail.TotalTokens,
		&detail.ModelsUsed,
		&primaryModel,
		&lastSummarizedAt,
		&detail.KeyTopics,
		&qualityScore,
		&totalPromptTokens,
		&totalCompletionTokens,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("failed to query session: %v", err), http.StatusInternalServerError)
		return
	}
	
	if projectID.Valid {
		detail.ProjectID = &projectID.String
	}
	if taskID.Valid {
		detail.TaskID = &taskID.String
	}
	if userIntent.Valid {
		detail.UserIntent = &userIntent.String
	}
	if primaryModel.Valid {
		detail.PrimaryModel = &primaryModel.String
	}
	if lastSummarizedAt.Valid {
		detail.LastSummarizedAt = &lastSummarizedAt.Time
	}
	if qualityScore.Valid {
		score := int(qualityScore.Int32)
		detail.QualityScore = &score
	}
	
	// 查询会话中的请求列表（最近50条）
	requestsQuery := `
		SELECT request_id, ts, client_model, request_preview, success, 
		       total_tokens, cost_usd, latency_ms
		FROM request_logs_hot
		WHERE gw_session_id = $1
		ORDER BY ts DESC
		LIMIT 50
	`
	
	rows, err := h.db.Query(ctx, requestsQuery, sessionKey)
	if err != nil {
		// 非关键错误，继续返回基本信息
		detail.Requests = []SessionRequestBrief{}
	} else {
		defer rows.Close()
		requests := make([]SessionRequestBrief, 0)
		for rows.Next() {
			var req SessionRequestBrief
			var clientModel, requestPreview sql.NullString
			var latencyMs sql.NullInt32
			
			err := rows.Scan(
				&req.RequestID,
				&req.Timestamp,
				&clientModel,
				&requestPreview,
				&req.Success,
				&req.Tokens,
				&req.CostUSD,
				&latencyMs,
			)
			if err == nil {
				if clientModel.Valid {
					req.ClientModel = &clientModel.String
				}
				if requestPreview.Valid {
					req.RequestPreview = &requestPreview.String
				}
				if latencyMs.Valid {
					latency := int(latencyMs.Int32)
					req.LatencyMs = &latency
				}
				requests = append(requests, req)
			}
		}
		detail.Requests = requests
	}
	
	// 查询相关会话（同任务下的前后会话）
	if detail.TaskID != nil {
		detail.RelatedSessions = h.queryRelatedSessions(ctx, sessionKey, *detail.TaskID, detail.TenantID, detail.FirstRequestAt)
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(detail)
}

// HandleSessionUpdate 处理会话更新请求
// PATCH /api/sessions/{session_key}
func (h *Handler) handleSessionUpdate(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	sessionKey := strings.TrimPrefix(r.URL.Path, "/api/sessions/update/")
	if sessionKey == "" {
		http.Error(w, "session_key required", http.StatusBadRequest)
		return
	}
	
	var req SessionUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request body: %v", err), http.StatusBadRequest)
		return
	}
	
	// 构建更新语句
	updates := make([]string, 0)
	args := make([]interface{}, 0)
	argIdx := 1
	
	if req.ProjectID != nil {
		updates = append(updates, fmt.Sprintf("gw_project_id = $%d", argIdx))
		args = append(args, *req.ProjectID)
		argIdx++
	}
	if req.TaskID != nil {
		updates = append(updates, fmt.Sprintf("gw_task_id = $%d", argIdx))
		args = append(args, *req.TaskID)
		argIdx++
	}
	if req.UserTags != nil {
		updates = append(updates, fmt.Sprintf("user_tags = $%d", argIdx))
		args = append(args, req.UserTags)
		argIdx++
	}
	if req.Status != nil {
		updates = append(updates, fmt.Sprintf("session_status = $%d", argIdx))
		args = append(args, *req.Status)
		argIdx++
	}
	
	if len(updates) == 0 {
		http.Error(w, "no fields to update", http.StatusBadRequest)
		return
	}
	
	updates = append(updates, "updated_at = NOW()")
	args = append(args, sessionKey)
	
	query := fmt.Sprintf(`
		UPDATE session_summaries
		SET %s
		WHERE session_key = $%d
	`, strings.Join(updates, ", "), argIdx)
	
	_, err := h.db.Exec(ctx, query, args...)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to update session: %v", err), http.StatusInternalServerError)
		return
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":     true,
		"session_key": sessionKey,
	})
}

// queryRelatedSessions 查询相关会话（同任务下的前后会话）
func (h *Handler) queryRelatedSessions(ctx context.Context, sessionKey, taskID, tenantID string, firstRequestAt time.Time) *RelatedSessions {
	related := &RelatedSessions{}
	
	// 查询前一个会话
	prevQuery := `
		SELECT session_key, tenant_id, title, gw_project_id, gw_task_id, user_tags,
		       session_status, first_request_at, last_request_at, duration_seconds,
		       request_count, success_count, error_count, total_cost_usd, total_tokens
		FROM session_summaries
		WHERE tenant_id = $1 AND gw_task_id = $2 AND first_request_at < $3
		ORDER BY first_request_at DESC
		LIMIT 1
	`
	
	var prev SessionManagementItem
	var projectID, taskIDPrev sql.NullString
	err := h.db.QueryRow(ctx, prevQuery, tenantID, taskID, firstRequestAt).Scan(
		&prev.SessionKey, &prev.TenantID, &prev.Title, &projectID, &taskIDPrev,
		&prev.UserTags, &prev.Status, &prev.FirstRequestAt, &prev.LastRequestAt,
		&prev.DurationSeconds, &prev.RequestCount, &prev.SuccessCount, &prev.ErrorCount,
		&prev.TotalCostUSD, &prev.TotalTokens,
	)
	if err == nil {
		if projectID.Valid {
			prev.ProjectID = &projectID.String
		}
		if taskIDPrev.Valid {
			prev.TaskID = &taskIDPrev.String
		}
		related.Prev = &prev
	}
	
	// 查询后一个会话
	nextQuery := `
		SELECT session_key, tenant_id, title, gw_project_id, gw_task_id, user_tags,
		       session_status, first_request_at, last_request_at, duration_seconds,
		       request_count, success_count, error_count, total_cost_usd, total_tokens
		FROM session_summaries
		WHERE tenant_id = $1 AND gw_task_id = $2 AND first_request_at > $3
		ORDER BY first_request_at ASC
		LIMIT 1
	`
	
	var next SessionManagementItem
	var projectIDNext, taskIDNext sql.NullString
	err = h.db.QueryRow(ctx, nextQuery, tenantID, taskID, firstRequestAt).Scan(
		&next.SessionKey, &next.TenantID, &next.Title, &projectIDNext, &taskIDNext,
		&next.UserTags, &next.Status, &next.FirstRequestAt, &next.LastRequestAt,
		&next.DurationSeconds, &next.RequestCount, &next.SuccessCount, &next.ErrorCount,
		&next.TotalCostUSD, &next.TotalTokens,
	)
	if err == nil {
		if projectIDNext.Valid {
			next.ProjectID = &projectIDNext.String
		}
		if taskIDNext.Valid {
			next.TaskID = &taskIDNext.String
		}
		related.Next = &next
	}
	
	if related.Prev == nil && related.Next == nil {
		return nil
	}
	
	return related
}

// SessionFilters 会话过滤条件
type SessionFilters struct {
	TenantID  string
	ProjectID string
	TaskID    string
	Tags      []string
	Status    string
	Search    string
	FromDate  *time.Time
	ToDate    *time.Time
	SortBy    string
	SortOrder string
	Page      int
	PageSize  int
}

// parseSessionFilters 解析查询参数
func parseSessionFilters(r *http.Request) SessionFilters {
	q := r.URL.Query()
	
	filters := SessionFilters{
		TenantID:  strings.TrimSpace(q.Get("tenant_id")),
		ProjectID: strings.TrimSpace(q.Get("project_id")),
		TaskID:    strings.TrimSpace(q.Get("task_id")),
		Status:    strings.TrimSpace(q.Get("status")),
		Search:    strings.TrimSpace(q.Get("search")),
		SortBy:    strings.TrimSpace(q.Get("sort_by")),
		SortOrder: strings.TrimSpace(q.Get("sort_order")),
		Page:      1,
		PageSize:  20,
	}
	
	// 解析标签（逗号分隔）
	if tagsStr := strings.TrimSpace(q.Get("tags")); tagsStr != "" {
		filters.Tags = strings.Split(tagsStr, ",")
	}
	
	// 解析日期范围
	if fromStr := strings.TrimSpace(q.Get("from_date")); fromStr != "" {
		if t, err := time.Parse("2006-01-02", fromStr); err == nil {
			filters.FromDate = &t
		}
	}
	if toStr := strings.TrimSpace(q.Get("to_date")); toStr != "" {
		if t, err := time.Parse("2006-01-02", toStr); err == nil {
			filters.ToDate = &t
		}
	}
	
	// 解析分页
	if pageStr := q.Get("page"); pageStr != "" {
		if page, err := strconv.Atoi(pageStr); err == nil && page > 0 {
			filters.Page = page
		}
	}
	if pageSizeStr := q.Get("page_size"); pageSizeStr != "" {
		if pageSize, err := strconv.Atoi(pageSizeStr); err == nil && pageSize > 0 && pageSize <= 100 {
			filters.PageSize = pageSize
		}
	}
	
	// 默认排序
	if filters.SortBy == "" {
		filters.SortBy = "last_request_at"
	}
	if filters.SortOrder == "" {
		filters.SortOrder = "desc"
	}
	
	return filters
}

// buildSessionListQuery 构建会话列表查询
func buildSessionListQuery(filters SessionFilters) (string, []interface{}) {
	where := make([]string, 0)
	args := make([]interface{}, 0)
	argIdx := 1
	
	// 租户过滤
	if filters.TenantID != "" {
		where = append(where, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, filters.TenantID)
		argIdx++
	}
	
	// 项目过滤
	if filters.ProjectID != "" {
		where = append(where, fmt.Sprintf("gw_project_id = $%d", argIdx))
		args = append(args, filters.ProjectID)
		argIdx++
	}
	
	// 任务过滤
	if filters.TaskID != "" {
		where = append(where, fmt.Sprintf("gw_task_id = $%d", argIdx))
		args = append(args, filters.TaskID)
		argIdx++
	}
	
	// 标签过滤（包含任一标签）
	if len(filters.Tags) > 0 {
		where = append(where, fmt.Sprintf("user_tags && $%d", argIdx))
		args = append(args, filters.Tags)
		argIdx++
	}
	
	// 状态过滤
	if filters.Status != "" {
		where = append(where, fmt.Sprintf("session_status = $%d", argIdx))
		args = append(args, filters.Status)
		argIdx++
	}
	
	// 全文搜索
	if filters.Search != "" {
		where = append(where, fmt.Sprintf("search_vector @@ plainto_tsquery('simple', $%d)", argIdx))
		args = append(args, filters.Search)
		argIdx++
	}
	
	// 日期范围
	if filters.FromDate != nil {
		where = append(where, fmt.Sprintf("first_request_at >= $%d", argIdx))
		args = append(args, *filters.FromDate)
		argIdx++
	}
	if filters.ToDate != nil {
		where = append(where, fmt.Sprintf("last_request_at <= $%d", argIdx))
		args = append(args, *filters.ToDate)
		argIdx++
	}
	
	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}
	
	// 排序
	orderBy := fmt.Sprintf("ORDER BY %s %s", filters.SortBy, strings.ToUpper(filters.SortOrder))
	
	// 分页
	offset := (filters.Page - 1) * filters.PageSize
	limit := filters.PageSize
	
	query := fmt.Sprintf(`
		SELECT 
			session_key, tenant_id, title, COALESCE(summary, ''), gw_project_id, gw_task_id,
			user_tags, user_intent, session_status, first_request_at, last_request_at,
			duration_seconds, request_count, success_count, error_count,
			total_cost_usd, total_tokens, models_used, primary_model, last_summarized_at
		FROM session_summaries
		%s
		%s
		LIMIT %d OFFSET %d
	`, whereClause, orderBy, limit, offset)
	
	return query, args
}

// buildSessionCountQuery 构建会话计数查询
func buildSessionCountQuery(filters SessionFilters) (string, []interface{}) {
	where := make([]string, 0)
	args := make([]interface{}, 0)
	argIdx := 1
	
	if filters.TenantID != "" {
		where = append(where, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, filters.TenantID)
		argIdx++
	}
	if filters.ProjectID != "" {
		where = append(where, fmt.Sprintf("gw_project_id = $%d", argIdx))
		args = append(args, filters.ProjectID)
		argIdx++
	}
	if filters.TaskID != "" {
		where = append(where, fmt.Sprintf("gw_task_id = $%d", argIdx))
		args = append(args, filters.TaskID)
		argIdx++
	}
	if len(filters.Tags) > 0 {
		where = append(where, fmt.Sprintf("user_tags && $%d", argIdx))
		args = append(args, filters.Tags)
		argIdx++
	}
	if filters.Status != "" {
		where = append(where, fmt.Sprintf("session_status = $%d", argIdx))
		args = append(args, filters.Status)
		argIdx++
	}
	if filters.Search != "" {
		where = append(where, fmt.Sprintf("search_vector @@ plainto_tsquery('simple', $%d)", argIdx))
		args = append(args, filters.Search)
		argIdx++
	}
	if filters.FromDate != nil {
		where = append(where, fmt.Sprintf("first_request_at >= $%d", argIdx))
		args = append(args, *filters.FromDate)
		argIdx++
	}
	if filters.ToDate != nil {
		where = append(where, fmt.Sprintf("last_request_at <= $%d", argIdx))
		args = append(args, *filters.ToDate)
		argIdx++
	}
	
	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}
	
	query := fmt.Sprintf("SELECT COUNT(*) FROM session_summaries %s", whereClause)
	return query, args
}
