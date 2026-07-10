// Package dashboardapi - session_active.go
// 活跃会话 API：查询当前活跃的会话列表
package dashboardapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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
	SessionKey    string    `json:"session_key"`
	TenantID      string    `json:"tenant_id"`
	ClientID      string    `json:"client_id"`
	Model         string    `json:"model"`
	RequestCount  int       `json:"request_count"`
	TotalCost     float64   `json:"total_cost"`
	HealthScore   *int      `json:"health_score,omitempty"`
	HealthGrade   string    `json:"health_grade"`
	LastActiveAt  time.Time `json:"last_active_at"`
	CreatedAt     time.Time `json:"created_at"`
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

	params := ParseQueryParams(r)
	ctx, cancel := GetRequestContext(r, 15*time.Second)
	defer cancel()

	// 查询活跃会话（最近1小时有请求）
	where := []string{"last_request_at >= NOW() - INTERVAL '1 hour'"}
	args := []interface{}{}
	argIdx := 1

	if params.TenantID != "" {
		where = append(where, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, params.TenantID)
		argIdx++
	}
	whereClause := "WHERE " + joinStrings(where, " AND ")

	// 总数查询
	var totalActive int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM session_summaries %s", whereClause)
	if err := h.db.QueryRow(ctx, countQuery, args...).Scan(&totalActive); err != nil {
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to count active sessions", err.Error())
		return
	}

	// 分页查询
	offset := (params.Page - 1) * params.Size
	query := fmt.Sprintf(`
		SELECT
			session_key,
			COALESCE(tenant_id, '') as tenant_id,
			COALESCE(client_id, '') as client_id,
			COALESCE(models_used[1], '') as model,
			COALESCE(request_count, 0) as request_count,
			COALESCE(total_cost_usd, 0) as total_cost,
			health_score,
			COALESCE(health_grade, '') as health_grade,
			last_request_at,
			first_request_at
		FROM session_summaries
		%s
		ORDER BY last_request_at DESC
		LIMIT $%d OFFSET $%d
	`, whereClause, argIdx, argIdx+1)
	args = append(args, params.Size, offset)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query active sessions", err.Error())
		return
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
			apiStatus = "error"
			writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to scan session", err.Error())
			return
		}
		sessions = append(sessions, item)
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
