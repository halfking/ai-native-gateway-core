// Package admin — request_transitions.go
//
// V3.2 (2026-08-13) BE-B1: 请求状态变更历史查询 API。
//
// Route (registered in RegisterRoutes):
//
//	GET /api/admin/requests/{id}/transitions   按 created_at 升序返回状态变更链
//
// 数据源：request_state_transitions（migration 511），由
// domains/dispatch.StateTransitionLogger 旁路异步写入。
package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// RequestTransition 是一条状态变更记录的 API 投影。
type RequestTransition struct {
	ID             int64          `json:"id"`
	RequestID      string         `json:"request_id"`
	TransitionType string         `json:"transition_type"` // route | node_switch | retry | error | state
	FromState      string         `json:"from_state,omitempty"`
	ToState        string         `json:"to_state,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
}

// handleRequestTransitions 返回一个请求的完整状态变更链（按时间升序）。
// GET /api/admin/requests/{id}/transitions
func (h *Handler) handleRequestTransitions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	requestID := r.PathValue("id")
	if requestID == "" {
		writeError(w, http.StatusBadRequest, "request id required")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT id, request_id, transition_type,
		       COALESCE(from_state,''), COALESCE(to_state,''),
		       metadata, created_at
		FROM request_state_transitions
		WHERE request_id = $1
		ORDER BY created_at ASC, id ASC
		LIMIT 200`, requestID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	out := make([]RequestTransition, 0, 16)
	for rows.Next() {
		var t RequestTransition
		var meta []byte
		if err := rows.Scan(&t.ID, &t.RequestID, &t.TransitionType,
			&t.FromState, &t.ToState, &meta, &t.CreatedAt); err != nil {
			continue
		}
		if len(meta) > 0 {
			var m map[string]any
			if json.Unmarshal(meta, &m) == nil {
				t.Metadata = m
			}
		}
		out = append(out, t)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"request_id":  requestID,
		"transitions": out,
		"count":       len(out),
	})
}
