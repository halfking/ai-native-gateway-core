package admin

import (
	"context"
	"net/http"
	"time"
)

func (h *Handler) handleTags(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT DISTINCT jsonb_array_elements_text(COALESCE(tags, '[]'::jsonb)) AS tag
		FROM models_canonical
		WHERE tags IS NOT NULL AND tags != '[]'::jsonb
		ORDER BY tag
	`)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"tags": []string{}})
		return
	}
	defer rows.Close()

	var tags []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			continue
		}
		tags = append(tags, tag)
	}
	if tags == nil {
		tags = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

func (h *Handler) handleSystemTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tasks": []any{},
		"status": "running",
	})
}
