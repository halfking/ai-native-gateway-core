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
	pythonEndpoint string
	dbPool         *pgxpool.Pool
	client         *http.Client
}

// NewModelsHandler creates a new models handler.
func NewModelsHandler(pythonEndpoint string) *ModelsHandler {
	return &ModelsHandler{
		pythonEndpoint: pythonEndpoint,
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// SetDB sets the database pool for direct model queries.
func (h *ModelsHandler) SetDB(pool *pgxpool.Pool) {
	h.dbPool = pool
}

func (h *ModelsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Try to serve from database first (with filtered, normalized models)
	if h.dbPool != nil {
		h.serveFromDB(w, r)
		return
	}

	// Fallback to Python control plane
	if h.pythonEndpoint != "" {
		h.serveFromPython(w, r)
		return
	}

	// No backend available
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   []any{},
	})
}

func (h *ModelsHandler) serveFromDB(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Query models that have at least one active, available credential
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
		h.serveFromPython(w, r)
		return
	}
	defer rows.Close()

	type modelEntry struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Family  string `json:"family,omitempty"`
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

func (h *ModelsHandler) serveFromPython(w http.ResponseWriter, r *http.Request) {
	if h.pythonEndpoint == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"object": "list",
			"data":   []any{},
		})
		return
	}

	// Clean up endpoint
	endpoint := h.pythonEndpoint
	for _, suffix := range []string{"/v1/chat/completions", "/v1/responses", "/v1/completions"} {
		if len(endpoint) > len(suffix) && endpoint[len(endpoint)-len(suffix):] == suffix {
			endpoint = endpoint[:len(endpoint)-len(suffix)]
			break
		}
	}

	target := endpoint + "/v1/models"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		slog.Error("models: build request failed", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": map[string]string{
				"message": "failed to build models request",
				"type":    "server_error",
			},
		})
		return
	}

	for key, vals := range r.Header {
		for _, val := range vals {
			switch key {
			case "X-Client-Profile", "X-Request-Id":
				req.Header.Add(key, val)
			}
		}
	}

	resp, err := h.client.Do(req)
	if err != nil {
		slog.Error("models: upstream request failed", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": map[string]string{
				"message": "upstream models request failed",
				"type":    "server_error",
			},
		})
		return
	}
	defer resp.Body.Close()

	// Forward the response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)

	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
}
