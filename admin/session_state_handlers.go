// Package admin — session_state_handlers.go
//
// 会话状态管理 API 端点：
//
//	GET    /api/admin/sessions                   列出会话
//	GET    /api/admin/sessions/{id}              会话详情
//	GET    /api/admin/sessions/{id}/cred-rotations  凭据轮换历史
//	POST   /api/admin/sessions/{id}/stop         停止会话
//	POST   /api/admin/sessions/{id}/recover      恢复会话
//	PUT    /api/admin/sessions/{id}/annotation   更新标注
//	PUT    /api/admin/sessions/{id}/tags         更新标签
package admin

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/session"
)

// sessionListItem 列表项
type sessionListItem struct {
	SessionID         string     `json:"session_id"`
	TenantID          string     `json:"tenant_id"`
	APIKeyID          int        `json:"api_key_id"`
	Status            string     `json:"status"`
	CurrentModel      string     `json:"current_model"`
	CurrentCredential int        `json:"current_credential_id"`
	CurrentProvider   string     `json:"current_provider"`
	TotalTurns        int64      `json:"total_turns"`
	TotalCostUSD      float64    `json:"total_cost_usd"`
	Title             string     `json:"title"`
	Tags              string     `json:"tags"`
	Annotation        string     `json:"annotation"`
	CreatedAt         time.Time  `json:"created_at"`
	LastRequestAt     time.Time  `json:"last_request_at"`
	StoppedAt         *time.Time `json:"stopped_at,omitempty"`
}

// sessionListResponse 列表响应
type sessionListResponse struct {
	Sessions []sessionListItem `json:"sessions"`
	Total    int               `json:"total"`
}

// sessionDetailResponse 详情响应
type sessionDetailResponse struct {
	Session   *session.Session            `json:"session"`
	Stats     *session.SessionStats       `json:"stats"`
	Rotations []session.CredRotationEntry `json:"rotations"`
}

// handleListSessions 列出会话
func (h *Handler) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if h.sessionManager == nil {
		writeError(w, http.StatusServiceUnavailable, "session manager not wired")
		return
	}

	statusFilter := r.URL.Query().Get("status")
	if statusFilter == "" {
		statusFilter = "active"
	}
	tenantID := r.URL.Query().Get("tenant_id")
	limit := queryInt(r, "limit", 50)
	if limit > 200 {
		limit = 200
	}

	// 租户隔离
	if !IsSuperAdminOrLegacy(r) {
		tenantID = GetTenantID(r)
	}

	var sessionIDs []string
	switch statusFilter {
	case "active":
		sessionIDs = h.listActiveSessions(ctxFn(r), tenantID, limit)
	case "stopped":
		ids, _ := h.sessionManager.ListStoppedSessions(ctxFn(r), tenantID, limit)
		sessionIDs = ids
	default:
		active := h.listActiveSessions(ctxFn(r), tenantID, limit*2/3)
		stopped, _ := h.sessionManager.ListStoppedSessions(ctxFn(r), tenantID, limit-limit*2/3)
		sessionIDs = append(active, stopped...)
		if len(sessionIDs) > limit {
			sessionIDs = sessionIDs[:limit]
		}
	}

	items := make([]sessionListItem, 0, len(sessionIDs))
	for _, sid := range sessionIDs {
		sess, err := h.sessionManager.Get(ctxFn(r), sid)
		if err != nil {
			continue
		}
		if tenantID != "" && sess.TenantID != tenantID {
			continue
		}
		stats, _ := h.sessionManager.GetStats(ctxFn(r), sid)
		item := sessionListItem{
			SessionID:    sess.SessionID,
			TenantID:     sess.TenantID,
			APIKeyID:     sess.APIKeyID,
			Status:       getSessionStatus(ctxFn(r), h, sid),
			CurrentModel: sess.ProviderCache.OpenAICheckpoint,
			Title:        "",
			CreatedAt:    sess.CreatedAt,
		}
		if stats != nil {
			item.TotalTurns = stats.TotalTurns
			item.TotalCostUSD = stats.TotalCostUSD
			item.LastRequestAt = stats.LastRequestAt
		}
		items = append(items, item)
	}

	writeJSON(w, http.StatusOK, sessionListResponse{
		Sessions: items,
		Total:    len(items),
	})
}

// handleSessionSubrouter 子路由分发
func (h *Handler) handleSessionSubrouter(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/sessions/")
	if path == "" {
		writeError(w, http.StatusBadRequest, "session id required")
		return
	}

	var sessionID, action string
	if idx := strings.Index(path, "/"); idx >= 0 {
		sessionID = path[:idx]
		action = path[idx+1:]
	} else {
		sessionID = path
	}

	if action == "" {
		h.serveSessionDetail(w, r, sessionID)
		return
	}

	switch action {
	case "cred-rotations":
		h.serveCredRotations(w, r, sessionID)
	case "stop":
		h.serveStopSession(w, r, sessionID)
	case "recover":
		h.serveRecoverSession(w, r, sessionID)
	case "annotation":
		h.serveUpdateAnnotation(w, r, sessionID)
	case "tags":
		h.serveUpdateTags(w, r, sessionID)
	default:
		writeError(w, http.StatusNotFound, "unknown action: "+action)
	}
}

func (h *Handler) serveSessionDetail(w http.ResponseWriter, r *http.Request, sessionID string) {
	if h.sessionManager == nil {
		writeError(w, http.StatusServiceUnavailable, "session manager not wired")
		return
	}
	sess, err := h.sessionManager.Get(ctxFn(r), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if !IsSuperAdminOrLegacy(r) && sess.TenantID != GetTenantID(r) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}
	stats, _ := h.sessionManager.GetStats(ctxFn(r), sessionID)
	rotations, _ := h.sessionManager.GetCredRotations(ctxFn(r), sessionID, 100)
	writeJSON(w, http.StatusOK, sessionDetailResponse{
		Session: sess, Stats: stats, Rotations: rotations,
	})
}

func (h *Handler) serveCredRotations(w http.ResponseWriter, r *http.Request, sessionID string) {
	if h.sessionManager == nil {
		writeError(w, http.StatusServiceUnavailable, "session manager not wired")
		return
	}
	sess, err := h.sessionManager.Get(ctxFn(r), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if !IsSuperAdminOrLegacy(r) && sess.TenantID != GetTenantID(r) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}
	limit := queryInt(r, "limit", 100)
	rotations, err := h.sessionManager.GetCredRotations(ctxFn(r), sessionID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sessionID, "rotations": rotations, "total": len(rotations),
	})
}

func (h *Handler) serveStopSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.sessionManager == nil {
		writeError(w, http.StatusServiceUnavailable, "session manager not wired")
		return
	}
	if !IsSuperAdminOrLegacy(r) {
		writeError(w, http.StatusForbidden, "stop requires super_admin")
		return
	}
	reason := r.URL.Query().Get("reason")
	if reason == "" {
		reason = "admin_stop"
	}
	if err := h.sessionManager.StopSession(ctxFn(r), sessionID, reason); err != nil {
		writeError(w, http.StatusInternalServerError, "stop failed: "+err.Error())
		return
	}
	if h.sessionDBWriter != nil {
		h.sessionDBWriter.FlushSession(ctxFn(r), sessionID, "")
	}
	auth := GetAuthContext(r)
	if auth != nil {
		h.auditLog(auth.Username, "session.stop", "session", 0,
			map[string]any{"session_id": sessionID, "reason": reason})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"session_id": sessionID,
		"stopped_at": time.Now().UTC().Format(time.RFC3339),
	})
}

func (h *Handler) serveRecoverSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.sessionManager == nil {
		writeError(w, http.StatusServiceUnavailable, "session manager not wired")
		return
	}
	if !IsSuperAdminOrLegacy(r) {
		writeError(w, http.StatusForbidden, "recover requires super_admin")
		return
	}
	if err := h.sessionManager.RecoverSession(ctxFn(r), sessionID); err != nil {
		writeError(w, http.StatusInternalServerError, "recover failed: "+err.Error())
		return
	}
	auth := GetAuthContext(r)
	if auth != nil {
		h.auditLog(auth.Username, "session.recover", "session", 0,
			map[string]any{"session_id": sessionID})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"session_id":   sessionID,
		"recovered_at": time.Now().UTC().Format(time.RFC3339),
	})
}

func (h *Handler) serveUpdateAnnotation(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPut {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.sessionManager == nil {
		writeError(w, http.StatusServiceUnavailable, "session manager not wired")
		return
	}
	annotation := r.URL.Query().Get("annotation")
	if err := h.sessionManager.SetAnnotation(ctxFn(r), sessionID, annotation); err != nil {
		writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) serveUpdateTags(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPut {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.sessionManager == nil {
		writeError(w, http.StatusServiceUnavailable, "session manager not wired")
		return
	}
	tagsStr := r.URL.Query().Get("tags")
	var tags []string
	if tagsStr != "" {
		tags = strings.Split(tagsStr, ",")
	}
	if err := h.sessionManager.SetTags(ctxFn(r), sessionID, tags); err != nil {
		writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── 辅助函数 ─────────────────────────────────────────────────────────

// ctxFn helper
func ctxFn(r *http.Request) context.Context {
	if r == nil {
		return context.Background()
	}
	return r.Context()
}

func getSessionStatus(ctx context.Context, h *Handler, sessionID string) string {
	if h.sessionManager == nil {
		return ""
	}
	rc := h.sessionManager.GetRedisClient()
	if rc == nil {
		return "active"
	}
	raw := rc.Client()
	if raw == nil {
		return "active"
	}
	val, _ := raw.HGet(ctx, "session:"+sessionID, "status").Result()
	if val != "" {
		return val
	}
	return "active"
}

// listActiveSessions 列出活跃 session IDs
func (h *Handler) listActiveSessions(ctx context.Context, tenantID string, limit int) []string {
	if h.sessionManager == nil {
		return nil
	}
	rc := h.sessionManager.GetRedisClient()
	if rc == nil {
		return nil
	}
	raw := rc.Client()
	if raw == nil {
		return nil
	}

	pattern := "session:apiKey:*:active"
	iter := raw.Scan(ctx, 0, pattern, 100).Iterator()

	allIDs := make([]string, 0, limit)
	for iter.Next(ctx) {
		ids, err := raw.SRandMemberN(ctx, iter.Val(), int64(limit)).Result()
		if err != nil {
			continue
		}
		allIDs = append(allIDs, ids...)
		if len(allIDs) >= limit {
			break
		}
	}

	// 过滤掉不在指定 tenant 的
	if tenantID != "" {
		filtered := make([]string, 0, limit)
		for _, id := range allIDs {
			sess, err := h.sessionManager.Get(ctx, id)
			if err != nil {
				continue
			}
			if sess.TenantID == tenantID {
				filtered = append(filtered, id)
				if len(filtered) >= limit {
					break
				}
			}
		}
		return filtered
	}
	return allIDs
}

// 防止 strconv 引入被 unused 检测（已通过其他方式使用）
var _ = strconv.Itoa
