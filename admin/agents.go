package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/apihub"
)

// AgentsHandler exposes the unified asset registry as REST API (Phase 3 A3-1).
type AgentsHandler struct {
	svc *apihub.Service
}

// NewAgentsHandler returns a handler backed by apihub.Service.
func NewAgentsHandler(svc *apihub.Service) *AgentsHandler {
	return &AgentsHandler{svc: svc}
}

// List handles GET /api/agents — returns assets filtered by kind/tenant.
func (h *AgentsHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse query params
	kindStr := r.URL.Query().Get("kind")
	tenant := r.URL.Query().Get("tenant")
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	// Default tenant from auth context (TODO: extract from JWT)
	if tenant == "" {
		tenant = "default"
	}

	limit := 100
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 1000 {
			limit = l
		}
	}

	offset := 0
	if offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}

	// Build filter
	filter := apihub.Filter{
		TenantID: tenant,
		Limit:    limit,
	}

	if kindStr != "" && kindStr != "all" {
		filter.Kind = apihub.Kind(kindStr)
	}

	assets, err := h.svc.List(ctx, filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := map[string]interface{}{
		"agents": assets,
		"total":  len(assets),
		"limit":  limit,
		"offset": offset,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// Get handles GET /api/agents/:id — returns single asset.
func (h *AgentsHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract ID from path /api/agents/:id
	idStr := strings.TrimPrefix(r.URL.Path, "/api/agents/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	// Get asset by refID (apihub.Service.Get signature: ctx, kind, refID)
	// Note: tenant is extracted from context by Service.Get internally
	asset, err := h.svc.Get(ctx, apihub.KindLLMEndpoint, id)
	if err != nil {
		if err == apihub.ErrNotFound {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": "agent not found",
				"id":    id,
			})
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := map[string]interface{}{
		"agent": asset,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// Link handles POST /api/agents/:id/link — creates asset_link.
func (h *AgentsHandler) Link(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract source ID from path
	idStr := strings.TrimPrefix(r.URL.Path, "/api/agents/")
	idStr = strings.TrimSuffix(idStr, "/link")
	sourceID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	// Parse request body
	var req struct {
		TargetID int64  `json:"target_id"`
		LinkType string `json:"link_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Create relationship
	rel := apihub.Relationship{
		SrcKind: apihub.RelationEndpoint{
			Kind:  apihub.KindLLMEndpoint,
			RefID: sourceID,
		},
		DstKind: apihub.RelationEndpoint{
			Kind:  apihub.KindLLMEndpoint,
			RefID: req.TargetID,
		},
		Type: apihub.RelationType(req.LinkType),
	}

	if err := h.svc.Link(ctx, rel); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := map[string]interface{}{
		"link": map[string]interface{}{
			"source_id": sourceID,
			"target_id": req.TargetID,
			"link_type": req.LinkType,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}
