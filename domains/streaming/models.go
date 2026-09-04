package streaming

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// modelsStaleGrace bounds how long the last successful /v1/models result
// may be re-served after the DB query starts failing (2026-09-04
// availability work). One hour aligns with the provider package's
// candidate/reveal outage windows: clients keep discovering models while
// PostgreSQL is down instead of receiving 500s from a pure read endpoint.
const modelsStaleGrace = time.Hour

type modelEntry struct {
	ID            string `json:"id"`
	Object        string `json:"object"`
	Family        string `json:"family,omitempty"`
	Modality      string `json:"modality,omitempty"`
	ContextWindow *int   `json:"context_window,omitempty"`
}

// ModelsHandler serves the /v1/models endpoint.
// It returns only models that have valid, active credentials.
type ModelsHandler struct {
	dbPool *pgxpool.Pool

	staleMu      sync.Mutex
	staleEntries []modelEntry
	staleAt      time.Time
}

func NewModelsHandler() *ModelsHandler {
	return &ModelsHandler{}
}

func (h *ModelsHandler) SetDB(pool *pgxpool.Pool) {
	h.dbPool = pool
}

func (h *ModelsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.dbPool == nil {
		if entries, ok := h.lastGoodEntries(); ok {
			slog.Warn("models: no database connection, serving last-good list")
			h.writeEntries(w, entries)
			return
		}
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

// lastGoodEntries returns the cached successful result while it is inside
// the stale grace window.
func (h *ModelsHandler) lastGoodEntries() ([]modelEntry, bool) {
	h.staleMu.Lock()
	defer h.staleMu.Unlock()
	if len(h.staleEntries) == 0 || h.staleAt.IsZero() {
		return nil, false
	}
	if time.Since(h.staleAt) > modelsStaleGrace {
		return nil, false
	}
	return h.staleEntries, true
}

func (h *ModelsHandler) rememberGoodEntries(entries []modelEntry) {
	h.staleMu.Lock()
	defer h.staleMu.Unlock()
	h.staleEntries = entries
	h.staleAt = time.Now()
}

func (h *ModelsHandler) writeEntries(w http.ResponseWriter, entries []modelEntry) {
	// Serve a defensive copy so a concurrent refresh cannot race the
	// serializer.
	out := make([]modelEntry, len(entries))
	copy(out, entries)
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   out,
	})
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
		if entries, ok := h.lastGoodEntries(); ok {
			slog.Warn("models: serving last-good list during db outage",
				"models", len(entries), "stale_for", time.Since(h.staleSnapshotAt()).Round(time.Second).String())
			h.writeEntries(w, entries)
			return
		}
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
	h.rememberGoodEntries(models)
	h.writeEntries(w, models)
}

func (h *ModelsHandler) staleSnapshotAt() time.Time {
	h.staleMu.Lock()
	defer h.staleMu.Unlock()
	return h.staleAt
}
