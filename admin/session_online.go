// Package admin — session_online.go
//
// V3.2 (2026-08-13) BE-B2: 在线会话浏览 API。
//
// Routes (registered in RegisterRoutes):
//
//	GET /api/admin/sessions/online              在线会话列表（Redis SCAN + session_last_requests 丰富）
//	GET /api/admin/sessions/{id}/timeline       会话多轮次时间线（主请求 + 扩展请求子树）
//
// 数据源：
//   - 在线状态：Redis session:{id} Hash（domains/session Manager）
//   - 最后请求：session_last_requests（migrations/timeout-optimization/003）
//   - 多轮次：request_logs（gw_session_id 归属）+ parent_request_id 主从关联
//
// 约束：Redis 用 SCAN 游标（禁 KEYS）；租户隔离（ADR-V3-005）。
package admin

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// OnlineSession 是在线会话列表的一项。
type OnlineSession struct {
	SessionID         string         `json:"session_id"`
	Title             string         `json:"title,omitempty"`
	LastRequestStatus string         `json:"last_request_status,omitempty"`
	LastModel         string         `json:"last_model,omitempty"`
	LastProviderID    *int           `json:"last_provider_id,omitempty"`
	LastLatencyMs     *int           `json:"last_latency_ms,omitempty"`
	LastActiveAt      string         `json:"last_active_at,omitempty"`
	DeviceCount       int            `json:"device_count,omitempty"`
	Freshness         *FreshnessInfo `json:"freshness,omitempty"` // V3.2 freshness 信息
}

// handleSessionsOnline 返回在线会话列表。
// GET /api/admin/sessions/online?limit=50&cursor=xxx
//
// V3.2 改动（LP6）：
//   - tenant 隔离：从认证上下文取 tenant_id，过滤 session_last_requests
//   - cursor 分页：base64(RFC3339Nano)，时间倒序
//   - freshness：data_source + freshness_ms + stale
func (h *Handler) handleSessionsOnline(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// 1. Tenant 隔离（契约：从认证上下文取，忽略 query 覆盖）
	auth := GetAuthContext(r)
	if auth == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	tenantID := auth.TenantID
	if tenantID == "" {
		tenantID = "default"
	}

	// 2. 解析分页参数
	rawCursor := r.URL.Query().Get("cursor")
	limitStr := r.URL.Query().Get("limit")
	limit := 20
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			limit = n
		}
	}
	params := NormalizePaginationParams(PaginationParams{Cursor: rawCursor, Limit: limit})

	// 3. 解析 cursor
	pageCursor, err := parseOnlineSessionCursor(params.Cursor)
	if err != nil {
		writeErrorWithCode(w, http.StatusBadRequest, "session.pagination_invalid_cursor", "invalid cursor")
		return
	}

	// 4. 查询数据库（tenant 过滤 + cursor 分页 + freshness）
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// 查询 session_last_requests，按 updated_at 倒序（最新的在前）
	// cursor 分页：updated_at < cursorTime（首次请求 cursorTime 为 zero，不限制）
	query := `
		SELECT slr.session_id, slr.last_request_status, COALESCE(slr.last_model,''),
		       slr.last_provider_id, slr.last_latency_ms, slr.updated_at, rl.tenant_id
		FROM session_last_requests slr
		JOIN request_logs rl ON rl.id = slr.last_request_id
		WHERE 1 = 1`
	args := []interface{}{}
	if !IsSuperAdminOrLegacy(r) {
		query += ` AND rl.tenant_id = $1`
		args = append(args, tenantID)
	}
	if !pageCursor.UpdatedAt.IsZero() {
		if pageCursor.SessionID == "" {
			query += fmt.Sprintf(` AND slr.updated_at < $%d`, len(args)+1)
			args = append(args, pageCursor.UpdatedAt)
		} else {
			query += fmt.Sprintf(` AND (slr.updated_at, slr.session_id) < ($%d, $%d)`, len(args)+1, len(args)+2)
			args = append(args, pageCursor.UpdatedAt, pageCursor.SessionID)
		}
	}
	query += ` ORDER BY slr.updated_at DESC, slr.session_id DESC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, params.Limit+1)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	now := time.Now()
	out := make([]OnlineSession, 0, params.Limit+1)
	pageKeys := make([]onlineSessionCursor, 0, params.Limit+1)

	for rows.Next() {
		var sid, status, model string
		var providerID, latency *int
		var updatedAt time.Time
		var dbTenantID string

		if err := rows.Scan(&sid, &status, &model, &providerID, &latency, &updatedAt, &dbTenantID); err != nil {
			writeError(w, http.StatusInternalServerError, "read online session failed")
			return
		}

		// 再次确认 tenant（防御性编程）
		if !IsSuperAdminOrLegacy(r) && dbTenantID != tenantID {
			continue
		}

		// 计算 freshness（数据源暂定 hot，后续可从表字段读取）
		freshness := CalculateFreshness(DataSourceHot, updatedAt, now)

		os := OnlineSession{
			SessionID:         sid,
			LastRequestStatus: status,
			LastModel:         model,
			LastProviderID:    providerID,
			LastLatencyMs:     latency,
			Freshness:         &freshness,
		}
		if !updatedAt.IsZero() {
			os.LastActiveAt = updatedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, os)
		pageKeys = append(pageKeys, onlineSessionCursor{UpdatedAt: updatedAt, SessionID: sid})
	}

	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "rows iteration failed: "+err.Error())
		return
	}

	hasMore := len(out) > params.Limit
	lastKey := onlineSessionCursor{}
	if hasMore {
		out = out[:params.Limit]
		lastKey = pageKeys[params.Limit-1]
	}

	// 5. 构建分页响应
	pagination, err := buildOnlineSessionPaginationResponse(hasMore, lastKey.UpdatedAt, lastKey.SessionID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "pagination cursor unavailable")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"sessions":    out,
		"count":       len(out),
		"next_cursor": pagination.NextCursor,
		"has_more":    pagination.HasMore,
	})
}

// SessionTurn 是会话时间线的一轮（主请求 + 扩展请求子树）。
type SessionTurn struct {
	RequestID   string         `json:"request_id"`
	RequestType string         `json:"request_type"`
	Status      string         `json:"status"`
	Model       string         `json:"model,omitempty"`
	LatencyMs   *int           `json:"latency_ms,omitempty"`
	StartedAt   string         `json:"started_at,omitempty"`
	Children    []*SessionTurn `json:"children,omitempty"`
}

// handleSessionTimeline 返回会话的多轮次时间线（主请求 + 扩展请求子树）。
// GET /api/admin/sessions/{id}/timeline
func (h *Handler) handleSessionTimeline(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session id required")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	auth := GetAuthContext(r)
	if auth == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// 查该会话的所有请求（主请求 + 扩展请求），按时间升序。
	// 主请求：gw_session_id = sessionID 且 parent_request_id IS NULL
	// 扩展请求：parent_request_id 指向主请求（request_type 区分类型）
	query := `
		SELECT request_id, COALESCE(request_type,'main'), COALESCE(request_status,''),
		       COALESCE(outbound_model, client_model, ''), latency_ms, ts,
		       COALESCE(parent_request_id, '')
		FROM request_logs_hot
		WHERE gw_session_id = $1`
	args := []any{sessionID}
	if !IsSuperAdminOrLegacy(r) {
		if auth.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "tenant_id required")
			return
		}
		query += " AND tenant_id = $2"
		args = append(args, auth.TenantID)
	}
	query += " ORDER BY ts ASC LIMIT 200"
	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	// 第一遍：读所有行，记录 parent 关系
	type row struct {
		turn     *SessionTurn
		parentID string
	}
	mains := make([]*SessionTurn, 0, 32)
	byID := make(map[string]*SessionTurn)
	var children []*row

	for rows.Next() {
		t := &SessionTurn{}
		var latency *int
		var ts time.Time
		var parentID string
		if err := rows.Scan(&t.RequestID, &t.RequestType, &t.Status,
			&t.Model, &latency, &ts, &parentID); err != nil {
			writeError(w, http.StatusInternalServerError, "read session timeline failed")
			return
		}
		t.LatencyMs = latency
		t.StartedAt = ts.UTC().Format(time.RFC3339)
		byID[t.RequestID] = t
		if parentID == "" {
			mains = append(mains, t)
		} else {
			children = append(children, &row{turn: t, parentID: parentID})
		}
	}

	// 第二遍：把扩展请求挂到父请求（循环保护：父不存在则挂为顶层）
	for _, c := range children {
		if parent, ok := byID[c.parentID]; ok && parent.RequestID != c.turn.RequestID {
			parent.Children = append(parent.Children, c.turn)
		} else {
			// 父请求不在结果集（可能跨会话或已过期）：作为顶层展示，保留 parent 标记
			mains = append(mains, c.turn)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sessionID,
		"turns":      mains,
		"count":      len(mains),
	})
}

// countColon 返回字符串中冒号数量（用于区分 session:{id} 主键与子键）。
func countColon(s string) int {
	n := 0
	for _, c := range s {
		if c == ':' {
			n++
		}
	}
	return n
}
