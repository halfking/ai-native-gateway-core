package admin

import (
	"context"
	"net/http"
	"strconv"

	"github.com/kaixuan/llm-gateway-go/storage"
)

// LiteSessionsReader lists sessions from the lite (SQLite) session store.
type LiteSessionsReader func(ctx context.Context, tenantID string, opts *storage.ListOptions) ([]*storage.Session, error)

// R28-S-2 (2026-09-30 round 31): lite mode previously wrote sessions to
// SQLite with zero read surfaces — operators saw data "disappear" into a
// file. This endpoint exposes the stored sessions so the lite deployment's
// own data is queryable without opening the SQLite file by hand.
func (h *Handler) SetLiteSessionsReader(r LiteSessionsReader) {
	h.liteSessions = r
}

func (h *Handler) handleLiteSessions(w http.ResponseWriter, r *http.Request) {
	if h.liteSessions == nil {
		writeError(w, http.StatusServiceUnavailable, "lite storage not active: this gateway is not running in lite (sqlite) storage mode")
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tenantID := r.URL.Query().Get("tenant_id")
	opts := &storage.ListOptions{}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			opts.Limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			opts.Offset = n
		}
	}
	sessions, err := h.liteSessions(r.Context(), tenantID, opts)
	if err != nil {
		writeInternalTextErr(w, "lite sessions list", err)
		return
	}
	if sessions == nil {
		sessions = []*storage.Session{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions, "count": len(sessions)})
}
