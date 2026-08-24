package streaming

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
)

// ModelsHandler serves the /v1/models endpoint.
// It returns only models that have valid, active credentials.
type ModelsHandler struct {
	dbPool      *pgxpool.Pool
	keyVerifier *authentication.KeyVerifier
}

func NewModelsHandler() *ModelsHandler {
	return &ModelsHandler{}
}

func (h *ModelsHandler) SetDB(pool *pgxpool.Pool) {
	h.dbPool = pool
}

// SetKeyVerifier wires the data-plane key verifier. Once set, /v1/models
// requires a valid sk-* API key like every other /v1 endpoint (rule 20 §2).
// Before the 2026-08-24 static-gate fix this handler relied on the global
// static gate for auth, which only accepted the single key configured in
// LLM_GATEWAY_API_KEY; sk-* keys now bypass that gate and must be verified
// here against api_keys.
func (h *ModelsHandler) SetKeyVerifier(v *authentication.KeyVerifier) {
	h.keyVerifier = v
}

func (h *ModelsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.keyVerifier != nil && h.keyVerifier.Enabled() {
		rawKey := extractBearerToken(r)
		if rawKey == "" {
			writeErrorJSON(w, http.StatusUnauthorized, "", "Missing API key", "authentication_error", "missing_key")
			return
		}
		keyInfo, err := h.keyVerifier.Verify(r.Context(), rawKey)
		if err != nil {
			if _, ok := err.(*authentication.InvalidKeyError); ok {
				writeErrorJSON(w, http.StatusUnauthorized, "", "Invalid or expired API key", "authentication_error", "invalid_key")
				return
			}
			slog.Warn("models: key verification failed", "error", err)
			writeErrorJSON(w, http.StatusServiceUnavailable, "", "Authentication service temporarily unavailable", "server_error", "auth_unavailable")
			return
		}
		_ = keyInfo
	}
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
			COALESCE(mc.modality, 'text') AS modality,
			mc.context_window
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
		ID            string `json:"id"`
		Object        string `json:"object"`
		Family        string `json:"family,omitempty"`
		Modality      string `json:"modality,omitempty"`
		ContextWindow *int   `json:"context_window,omitempty"`
	}

	models := make([]modelEntry, 0)
	for rows.Next() {
		var name, family, modality string
		var contextWindow *int
		if err := rows.Scan(&name, &family, &modality, &contextWindow); err != nil {
			continue
		}
		models = append(models, modelEntry{
			ID:            name,
			Object:        "model",
			Family:        family,
			Modality:      modality,
			ContextWindow: contextWindow,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   models,
	})
}
