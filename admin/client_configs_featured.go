package admin

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

// Client setup uses every featured policy for the selected key's tenant. The
// routing editor's single-policy GET is intentionally not the source here.
const clientConfigFeaturedSQL = `
 WITH featured AS (
   SELECT btrim(f.model) AS model, min(p.id) AS policy_id, min(f.ord) AS ord
   FROM routing_policy p
   CROSS JOIN LATERAL unnest(p.featured_models) WITH ORDINALITY AS f(model, ord)
   WHERE p.tenant_id = $1 AND btrim(f.model) <> ''
   GROUP BY btrim(f.model)
 )
 SELECT f.model, COALESCE(mc.context_window, alias.context_window)
 FROM featured f
 LEFT JOIN models_canonical mc ON mc.canonical_name = f.model
 LEFT JOIN LATERAL (
   SELECT c.context_window FROM model_aliases a
   JOIN models_canonical c ON c.id = a.canonical_id
   WHERE a.raw_name = f.model AND a.status = 'active' ORDER BY c.id LIMIT 1
 ) alias ON true
 ORDER BY f.policy_id, f.ord, f.model`

func (h *Handler) handleClientConfigFeatured(w http.ResponseWriter, r *http.Request) {
	if !requireUserManager(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id, err := strconv.Atoi(r.URL.Query().Get("key_id"))
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "valid key_id required")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	if !h.assertKeyTenantScope(w, r, id) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var tenantID string
	err = h.db.QueryRow(ctx, `SELECT tenant_id FROM api_keys WHERE id = $1
   AND enabled AND status = 'active' AND (expires_at IS NULL OR expires_at > now())`, id).Scan(&tenantID)
	if err != nil {
		writeLookupErr(w, "active key not found", err)
		return
	}
	rows, err := h.db.Query(ctx, clientConfigFeaturedSQL, tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "featured models query failed")
		return
	}
	defer rows.Close()
	type model struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		ContextWindow *int   `json:"contextWindow,omitempty"`
	}
	names := []string{}
	models := []model{}
	for rows.Next() {
		var item model
		if err := rows.Scan(&item.ID, &item.ContextWindow); err != nil {
			writeError(w, http.StatusInternalServerError, "featured models query failed")
			return
		}
		item.Name = item.ID
		names = append(names, item.ID)
		models = append(models, item)
	}
	if rows.Err() != nil {
		writeError(w, http.StatusInternalServerError, "featured models query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"featured_models": names, "models": models})
}
