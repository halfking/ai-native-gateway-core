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
	"github.com/kaixuan/llm-gateway-go/internal/jsonbody"
)

// SessionManagementAPI 提供会话管理的列表、详情、更新等功能
// 2026-08-06: 新增会话列表页面所需的 API，支持按项目、任务、标签等维度过滤和组织会话
type SessionManagementAPI struct {
	pool *pgxpool.Pool
}

// SessionManagementItem 会话列表项
type SessionManagementItem struct {
	SessionKey       string     `json:"session_key"`
	TenantID         string     `json:"tenant_id"`
	Title            string     `json:"title"`
	Summary          string     `json:"summary,omitempty"`
	ProjectID        *string    `json:"project_id,omitempty"`
	TaskID           *string    `json:"task_id,omitempty"`
	UserTags         []string   `json:"user_tags"`
	UserIntent       *string    `json:"user_intent,omitempty"`
	Status           string     `json:"status"`
	FirstRequestAt   time.Time  `json:"first_request_at"`
	LastRequestAt    time.Time  `json:"last_request_at"`
	DurationSeconds  int        `json:"duration_seconds"`
	RequestCount     int        `json:"request_count"`
	SuccessCount     int        `json:"success_count"`
	ErrorCount       int        `json:"error_count"`
	TotalCostUSD     float64    `json:"total_cost_usd"`
	TotalTokens      int64      `json:"total_tokens"`
	ModelsUsed       []string   `json:"models_used"`
	PrimaryModel     *string    `json:"primary_model,omitempty"`
	LastSummarizedAt *time.Time `json:"last_summarized_at,omitempty"`
}

// SessionManagementResponse 会话列表响应
type SessionManagementResponse struct {
	Sessions []SessionManagementItem `json:"sessions"`
	Total    int                     `json:"total"`
	Page     int                     `json:"page"`
	PageSize int                     `json:"page_size"`
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
	query, args := buildSessionListQueryForRequest(r, filters)
	countQuery, countArgs := buildSessionCountQueryForRequest(r, filters)

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
				ss.session_key, ss.tenant_id, COALESCE(ss.title, ''), COALESCE(ss.summary, ''), ss.gw_project_id, sd.task_id,
				ss.user_tags, ss.user_intent, ss.session_status, ss.first_request_at, ss.last_request_at,
				ss.duration_seconds, ss.request_count, ss.success_count, ss.error_count,
				ss.total_cost_usd, ss.total_tokens, ss.models_used, ss.primary_model,
				ss.last_summarized_at, ss.key_topics, ss.quality_score,
				ss.total_prompt_tokens, ss.total_completion_tokens
			FROM session_summaries ss
			LEFT JOIN session_dim sd ON sd.gw_session_id = ss.session_key AND sd.tenant_id = ss.tenant_id
			WHERE ss.session_key = $1
		`
	args := []interface{}{sessionKey}
	if tenantID := effectiveScopeTenant(r); tenantID != "" {
		query += " AND ss.tenant_id = $2"
		args = append(args, tenantID)
	}
	ownerFrag, ownerArgs, _ := ownerScopeClause(r, "sd.owner_user", len(args)+1)
	query += ownerFrag
	args = append(args, ownerArgs...)

	var totalPromptTokens, totalCompletionTokens int64
	err := h.db.QueryRow(ctx, query, args...).Scan(
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
		`
	requestArgs := []interface{}{sessionKey}
	if tenantID := effectiveScopeTenant(r); tenantID != "" {
		requestsQuery += " AND tenant_id = $2"
		requestArgs = append(requestArgs, tenantID)
	}
	if IsRegularUser(r) {
		requestsQuery += fmt.Sprintf(" AND EXISTS (SELECT 1 FROM session_dim sd_scope WHERE sd_scope.gw_session_id = request_logs_hot.gw_session_id AND sd_scope.tenant_id = request_logs_hot.tenant_id AND sd_scope.owner_user = $%d)", len(requestArgs)+1)
		requestArgs = append(requestArgs, GetAuthContext(r).Username)
	}
	requestsQuery += " ORDER BY ts DESC LIMIT 50"

	rows, err := h.db.Query(ctx, requestsQuery, requestArgs...)
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
		detail.RelatedSessions = h.queryRelatedSessions(ctx, r, sessionKey, *detail.TaskID, detail.TenantID, detail.FirstRequestAt)
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
	if RequireSuperAdminForWrite(w, r) {
		return
	}

	ctx := r.Context()
	sessionKey := strings.TrimPrefix(r.URL.Path, "/api/sessions/update/")
	if sessionKey == "" {
		http.Error(w, "session_key required", http.StatusBadRequest)
		return
	}

	var req SessionUpdateRequest
	if err := jsonbody.DecodeRequest(r, &req, jsonbody.MaxRequiredBody, true); err != nil {
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
	sessionArg := argIdx
	argIdx++
	whereParts := []string{fmt.Sprintf("ss.session_key = $%d", sessionArg)}
	if tenantID := effectiveScopeTenant(r); tenantID != "" {
		whereParts = append(whereParts, fmt.Sprintf("ss.tenant_id = $%d", argIdx))
		args = append(args, tenantID)
		argIdx++
	}
	if IsRegularUser(r) {
		whereParts = append(whereParts, fmt.Sprintf("EXISTS (SELECT 1 FROM session_dim sd_scope WHERE sd_scope.gw_session_id = ss.session_key AND sd_scope.tenant_id = ss.tenant_id AND sd_scope.owner_user = $%d)", argIdx))
		args = append(args, GetAuthContext(r).Username)
		argIdx++
	}

	query := fmt.Sprintf(`
			UPDATE session_summaries ss
			SET %s
			WHERE %s
		`, strings.Join(updates, ", "), strings.Join(whereParts, " AND "))

	result, err := h.db.Exec(ctx, query, args...)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to update session: %v", err), http.StatusInternalServerError)
		return
	}
	if result.RowsAffected() == 0 {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":     true,
		"session_key": sessionKey,
	})
}

// queryRelatedSessions 查询相关会话（同任务下的前后会话）
func (h *Handler) queryRelatedSessions(ctx context.Context, r *http.Request, sessionKey, taskID, tenantID string, firstRequestAt time.Time) *RelatedSessions {
	related := &RelatedSessions{}

	// 查询前一个会话
	prevQuery := `
			SELECT ss.session_key, ss.tenant_id, COALESCE(ss.title, ''), ss.gw_project_id, sd.task_id, ss.user_tags,
			       ss.session_status, ss.first_request_at, ss.last_request_at, ss.duration_seconds,
			       ss.request_count, ss.success_count, ss.error_count, ss.total_cost_usd, ss.total_tokens
			FROM session_summaries ss
			LEFT JOIN session_dim sd ON sd.gw_session_id = ss.session_key AND sd.tenant_id = ss.tenant_id
			WHERE ss.tenant_id = $1 AND sd.task_id = $2 AND ss.first_request_at < $3
			ORDER BY ss.first_request_at DESC
			LIMIT 1
		`
	if ownerFrag, _, _ := ownerScopeClause(r, "sd.owner_user", 4); ownerFrag != "" {
		prevQuery = strings.Replace(prevQuery, "\t\t\tORDER BY", ownerFrag+"\n\t\t\tORDER BY", 1)
	}

	var prev SessionManagementItem
	var projectID, taskIDPrev sql.NullString
	prevArgs := []interface{}{tenantID, taskID, firstRequestAt}
	if IsRegularUser(r) {
		prevQuery = strings.Replace(prevQuery, " AND sd.owner_user = $4", "", 1)
		prevQuery = strings.Replace(prevQuery, "\n\t\t\tORDER BY", " AND sd.owner_user = $4\n\t\t\tORDER BY", 1)
		prevArgs = append(prevArgs, GetAuthContext(r).Username)
	}
	err := h.db.QueryRow(ctx, prevQuery, prevArgs...).Scan(
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
			SELECT ss.session_key, ss.tenant_id, COALESCE(ss.title, ''), ss.gw_project_id, sd.task_id, ss.user_tags,
			       ss.session_status, ss.first_request_at, ss.last_request_at, ss.duration_seconds,
			       ss.request_count, ss.success_count, ss.error_count, ss.total_cost_usd, ss.total_tokens
			FROM session_summaries ss
			LEFT JOIN session_dim sd ON sd.gw_session_id = ss.session_key AND sd.tenant_id = ss.tenant_id
			WHERE ss.tenant_id = $1 AND sd.task_id = $2 AND ss.first_request_at > $3
		`
	nextArgs := []interface{}{tenantID, taskID, firstRequestAt}
	if IsRegularUser(r) {
		nextQuery += " AND sd.owner_user = $4"
		nextArgs = append(nextArgs, GetAuthContext(r).Username)
	}
	nextQuery += " ORDER BY ss.first_request_at ASC LIMIT 1"

	var next SessionManagementItem
	var projectIDNext, taskIDNext sql.NullString
	err = h.db.QueryRow(ctx, nextQuery, nextArgs...).Scan(
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
	OwnerUser string
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
		TenantID:  effectiveScopeTenant(r),
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
	return buildSessionListQueryForRequest(nil, filters)
}

func buildSessionListQueryForRequest(r *http.Request, filters SessionFilters) (string, []interface{}) {
	where := make([]string, 0)
	args := make([]interface{}, 0)
	argIdx := 1

	if r != nil {
		filters.TenantID = effectiveScopeTenant(r)
	}
	if filters.TenantID != "" {
		where = append(where, fmt.Sprintf("ss.tenant_id = $%d", argIdx))
		args = append(args, filters.TenantID)
		argIdx++
	}
	if r != nil {
		ownerFrag, ownerArgs, next := ownerScopeClause(r, "sd.owner_user", argIdx)
		if ownerFrag != "" {
			where = append(where, strings.TrimPrefix(ownerFrag, " AND "))
			args = append(args, ownerArgs...)
			argIdx = next
		}
	}

	if filters.ProjectID != "" {
		where = append(where, fmt.Sprintf("ss.gw_project_id = $%d", argIdx))
		args = append(args, filters.ProjectID)
		argIdx++
	}
	if filters.TaskID != "" {
		where = append(where, fmt.Sprintf("sd.task_id = $%d", argIdx))
		args = append(args, filters.TaskID)
		argIdx++
	}
	if len(filters.Tags) > 0 {
		where = append(where, fmt.Sprintf("ss.user_tags && $%d", argIdx))
		args = append(args, filters.Tags)
		argIdx++
	}
	if filters.Status != "" {
		where = append(where, fmt.Sprintf("ss.session_status = $%d", argIdx))
		args = append(args, filters.Status)
		argIdx++
	}
	if filters.Search != "" {
		where = append(where, fmt.Sprintf("ss.search_vector @@ plainto_tsquery('simple', $%d)", argIdx))
		args = append(args, filters.Search)
		argIdx++
	}
	if filters.FromDate != nil {
		where = append(where, fmt.Sprintf("ss.first_request_at >= $%d", argIdx))
		args = append(args, *filters.FromDate)
		argIdx++
	}
	if filters.ToDate != nil {
		where = append(where, fmt.Sprintf("ss.last_request_at <= $%d", argIdx))
		args = append(args, *filters.ToDate)
		argIdx++
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}

	allowedSort := map[string]bool{
		"last_request_at": true, "first_request_at": true, "total_cost_usd": true,
		"request_count": true, "title": true,
	}
	if !allowedSort[filters.SortBy] {
		filters.SortBy = "last_request_at"
	}
	orderDir := strings.ToUpper(filters.SortOrder)
	if orderDir != "ASC" && orderDir != "DESC" {
		orderDir = "DESC"
	}
	orderBy := fmt.Sprintf("ORDER BY ss.%s %s", filters.SortBy, orderDir)

	offset := (filters.Page - 1) * filters.PageSize
	query := fmt.Sprintf(`
		SELECT
			ss.session_key, ss.tenant_id, COALESCE(ss.title, ''), COALESCE(ss.summary, ''), ss.gw_project_id, sd.task_id,
			ss.user_tags, ss.user_intent, ss.session_status, ss.first_request_at, ss.last_request_at,
			ss.duration_seconds, ss.request_count, ss.success_count, ss.error_count,
			ss.total_cost_usd, ss.total_tokens, ss.models_used, ss.primary_model, ss.last_summarized_at
		FROM session_summaries ss
		LEFT JOIN session_dim sd ON sd.gw_session_id = ss.session_key AND sd.tenant_id = ss.tenant_id
		%s
		%s
		LIMIT %d OFFSET %d
	`, whereClause, orderBy, filters.PageSize, offset)

	return query, args
}

// buildSessionCountQuery 构建会话计数查询
func buildSessionCountQuery(filters SessionFilters) (string, []interface{}) {
	return buildSessionCountQueryForRequest(nil, filters)
}

func buildSessionCountQueryForRequest(r *http.Request, filters SessionFilters) (string, []interface{}) {
	where := make([]string, 0)
	args := make([]interface{}, 0)
	argIdx := 1

	if r != nil {
		filters.TenantID = effectiveScopeTenant(r)
	}
	if filters.TenantID != "" {
		where = append(where, fmt.Sprintf("ss.tenant_id = $%d", argIdx))
		args = append(args, filters.TenantID)
		argIdx++
	}
	if r != nil {
		ownerFrag, ownerArgs, next := ownerScopeClause(r, "sd.owner_user", argIdx)
		if ownerFrag != "" {
			where = append(where, strings.TrimPrefix(ownerFrag, " AND "))
			args = append(args, ownerArgs...)
			argIdx = next
		}
	}
	if filters.ProjectID != "" {
		where = append(where, fmt.Sprintf("ss.gw_project_id = $%d", argIdx))
		args = append(args, filters.ProjectID)
		argIdx++
	}
	if filters.TaskID != "" {
		where = append(where, fmt.Sprintf("sd.task_id = $%d", argIdx))
		args = append(args, filters.TaskID)
		argIdx++
	}
	if len(filters.Tags) > 0 {
		where = append(where, fmt.Sprintf("ss.user_tags && $%d", argIdx))
		args = append(args, filters.Tags)
		argIdx++
	}
	if filters.Status != "" {
		where = append(where, fmt.Sprintf("ss.session_status = $%d", argIdx))
		args = append(args, filters.Status)
		argIdx++
	}
	if filters.Search != "" {
		where = append(where, fmt.Sprintf("ss.search_vector @@ plainto_tsquery('simple', $%d)", argIdx))
		args = append(args, filters.Search)
		argIdx++
	}
	if filters.FromDate != nil {
		where = append(where, fmt.Sprintf("ss.first_request_at >= $%d", argIdx))
		args = append(args, *filters.FromDate)
		argIdx++
	}
	if filters.ToDate != nil {
		where = append(where, fmt.Sprintf("ss.last_request_at <= $%d", argIdx))
		args = append(args, *filters.ToDate)
		argIdx++
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}
	query := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM session_summaries ss
		LEFT JOIN session_dim sd ON sd.gw_session_id = ss.session_key AND sd.tenant_id = ss.tenant_id
		%s
	`, whereClause)
	return query, args
}
