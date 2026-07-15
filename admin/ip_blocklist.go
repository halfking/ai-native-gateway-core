package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/security/ipblocklist"
)

func (h *Handler) handleIPBlocklistCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleIPBlocklistList(w, r)
	case http.MethodPost:
		h.handleIPBlocklistCreate(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleIPBlocklistList(w http.ResponseWriter, r *http.Request) {
	if h.ipBlocklist == nil {
		writeError(w, http.StatusServiceUnavailable, "ip blocklist not configured")
		return
	}
	scope := r.URL.Query().Get("scope")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	items, total, err := h.ipBlocklist.Store.List(r.Context(), scope, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (h *Handler) handleIPBlocklistCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.ipBlocklist == nil {
		writeError(w, http.StatusServiceUnavailable, "ip blocklist not configured")
		return
	}
	var req struct {
		IPOrCIDR  string     `json:"ip_or_cidr"`
		Reason    string     `json:"reason"`
		Scope     string     `json:"scope"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	actor := "system"
	if auth := GetAuthContext(r); auth != nil && auth.Username != "" {
		actor = auth.Username
	}
	entry, err := h.ipBlocklist.Store.Create(r.Context(), ipblocklist.CreateInput{
		IPOrCIDR:  req.IPOrCIDR,
		Reason:    req.Reason,
		Scope:     req.Scope,
		Source:    ipblocklist.SourceManual,
		ExpiresAt: req.ExpiresAt,
		CreatedBy: actor,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.ipBlocklist.AfterMutation(r.Context(), entry.Scope)
	writeJSON(w, http.StatusCreated, entry)
}

func (h *Handler) handleIPBlocklistItem(w http.ResponseWriter, r *http.Request) {
	if h.ipBlocklist == nil {
		writeError(w, http.StatusServiceUnavailable, "ip blocklist not configured")
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/api/admin/security/ip-blocklist/")
	idStr = strings.Trim(idStr, "/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	switch r.Method {
	case http.MethodPatch:
		h.patchIPBlocklist(w, r, id)
	case http.MethodDelete:
		h.deleteIPBlocklist(w, r, id)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) patchIPBlocklist(w http.ResponseWriter, r *http.Request, id int64) {
	var req struct {
		Reason    *string    `json:"reason"`
		Enabled   *bool      `json:"enabled"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	entry, err := h.ipBlocklist.Store.Update(r.Context(), id, ipblocklist.UpdateInput{
		Reason: req.Reason, Enabled: req.Enabled, ExpiresAt: req.ExpiresAt,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.ipBlocklist.AfterMutation(r.Context(), entry.Scope)
	writeJSON(w, http.StatusOK, entry)
}

func (h *Handler) deleteIPBlocklist(w http.ResponseWriter, r *http.Request, id int64) {
	cur, err := h.ipBlocklist.Store.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err := h.ipBlocklist.Store.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.ipBlocklist.AfterMutation(r.Context(), cur.Scope)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleIPBlocklistReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.ipBlocklist == nil {
		writeError(w, http.StatusServiceUnavailable, "ip blocklist not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := h.ipBlocklist.Warmup(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
}
