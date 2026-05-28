package admin

import (
	"context"
	"net/http"
	"time"
)

func (h *Handler) handleCatalog(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	remaining := r.URL.Path[len("/api/catalog/"):]
	if remaining == "" {
		h.listCatalog(w, r)
		return
	}
	h.getCatalog(w, r)
}

func (h *Handler) handleCatalogRoot(w http.ResponseWriter, r *http.Request) {
	h.listCatalog(w, r)
}

func (h *Handler) listCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT code, COALESCE(display_name,''), COALESCE(protocol,'')
		FROM provider_catalog ORDER BY code
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type catalogEntry struct {
		Code        string `json:"code"`
		DisplayName string `json:"display_name"`
		Protocol    string `json:"protocol"`
	}
	var entries []catalogEntry
	for rows.Next() {
		var e catalogEntry
		if err := rows.Scan(&e.Code, &e.DisplayName, &e.Protocol); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	writeJSON(w, http.StatusOK, entries)
}

func (h *Handler) getCatalog(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotImplemented, "not yet implemented")
}
