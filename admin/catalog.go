package admin

import (
	"context"
	"log/slog"
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

type catalogEntry struct {
	Code                   string  `json:"code"`
	Tier                   string  `json:"tier"`
	DisplayName            string  `json:"display_name"`
	DisplayNameEn          string  `json:"display_name_en"`
	Category               string  `json:"category"`
	Kind                   string  `json:"kind"`
	Protocol               string  `json:"protocol"`
	BaseURLTemplate        string  `json:"base_url_template"`
	DocsURL                string  `json:"docs_url"`
	DefaultEgressProfile   string  `json:"default_egress_profile"`
	Domestic               bool    `json:"domestic"`
	DiscountRateDefault    float64 `json:"discount_rate_default"`
	ModelsManifestJSON     any     `json:"models_manifest_json"`
	DiscoveryStrategy      string  `json:"discovery_strategy"`
	Hidden                 bool    `json:"hidden"`
	HeaderProfileCode      string  `json:"header_profile_code"`
	Capabilities           any     `json:"capabilities"`
	VendorName             string  `json:"vendor_name"`
	ModelsEndpointTemplate string  `json:"models_endpoint_template"`
	Notes                  string  `json:"notes"`
}

const catalogColumns = `
	code, tier, COALESCE(display_name,''), COALESCE(display_name_en,''),
	COALESCE(category,''), COALESCE(kind,''), COALESCE(protocol,''),
	COALESCE(base_url_template,''), COALESCE(docs_url,''),
	COALESCE(default_egress_profile,'direct'),
	COALESCE(domestic, FALSE),
	COALESCE(discount_rate_default, 1.0),
	COALESCE(models_manifest_json, '[]'::jsonb),
	COALESCE(discovery_strategy, 'auto'),
	COALESCE(hidden, FALSE),
	COALESCE(header_profile_code, ''),
	COALESCE(capabilities, '{}'::jsonb),
	COALESCE(vendor_name, ''),
	COALESCE(models_endpoint_template, ''),
	COALESCE(notes, '')
`

func (h *Handler) listCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `SELECT `+catalogColumns+` FROM provider_catalog ORDER BY code`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	entries := make([]catalogEntry, 0)
	for rows.Next() {
		var e catalogEntry
		if err := rows.Scan(
			&e.Code, &e.Tier, &e.DisplayName, &e.DisplayNameEn,
			&e.Category, &e.Kind, &e.Protocol,
			&e.BaseURLTemplate, &e.DocsURL,
			&e.DefaultEgressProfile, &e.Domestic,
			&e.DiscountRateDefault, &e.ModelsManifestJSON,
			&e.DiscoveryStrategy, &e.Hidden,
			&e.HeaderProfileCode, &e.Capabilities,
			&e.VendorName, &e.ModelsEndpointTemplate,
			&e.Notes,
		); err != nil {
			slog.Warn("listCatalog scan failed", "error", err)
			continue
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("listCatalog rows error", "error", err)
	}
	writeJSON(w, http.StatusOK, entries)
}

func (h *Handler) getCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	remaining := r.URL.Path[len("/api/catalog/"):]
	if remaining == "" {
		h.listCatalog(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var e catalogEntry
	err := h.db.QueryRow(ctx, `SELECT `+catalogColumns+` FROM provider_catalog WHERE code = $1`, remaining).Scan(
		&e.Code, &e.Tier, &e.DisplayName, &e.DisplayNameEn,
		&e.Category, &e.Kind, &e.Protocol,
		&e.BaseURLTemplate, &e.DocsURL,
		&e.DefaultEgressProfile, &e.Domestic,
		&e.DiscountRateDefault, &e.ModelsManifestJSON,
		&e.DiscoveryStrategy, &e.Hidden,
		&e.HeaderProfileCode, &e.Capabilities,
		&e.VendorName, &e.ModelsEndpointTemplate,
		&e.Notes,
	)
	if err != nil {
		writeError(w, http.StatusNotFound, "catalog entry not found")
		return
	}
	writeJSON(w, http.StatusOK, e)
}
