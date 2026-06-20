package admin

import (
	"context"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/registry"
)

// ToolRegistryAPI handles tool registry admin endpoints
type ToolRegistryAPI struct {
	toolRegistry *registry.ToolRegistry
}

// NewToolRegistryAPI creates a new ToolRegistryAPI instance
func NewToolRegistryAPI(tr *registry.ToolRegistry) *ToolRegistryAPI {
	return &ToolRegistryAPI{
		toolRegistry: tr,
	}
}

// HandleReload handles POST /api/admin/tools/reload
// Manually triggers tool registry cache refresh
func (api *ToolRegistryAPI) HandleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if err := api.toolRegistry.Reload(ctx); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "Failed to reload tool registry",
			"error":   err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"message": "Tool registry reloaded successfully",
		"stats":   api.toolRegistry.Stats(),
	})
}

// HandleList handles GET /api/admin/tools/list?tenant_id=&category=
// Lists tools by optional tenant_id and category filters
func (api *ToolRegistryAPI) HandleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tenantID := r.URL.Query().Get("tenant_id")
	if tenantID == "" {
		tenantID = "default"
	}

	category := r.URL.Query().Get("category")

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if category != "" {
		// List by category
		tools, err := api.toolRegistry.GetCategory(ctx, tenantID, category)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"status":  "error",
				"message": "Failed to list tools",
				"error":   err.Error(),
			})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"tools":     tools,
			"count":     len(tools),
			"tenant_id": tenantID,
			"category":  category,
		})
	} else {
		// Return stats only (no full list without category)
		stats := api.toolRegistry.Stats()
		writeJSON(w, http.StatusOK, map[string]any{
			"status":    "ok",
			"message":   "Specify category parameter to list tools",
			"stats":     stats,
			"tenant_id": tenantID,
		})
	}
}

// HandleGet handles GET /api/admin/tools/get?tenant_id=&tool_id=
// Gets a single tool by tool_id
func (api *ToolRegistryAPI) HandleGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tenantID := r.URL.Query().Get("tenant_id")
	if tenantID == "" {
		tenantID = "default"
	}

	toolID := r.URL.Query().Get("tool_id")
	if toolID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "tool_id parameter is required",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tool, err := api.toolRegistry.Get(ctx, tenantID, toolID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "Failed to get tool",
			"error":   err.Error(),
		})
		return
	}

	if tool == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"status":    "error",
			"message":   "Tool not found",
			"tenant_id": tenantID,
			"tool_id":   toolID,
		})
		return
	}

	writeJSON(w, http.StatusOK, tool)
}
