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
	"net/http"
	"strconv"
	"time"
)

// OnlineSession 是在线会话列表的一项。
type OnlineSession struct {
	SessionID         string `json:"session_id"`
	Title             string `json:"title,omitempty"`
	LastRequestStatus string `json:"last_request_status,omitempty"`
	LastModel         string `json:"last_model,omitempty"`
	LastProviderID    *int   `json:"last_provider_id,omitempty"`
	LastLatencyMs     *int   `json:"last_latency_ms,omitempty"`
	LastActiveAt      string `json:"last_active_at,omitempty"`
	DeviceCount       int    `json:"device_count,omitempty"`
}

// handleSessionsOnline 返回在线会话列表。
// GET /api/admin/sessions/online?limit=50&cursor=0
func (h *Handler) handleSessionsOnline(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.sessionManager == nil || h.sessionManager.GetRedisClient() == nil {
		writeError(w, http.StatusServiceUnavailable, "session manager not configured")
		return
	}

	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rdb := h.sessionManager.GetRedisClient().Client()

	// SCAN 游标遍历 session:* Hash（禁 KEYS，rule: 在线列表 P95<200ms）
	var sessionIDs []string
	var cursor uint64
	pattern := "session:*"
	for {
		keys, next, err := rdb.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "redis scan failed: "+err.Error())
			return
		}
		for _, k := range keys {
			// 排除子键（session:{id}:sanitize 等），只留 session:{id} 主键
			// 主键格式：session:<id>（一个冒号）
			if countColon(k) == 1 {
				sessionIDs = append(sessionIDs, k[len("session:"):])
			}
			if len(sessionIDs) >= limit {
				break
			}
		}
		cursor = next
		if cursor == 0 || len(sessionIDs) >= limit {
			break
		}
	}

	// 丰富：从 session_last_requests 补最后请求状态/模型/供应商
	out := make([]OnlineSession, 0, len(sessionIDs))
	for _, sid := range sessionIDs {
		os := OnlineSession{SessionID: sid}
		if h.db != nil {
			var status, model string
			var providerID, latency *int
			var updatedAt *time.Time
			err := h.db.QueryRow(ctx, `
				SELECT last_request_status, COALESCE(last_model,''),
				       last_provider_id, last_latency_ms, updated_at
				FROM session_last_requests
				WHERE session_id = $1`, sid).
				Scan(&status, &model, &providerID, &latency, &updatedAt)
			if err == nil {
				os.LastRequestStatus = status
				os.LastModel = model
				os.LastProviderID = providerID
				os.LastLatencyMs = latency
				if updatedAt != nil {
					os.LastActiveAt = updatedAt.UTC().Format(time.RFC3339)
				}
			}
		}
		out = append(out, os)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"sessions": out,
		"count":    len(out),
	})
}

// SessionTurn 是会话时间线的一轮（主请求 + 扩展请求子树）。
type SessionTurn struct {
	RequestID   string             `json:"request_id"`
	RequestType string             `json:"request_type"`
	Status      string             `json:"status"`
	Model       string             `json:"model,omitempty"`
	LatencyMs   *int               `json:"latency_ms,omitempty"`
	StartedAt   string             `json:"started_at,omitempty"`
	Children    []*SessionTurn     `json:"children,omitempty"`
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

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// 查该会话的所有请求（主请求 + 扩展请求），按时间升序。
	// 主请求：gw_session_id = sessionID 且 parent_request_id IS NULL
	// 扩展请求：parent_request_id 指向主请求（request_type 区分类型）
	rows, err := h.db.Query(ctx, `
		SELECT request_id, COALESCE(request_type,'main'), COALESCE(request_status,''),
		       COALESCE(outbound_model, client_model, ''), latency_ms, ts,
		       COALESCE(parent_request_id, '')
		FROM request_logs_hot
		WHERE gw_session_id = $1
		ORDER BY ts ASC
		LIMIT 200`, sessionID)
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
			continue
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
