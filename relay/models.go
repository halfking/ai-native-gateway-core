package relay

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ModelsHandler serves the /v1/models endpoint.
// It returns only models that have valid, active credentials.
type ModelsHandler struct {
	dbPool *pgxpool.Pool
}

// NewModelsHandler creates a new models handler.
func NewModelsHandler() *ModelsHandler {
	return &ModelsHandler{}
}

// SetDB sets the database pool for direct model queries.
func (h *ModelsHandler) SetDB(pool *pgxpool.Pool) {
	h.dbPool = pool
}

func (h *ModelsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.dbPool == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]string{
				"message": "Models service unavailable: no database connection",
				"type":    "server_error",
				"code":    "database_unavailable",
			},
		})
		return
	}
	h.serveFromDB(w, r)
}

func (h *ModelsHandler) serveFromDB(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	rows, err := h.dbPool.Query(ctx, `
		SELECT DISTINCT
			mc.canonical_name,
			COALESCE(mc.family, 'unknown') AS family,
			COALESCE(mc.modality, 'text') AS modality
		FROM models_canonical mc
		JOIN model_offers mo ON mo.canonical_id = mc.id
		JOIN credentials c ON c.id = mo.credential_id
		JOIN providers p ON p.id = c.provider_id
		WHERE mo.available = TRUE
		  AND c.status = 'active'
		  AND c.trust_level NOT IN ('quarantine')
		  AND p.enabled = TRUE
		ORDER BY family, mc.canonical_name
	`)
	if err != nil {
		slog.Error("models: db query failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]string{
				"message": "Failed to query models from database",
				"type":    "server_error",
				"code":    "database_query_error",
			},
		})
		return
	}
	defer rows.Close()

	type modelEntry struct {
		ID       string `json:"id"`
		Object   string `json:"object"`
		Family   string `json:"family,omitempty"`
		Modality string `json:"modality,omitempty"`
	}

	var models []modelEntry
	for rows.Next() {
		var name, family, modality string
		if err := rows.Scan(&name, &family, &modality); err != nil {
			continue
		}
		models = append(models, modelEntry{
			ID:       name,
			Object:   "model",
			Family:   family,
			Modality: modality,
		})
	}

	if models == nil {
		models = []modelEntry{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   models,
	})
}
