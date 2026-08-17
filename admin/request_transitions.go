// Package admin — request_transitions.go
//
// V3.2 (2026-08-13) BE-B1: 请求状态变更历史查询 API。
//
// Route (registered in RegisterRoutes):
//
//	GET /api/admin/requests/{id}/transitions   按 created_at 升序返回状态变更链
//
// 数据源：request_state_transitions（migration 511）。2026-08-17 B3-PR1 起，
// streaming / streamretry 两个事件点已迁入 requestjourney（journey 行，
// event_type 非空，经 admin/request_journey.go serveList 查询）；本 endpoint
// 只剩 admin 节点操作审计（node_operations_audit.go）仍在写 legacy 行
// （transition_type 非空），以及迁移前的历史行。endpoint 收敛计划见
// AUDIT_24H_20260817.md §E.3（B3 PR2）。
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
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

	out := make([]RequestTransition, 0, 16)
	query := func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, request_id, transition_type,
			       COALESCE(from_state,''), COALESCE(to_state,''),
			       metadata, created_at
			FROM request_state_transitions
			WHERE request_id = $1
			ORDER BY created_at ASC, id ASC
			LIMIT 200`, requestID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t RequestTransition
			var meta []byte
			if err := rows.Scan(&t.ID, &t.RequestID, &t.TransitionType,
				&t.FromState, &t.ToState, &meta, &t.CreatedAt); err != nil {
				return fmt.Errorf("scan transition: %w", err)
			}
			if len(meta) > 0 {
				var m map[string]any
				if json.Unmarshal(meta, &m) == nil {
					t.Metadata = m
				}
			}
			out = append(out, t)
		}
		return rows.Err()
	}

	var err error
	if IsSuperAdminOrLegacy(r) {
		err = withAllTenantReadOnlyTx(ctx, h.db, query)
	} else {
		err = withTenantTx(ctx, h.db, GetTenantID(r), query)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"request_id":  requestID,
		"transitions": out,
		"count":       len(out),
	})
}
