package admin

import (
	"encoding/json"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/metatools"
)

// MetaToolsHandler provides HTTP endpoints for Phase 2 meta-tools.
type MetaToolsHandler struct {
	handler *metatools.Handler
}

// NewMetaToolsHandler creates a new meta-tools HTTP handler.
func NewMetaToolsHandler(handler *metatools.Handler) *MetaToolsHandler {
	return &MetaToolsHandler{handler: handler}
}

// ListCategories handles GET /api/meta-tools/categories
func (h *MetaToolsHandler) ListCategories(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	categories, err := h.handler.ListCategories(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	//nolint:errcheck // HTTP write error non-recoverable
	json.NewEncoder(w).Encode(map[string]interface{}{
		"categories": categories,
	})
}

// LoadTools handles POST /api/meta-tools/load
func (h *MetaToolsHandler) LoadTools(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req struct {
		Categories []string `json:"categories"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	result, err := h.handler.LoadTools(ctx, req.Categories)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	//nolint:errcheck // HTTP write error non-recoverable
	json.NewEncoder(w).Encode(result)
}

// GetMetaToolDefinitions handles GET /api/meta-tools/definitions
// Returns the two meta-tool definitions for client initialization
func (h *MetaToolsHandler) GetMetaToolDefinitions(w http.ResponseWriter, r *http.Request) {
	defs := metatools.MetaToolDefinitions()

	w.Header().Set("Content-Type", "application/json")
	//nolint:errcheck // HTTP write error non-recoverable
	json.NewEncoder(w).Encode(map[string]interface{}{
		"meta_tools": defs,
	})
}
