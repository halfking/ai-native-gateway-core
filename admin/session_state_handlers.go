package admin

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/session"
)

type sessionListItem struct {
	SessionID             string     `json:"session_id"`
	TenantID              string     `json:"tenant_id"`
	APIKeyID              int        `json:"api_key_id"`
	Status                string     `json:"status"`
	CurrentModel          string     `json:"current_model"`
	CurrentCredential     int        `json:"current_credential_id"`
	CurrentProvider       string     `json:"current_provider"`
	TotalTurns            int64      `json:"total_turns"`
	TotalPromptTokens     int64      `json:"total_prompt_tokens"`
	TotalCompletionTokens int64      `json:"total_completion_tokens"`
	TotalCostUSDCents     int64      `json:"total_cost_usd_cents"`
	TotalCostUSD          float64    `json:"total_cost_usd"`
	Title                 string     `json:"title"`
	Tags                  string     `json:"tags"`
	Annotation            string     `json:"annotation"`
	CreatedAt             time.Time  `json:"created_at"`
	LastActive            time.Time  `json:"last_active"`
	LastRequestAt         time.Time  `json:"last_request_at"`
	StoppedAt             *time.Time `json:"stopped_at,omitempty"`
}

type sessionListResponse struct {
	Sessions []sessionListItem `json:"sessions"`
	Total    int               `json:"total"`
}

type sessionDetailResponse struct {
	Session   *session.Session            `json:"session"`
	Stats     *session.SessionStats       `json:"stats"`
	Rotations []session.CredRotationEntry `json:"rotations"`
}

func (h *Handler) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if h.sessionManager == nil {
		writeError(w, http.StatusServiceUnavailable, "session manager not wired")
		return
	}

	statusFilter := r.URL.Query().Get("status")
	if statusFilter == "" {
		statusFilter = session.StatusActive
	}
	tenantID := r.URL.Query().Get("tenant_id")
	limit := queryInt(r, "limit", 50)
	if limit > 200 {
		limit = 200
	}
	if !IsSuperAdminOrLegacy(r) {
		tenantID = GetTenantID(r)
	}

	var sessionIDs []string
	switch statusFilter {
	case session.StatusStopped:
		ids, _ := h.sessionManager.ListStoppedSessions(ctxFn(r), tenantID, limit)
		sessionIDs = ids
	case "all":
		active := h.listActiveSessions(ctxFn(r), tenantID, limit)
		stopped, _ := h.sessionManager.ListStoppedSessions(ctxFn(r), tenantID, limit)
		sessionIDs = append(active, stopped...)
		if len(sessionIDs) > limit {
			sessionIDs = sessionIDs[:limit]
		}
	default:
		sessionIDs = h.listActiveSessions(ctxFn(r), tenantID, limit)
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
			SessionID:         sess.SessionID,
			TenantID:          sess.TenantID,
			APIKeyID:          sess.APIKeyID,
			Status:            defaultString(sess.Status, session.StatusActive),
			CurrentModel:      sess.CurrentModel,
			CurrentCredential: sess.CurrentCredentialID,
			CurrentProvider:   sess.CurrentProvider,
			Title:             sess.Title,
			Tags:              sess.Tags,
			Annotation:        sess.Annotation,
			CreatedAt:         sess.CreatedAt,
			LastActive:        sess.LastActive,
			LastRequestAt:     sess.LastRequestAt,
		}
		if !sess.StoppedAt.IsZero() {
			st := sess.StoppedAt
			item.StoppedAt = &st
		}
		if stats != nil {
			item.TotalTurns = stats.TotalTurns
			item.TotalPromptTokens = stats.TotalPromptTokens
			item.TotalCompletionTokens = stats.TotalCompletionTokens
			item.TotalCostUSDCents = stats.TotalCostUSDCents
			item.TotalCostUSD = stats.TotalCostUSD
			if !stats.LastRequestAt.IsZero() {
				item.LastRequestAt = stats.LastRequestAt
			}
		}
		items = append(items, item)
	}

	writeJSON(w, http.StatusOK, sessionListResponse{Sessions: items, Total: len(items)})
}

func (h *Handler) handleSessionSubrouter(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/sessions/")
	if path == "" {
		writeError(w, http.StatusBadRequest, "session id required")
		return
	}
	parts := strings.Split(path, "/")
	sessionID := parts[0]
	action := ""
	if len(parts) > 1 {
		action = strings.Join(parts[1:], "/")
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
	writeJSON(w, http.StatusOK, sessionDetailResponse{Session: sess, Stats: stats, Rotations: rotations})
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
	writeJSON(w, http.StatusOK, map[string]any{"session_id": sessionID, "rotations": rotations, "total": len(rotations)})
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
		h.sessionDBWriter.FlushSession(ctxFn(r), sessionID)
	}
	if auth := GetAuthContext(r); auth != nil {
		h.auditLog(auth.Username, "session.stop", "session", 0, map[string]any{"session_id": sessionID, "reason": reason})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "stopped_at": time.Now().UTC().Format(time.RFC3339)})
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
	if auth := GetAuthContext(r); auth != nil {
		h.auditLog(auth.Username, "session.recover", "session", 0, map[string]any{"session_id": sessionID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "recovered_at": time.Now().UTC().Format(time.RFC3339)})
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
	if err := h.sessionManager.SetAnnotation(ctxFn(r), sessionID, r.URL.Query().Get("annotation")); err != nil {
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
	tagStr := r.URL.Query().Get("tags")
	var tags []string
	if tagStr != "" {
		tags = strings.Split(tagStr, ",")
	}
	if err := h.sessionManager.SetTags(ctxFn(r), sessionID, tags); err != nil {
		writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func ctxFn(r *http.Request) context.Context {
	if r == nil {
		return context.Background()
	}
	return r.Context()
}

func (h *Handler) listActiveSessions(ctx context.Context, tenantID string, limit int) []string {
	if h.sessionManager == nil {
		return nil
	}
	rc := h.sessionManager.GetRedisClient()
	if rc == nil || rc.Client() == nil {
		return nil
	}
	iter := rc.Client().Scan(ctx, 0, "session:apiKey:*:active", 100).Iterator()
	ids := make([]string, 0, limit)
	for iter.Next(ctx) {
		members, err := rc.Client().SRandMemberN(ctx, iter.Val(), int64(limit)).Result()
		if err != nil {
			continue
		}
		for _, id := range members {
			if tenantID != "" {
				sess, err := h.sessionManager.Get(ctx, id)
				if err != nil || sess.TenantID != tenantID {
					continue
				}
			}
			ids = append(ids, id)
			if len(ids) >= limit {
				return ids
			}
		}
	}
	return ids
}

func defaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
