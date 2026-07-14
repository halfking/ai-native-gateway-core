// Package dashboardapi - session_active.go
// 活跃会话 API：查询当前活跃的会话列表
//
// 2026-07-13: 增加 session_summaries 表缺失时的优雅降级
package dashboardapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/admin/dashboarddegrade"
)

// SessionActiveHandler 活跃会话 Handler
type SessionActiveHandler struct {
	db *pgxpool.Pool
}

// NewSessionActiveHandler 创建 Handler
func NewSessionActiveHandler(db *pgxpool.Pool) *SessionActiveHandler {
	return &SessionActiveHandler{db: db}
}

// ActiveSessionItem 活跃会话项
type ActiveSessionItem struct {
	SessionKey   string    `json:"session_key"`
	TenantID     string    `json:"tenant_id"`
	ClientID     string    `json:"client_id"`
	Model        string    `json:"model"`
	RequestCount int       `json:"request_count"`
	TotalCost    float64   `json:"total_cost"`
	HealthScore  *int      `json:"health_score,omitempty"`
	HealthGrade  string    `json:"health_grade"`
	LastActiveAt time.Time `json:"last_active_at"`
	CreatedAt    time.Time `json:"created_at"`
}

// SessionActiveResponse 活跃会话响应
type SessionActiveResponse struct {
	Sessions    []ActiveSessionItem `json:"sessions"`
	TotalActive int                 `json:"total_active"`
	Page        int                 `json:"page"`
	Size        int                 `json:"size"`
}

// HandleSessionActive 处理活跃会话请求
//
// GET /api/admin/dashboard/session-active
func (h *SessionActiveHandler) HandleSessionActive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErrorJSON(w, http.StatusMethodNotAllowed, ErrCodeInvalidParam, "method not allowed", "")
		return
	}

	startTime := time.Now()
	apiStatus := "success"
	defer func() {
		recordAPIRequest("session-active", apiStatus, time.Since(startTime))
	}()

	params, _, ok := prepareDashboardRequest(w, r, h.db)
	if !ok {
		return
	}
	ctx, cancel := GetRequestContext(r, 15*time.Second)
	defer cancel()

	totalActive, sessions, err := h.queryActiveSessions(ctx, params, true)
	if err != nil && isMissingSessionDim(err) {
		totalActive, sessions, err = h.queryActiveSessions(ctx, params, false)
	}
	if err != nil {
		if dashboarddegrade.IsMissingRelationError(err) {
			h.writeDegraded(w, params, startTime, "session-active", err)
			return
		}
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query active sessions", err.Error())
		return
	}

	resp := SessionActiveResponse{
		Sessions:    sessions,
		TotalActive: totalActive,
		Page:        params.Page,
		Size:        params.Size,
	}

	metadata := &Metadata{
		Total:       totalActive,
		Page:        params.Page,
		Size:        params.Size,
		GeneratedAt: time.Now(),
		TookMs:      time.Since(startTime).Milliseconds(),
	}
	writeSuccessJSON(w, resp, metadata)
}

func isMissingSessionDim(err error) bool {
	return dashboarddegrade.IsMissingRelationError(err) &&
		dashboarddegrade.ExtractRelationName(err) == "session_dim"
}

func (h *SessionActiveHandler) queryActiveSessions(ctx context.Context, params QueryParams, withDim bool) (int, []ActiveSessionItem, error) {
	fromClause := "FROM session_summaries ss"
	if withDim {
		fromClause = `FROM session_summaries ss
		LEFT JOIN session_dim sd ON sd.gw_session_id = ss.session_key`
	}

	where := []string{"ss.last_request_at >= NOW() - INTERVAL '1 hour'"}
	args := []interface{}{}
	argIdx := 1

	appendDashboardScope(&where, params, &args, &argIdx, "ss", false)
	if withDim {
		if clause := buildOwnerWhere(params.auth, &args, &argIdx, "sd"); clause != "" {
			where = append(where, clause)
		}
	}
	whereClause := "WHERE " + joinStrings(where, " AND ")

	var totalActive int
	if err := h.db.QueryRow(ctx, fmt.Sprintf("SELECT COUNT(*) %s %s", fromClause, whereClause), args...).Scan(&totalActive); err != nil {
		return 0, nil, err
	}

	clientExpr := "COALESCE(NULLIF(ss.client_models[1], ''), '')"
	if withDim {
		clientExpr = "COALESCE(NULLIF(TRIM(sd.client_id), ''), NULLIF(ss.client_models[1], ''), '')"
	}

	offset := (params.Page - 1) * params.Size
	listArgs := append([]interface{}{}, args...)
	listArgs = append(listArgs, params.Size, offset)
	query := fmt.Sprintf(`
		SELECT
			ss.session_key,
			COALESCE(ss.tenant_id, '') as tenant_id,
			%s as client_id,
			COALESCE(ss.models_used[1], '') as model,
			COALESCE(ss.request_count, 0) as request_count,
			COALESCE(ss.total_cost_usd, 0) as total_cost,
			ss.health_score,
			COALESCE(ss.health_grade, '') as health_grade,
			ss.last_request_at,
			ss.first_request_at
		%s
		%s
		ORDER BY ss.last_request_at DESC
		LIMIT $%d OFFSET $%d
	`, clientExpr, fromClause, whereClause, argIdx, argIdx+1)

	rows, err := h.db.Query(ctx, query, listArgs...)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()

	sessions := make([]ActiveSessionItem, 0)
	for rows.Next() {
		var item ActiveSessionItem
		if err := rows.Scan(
			&item.SessionKey, &item.TenantID, &item.ClientID, &item.Model,
			&item.RequestCount, &item.TotalCost, &item.HealthScore, &item.HealthGrade,
			&item.LastActiveAt, &item.CreatedAt,
		); err != nil {
			return 0, nil, err
		}
		sessions = append(sessions, item)
	}
	return totalActive, sessions, nil
}

// writeDegraded 缺表降级 — 返回 0 值 + degraded:true，前端按空态渲染。
func (h *SessionActiveHandler) writeDegraded(w http.ResponseWriter, params QueryParams, startTime time.Time, op string, err error) {
	view := dashboarddegrade.ExtractRelationName(err)
	writeSuccessJSON(w, SessionActiveResponse{
		Sessions:    []ActiveSessionItem{},
		TotalActive: 0,
		Page:        params.Page,
		Size:        params.Size,
	}, &Metadata{
		Total:       0,
		Page:        params.Page,
		Size:        params.Size,
		GeneratedAt: time.Now(),
		TookMs:      time.Since(startTime).Milliseconds(),
		Degraded:    true,
		MissingView: view,
		Hint:        "数据视图尚未初始化，请先执行数据聚合迁移",
	})
}
