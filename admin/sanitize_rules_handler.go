package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

const sanitizeRulesPath = "/api/admin/sanitize-rules"

func (h *Handler) registerSanitizeRuleRoutes(mux *http.ServeMux) {
	mux.HandleFunc(sanitizeRulesPath, h.admin(h.handleSanitizeRules))
	mux.HandleFunc(sanitizeRulesPath+"/", h.admin(h.handleSanitizeRules))
}

// handleSanitizeRules authorizes each operation after the shared JWT middleware.
// AdminMiddleware authenticates ordinary users as well, so role checks belong here.
func (h *Handler) handleSanitizeRules(w http.ResponseWriter, r *http.Request) {
	auth := GetAuthContext(r)
	if auth == nil {
		writeError(w, 401, "authentication required")
		return
	}
	super := IsSuperAdminOrLegacy(r)
	path := strings.TrimPrefix(r.URL.Path, sanitizeRulesPath)
	eventRead := path == "/events"
	eventReview := strings.HasPrefix(path, "/events/") && strings.HasSuffix(path, "/review")
	if !super && !(auth.Role == "tenant_admin" && auth.TenantID != "" && (eventRead || eventReview)) {
		writeError(w, 403, "insufficient permission for rule management")
		return
	}
	if h.sanitizeRuleManager == nil {
		writeError(w, 503, "sanitize rule management unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	switch path {
	case "", "/":
		if !sanitizeRuleMethod(w, r, http.MethodGet) {
			return
		}
		snap := h.sanitizeRuleManager.Current()
		writeJSON(w, 200, map[string]any{"version": snap.Version, "config": snap.Config(), "config_digest": snap.ConfigDigest, "status": h.sanitizeRuleManager.Status()})
	case "/validate":
		if !sanitizeRuleMethod(w, r, http.MethodPost) {
			return
		}
		var cfg sanitizerules.Config
		if !decodeSanitizeRuleBody(w, r, &cfg) {
			return
		}
		snap, err := sanitizerules.Compile(cfg, 0)
		if err != nil {
			writeSanitizeRuleError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"valid": true, "config_digest": snap.ConfigDigest})
	case "/publish":
		h.publishSanitizeRules(w, r)
	case "/rollback":
		if !sanitizeRuleMethod(w, r, http.MethodPost) {
			return
		}
		var req struct {
			Base    *int64 `json:"base_version"`
			Target  *int64 `json:"target_version"`
			Comment string `json:"comment"`
		}
		if !decodeSanitizeRuleBody(w, r, &req) {
			return
		}
		if req.Base == nil || *req.Base < 0 || req.Target == nil || *req.Target < 1 {
			writeError(w, 400, "base_version and published target_version are required")
			return
		}
		v, err := h.sanitizeRuleManager.Rollback(ctx, *req.Base, *req.Target, sanitizeRuleActor(auth), req.Comment)
		if err != nil {
			writeSanitizeRuleError(w, err)
			return
		}
		writeJSON(w, 200, v)
	case "/versions":
		if !sanitizeRuleMethod(w, r, http.MethodGet) {
			return
		}
		limit, ok := sanitizeRuleLimit(w, r)
		if !ok {
			return
		}
		versions, err := h.sanitizeRuleManager.Store().ListVersions(ctx, limit)
		if err != nil {
			writeSanitizeRuleError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"versions": versions})
	case "/events":
		h.listSanitizeRuleEvents(w, r)
	case "/instances":
		if !sanitizeRuleMethod(w, r, http.MethodGet) {
			return
		}
		instances, err := h.sanitizeRuleManager.Store().ListInstances(ctx)
		if err != nil {
			writeSanitizeRuleError(w, err)
			return
		}
		target, err := h.sanitizeRuleManager.Store().Current(ctx)
		if err != nil {
			writeSanitizeRuleError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"target_version": target.ID, "instances": instances})
	default:
		if eventReview {
			h.reviewSanitizeRuleEvent(w, r)
			return
		}
		writeError(w, 404, "sanitize rule endpoint not found")
	}
}
func (h *Handler) publishSanitizeRules(w http.ResponseWriter, r *http.Request) {
	if !sanitizeRuleMethod(w, r, http.MethodPost) {
		return
	}
	var req struct {
		Base           *int64               `json:"base_version"`
		Config         sanitizerules.Config `json:"config"`
		Comment        string               `json:"comment"`
		ReviewEventIDs []int64              `json:"review_event_ids"`
	}
	if !decodeSanitizeRuleBody(w, r, &req) {
		return
	}
	if req.Base == nil || *req.Base < 0 || len(req.ReviewEventIDs) > 100 {
		writeError(w, 400, "valid base_version and at most 100 review events are required")
		return
	}
	links := make([]sanitizerules.ReviewLink, 0, len(req.ReviewEventIDs))
	seen := map[int64]bool{}
	for _, id := range req.ReviewEventIDs {
		if id < 1 || seen[id] {
			writeError(w, 400, "review event IDs must be positive and unique")
			return
		}
		seen[id] = true
		e, err := h.sanitizeRuleManager.Store().GetEvent(r.Context(), id)
		if err != nil {
			writeSanitizeRuleError(w, err)
			return
		}
		if e.Status == "pending" {
			writeError(w, 400, "review events must be reviewed before publication")
			return
		}
		links = append(links, sanitizerules.ReviewLink{ID: id, TenantID: e.TenantID})
	}
	v, err := h.sanitizeRuleManager.Publish(r.Context(), *req.Base, req.Config, sanitizeRuleActor(GetAuthContext(r)), req.Comment, links...)
	if err != nil {
		writeSanitizeRuleError(w, err)
		return
	}
	writeJSON(w, 200, v)
}
