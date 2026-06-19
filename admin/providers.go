package admin

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/internal/probeutil"
	"github.com/kaixuan/llm-gateway-go/internal/providercap"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
	"github.com/kaixuan/llm-gateway-go/modelcatalog"
	"github.com/kaixuan/llm-gateway-go/modelname"
	"github.com/jackc/pgx/v5/pgxpool"
)

func extractID(path string) (int, bool) {
	parts := splitPath(path)
	if len(parts) == 0 {
		return 0, false
	}
	id, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return 0, false
	}
	return id, true
}

func splitPath(p string) []string {
	var parts []string
	for _, s := range splitSlash(p) {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return parts
}

func splitSlash(s string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}

func (h *Handler) checkProvider(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	var code, displayName string
	var enabled bool
	err := h.db.QueryRow(ctx, `SELECT code, display_name, enabled FROM providers WHERE id = $1 AND tenant_id = 'default'`, providerID).Scan(&code, &displayName, &enabled)
	if err != nil {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}

	rows, err := h.db.Query(ctx, `
		SELECT c.id, c.label, c.secret_ciphertext, p.base_url, p.protocol
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		WHERE c.provider_id = $1 AND c.status = 'active' AND p.enabled = TRUE
		ORDER BY c.id
	`, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type credResult struct {
		CredentialID int    `json:"credential_id"`
		Label        string `json:"label"`
		Status       string `json:"status"`
		Error        string `json:"error,omitempty"`
	}
	results := []credResult{}
	checked := 0
	healthy := 0

	for rows.Next() {
		var credID int
		var label, baseURL, protocol string
		var ciphertext []byte
		if err := rows.Scan(&credID, &label, &ciphertext, &baseURL, &protocol); err != nil {
			slog.Warn("check-provider: scan row failed", "error", err.Error(), "label", label)
			continue
		}

		healthStatus := "unknown"
		errMsg := ""

		decrypted, decErr := h.decryptCredStr(string(ciphertext))
		if decErr != nil {
			healthStatus = "error"
			errMsg = "decrypt failed"
			results = append(results, credResult{CredentialID: credID, Label: label, Status: healthStatus, Error: errMsg})
			continue
		}

		if _, probeErr := probeProvider(ctx, baseURL, protocol, decrypted); probeErr != nil {
			healthStatus = "unreachable"
			errMsg = probeErr.Error()
		} else {
			healthStatus = "healthy"
			healthy++
		}

		h.db.Exec(ctx, `
			UPDATE credentials
			SET health_status = $1, health_error = $2, health_checked_at = NOW()
			WHERE id = $3
		`, healthStatus, errMsg, credID)

		results = append(results, credResult{CredentialID: credID, Label: label, Status: healthStatus, Error: errMsg})
		checked++
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"accepted":  true,
		"reason":    "health check completed",
		"checked":   checked,
		"healthy":   healthy,
	})
}

func probeProvider(ctx context.Context, baseURL, protocol, apiKey string) (bool, error) {
	if baseURL == "" {
		return false, fmt.Errorf("empty base URL")
	}
	desc := providercap.Resolve(protocol, "")
	if !desc.SupportsModelsEndpoint {
		return true, nil
	}
	result, err := doProbeRequest(ctx, modelsURLCandidatesForBase(baseURL), apiKey)
	if err != nil {
		return false, err
	}
	return result.statusCode == http.StatusOK, nil
}

func (h *Handler) handleProvidersRoot(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	if r.Method == http.MethodGet {
		h.listProviders(w, r)
	} else if r.Method == http.MethodPost {
		h.createProvider(w, r)
	} else {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleProviders(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	path := r.URL.Path
	stripPrefix := "/api/providers/"
	remaining := path[len(stripPrefix):]

	if remaining == "" {
		if r.Method == http.MethodGet {
			h.listProviders(w, r)
		} else if r.Method == http.MethodPost {
			h.createProvider(w, r)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	idStr := remaining
	subPath := ""
	for i, c := range remaining {
		if c == '/' {
			idStr = remaining[:i]
			subPath = remaining[i+1:]
			break
		}
	}
	providerID, err := strconv.Atoi(idStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid provider id")
		return
	}

	switch subPath {
	case "":
		if r.Method == http.MethodPatch {
			h.updateProvider(w, r, providerID)
		} else if r.Method == http.MethodGet {
			h.getProvider(w, r, providerID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "toggle":
		if r.Method == http.MethodPatch {
			h.toggleProvider(w, r, providerID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "check":
		if r.Method == http.MethodPost {
			h.checkProvider(w, r, providerID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "diagnose":
		if r.Method == http.MethodPost {
			h.startDiagnose(w, r, providerID)
		} else if r.Method == http.MethodGet {
			h.getDiagnoseResult(w, r, providerID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "models":
		if r.Method == http.MethodDelete {
			h.clearProviderModels(w, r, providerID)
		} else if r.Method == http.MethodGet || r.Method == http.MethodPost {
			h.getProviderModels(w, r, providerID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "models/":
		h.handleProviderModelOffer(w, r, providerID, "")
	case "query":
		if r.Method == http.MethodPost || r.Method == http.MethodGet {
			h.queryProviderModels(w, r, providerID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "refresh-models":
		switch r.Method {
		case http.MethodPost:
			h.startRefreshProviderModels(w, r, providerID)
		case http.MethodGet:
			h.getRefreshProviderModelsStatus(w, r, providerID)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "logs":
		if r.Method == http.MethodGet || r.Method == http.MethodPost {
			h.providerLogs(w, r, providerID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "probe-history":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.handleProviderProbeHistory(w, r, providerID)
	case "probe-history/recent-failures":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.handleProviderProbeHistoryRecentFailures(w, r, providerID)
	case "probe-history/trigger":
		h.handleProviderProbeHistoryTrigger(w, r, providerID)
	case "probe-states":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.handleProviderProbeStates(w, r, providerID)
	case "batch-recover":
		if r.Method == http.MethodPost {
			h.batchRecoverCredentials(w, r, providerID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "manual-disabled":
		if r.Method == http.MethodPatch {
			h.setProviderManualDisabled(w, r, providerID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "routable-summary":
		if r.Method == http.MethodGet {
			h.getRoutableSummary(w, r, providerID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "credentials":
		h.handleProviderCredentials(w, r, providerID, "")
	default:
		if len(subPath) > 12 && subPath[:12] == "credentials/" {
			credPath := subPath[12:]
			h.handleProviderCredentials(w, r, providerID, credPath)
			return
		}
		if len(subPath) > 7 && subPath[:7] == "models/" {
			offerPath := subPath[7:]
			h.handleProviderModelOffer(w, r, providerID, offerPath)
			return
		}
		http.NotFound(w, r)
	}
}

func (h *Handler) listProviders(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Build dynamic WHERE clauses
	whereClauses := []string{"p.tenant_id = 'default'"}
	args := []any{}
	argIdx := 1

	if search := queryString(r, "search"); search != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("p.display_name ILIKE $%d", argIdx))
		args = append(args, search+"%")
		argIdx++
	}

	whereSQL := strings.Join(whereClauses, " AND ")

	query := fmt.Sprintf(`
		SELECT p.id, p.tenant_id, p.code, p.display_name, p.catalog_code,
		       p.is_custom, p.kind, p.category, p.protocol, p.base_url,
		       p.egress_profile, p.domestic, p.discount_rate::float8, p.enabled,
		       COALESCE(p.notes, ''),
		       p.network_quality_score, p.owner_user,
		       p.created_at, p.updated_at,
		       COALESCE(pc.vendor_name, pc.code, '') as vendor_name,
		       COALESCE(pc.header_profile_code, '') as header_profile_code,
		       COUNT(c.id) FILTER (WHERE c.status = 'active') AS active_credential_count,
		       COUNT(c.id) FILTER (WHERE c.health_status = 'healthy') AS healthy_credential_count,
		       COUNT(c.id) FILTER (WHERE c.health_status = 'warning') AS warning_credential_count,
		       COUNT(c.id) FILTER (WHERE c.health_status = 'unreachable') AS unreachable_credential_count,
		       MAX(c.health_checked_at) AS health_checked_at,
		       -- 900-series: routable count from VIEW (manual > auto priority)
		       COALESCE(rs.routable_count, 0)::int AS routable_binding_count,
		       COALESCE(rs.total_count, 0)::int AS total_binding_count,
		       -- 2026-06-19 quality fix mode (017_quality_fix_mode.sql).
		       -- Surface in the list response so the admin UI can render
		       -- the per-provider switch without a second round-trip.
		       COALESCE(p.quality_fix_mode, 'off') AS quality_fix_mode
		FROM providers p
		LEFT JOIN provider_catalog pc ON pc.code = COALESCE(NULLIF(p.catalog_code, ''), p.code)
		LEFT JOIN credentials c ON c.provider_id = p.id
		LEFT JOIN (
		    SELECT provider_id,
		           count(*) FILTER (WHERE is_routable) AS routable_count,
		           count(*) AS total_count
		    FROM v_routable_credential_models
		    WHERE tenant_id = 'default'
		    GROUP BY provider_id
		) rs ON rs.provider_id = p.id
		WHERE %s
		GROUP BY p.id, pc.code, pc.header_profile_code, pc.vendor_name, rs.routable_count, rs.total_count
		ORDER BY p.display_name ASC NULLS LAST, p.id ASC
	`, whereSQL)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type provider struct {
		ID                     int        `json:"id"`
		TenantID               string     `json:"tenant_id"`
		Code                   string     `json:"code"`
		DisplayName            string     `json:"display_name"`
		CatalogCode            *string    `json:"catalog_code"`
		IsCustom               bool       `json:"is_custom"`
		Kind                   string     `json:"kind"`
		Category               string     `json:"category"`
		Protocol               string     `json:"protocol"`
		BaseURL                string     `json:"base_url"`
		EgressProfile          string     `json:"egress_profile"`
		Domestic               bool       `json:"domestic"`
		DiscountRate           float64    `json:"discount_rate"`
		Enabled                bool       `json:"enabled"`
		Notes                  string     `json:"notes"`
		NetworkQualityScore    *string    `json:"network_quality_score"`
		OwnerUser              *string    `json:"owner_user"`
		CreatedAt              *time.Time `json:"created_at"`
		UpdatedAt              *time.Time `json:"updated_at"`
		VendorName             string     `json:"vendor_name"`
		HeaderProfileCode      string     `json:"header_profile_code"`
		ActiveCredCount        int        `json:"active_credential_count"`
		HealthyCredCount       int        `json:"healthy_credential_count"`
		WarningCredCount       int        `json:"warning_credential_count"`
		UnreachableCredCount   int        `json:"unreachable_credential_count"`
		HealthCheckedAt        *time.Time `json:"health_checked_at"`
		FreeModelCount         int        `json:"free_model_count"`
		HealthStatus           string     `json:"health_status"`
		RoutableBindingCount   int        `json:"routable_binding_count"`
		TotalBindingCount      int        `json:"total_binding_count"`
		QualityFixMode         string     `json:"quality_fix_mode"`
	}
	providers := make([]provider, 0)
	for rows.Next() {
		var p provider
		if err := rows.Scan(
			&p.ID, &p.TenantID, &p.Code, &p.DisplayName, &p.CatalogCode,
			&p.IsCustom, &p.Kind, &p.Category, &p.Protocol, &p.BaseURL,
			&p.EgressProfile, &p.Domestic, &p.DiscountRate, &p.Enabled,
			&p.Notes, &p.NetworkQualityScore, &p.OwnerUser,
			&p.CreatedAt, &p.UpdatedAt,
			&p.VendorName, &p.HeaderProfileCode,
			&p.ActiveCredCount, &p.HealthyCredCount, &p.WarningCredCount, &p.UnreachableCredCount,
			&p.HealthCheckedAt,
			&p.RoutableBindingCount, &p.TotalBindingCount,
			&p.QualityFixMode,
		); err != nil {
			slog.Warn("listProviders scan failed", "error", err)
			continue
		}
		// Compute health_status from counts (same as Python)
		if p.HealthyCredCount > 0 {
			p.HealthStatus = "healthy"
		} else if p.WarningCredCount > 0 {
			p.HealthStatus = "warning"
		} else if p.UnreachableCredCount > 0 {
			p.HealthStatus = "unreachable"
		} else {
			p.HealthStatus = "unknown"
		}
		p.FreeModelCount = 0 // TODO: join model_offers when table exists

		// Apply health_status filter (post-query like Python)
		if hs := queryString(r, "health_status"); hs != "" && hs != "all" {
			if p.HealthStatus != hs {
				continue
			}
		}

		providers = append(providers, p)
	}
	writeJSON(w, http.StatusOK, providers)
}

func (h *Handler) getProvider(w http.ResponseWriter, r *http.Request, id int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var p struct {
		ID                   int     `json:"id"`
		Code                 string  `json:"code"`
		DisplayName          string  `json:"display_name"`
		CatalogCode          *string `json:"catalog_code"`
		Kind                 string  `json:"kind"`
		Category             string  `json:"category"`
		Protocol             string  `json:"protocol"`
		BaseURL              string  `json:"base_url"`
		EgressProfile        *string `json:"egress_profile"`
		Domestic             bool    `json:"domestic"`
		DiscountRate         float64 `json:"discount_rate"`
		Enabled              bool    `json:"enabled"`
		ManualDisabled       bool    `json:"manual_disabled"`
		HeaderProfileCode    string  `json:"header_profile_code"`
		Notes                *string `json:"notes"`
		VendorName           *string `json:"vendor_name"`
		ActiveCredCount      int     `json:"active_cred_count"`
		HealthyCredCount     int     `json:"healthy_cred_count"`
		WarningCredCount     int     `json:"warning_cred_count"`
		CoolingCredCount     int     `json:"cooling_cred_count"`
		UnreachableCredCount int     `json:"unreachable_cred_count"`
		AvailableModelCount  int     `json:"available_model_count"`
		UnavailableModelCount int    `json:"unavailable_model_count"`
		ErrorRate24h         float64 `json:"error_rate_24h"`
		CreatedAt            *time.Time `json:"created_at"`
	}

	err := h.db.QueryRow(ctx, `
		SELECT p.id, COALESCE(p.code,''), COALESCE(p.display_name,''), p.catalog_code,
		       COALESCE(p.kind,''), COALESCE(p.category,''), COALESCE(p.protocol,''),
		       COALESCE(p.base_url,''), p.egress_profile, p.domestic,
		       COALESCE(p.discount_rate::float8, 0), p.enabled,
		       COALESCE(p.manual_disabled, false),
		       COALESCE(pc.header_profile_code, ''),
		       p.notes, pc.vendor_name,
		       COALESCE(ac.cnt, 0), COALESCE(hc.cnt, 0), COALESCE(wc.cnt, 0),
		       COALESCE(cc.cnt, 0), COALESCE(uc.cnt, 0),
		       COALESCE(am.cnt, 0), COALESCE(um.cnt, 0),
		       COALESCE(er.rate, 0),
		       p.created_at
		FROM providers p
		LEFT JOIN provider_catalog pc ON pc.code = p.catalog_code
		LEFT JOIN (SELECT provider_id, COUNT(*) cnt FROM credentials WHERE status='active' GROUP BY provider_id) ac ON ac.provider_id = p.id
		LEFT JOIN (SELECT provider_id, COUNT(*) cnt FROM credentials WHERE health_status='healthy' AND status='active' GROUP BY provider_id) hc ON hc.provider_id = p.id
		LEFT JOIN (SELECT provider_id, COUNT(*) cnt FROM credentials WHERE health_status='warning' GROUP BY provider_id) wc ON wc.provider_id = p.id
		LEFT JOIN (SELECT provider_id, COUNT(*) cnt FROM credentials WHERE circuit_state='open' GROUP BY provider_id) cc ON cc.provider_id = p.id
		LEFT JOIN (SELECT provider_id, COUNT(*) cnt FROM credentials WHERE availability_state='unreachable' GROUP BY provider_id) uc ON uc.provider_id = p.id
		LEFT JOIN (SELECT c.provider_id, COUNT(*) cnt FROM model_offers mo JOIN credentials c ON c.id = mo.credential_id WHERE mo.available=true GROUP BY c.provider_id) am ON am.provider_id = p.id
		LEFT JOIN (SELECT c.provider_id, COUNT(*) cnt FROM model_offers mo JOIN credentials c ON c.id = mo.credential_id WHERE mo.available=false GROUP BY c.provider_id) um ON um.provider_id = p.id
		LEFT JOIN (SELECT provider_id, COALESCE(SUM(CASE WHEN NOT success THEN 1 ELSE 0 END)::float8 / NULLIF(COUNT(*),0), 0) rate FROM request_logs WHERE ts >= now() - interval '24 hours' GROUP BY provider_id) er ON er.provider_id = p.id
		WHERE p.id = $1 AND p.tenant_id = 'default'
	`, id).Scan(
		&p.ID, &p.Code, &p.DisplayName, &p.CatalogCode,
		&p.Kind, &p.Category, &p.Protocol, &p.BaseURL,
		&p.EgressProfile, &p.Domestic, &p.DiscountRate, &p.Enabled,
		&p.ManualDisabled, &p.HeaderProfileCode,
		&p.Notes, &p.VendorName,
		&p.ActiveCredCount, &p.HealthyCredCount, &p.WarningCredCount,
		&p.CoolingCredCount, &p.UnreachableCredCount,
		&p.AvailableModelCount, &p.UnavailableModelCount,
		&p.ErrorRate24h,
		&p.CreatedAt,
	)
	if err != nil {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *Handler) createProvider(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code        string  `json:"code"`
		DisplayName *string `json:"display_name"`
		BaseURL     *string `json:"base_url"`
		Protocol    *string `json:"protocol"`
		CatalogCode *string `json:"catalog_code"`
		Notes       *string `json:"notes"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	catalogCode := ""
	if req.CatalogCode != nil {
		catalogCode = *req.CatalogCode
	}

	// ── Custom provider (no catalog lookup) ──
	if catalogCode == "__custom__" || catalogCode == "" {
		code := req.Code
		if code == "" {
			writeError(w, http.StatusBadRequest, "code is required for custom provider")
			return
		}
		displayName := code
		if req.DisplayName != nil && *req.DisplayName != "" {
			displayName = *req.DisplayName
		}
		baseURL := ""
		if req.BaseURL != nil {
			baseURL = *req.BaseURL
		}
		protocol := "openai-completions"
		if req.Protocol != nil && *req.Protocol != "" {
			protocol = *req.Protocol
		}

		var id int
		err := h.db.QueryRow(ctx, `
			INSERT INTO providers (tenant_id, code, display_name, base_url, protocol, is_custom, kind, category, enabled)
			VALUES ('default', $1, $2, $3, $4, TRUE, 'cloud', 'official', TRUE)
			RETURNING id
		`, code, displayName, baseURL, protocol).Scan(&id)
		if err != nil {
			if strings.Contains(err.Error(), "duplicate key") {
				writeError(w, http.StatusConflict, "provider already exists: "+code)
				return
			}
			writeError(w, http.StatusInternalServerError, "create failed: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "message": "ok"})
		return
	}

	// ── Catalog-based provider ──
	var catProtocol, catBaseURL, catHeaderProfile string
	err := h.db.QueryRow(ctx, `
		SELECT protocol, base_url_template, COALESCE(header_profile_code,'')
		FROM provider_catalog WHERE code = $1
	`, catalogCode).Scan(&catProtocol, &catBaseURL, &catHeaderProfile)
	if err != nil {
		writeError(w, http.StatusBadRequest, "unknown catalog code: "+catalogCode)
		return
	}

	code := catalogCode
	displayName := code
	if req.DisplayName != nil && *req.DisplayName != "" {
		displayName = *req.DisplayName
	}
	baseURL := catBaseURL
	if req.BaseURL != nil && *req.BaseURL != "" {
		baseURL = *req.BaseURL
	}
	protocol := catProtocol
	if req.Protocol != nil && *req.Protocol != "" {
		protocol = *req.Protocol
	}

	var id int
	err = h.db.QueryRow(ctx, `
		INSERT INTO providers (tenant_id, code, display_name, base_url, protocol, catalog_code, is_custom, kind, category, enabled)
		VALUES ('default', $1, $2, $3, $4, $5, FALSE, 'cloud', 'official', TRUE)
		RETURNING id
	`, code, displayName, baseURL, protocol, catalogCode).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict, "provider already exists: "+code)
			return
		}
		writeError(w, http.StatusInternalServerError, "create failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "message": "ok"})
}

func (h *Handler) updateProvider(w http.ResponseWriter, r *http.Request, id int) {
	var req struct {
		DisplayName    *string  `json:"display_name"`
		BaseURL        *string  `json:"base_url"`
		Protocol       *string  `json:"protocol"`
		Kind           *string  `json:"kind"`
		Category       *string  `json:"category"`
		DiscountRate   *float64 `json:"discount_rate"`
		EgressProfile  *string  `json:"egress_profile"`
		Notes          *string  `json:"notes"`
		Enabled        *bool    `json:"enabled"`
		// 2026-06-19 quality fix mode (017_quality_fix_mode.sql).
		// Three-state: "off" (passthrough), "detect_only" (annotate only),
		// "fix" (detect + actively rewrite the response body). The
		// column has a CHECK constraint that rejects other values; we
		// surface the DB error verbatim so the operator sees a clear
		// "violates check constraint" instead of a silent 500.
		QualityFixMode *string  `json:"quality_fix_mode"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if req.DisplayName != nil {
		h.db.Exec(ctx, `UPDATE providers SET display_name = $1, updated_at = now() WHERE id = $2`, *req.DisplayName, id)
	}
	if req.BaseURL != nil {
		h.db.Exec(ctx, `UPDATE providers SET base_url = $1, updated_at = now() WHERE id = $2`, *req.BaseURL, id)
	}
	if req.Protocol != nil {
		h.db.Exec(ctx, `UPDATE providers SET protocol = $1, updated_at = now() WHERE id = $2`, *req.Protocol, id)
	}
	if req.Kind != nil {
		h.db.Exec(ctx, `UPDATE providers SET kind = $1, updated_at = now() WHERE id = $2`, *req.Kind, id)
	}
	if req.Category != nil {
		h.db.Exec(ctx, `UPDATE providers SET category = $1, updated_at = now() WHERE id = $2`, *req.Category, id)
	}
	if req.DiscountRate != nil {
		h.db.Exec(ctx, `UPDATE providers SET discount_rate = $1, updated_at = now() WHERE id = $2`, *req.DiscountRate, id)
	}
	if req.EgressProfile != nil {
		h.db.Exec(ctx, `UPDATE providers SET egress_profile = $1, updated_at = now() WHERE id = $2`, *req.EgressProfile, id)
	}
	if req.Notes != nil {
		h.db.Exec(ctx, `UPDATE providers SET notes = $1, updated_at = now() WHERE id = $2`, *req.Notes, id)
	}
	if req.Enabled != nil {
		h.db.Exec(ctx, `UPDATE providers SET enabled = $1, updated_at = now() WHERE id = $2`, *req.Enabled, id)
	}
	if req.QualityFixMode != nil {
		if _, err := h.db.Exec(ctx, `UPDATE providers SET quality_fix_mode = $1, updated_at = now() WHERE id = $2`, *req.QualityFixMode, id); err != nil {
			writeError(w, http.StatusBadRequest, "quality_fix_mode update failed: "+err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "updated"})
}

func (h *Handler) toggleProvider(w http.ResponseWriter, r *http.Request, id int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	_, err := h.db.Exec(ctx, `UPDATE providers SET enabled = NOT enabled WHERE id = $1`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "toggle failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "toggled"})
}

func (h *Handler) handleSeedFromCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	type createdProvider struct {
		ID          int    `json:"id"`
		Code        string `json:"code"`
		DisplayName string `json:"display_name"`
	}

	created := make([]createdProvider, 0)

	rows, err := h.db.Query(ctx, `
		INSERT INTO providers (
			tenant_id, code, display_name, catalog_code, protocol, base_url,
			kind, category, domestic, egress_profile, enabled
		)
		SELECT
			'default',
			pc.code,
			pc.display_name,
			pc.code,
			COALESCE(pc.protocol, 'openai-completions'),
			COALESCE(pc.base_url_template, ''),
			COALESCE(pc.kind, 'cloud'),
			COALESCE(pc.category, 'official'),
			COALESCE(pc.domestic, TRUE),
			COALESCE(pc.default_egress_profile, 'direct'),
			TRUE
		FROM provider_catalog pc
		WHERE pc.hidden IS NOT TRUE
		  AND NOT EXISTS (
			SELECT 1 FROM providers p
			WHERE p.tenant_id = 'default' AND p.catalog_code = pc.code
		)
		ORDER BY pc.code
		RETURNING id, code, display_name
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "seed failed: "+err.Error())
		return
	}
	defer rows.Close()

	for rows.Next() {
		var cp createdProvider
		if err := rows.Scan(&cp.ID, &cp.Code, &cp.DisplayName); err != nil {
			continue
		}
		created = append(created, cp)
	}

	var total int
	h.db.QueryRow(ctx, `SELECT COUNT(*) FROM providers WHERE tenant_id = 'default'`).Scan(&total)

	writeJSON(w, http.StatusCreated, map[string]any{
		"message":  fmt.Sprintf("Seeded %d new providers from catalog", len(created)),
		"created":  len(created),
		"total":    total,
		"providers": created,
	})
}

// SeedProvidersFromCatalog creates provider rows for every catalog entry that
// does not yet exist. Safe to call on every startup (idempotent).
func SeedProvidersFromCatalog(ctx context.Context, db *pgxpool.Pool) (int, error) {
	if db == nil {
		return 0, nil
	}
	tag, err := db.Exec(ctx, `
		INSERT INTO providers (
			tenant_id, code, display_name, catalog_code, protocol, base_url,
			kind, category, domestic, egress_profile, enabled
		)
		SELECT
			'default',
			pc.code,
			pc.display_name,
			pc.code,
			COALESCE(pc.protocol, 'openai-completions'),
			COALESCE(pc.base_url_template, ''),
			COALESCE(pc.kind, 'cloud'),
			COALESCE(pc.category, 'official'),
			COALESCE(pc.domestic, TRUE),
			COALESCE(pc.default_egress_profile, 'direct'),
			TRUE
		FROM provider_catalog pc
		WHERE pc.hidden IS NOT TRUE
		  AND NOT EXISTS (
			SELECT 1 FROM providers p
			WHERE p.tenant_id = 'default' AND p.catalog_code = pc.code
		)
	`)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (h *Handler) handleProviderCredentials(w http.ResponseWriter, r *http.Request, providerID int, credPath string) {
	if credPath == "" {
		if r.Method == http.MethodPost {
			h.addCredential(w, r, providerID)
		} else if r.Method == http.MethodGet {
			h.listCredentials(w, r, providerID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	credIDStr := credPath
	subPath := ""
	for i, c := range credPath {
		if c == '/' {
			credIDStr = credPath[:i]
			subPath = credPath[i+1:]
			break
		}
	}
	credID, err := strconv.Atoi(credIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid credential id")
		return
	}

	switch subPath {
	case "":
		if r.Method == http.MethodPatch {
			h.updateCredential(w, r, providerID, credID)
		} else if r.Method == http.MethodDelete {
			h.deleteCredential(w, r, providerID, credID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "reveal":
		if r.Method == http.MethodPost {
			h.revealCredential(w, r, providerID, credID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "check", "check-health":
		if r.Method == http.MethodPost {
			h.startCheckCredentialHealth(w, r, providerID, credID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "usage":
		if r.Method == http.MethodGet {
			h.getCredentialUsage(w, r, credID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "lifecycle":
		if r.Method == http.MethodPatch {
			h.updateCredentialLifecycle(w, r, providerID, credID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "manual-disabled":
		if r.Method == http.MethodPatch {
			h.setCredentialManualDisabled(w, r, providerID, credID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "default-probe-model":
		if r.Method == http.MethodPatch {
			h.setDefaultProbeModel(w, r, providerID, credID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "pick-default-probe-model":
		if r.Method == http.MethodPost {
			h.pickDefaultProbeModel(w, r, providerID, credID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "reset-availability":
		if r.Method == http.MethodPost {
			h.resetCredentialAvailability(w, r, providerID, credID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "reset-quota":
		if r.Method == http.MethodPost {
			h.resetCredentialQuota(w, r, providerID, credID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) addCredential(w http.ResponseWriter, r *http.Request, providerID int) {
	var req struct {
		Label            *string `json:"label"`
		APIKey           string  `json:"api_key"`
		ConcurrencyLimit *int    `json:"concurrency_limit"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.APIKey == "" {
		writeError(w, http.StatusBadRequest, "api_key required")
		return
	}

	encrypted, err := h.encryptCred([]byte(req.APIKey))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encryption failed")
		return
	}

	label := "default"
	if req.Label != nil && *req.Label != "" {
		label = *req.Label
	}
	concurrencyLimit := 10
	if req.ConcurrencyLimit != nil {
		concurrencyLimit = *req.ConcurrencyLimit
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var id int
	err = h.db.QueryRow(ctx, `
		INSERT INTO credentials (provider_id, label, secret_ciphertext, status, concurrency_limit, balance_usd)
		VALUES ($1, $2, $3, 'active', $4, 1000.0)
		RETURNING id
	`, providerID, label, encrypted, concurrencyLimit).Scan(&id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "message": "ok"})
}

func (h *Handler) listCredentials(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT c.id, c.provider_id, COALESCE(c.label,''), COALESCE(c.status,'active'),
		       COALESCE(c.trust_level,'standard'), c.concurrency_limit,
		       c.balance_usd::float8,
		       COALESCE(c.circuit_state,'closed'),
		       c.circuit_opened_at,
		       COALESCE(c.consecutive_failures, 0),
		       c.cooling_until,
		       COALESCE(c.lifecycle_status,'active'),
		       COALESCE(c.availability_state,'ready'),
		       c.availability_recover_at,
		       COALESCE(c.quota_state,'ok'),
		       c.quota_recover_at,
		       c.state_reason_code,
		       c.state_reason_detail,
		       c.state_updated_at,
		       COALESCE(c.health_status,'unknown'),
		       c.health_checked_at,
		       c.health_source,
		       c.health_warning_code,
		       c.health_error,
		       c.health_latency_ms,
		       c.health_probe_model,
		       c.api_models_ok,
		       c.api_models_last_checked_at,
		       c.api_models_error,
		       c.effective_at,
		       c.expires_at,
		       c.tags,
		       COALESCE(c.notes,''),
		       c.secret_ciphertext,
		       c.created_at,
		       c.updated_at
		FROM credentials c
		WHERE c.provider_id = $1
		ORDER BY c.id
	`, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type cred struct {
		ID                      int        `json:"id"`
		ProviderID              int        `json:"provider_id"`
		Label                   string     `json:"label"`
		Status                  string     `json:"status"`
		TrustLevel              string     `json:"trust_level"`
		ConcurrencyLimit        *int       `json:"concurrency_limit"`
		BalanceUSD              *float64   `json:"balance_usd"`
		CircuitState            string     `json:"circuit_state"`
		CircuitOpenedAt         *time.Time `json:"circuit_opened_at"`
		ConsecutiveFailures     int        `json:"consecutive_failures"`
		CoolingUntil           *time.Time `json:"cooling_until"`
		LifecycleStatus         string     `json:"lifecycle_status"`
		AvailabilityState       string     `json:"availability_state"`
		AvailabilityRecoverAt   *time.Time `json:"availability_recover_at"`
		QuotaState              string     `json:"quota_state"`
		QuotaRecoverAt          *time.Time `json:"quota_recover_at"`
		StateReasonCode         *string    `json:"state_reason_code"`
		StateReasonDetail       *string    `json:"state_reason_detail"`
		StateUpdatedAt          *time.Time `json:"state_updated_at"`
		HealthStatus            string     `json:"health_status"`
		HealthCheckedAt         *time.Time `json:"health_checked_at"`
		HealthSource            *string    `json:"health_source"`
		HealthWarningCode       *string    `json:"health_warning_code"`
		HealthError             *string    `json:"health_error"`
		HealthLatencyMs         *int       `json:"health_latency_ms"`
		HealthProbeModel        *string    `json:"health_probe_model"`
		ApiModelsOk             *bool      `json:"api_models_ok"`
		ApiModelsLastCheckedAt  *time.Time `json:"api_models_last_checked_at"`
		ApiModelsError          *string    `json:"api_models_error"`
		EffectiveAt             *time.Time `json:"effective_at"`
		ExpiresAt               *time.Time `json:"expires_at"`
		Tags                    []string   `json:"tags"`
		Notes                   string     `json:"notes"`
		KeyMasked               *string    `json:"key_masked"`
		KeyMaskError            *string    `json:"key_mask_error"`
		FpSlotLimit             *int       `json:"fp_slot_limit"`
		FpSlotsUsed             *int       `json:"fp_slots_used"`
		FpSlotsFree             *int       `json:"fp_slots_free"`
		EffectiveFpSlotLimit    *int       `json:"effective_fp_slot_limit"`
		CreatedAt               *time.Time `json:"created_at"`
		UpdatedAt               *time.Time `json:"updated_at"`
	}

	var creds []cred
	for rows.Next() {
		var c cred
		var tagsStr sql.NullString
		var balanceUSD sql.NullFloat64
		var ciphertext []byte

		if err := rows.Scan(
			&c.ID, &c.ProviderID, &c.Label, &c.Status,
			&c.TrustLevel, &c.ConcurrencyLimit,
			&balanceUSD,
			&c.CircuitState,
			&c.CircuitOpenedAt,
			&c.ConsecutiveFailures,
			&c.CoolingUntil,
			&c.LifecycleStatus,
			&c.AvailabilityState,
			&c.AvailabilityRecoverAt,
			&c.QuotaState,
			&c.QuotaRecoverAt,
			&c.StateReasonCode,
			&c.StateReasonDetail,
			&c.StateUpdatedAt,
			&c.HealthStatus,
			&c.HealthCheckedAt,
			&c.HealthSource,
			&c.HealthWarningCode,
			&c.HealthError,
			&c.HealthLatencyMs,
			&c.HealthProbeModel,
			&c.ApiModelsOk,
			&c.ApiModelsLastCheckedAt,
			&c.ApiModelsError,
			&c.EffectiveAt,
			&c.ExpiresAt,
			&tagsStr,
			&c.Notes,
			&ciphertext,
			&c.CreatedAt,
			&c.UpdatedAt,
		); err != nil {
			slog.Warn("listCredentials scan failed", "error", err)
			continue
		}

		if balanceUSD.Valid {
			c.BalanceUSD = &balanceUSD.Float64
		}

		c.Tags = parseTags(tagsStr)
		if len(ciphertext) > 0 {
			if plaintext, decErr := h.decryptCredStr(string(ciphertext)); decErr != nil {
				errCode := "decrypt_failed"
				c.KeyMaskError = &errCode
			} else {
				masked := maskAPIKey(plaintext)
				c.KeyMasked = &masked
			}
		}
		if h.fpSlots != nil {
			c.FpSlotLimit, c.FpSlotsUsed, c.FpSlotsFree = h.fpSlots.Stats(ctx, c.ID, c.ConcurrencyLimit)
			c.EffectiveFpSlotLimit = credentialfpslot.EffectiveLimit(c.ConcurrencyLimit, h.fpSlotsDefaultLimit())
		}
		creds = append(creds, c)
	}
	if creds == nil {
		creds = []cred{}
	}
	writeJSON(w, http.StatusOK, creds)
}

func parseTags(ns sql.NullString) []string {
	if !ns.Valid || ns.String == "" {
		return []string{}
	}
	s := strings.TrimSpace(ns.String)
	if s == "" {
		return []string{}
	}
	if s[0] == '[' {
		var arr []string
		if err := json.Unmarshal([]byte(s), &arr); err == nil {
			return arr
		}
	}
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func (h *Handler) getProviderModels(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT mo.id, mo.credential_id, COALESCE(c.label,'') AS credential_label,
		       COALESCE(mo.raw_model_name,''), COALESCE(mo.standardized_name,''),
		       mo.canonical_id, COALESCE(mo.outbound_model_name,'') AS display_name,
		       mo.available, mo.unavailable_reason, mo.unavailable_at,
		       mo.p95_latency_ms, mo.success_rate::float8,
		       mo.unit_price_in_per_1m::float8 AS input_price,
		       mo.unit_price_out_per_1m::float8 AS output_price,
		       mo.last_seen_at, COALESCE(mo.routing_tier::text,'')
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		WHERE c.provider_id = $1
		ORDER BY mo.raw_model_name
	`, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type modelOffer struct {
		ID                int        `json:"id"`
		CredentialID      int        `json:"credential_id"`
		CredentialLabel   string     `json:"credential_label"`
		RawModelName      string     `json:"raw_model_name"`
		StandardizedName  string     `json:"standardized_name"`
		CanonicalID       *int       `json:"canonical_id"`
		DisplayName       string     `json:"display_name"`
		Available         bool       `json:"available"`
		UnavailableReason *string    `json:"unavailable_reason"`
		UnavailableAt     *time.Time `json:"unavailable_at"`
		P95LatencyMs      *int       `json:"p95_latency_ms"`
		SuccessRate       *float64   `json:"success_rate"`
		InputPrice        *float64   `json:"input_price"`
		OutputPrice       *float64   `json:"output_price"`
		LastSeenAt        *time.Time `json:"last_seen_at"`
		RoutingTier       string     `json:"routing_tier"`
		AvailabilitySource string    `json:"availability_source"`
	}

	var offers []modelOffer
	for rows.Next() {
		var o modelOffer
		if err := rows.Scan(
			&o.ID, &o.CredentialID, &o.CredentialLabel,
			&o.RawModelName, &o.StandardizedName,
			&o.CanonicalID, &o.DisplayName,
			&o.Available, &o.UnavailableReason, &o.UnavailableAt,
			&o.P95LatencyMs, &o.SuccessRate,
			&o.InputPrice, &o.OutputPrice,
			&o.LastSeenAt, &o.RoutingTier,
		); err != nil {
			slog.Warn("getProviderModels scan failed", "error", err)
			continue
		}
		o.AvailabilitySource = classifyAvailability(o.Available, o.UnavailableReason)
		offers = append(offers, o)
	}
	if offers == nil {
		offers = []modelOffer{}
	}
	writeJSON(w, http.StatusOK, offers)
}

// clearProviderModels hard-deletes all credential_model_bindings for a
// provider so the list is empty and the operator can re-fetch from the vendor.
// We bypass DELETE on model_offers — that view's trigger only soft-deletes
// (available=false, reason=deleted), which leaves a full disabled list.
func (h *Handler) clearProviderModels(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var exists int
	if err := h.db.QueryRow(ctx, `
		SELECT 1 FROM providers WHERE id = $1 AND tenant_id = 'default'
	`, providerID).Scan(&exists); err != nil {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}

	deleted, err := modelcatalog.ClearProviderBindings(ctx, h.db, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "clear failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"message": "ok",
		"deleted": int(deleted),
	})
}

func classifyAvailability(available bool, reason *string) string {
	if reason == nil {
		if available {
			return "auto"
		}
		return "never"
	}
	r := *reason
	if strings.HasPrefix(r, "auto_") {
		return "auto"
	}
	if strings.HasPrefix(r, "manual") {
		return "manual"
	}
	if available {
		return "auto"
	}
	return "never"
}

func (h *Handler) queryProviderModels(w http.ResponseWriter, r *http.Request, providerID int) {
	var req struct {
		Q             *string   `json:"q"`
		Available     *bool     `json:"available"`
		UnavailReason *string   `json:"unavailable_reason"`
		CredentialID  *int      `json:"credential_id"`
		MinSuccess    *float64  `json:"min_success_rate"`
		MaxP95Latency *int      `json:"max_p95_latency"`
		Page          int       `json:"page"`
		PageSize      int       `json:"page_size"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Page <= 0 {
		req.Page = 1
	}
	if req.PageSize <= 0 {
		req.PageSize = 50
	}

	conditions := []string{"c.provider_id = $1"}
	args := []any{providerID}
	argIdx := 2

	if req.Q != nil && *req.Q != "" {
		conditions = append(conditions, fmt.Sprintf("(mo.raw_model_name ILIKE $%d OR mo.standardized_name ILIKE $%d)", argIdx, argIdx))
		args = append(args, "%"+*req.Q+"%")
		argIdx++
	}
	if req.Available != nil {
		conditions = append(conditions, fmt.Sprintf("mo.available = $%d", argIdx))
		args = append(args, *req.Available)
		argIdx++
	}
	if req.UnavailReason != nil && *req.UnavailReason != "" {
		conditions = append(conditions, fmt.Sprintf("mo.unavailable_reason = $%d", argIdx))
		args = append(args, *req.UnavailReason)
		argIdx++
	}
	if req.CredentialID != nil {
		conditions = append(conditions, fmt.Sprintf("mo.credential_id = $%d", argIdx))
		args = append(args, *req.CredentialID)
		argIdx++
	}
	if req.MinSuccess != nil {
		conditions = append(conditions, fmt.Sprintf("mo.success_rate >= $%d", argIdx))
		args = append(args, *req.MinSuccess)
		argIdx++
	}
	if req.MaxP95Latency != nil {
		conditions = append(conditions, fmt.Sprintf("mo.p95_latency_ms <= $%d", argIdx))
		args = append(args, *req.MaxP95Latency)
		argIdx++
	}

	where := strings.Join(conditions, " AND ")

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var total int
	countSQL := fmt.Sprintf(`SELECT COUNT(*) FROM model_offers mo JOIN credentials c ON c.id = mo.credential_id WHERE %s`, where)
	if err := h.db.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		slog.Warn("queryProviderModels count failed", "error", err)
		total = 0
	}

	offset := (req.Page - 1) * req.PageSize
	dataSQL := fmt.Sprintf(`
		SELECT mo.id, mo.credential_id, COALESCE(c.label,'') AS credential_label,
		       COALESCE(mo.raw_model_name,''), COALESCE(mo.standardized_name,''),
		       mo.canonical_id, COALESCE(mo.outbound_model_name,'') AS display_name,
		       mo.available, mo.unavailable_reason, mo.unavailable_at,
		       mo.p95_latency_ms, mo.success_rate::float8,
		       mo.unit_price_in_per_1m::float8 AS input_price,
		       mo.unit_price_out_per_1m::float8 AS output_price,
		       mo.last_seen_at, COALESCE(mo.routing_tier::text,'')
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		WHERE %s
		ORDER BY mo.raw_model_name
		LIMIT $%d OFFSET $%d
	`, where, argIdx, argIdx+1)
	args = append(args, req.PageSize, offset)

	rows, err := h.db.Query(ctx, dataSQL, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type modelOffer struct {
		ID                int        `json:"id"`
		CredentialID      int        `json:"credential_id"`
		CredentialLabel   string     `json:"credential_label"`
		RawModelName      string     `json:"raw_model_name"`
		StandardizedName  string     `json:"standardized_name"`
		CanonicalID       *int       `json:"canonical_id"`
		DisplayName       string     `json:"display_name"`
		Available         bool       `json:"available"`
		UnavailableReason *string    `json:"unavailable_reason"`
		UnavailableAt     *time.Time `json:"unavailable_at"`
		P95LatencyMs      *int       `json:"p95_latency_ms"`
		SuccessRate       *float64   `json:"success_rate"`
		InputPrice        *float64   `json:"input_price"`
		OutputPrice       *float64   `json:"output_price"`
		LastSeenAt        *time.Time `json:"last_seen_at"`
		RoutingTier       string     `json:"routing_tier"`
		AvailabilitySource string    `json:"availability_source"`
	}

	offers := make([]modelOffer, 0)
	for rows.Next() {
		var o modelOffer
		if err := rows.Scan(
			&o.ID, &o.CredentialID, &o.CredentialLabel,
			&o.RawModelName, &o.StandardizedName,
			&o.CanonicalID, &o.DisplayName,
			&o.Available, &o.UnavailableReason, &o.UnavailableAt,
			&o.P95LatencyMs, &o.SuccessRate,
			&o.InputPrice, &o.OutputPrice,
			&o.LastSeenAt, &o.RoutingTier,
		); err != nil {
			slog.Warn("queryProviderModels scan failed", "error", err)
			continue
		}
		o.AvailabilitySource = classifyAvailability(o.Available, o.UnavailableReason)
		offers = append(offers, o)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":     offers,
		"total":     total,
		"page":      req.Page,
		"page_size": req.PageSize,
	})
}

func (h *Handler) providerLogs(w http.ResponseWriter, r *http.Request, providerID int) {
	var filter struct {
		CredentialID *int       `json:"credential_id"`
		Model        *string    `json:"model"`
		FromTS       *time.Time `json:"from_ts"`
		ToTS         *time.Time `json:"to_ts"`
		Success      *bool      `json:"success"`
		ErrorKind    *string    `json:"error_kind"`
		Page         int        `json:"page"`
		PageSize     int        `json:"page_size"`
	}

	if r.Method == http.MethodPost {
		if err := readJSON(r, &filter); err != nil {
			writeError(w, http.StatusBadRequest, "invalid body")
			return
		}
	} else {
		filter.CredentialID = queryIntPtr(r, "credential_id")
		if m := queryString(r, "model"); m != "" {
			filter.Model = &m
		}
		if s := queryString(r, "success"); s != "" {
			b := s == "true" || s == "1"
			filter.Success = &b
		}
		if ek := queryString(r, "error_kind"); ek != "" {
			filter.ErrorKind = &ek
		}
	}

	if filter.Page <= 0 {
		filter.Page = 1
	}
	if filter.PageSize <= 0 {
		filter.PageSize = 50
	}

	conditions := []string{"rl.provider_id = $1"}
	args := []any{providerID}
	argIdx := 2

	if filter.CredentialID != nil {
		conditions = append(conditions, fmt.Sprintf("rl.credential_id = $%d", argIdx))
		args = append(args, *filter.CredentialID)
		argIdx++
	}
	if filter.Model != nil && *filter.Model != "" {
		conditions = append(conditions, fmt.Sprintf("(rl.client_model ILIKE $%d OR rl.outbound_model ILIKE $%d)", argIdx, argIdx))
		args = append(args, "%"+*filter.Model+"%")
		argIdx++
	}
	if filter.FromTS != nil {
		conditions = append(conditions, fmt.Sprintf("rl.ts >= $%d", argIdx))
		args = append(args, *filter.FromTS)
		argIdx++
	}
	if filter.ToTS != nil {
		conditions = append(conditions, fmt.Sprintf("rl.ts <= $%d", argIdx))
		args = append(args, *filter.ToTS)
		argIdx++
	}
	if filter.Success != nil {
		conditions = append(conditions, fmt.Sprintf("rl.success = $%d", argIdx))
		args = append(args, *filter.Success)
		argIdx++
	}
	if filter.ErrorKind != nil && *filter.ErrorKind != "" {
		conditions = append(conditions, fmt.Sprintf("rl.error_kind = $%d", argIdx))
		args = append(args, *filter.ErrorKind)
		argIdx++
	}

	where := strings.Join(conditions, " AND ")

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var total int
	countSQL := fmt.Sprintf(`SELECT COUNT(*) FROM request_logs rl WHERE %s`, where)
	if err := h.db.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		slog.Warn("providerLogs count failed", "error", err)
		total = 0
	}

	offset := (filter.Page - 1) * filter.PageSize
	dataSQL := fmt.Sprintf(`
		SELECT rl.ts, rl.request_id, rl.credential_id,
		       rl.client_model, rl.outbound_model,
		       rl.success, rl.error_kind,
		       rl.prompt_tokens, rl.completion_tokens, rl.total_tokens,
		       rl.cost_usd::float8, rl.latency_ms
		FROM request_logs rl
		WHERE %s
		ORDER BY rl.ts DESC
		LIMIT $%d OFFSET $%d
	`, where, argIdx, argIdx+1)
	args = append(args, filter.PageSize, offset)

	rows, err := h.db.Query(ctx, dataSQL, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type logEntry struct {
		Ts               *time.Time `json:"ts"`
		RequestID        *string    `json:"request_id"`
		CredentialID     *int       `json:"credential_id"`
		ClientModel      *string    `json:"client_model"`
		OutboundModel    *string    `json:"outbound_model"`
		Success          bool       `json:"success"`
		ErrorKind        *string    `json:"error_kind"`
		PromptTokens     *int       `json:"prompt_tokens"`
		CompletionTokens *int       `json:"completion_tokens"`
		TotalTokens      *int       `json:"total_tokens"`
		CostUSD          *float64   `json:"cost_usd"`
		LatencyMs        *int       `json:"latency_ms"`
	}

	items := make([]logEntry, 0)
	for rows.Next() {
		var l logEntry
		if err := rows.Scan(
			&l.Ts, &l.RequestID, &l.CredentialID,
			&l.ClientModel, &l.OutboundModel,
			&l.Success, &l.ErrorKind,
			&l.PromptTokens, &l.CompletionTokens, &l.TotalTokens,
			&l.CostUSD, &l.LatencyMs,
		); err != nil {
			slog.Warn("providerLogs scan failed", "error", err)
			continue
		}
		items = append(items, l)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":     items,
		"total":     total,
		"page":      filter.Page,
		"page_size": filter.PageSize,
	})
}

func queryIntPtr(r *http.Request, key string) *int {
	s := r.URL.Query().Get(key)
	if s == "" {
		return nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return nil
	}
	return &v
}

func (h *Handler) diagnoseProvider(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	var providerCode, baseURL, protocol string
	var enabled bool
	err := h.db.QueryRow(ctx, `
		SELECT COALESCE(code,''), COALESCE(base_url,''), COALESCE(protocol,''), enabled
		FROM providers WHERE id = $1 AND tenant_id = 'default'
	`, providerID).Scan(&providerCode, &baseURL, &protocol, &enabled)
	if err != nil {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}

	type modelsProbe struct {
		StatusCode  int      `json:"status_code"`
		LatencyMs   int      `json:"latency_ms"`
		Error       string   `json:"error,omitempty"`
		ModelsCount int      `json:"models_count"`
		SampleModels []string `json:"sample_models"`
	}

	type chatProbe struct {
		StatusCode       int    `json:"status_code"`
		LatencyMs        int    `json:"latency_ms"`
		Error            string `json:"error,omitempty"`
		ErrorCode        string `json:"error_code,omitempty"`
		ModelInResponse  string `json:"model_in_response,omitempty"`
	}

	type credDiag struct {
		CredentialID       int         `json:"credential_id"`
		Label              string      `json:"label"`
		Status             string      `json:"status"`
		CircuitState       string      `json:"circuit_state"`
		AvailabilityState  string      `json:"availability_state"`
		HealthStatus       string      `json:"health_status"`
		ConsecutiveFailures int        `json:"consecutive_failures"`
		ModelsProbe        modelsProbe `json:"models_probe"`
		ChatProbe          chatProbe   `json:"chat_probe"`
	}

	rows, err := h.db.Query(ctx, `
		SELECT c.id, COALESCE(c.label,''), COALESCE(c.status,'active'),
		       COALESCE(c.circuit_state,'closed'), COALESCE(c.availability_state,'ready'),
		       COALESCE(c.health_status,'unknown'), COALESCE(c.consecutive_failures,0),
		       c.secret_ciphertext
		FROM credentials c
		WHERE c.provider_id = $1 AND c.status = 'active'
		ORDER BY c.id
	`, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	var credDiags []credDiag
	var totalLatencyMs int
	latencyCount := 0

	for rows.Next() {
		var cd credDiag
		var ciphertext string
		if err := rows.Scan(&cd.CredentialID, &cd.Label, &cd.Status,
			&cd.CircuitState, &cd.AvailabilityState, &cd.HealthStatus,
			&cd.ConsecutiveFailures, &ciphertext); err != nil {
			continue
		}

	apiKey, decErr := h.decryptCredStr(string(ciphertext))
		if decErr != nil {
			cd.ModelsProbe = modelsProbe{Error: "decrypt failed"}
			cd.ChatProbe = chatProbe{Error: "decrypt failed"}
			credDiags = append(credDiags, cd)
			continue
		}

		modelsURLs := modelsURLCandidatesForBase(baseURL)

		start := time.Now()
		modelsResp, modelsErr := doProbeRequest(ctx, modelsURLs, apiKey)
		modelsLatency := int(time.Since(start).Milliseconds())
		totalLatencyMs += modelsLatency
		latencyCount++

		if modelsErr != nil {
			cd.ModelsProbe = modelsProbe{
				LatencyMs: modelsLatency,
				Error:     modelsErr.Error(),
			}
		} else {
			cd.ModelsProbe = modelsProbe{
				StatusCode:   modelsResp.statusCode,
				LatencyMs:    modelsLatency,
				ModelsCount:  modelsResp.modelCount,
				SampleModels: modelsResp.sampleModels,
			}
		}

		var testModel string
		err := h.db.QueryRow(ctx, `
			SELECT COALESCE(NULLIF(mo.outbound_model_name, ''), mo.raw_model_name) AS probe_model
			FROM model_offers mo
			JOIN credentials c ON c.id = mo.credential_id
			WHERE c.provider_id = $1 AND mo.available = TRUE
			ORDER BY mo.id
			LIMIT 1
		`, providerID).Scan(&testModel)
		if err != nil {
			testModel = "gpt-4o-mini"
		}

		chatURL := upstreamurl.ChatCompletionsURL(baseURL)
		start = time.Now()
		chatResp, chatErr := doChatProbe(ctx, chatURL, apiKey, testModel)
		chatLatency := int(time.Since(start).Milliseconds())
		totalLatencyMs += chatLatency
		latencyCount++

		if chatErr != nil {
			cd.ChatProbe = chatProbe{
				LatencyMs: chatLatency,
				Error:     chatErr.Error(),
			}
		} else {
			cp := chatProbe{
				StatusCode:      chatResp.statusCode,
				LatencyMs:       chatLatency,
				ModelInResponse: chatResp.modelInResponse,
				ErrorCode:       chatResp.errorCode,
			}
			if chatResp.errorCode == probeutil.EndpointIDRequiredErrCode {
				cp.Error = "此模型（如火山方舟 minimax-m3 / glm-5.1）需要 endpoint ID（outbound_model_name），请在 Models 标签页设置"
			}
			cd.ChatProbe = cp
		}

		credDiags = append(credDiags, cd)
	}

	healthy := 0
	degraded := 0
	unreachable := 0
	cooling := 0
	disabled := 0
	for _, cd := range credDiags {
		switch {
		case cd.HealthStatus == "healthy":
			healthy++
		case cd.AvailabilityState == "unreachable":
			unreachable++
		case cd.CircuitState == "open":
			cooling++
		case cd.HealthStatus == "warning" || cd.HealthStatus == "degraded":
			degraded++
		default:
			if cd.Status != "active" {
				disabled++
			}
		}
	}

	var availableCount, totalOffers int
	h.db.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE mo.available = TRUE), COUNT(*)
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		WHERE c.provider_id = $1
	`, providerID).Scan(&availableCount, &totalOffers)

	coveragePct := 0.0
	if totalOffers > 0 {
		coveragePct = float64(availableCount) / float64(totalOffers) * 100
	}

	avgLatency := 0.0
	if latencyCount > 0 {
		avgLatency = float64(totalLatencyMs) / float64(latencyCount)
	}

	type errorClassification struct {
		AuthErrors         int `json:"auth_errors"`
		RateLimitErrors    int `json:"rate_limit_errors"`
		TimeoutErrors      int `json:"timeout_errors"`
		ModelNotFoundErrors int `json:"model_not_found_errors"`
		OtherErrors        int `json:"other_errors"`
	}

	var ec errorClassification
	ecRows, err := h.db.Query(ctx, `
		SELECT COALESCE(error_kind,'other'), COUNT(*)
		FROM request_logs
		WHERE provider_id = $1 AND ts >= now() - interval '24 hours'
		GROUP BY error_kind
	`, providerID)
	if err == nil {
		for ecRows.Next() {
			var kind string
			var cnt int
			if ecRows.Scan(&kind, &cnt) != nil {
				continue
			}
			switch {
			case strings.Contains(kind, "auth") || strings.Contains(kind, "401") || strings.Contains(kind, "403"):
				ec.AuthErrors += cnt
			case strings.Contains(kind, "rate") || strings.Contains(kind, "429"):
				ec.RateLimitErrors += cnt
			case strings.Contains(kind, "timeout") || strings.Contains(kind, "deadline"):
				ec.TimeoutErrors += cnt
			case strings.Contains(kind, "model") || strings.Contains(kind, "404"):
				ec.ModelNotFoundErrors += cnt
			default:
				ec.OtherErrors += cnt
			}
		}
		ecRows.Close()
	}

	type healthScore struct {
		CredentialID int     `json:"credential_id"`
		Score        float64 `json:"score"`
	}
	scores := make([]healthScore, 0, len(credDiags))
	for _, cd := range credDiags {
		score := 100.0
		if cd.CircuitState != "closed" {
			score -= 30
		}
		if cd.HealthStatus != "healthy" {
			score -= 20
		}
		if cd.ConsecutiveFailures > 0 {
			score -= 10
			score -= math.Min(20, float64(cd.ConsecutiveFailures)*5)
		}
		score = math.Max(0, math.Min(100, score))
		scores = append(scores, healthScore{CredentialID: cd.CredentialID, Score: score})
	}

	totalCreds := len(credDiags)
	result := map[string]any{
		"provider_id":  providerID,
		"provider_code": providerCode,
		"enabled":      enabled,
		"base_url":     baseURL,
		"protocol":     protocol,
		"timestamp":    time.Now().UTC().Format(time.RFC3339),
		"credentials":  credDiags,
		"summary": map[string]any{
			"total_credentials":    totalCreds,
			"healthy":              healthy,
			"degraded":             degraded,
			"unreachable":          unreachable,
			"cooling":              cooling,
			"disabled":             disabled,
			"models_coverage_pct":  coveragePct,
			"avg_latency_ms":       avgLatency,
		},
		"error_classification": ec,
		"health_scores":        scores,
	}

	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) startDiagnose(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	taskID, err := insertBackgroundTask(ctx, h.db, "diagnose", &providerID, nil, map[string]any{"provider_id": providerID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create task: "+err.Error())
		return
	}

	go h.runDiagnose(providerID, taskID)

	writeJSON(w, http.StatusAccepted, map[string]any{"task_id": taskID, "status": "running"})
}

func (h *Handler) getDiagnoseResult(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	result, err := getLatestDiagnoseResult(ctx, h.db, providerID)
	if err != nil {
		writeError(w, http.StatusNotFound, "no completed diagnose result found")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) runDiagnose(providerID int, taskID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	result := h.doDiagnose(ctx, providerID)
	if ctx.Err() != nil {
		failBackgroundTask(ctx, h.db, taskID, "timeout")
		return
	}
	completeBackgroundTask(ctx, h.db, taskID, result)
}

func (h *Handler) doDiagnose(ctx context.Context, providerID int) map[string]any {
	var providerCode, baseURL, protocol string
	var enabled bool
	h.db.QueryRow(ctx, `
		SELECT COALESCE(code,''), COALESCE(base_url,''), COALESCE(protocol,''), enabled
		FROM providers WHERE id = $1 AND tenant_id = 'default'
	`, providerID).Scan(&providerCode, &baseURL, &protocol, &enabled)

	type modelsProbe struct {
		StatusCode   int      `json:"status_code"`
		LatencyMs    int      `json:"latency_ms"`
		Error        string   `json:"error,omitempty"`
		ModelsCount  int      `json:"models_count"`
		SampleModels []string `json:"sample_models"`
	}
	type chatProbe struct {
		StatusCode      int    `json:"status_code"`
		LatencyMs       int    `json:"latency_ms"`
		Error           string `json:"error,omitempty"`
		ModelInResponse string `json:"model_in_response,omitempty"`
	}
	type credDiag struct {
		CredentialID      int         `json:"credential_id"`
		Label             string      `json:"label"`
		Status            string      `json:"status"`
		CircuitState      string      `json:"circuit_state"`
		AvailabilityState string      `json:"availability_state"`
		HealthStatus      string      `json:"health_status"`
		ConsecutiveFailures int       `json:"consecutive_failures"`
		ModelsProbe       modelsProbe `json:"models_probe"`
		ChatProbe         chatProbe   `json:"chat_probe"`
	}

	rows, err := h.db.Query(ctx, `
		SELECT c.id, COALESCE(c.label,''), COALESCE(c.status,'active'),
		       COALESCE(c.circuit_state,'closed'), COALESCE(c.availability_state,'ready'),
		       COALESCE(c.health_status,'unknown'), COALESCE(c.consecutive_failures,0),
		       c.secret_ciphertext
		FROM credentials c WHERE c.provider_id = $1 AND c.status = 'active' ORDER BY c.id
	`, providerID)

	var credDiags []credDiag
	var totalLatencyMs int
	latencyCount := 0

	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var cd credDiag
			var ciphertext string
			if rows.Scan(&cd.CredentialID, &cd.Label, &cd.Status, &cd.CircuitState,
				&cd.AvailabilityState, &cd.HealthStatus, &cd.ConsecutiveFailures, &ciphertext) != nil {
				continue
			}

	apiKey, decErr := h.decryptCredStr(string(ciphertext))
			if decErr != nil {
				cd.ModelsProbe = modelsProbe{Error: "decrypt failed"}
				cd.ChatProbe = chatProbe{Error: "decrypt failed"}
				credDiags = append(credDiags, cd)
				continue
			}

			modelsURLs := modelsURLCandidatesForBase(baseURL)
			start := time.Now()
			modelsResp, modelsErr := doProbeRequest(ctx, modelsURLs, apiKey)
			modelsLatency := int(time.Since(start).Milliseconds())
			totalLatencyMs += modelsLatency
			latencyCount++

			if modelsErr != nil {
				cd.ModelsProbe = modelsProbe{LatencyMs: modelsLatency, Error: modelsErr.Error()}
			} else {
				cd.ModelsProbe = modelsProbe{StatusCode: modelsResp.statusCode, LatencyMs: modelsLatency, ModelsCount: modelsResp.modelCount, SampleModels: modelsResp.sampleModels}
			}

			var testModel string
			h.db.QueryRow(ctx, `SELECT mo.raw_model_name FROM model_offers mo JOIN credentials c ON c.id = mo.credential_id WHERE c.provider_id = $1 AND mo.available = TRUE LIMIT 1`, providerID).Scan(&testModel)
			if testModel == "" { testModel = "gpt-4o-mini" }

			chatURL := upstreamurl.ChatCompletionsURL(baseURL)
			start = time.Now()
			chatResp, chatErr := doChatProbe(ctx, chatURL, apiKey, testModel)
			chatLatency := int(time.Since(start).Milliseconds())
			totalLatencyMs += chatLatency
			latencyCount++

			if chatErr != nil {
				cd.ChatProbe = chatProbe{LatencyMs: chatLatency, Error: chatErr.Error()}
			} else {
				cd.ChatProbe = chatProbe{StatusCode: chatResp.statusCode, LatencyMs: chatLatency, ModelInResponse: chatResp.modelInResponse}
			}
			credDiags = append(credDiags, cd)
		}
	}

	healthy, degraded, unreachable, cooling, disabled := 0, 0, 0, 0, 0
	for _, cd := range credDiags {
		switch {
		case cd.HealthStatus == "healthy": healthy++
		case cd.AvailabilityState == "unreachable": unreachable++
		case cd.CircuitState == "open": cooling++
		case cd.HealthStatus == "warning" || cd.HealthStatus == "degraded": degraded++
		default:
			if cd.Status != "active" { disabled++ }
		}
	}

	var availableCount, totalOffers int
	h.db.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE mo.available = TRUE), COUNT(*) FROM model_offers mo JOIN credentials c ON c.id = mo.credential_id WHERE c.provider_id = $1`, providerID).Scan(&availableCount, &totalOffers)
	coveragePct := 0.0
	if totalOffers > 0 { coveragePct = float64(availableCount) / float64(totalOffers) * 100 }
	avgLatency := 0.0
	if latencyCount > 0 { avgLatency = float64(totalLatencyMs) / float64(latencyCount) }

	type errorClassification struct {
		AuthErrors int `json:"auth_errors"`; RateLimitErrors int `json:"rate_limit_errors"`; TimeoutErrors int `json:"timeout_errors"`; ModelNotFoundErrors int `json:"model_not_found_errors"`; OtherErrors int `json:"other_errors"`
	}
	var ec errorClassification
	ecRows, _ := h.db.Query(ctx, `SELECT COALESCE(error_kind,'other'), COUNT(*) FROM request_logs WHERE provider_id = $1 AND ts >= now() - interval '24 hours' GROUP BY error_kind`, providerID)
	if ecRows != nil {
		for ecRows.Next() {
			var kind string; var cnt int
			if ecRows.Scan(&kind, &cnt) != nil { continue }
			switch {
			case strings.Contains(kind, "auth") || strings.Contains(kind, "401") || strings.Contains(kind, "403"): ec.AuthErrors += cnt
			case strings.Contains(kind, "rate") || strings.Contains(kind, "429"): ec.RateLimitErrors += cnt
			case strings.Contains(kind, "timeout") || strings.Contains(kind, "deadline"): ec.TimeoutErrors += cnt
			case strings.Contains(kind, "model") || strings.Contains(kind, "404"): ec.ModelNotFoundErrors += cnt
			default: ec.OtherErrors += cnt
			}
		}
		ecRows.Close()
	}

	type healthScore struct { CredentialID int `json:"credential_id"`; Score float64 `json:"score"` }
	scores := make([]healthScore, 0, len(credDiags))
	for _, cd := range credDiags {
		score := 100.0
		if cd.CircuitState != "closed" { score -= 30 }
		if cd.HealthStatus != "healthy" { score -= 20 }
		if cd.ConsecutiveFailures > 0 { score -= 10; score -= math.Min(20, float64(cd.ConsecutiveFailures)*5) }
		score = math.Max(0, math.Min(100, score))
		scores = append(scores, healthScore{CredentialID: cd.CredentialID, Score: score})
	}

	return map[string]any{
		"provider_id": providerID, "provider_code": providerCode, "enabled": enabled,
		"base_url": baseURL, "protocol": protocol, "timestamp": time.Now().UTC().Format(time.RFC3339),
		"credentials": credDiags, "summary": map[string]any{
			"total_credentials": len(credDiags), "healthy": healthy, "degraded": degraded,
			"unreachable": unreachable, "cooling": cooling, "disabled": disabled,
			"models_coverage_pct": coveragePct, "avg_latency_ms": avgLatency,
		},
		"error_classification": ec, "health_scores": scores,
	}
}

type probeResult struct {
	statusCode   int
	modelCount   int
	sampleModels []string
	authOK       bool   // whether the credential is authoritative for this base
	probeURL     string // which URL candidate succeeded
}

// isAcceptableStatus mirrors the Python probe loop: 200/401/403 indicate
// the endpoint exists and responds. 401/403 are accepted (auth needed);
// the caller's apiKey presence determines whether that means "ok".
func isAcceptableStatus(code int) bool {
	return code == http.StatusOK || code == http.StatusUnauthorized || code == http.StatusForbidden
}

// doProbeRequest tries each candidate URL in turn and returns the first
// one whose response is 200/401/403. Mirrors free_pool_probe.probe_openai_compatible_base
// in the Python llm-gateway. Returns the last transport error if all
// candidates fail.
//
// authOK semantics:
//   - response 200 + non-empty apiKey         → true
//   - response 200 + empty apiKey              → true  (endpoint reachable, no auth challenge)
//   - response 401/403 + non-empty apiKey     → false (endpoint exists, but credential rejected)
//   - response 401/403 + empty apiKey         → true  (endpoint exists, requires auth)
func doProbeRequest(ctx context.Context, urls []string, apiKey string) (*probeResult, error) {
	var lastErr error
	hasKey := strings.TrimSpace(apiKey) != ""

	for _, u := range urls {
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			lastErr = err
			continue
		}
		if hasKey {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()

		if !isAcceptableStatus(resp.StatusCode) {
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
			continue
		}

		result := &probeResult{
			statusCode: resp.StatusCode,
			probeURL:   u,
		}
		if resp.StatusCode == http.StatusOK {
			// Parse models list (tolerate alternative shapes like {models:[...]})
			var modelsResp struct {
				Data   []map[string]any `json:"data"`
				Models []map[string]any `json:"models"`
			}
			if json.Unmarshal(body, &modelsResp) == nil {
				rows := modelsResp.Data
				if len(rows) == 0 {
					rows = modelsResp.Models
				}
				result.modelCount = len(rows)
				limit := 3
				if len(rows) < limit {
					limit = len(rows)
				}
				for i := 0; i < limit; i++ {
					if id, ok := rows[i]["id"].(string); ok {
						result.sampleModels = append(result.sampleModels, id)
					} else if name, ok := rows[i]["name"].(string); ok {
						result.sampleModels = append(result.sampleModels, name)
					}
				}
			}
		}

		// authOK
		switch resp.StatusCode {
		case http.StatusOK:
			result.authOK = true
		case http.StatusUnauthorized, http.StatusForbidden:
			result.authOK = !hasKey // requires auth but no key supplied = expected
		}
		return result, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no candidate URLs")
	}
	return nil, lastErr
}

type chatResult struct {
	statusCode      int
	modelInResponse string
	// errorCode is set when the 404 body indicates the provider requires
	// an endpoint ID (outbound_model_name) rather than a raw model name.
	// The diagnose UI uses this to render a friendly hint.
	errorCode string
}

func doChatProbe(ctx context.Context, url, apiKey, model string) (*chatResult, error) {
	payload := map[string]any{
		"model":      model,
		"max_tokens": 5,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	result := &chatResult{statusCode: resp.StatusCode}

	if resp.StatusCode == http.StatusOK {
		var chatResp struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(respBody, &chatResp) == nil {
			result.modelInResponse = chatResp.Model
		}
	} else if resp.StatusCode == http.StatusNotFound &&
		probeutil.IsEndpointIDRequiredError(string(respBody)) {
		// 404 + "InvalidEndpointOrModel" / "endpoint not found" — the chosen
		// probe model needs an endpoint ID.  Surface this so the diagnose UI
		// can render a hint instead of a misleading "404 model not found".
		result.errorCode = probeutil.EndpointIDRequiredErrCode
	}

	return result, nil
}

func (h *Handler) updateCredential(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	var req struct {
		Label            *string  `json:"label"`
		Status           *string  `json:"status"`
		ConcurrencyLimit *int     `json:"concurrency_limit"`
		EffectiveAt      *string  `json:"effective_at"`
		ExpiresAt        *string  `json:"expires_at"`
		Tags             []string `json:"tags"`
		Notes            *string  `json:"notes"`
		BalanceUSD       *float64 `json:"balance_usd"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if req.Label != nil {
		h.db.Exec(ctx, `UPDATE credentials SET label = $1 WHERE id = $2 AND provider_id = $3`, *req.Label, credID, providerID)
	}
	if req.Status != nil {
		h.db.Exec(ctx, `UPDATE credentials SET status = $1 WHERE id = $2 AND provider_id = $3`, *req.Status, credID, providerID)
	}
	if req.ConcurrencyLimit != nil {
		h.db.Exec(ctx, `UPDATE credentials SET concurrency_limit = $1 WHERE id = $2 AND provider_id = $3`, *req.ConcurrencyLimit, credID, providerID)
	}
	if req.EffectiveAt != nil {
		h.db.Exec(ctx, `UPDATE credentials SET effective_at = $1 WHERE id = $2 AND provider_id = $3`, *req.EffectiveAt, credID, providerID)
	}
	if req.ExpiresAt != nil {
		h.db.Exec(ctx, `UPDATE credentials SET expires_at = $1 WHERE id = $2 AND provider_id = $3`, *req.ExpiresAt, credID, providerID)
	}
	if req.Tags != nil {
		tagsStr := strings.Join(req.Tags, ",")
		h.db.Exec(ctx, `UPDATE credentials SET tags = $1 WHERE id = $2 AND provider_id = $3`, tagsStr, credID, providerID)
	}
	if req.Notes != nil {
		h.db.Exec(ctx, `UPDATE credentials SET notes = $1 WHERE id = $2 AND provider_id = $3`, *req.Notes, credID, providerID)
	}
	if req.BalanceUSD != nil {
		h.db.Exec(ctx, `UPDATE credentials SET balance_usd = $1 WHERE id = $2 AND provider_id = $3`, *req.BalanceUSD, credID, providerID)
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "updated"})
}

func (h *Handler) deleteCredential(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	_, err := h.db.Exec(ctx, `UPDATE credentials SET status = 'disabled' WHERE id = $1 AND provider_id = $2`, credID, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "revoked"})
}

func (h *Handler) revealCredential(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var ciphertext []byte
	err := h.db.QueryRow(ctx, `
		SELECT secret_ciphertext FROM credentials
		WHERE id = $1 AND provider_id = $2 AND status <> 'disabled'
	`, credID, providerID).Scan(&ciphertext)
	if err != nil {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}
	if len(ciphertext) == 0 {
		writeError(w, http.StatusNotFound, "no secret stored")
		return
	}

	plaintext, err := h.decryptCredStr(string(ciphertext))
	if err != nil {
		slog.Warn("credential decrypt failed", "credential_id", credID, "error", err)
		writeError(w, http.StatusInternalServerError, "decryption failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"credential_id": credID,
		"api_key":       plaintext,
	})
}

func (h *Handler) updateCredentialLifecycle(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	var req struct {
		LifecycleStatus string `json:"lifecycle_status"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	h.db.Exec(ctx, `UPDATE credentials SET lifecycle_status = $1 WHERE id = $2 AND provider_id = $3`, req.LifecycleStatus, credID, providerID)
	writeJSON(w, http.StatusOK, map[string]string{"message": "updated"})
}

func (h *Handler) resetCredentialAvailability(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	h.db.Exec(ctx, `
		UPDATE credentials
		SET availability_state = 'ready', availability_recover_at = NULL,
		    state_reason_code = NULL, state_reason_detail = NULL, state_updated_at = now()
		WHERE id = $1 AND provider_id = $2
	`, credID, providerID)
	writeJSON(w, http.StatusOK, map[string]string{"message": "reset"})
}

func (h *Handler) resetCredentialQuota(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	h.db.Exec(ctx, `
		UPDATE credentials SET quota_state = 'ok', quota_recover_at = NULL
		WHERE id = $1 AND provider_id = $2
	`, credID, providerID)
	writeJSON(w, http.StatusOK, map[string]string{"message": "reset"})
}

func (h *Handler) startCheckCredentialHealth(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	taskID, err := insertBackgroundTask(ctx, h.db, "health_check", &providerID, &credID, map[string]any{"provider_id": providerID, "credential_id": credID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create task: "+err.Error())
		return
	}

	go h.runHealthCheck(providerID, credID, taskID)

	writeJSON(w, http.StatusAccepted, map[string]any{"task_id": taskID, "status": "running"})
}

func (h *Handler) runHealthCheck(providerID, credID int, taskID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := h.doHealthCheck(ctx, providerID, credID)
	if err != nil {
		slog.Error("health check failed", "provider_id", providerID, "credential_id", credID, "error", err)
		failBackgroundTask(ctx, h.db, taskID, "health check failed: "+err.Error())
		return
	}
	completeBackgroundTask(ctx, h.db, taskID, result)
}

func (h *Handler) doHealthCheck(ctx context.Context, providerID, credID int) (map[string]any, error) {
	cred, err := h.loadCredentialRowLite(ctx, providerID, credID)
	if err != nil {
		return nil, fmt.Errorf("query credential: %w", err)
	}

	apiKey, decErr := h.decryptCredStr(string(cred.secretCipher))
	probeOk := false
	var healthStatus, healthError string
	var healthLatencyMs int
	var modelsCount int
	var sampleModels []string
	var apiModelsErr *string

	if decErr != nil {
		healthStatus = "error"
		healthError = "decrypt failed"
		msg := healthError
		apiModelsErr = &msg
	} else {
		start := time.Now()
		models, source, fetchErr := h.resolveModelsForCredential(ctx, cred, apiKey, true)
		healthLatencyMs = int(time.Since(start).Milliseconds())

		if fetchErr != nil {
			healthStatus = "unreachable"
			healthError = fetchErr.Error()
			msg := fetchErr.Error()
			apiModelsErr = &msg
		} else if len(models) == 0 {
			healthStatus = "unreachable"
			healthError = fmt.Sprintf("no models returned (source=%s)", source)
			msg := healthError
			apiModelsErr = &msg
		} else {
			healthStatus = "healthy"
			probeOk = source == "api" || source == "api+manifest"
			modelsCount = len(models)
			limit := 3
			if len(models) < limit {
				limit = len(models)
			}
			sampleModels = models[:limit]
			if source != "api" && source != "api+manifest" {
				healthError = fmt.Sprintf("used %s fallback (%d models)", source, len(models))
			}
		}
	}

	if sampleModels == nil {
		sampleModels = []string{}
	}

	h.db.Exec(ctx, `
		UPDATE credentials SET health_status = $1, health_checked_at = now(), health_error = $2,
		    health_latency_ms = $3, health_source = 'probe', api_models_ok = $4,
		    api_models_last_checked_at = now(), api_models_error = $5
		WHERE id = $6
	`, healthStatus, healthError, healthLatencyMs, probeOk, apiModelsErr, credID)

	return map[string]any{
		"credential_id": credID, "health_status": healthStatus, "probe_ok": probeOk,
		"health_latency_ms": healthLatencyMs, "health_error": healthError,
		"models_count": modelsCount, "sample_models": sampleModels,
	}, nil
}

func (h *Handler) checkCredentialHealth(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	result, err := h.doHealthCheck(ctx, providerID, credID)
	if err != nil {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) batchRecoverCredentials(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	credTag, err := h.db.Exec(ctx, `
		UPDATE credentials
		SET availability_state = 'ready', availability_recover_at = NULL,
		    state_reason_code = NULL, state_reason_detail = NULL, state_updated_at = now()
		WHERE provider_id = $1
		  AND availability_state IN ('cooling','unreachable','degraded')
		  AND lifecycle_status = 'active'
	`, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "credential recovery failed")
		return
	}
	recoveredCreds := int(credTag.RowsAffected())

	offerTag, err := h.db.Exec(ctx, `
		UPDATE model_offers
		SET available = true, unavailable_reason = NULL, unavailable_at = NULL
		WHERE credential_id IN (
		    SELECT id FROM credentials
		    WHERE provider_id = $1 AND availability_state = 'ready'
		      AND lifecycle_status = 'active'
		) AND unavailable_reason LIKE 'auto_%%'
	`, providerID)
	recoveredOffers := 0
	if err == nil {
		recoveredOffers = int(offerTag.RowsAffected())
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"recovered_credentials": recoveredCreds,
		"recovered_offers":      recoveredOffers,
	})
}

func (h *Handler) getCredentialUsage(w http.ResponseWriter, r *http.Request, credID int) {
	days := queryInt(r, "days", 7)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var label, status, providerName string
	err := h.db.QueryRow(ctx, `
		SELECT c.label, COALESCE(c.status,''), COALESCE(p.display_name,'')
		FROM credentials c JOIN providers p ON p.id = c.provider_id
		WHERE c.id = $1 AND c.tenant_id = 'default'
	`, credID).Scan(&label, &status, &providerName)
	if err != nil {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}

	var reqCount int
	var promptTok, compTok int
	var cost, avgLatency, successRate float64
	h.db.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0),
		       COALESCE(SUM(cost_usd),0)::float8, COALESCE(AVG(latency_ms),0)::float8,
		       COALESCE(SUM(CASE WHEN success THEN 1 ELSE 0 END)::FLOAT / NULLIF(COUNT(*),0), 1.0)
		FROM usage_ledger WHERE credential_id = $1 AND tenant_id = 'default' AND ts >= now() - ($2 * INTERVAL '1 day')
	`, credID, days).Scan(&reqCount, &promptTok, &compTok, &cost, &avgLatency, &successRate)

	writeJSON(w, http.StatusOK, map[string]any{
		"credential_id":      credID,
		"label":              label,
		"status":             status,
		"provider_name":      providerName,
		"days":               days,
		"request_count":      reqCount,
		"prompt_tokens":      promptTok,
		"completion_tokens":  compTok,
		"cost_usd":           cost,
		"avg_latency_ms":     avgLatency,
		"success_rate":       successRate,
	})
}

func (h *Handler) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	path := r.URL.Path
	stripPrefix := "/api/tasks/"
	remaining := path[len(stripPrefix):]
	if remaining == "" {
		writeError(w, http.StatusBadRequest, "task_id required")
		return
	}

	taskID, err := strconv.ParseInt(remaining, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid task_id")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	task, err := getBackgroundTask(ctx, h.db, taskID)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	writeJSON(w, http.StatusOK, task)
}

func (h *Handler) handleProviderModelOffer(w http.ResponseWriter, r *http.Request, providerID int, offerPath string) {
	if offerPath == "" {
		writeError(w, http.StatusBadRequest, "offer_id required")
		return
	}

	parts := splitPath(offerPath)
	offerID, err := strconv.Atoi(parts[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid offer_id")
		return
	}

	if len(parts) > 1 {
		switch parts[1] {
		case "suggestions":
			h.getModelOfferSuggestions(w, r, providerID, offerID)
			return
		case "state":
			h.toggleModelOfferState(w, r, providerID, offerID)
			return
		}
	}

	if r.Method == http.MethodPatch {
		h.updateModelOffer(w, r, providerID, offerID)
	} else {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) updateModelOffer(w http.ResponseWriter, r *http.Request, providerID, offerID int) {
	var req struct {
		StandardizedName *string `json:"standardized_name"`
		CanonicalID      *int    `json:"canonical_id"`
		// OutboundModelName is the upstream-side model identifier — e.g. a
		// Volcano Ark endpoint ID like "ep-20241227XXXX".  When set, the
		// gateway uses this instead of raw_model_name when calling the
		// provider's chat completions / messages endpoint.  Pass an empty
		// string to clear it (revert to raw_model_name).
		OutboundModelName *string `json:"outbound_model_name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var currentID int
	var rawName string
	err := h.db.QueryRow(ctx, `
		SELECT mo.id, mo.raw_model_name FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		WHERE mo.id = $1 AND c.provider_id = $2
	`, offerID, providerID).Scan(&currentID, &rawName)
	if err != nil {
		writeError(w, http.StatusNotFound, "offer not found")
		return
	}

	if req.CanonicalID != nil {
		var canonName string
		err := h.db.QueryRow(ctx, `SELECT canonical_name FROM models_canonical WHERE id = $1`, *req.CanonicalID).Scan(&canonName)
		if err != nil {
			writeError(w, http.StatusBadRequest, "canonical model not found")
			return
		}
		h.db.Exec(ctx, `UPDATE model_offers SET canonical_id = $1 WHERE id = $2`, *req.CanonicalID, offerID)
		if req.StandardizedName == nil {
			h.db.Exec(ctx, `UPDATE model_offers SET standardized_name = $1 WHERE id = $2`, canonName, offerID)
		}
	}

	if req.StandardizedName != nil {
		h.db.Exec(ctx, `UPDATE model_offers SET standardized_name = $1 WHERE id = $2`, *req.StandardizedName, offerID)
	}

	// outbound_model_name is independent of canonical_id / standardized_name;
	// a model can have an endpoint ID without being mapped to a canonical row.
	// Use NULLIF('') so that an explicit empty string clears the column
	// (reverting to raw_model_name at query time).
	if req.OutboundModelName != nil {
		if _, err := h.db.Exec(ctx, `
			UPDATE model_offers
			SET outbound_model_name = NULLIF($1, ''),
			    updated_at = NOW()
			WHERE id = $2
		`, *req.OutboundModelName, offerID); err != nil {
			writeError(w, http.StatusInternalServerError, "update outbound_model_name failed: "+err.Error())
			return
		}
		slog.Info("model_offers.outbound_model_name updated",
			"offer_id", offerID,
			"raw_model_name", rawName,
			"provider_id", providerID,
			"new_value", *req.OutboundModelName,
		)
	}

	var result struct {
		ID               int     `json:"id"`
		RawModelName     string  `json:"raw_model_name"`
		StandardizedName *string `json:"standardized_name"`
		CanonicalID      *int    `json:"canonical_id"`
		CanonicalName    *string `json:"canonical_name"`
		OutboundModelName *string `json:"outbound_model_name"`
	}
	h.db.QueryRow(ctx, `
		SELECT mo.id, mo.raw_model_name, mo.standardized_name, mo.canonical_id,
		       mc.canonical_name, mo.outbound_model_name
		FROM model_offers mo
		LEFT JOIN models_canonical mc ON mc.id = mo.canonical_id
		WHERE mo.id = $1
	`, offerID).Scan(&result.ID, &result.RawModelName, &result.StandardizedName,
		&result.CanonicalID, &result.CanonicalName, &result.OutboundModelName)

	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) getModelOfferSuggestions(w http.ResponseWriter, r *http.Request, providerID, offerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var rawName string
	err := h.db.QueryRow(ctx, `
		SELECT mo.raw_model_name FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		WHERE mo.id = $1 AND c.provider_id = $2
	`, offerID, providerID).Scan(&rawName)
	if err != nil {
		writeError(w, http.StatusNotFound, "offer not found")
		return
	}

	rows, err := h.db.Query(ctx, `
		SELECT id, canonical_name, COALESCE(display_name,''), COALESCE(family,'')
		FROM models_canonical WHERE status = 'active' ORDER BY canonical_name
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type canonicalOption struct {
		ID            int    `json:"id"`
		CanonicalName string `json:"canonical_name"`
		DisplayName   string `json:"display_name"`
		Family        string `json:"family"`
	}
	options := make([]canonicalOption, 0)
	for rows.Next() {
		var o canonicalOption
		if err := rows.Scan(&o.ID, &o.CanonicalName, &o.DisplayName, &o.Family); err != nil {
			continue
		}
		options = append(options, o)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"offer_id":          offerID,
		"raw_model_name":    rawName,
		"rule_based":        rawName,
		"canonical_options": options,
	})
}

func (h *Handler) toggleModelOfferState(w http.ResponseWriter, r *http.Request, providerID, offerID int) {
	if r.Method != http.MethodPatch {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		Available bool `json:"available"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var credID int
	err := h.db.QueryRow(ctx, `
		SELECT mo.credential_id FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		WHERE mo.id = $1 AND c.provider_id = $2
	`, offerID, providerID).Scan(&credID)
	if err != nil {
		writeError(w, http.StatusNotFound, "offer not found")
		return
	}

	if req.Available {
		// Admin re-enable: clear unavailable_reason (so v_routable views the
		// binding as routable) and set admin_protected=TRUE so
		// expireStaleModels skips this binding on subsequent discovery runs.
		// The admin_protected column (added by 905) is the authoritative
		// protection flag — unavailable_reason is kept semantically clean.
		_, err = h.db.Exec(ctx, `
			UPDATE model_offers SET available = TRUE,
			unavailable_reason = NULL, unavailable_at = NULL,
			admin_protected = TRUE
			WHERE id = $1
		`, offerID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "update failed")
			return
		}
	} else {
		// Admin disable: set reason='manual' and clear protection.
		h.db.Exec(ctx, `
			UPDATE model_offers SET available = FALSE,
			unavailable_reason = 'manual', unavailable_at = now(),
			admin_protected = FALSE
			WHERE id = $1
		`, offerID)
	}

	h.db.Exec(ctx, `
		UPDATE credentials SET availability_state = 'ready',
		availability_recover_at = NULL, state_reason_code = NULL,
		state_reason_detail = NULL, state_updated_at = now()
		WHERE id = $1 AND lifecycle_status = 'active'
		AND availability_state IN ('cooling', 'rate_limited', 'unreachable')
	`, credID)

	writeJSON(w, http.StatusOK, map[string]any{
		"message":   "offer state updated",
		"offer_id":  offerID,
		"available": req.Available,
	})
}

func (h *Handler) handleForceRecover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	path := r.URL.Path
	stripPrefix := "/api/providers/credentials/"
	remaining := path[len(stripPrefix):]

	parts := splitPath(remaining)
	if len(parts) < 2 || parts[1] != "force-recover" {
		http.NotFound(w, r)
		return
	}

	credID, err := strconv.Atoi(parts[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid credential id")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tag, err := h.db.Exec(ctx, `
		UPDATE credentials
		SET availability_recover_at = now() - INTERVAL '1 second'
		WHERE id = $1 AND lifecycle_status = 'active'
	`, credID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "credential not found or not active")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"triggered":     true,
		"credential_id": credID,
	})
}

// =============================================================================
// 900-series endpoints: Manual disable + default probe model
// Spec: docs/superpowers/specs/2026-06-12-credential-availability-audit-design.md
// =============================================================================

// setProviderManualDisabled toggles providers.manual_disabled
// Audit: writes a synthetic model_offer_events row with raw_model_name=NULL
// (provider-level disable does not target a specific model)
func (h *Handler) setProviderManualDisabled(w http.ResponseWriter, r *http.Request, providerID int) {
	var req struct {
		ManualDisabled bool   `json:"manual_disabled"`
		Reason         string `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	actor := r.Header.Get("X-Admin-User")
	if actor == "" {
		actor = "admin"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tag, err := h.db.Exec(ctx, `
		UPDATE providers
		SET manual_disabled = $1, updated_at = now()
		WHERE id = $2 AND tenant_id = 'default'
	`, req.ManualDisabled, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}

	action := "disable"
	if !req.ManualDisabled {
		action = "enable"
	}
	reasonCode := "provider_manual_disabled"
	if !req.ManualDisabled {
		reasonCode = "provider_manual_enabled"
	}
	actorCopy := actor
	reqCopy := req
	h.db.Exec(ctx, `
		INSERT INTO model_offer_events
		    (source, action, credential_id, provider_id, raw_model_name, reason_code, reason_detail)
		VALUES ('admin', $1, 0, $2, '', $3, $4)
	`, action, providerID, reasonCode, actorCopy+": "+reqCopy.Reason)

	writeJSON(w, http.StatusOK, map[string]any{
		"message":         "updated",
		"manual_disabled": req.ManualDisabled,
		"actor":           actor,
	})
}

// setCredentialManualDisabled toggles credentials.manual_disabled
// Audit: writes a model_offer_events row per affected binding (raw_model_name='' is
// treated as "applies to all models under this credential")
func (h *Handler) setCredentialManualDisabled(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	var req struct {
		ManualDisabled bool   `json:"manual_disabled"`
		Reason         string `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	actor := r.Header.Get("X-Admin-User")
	if actor == "" {
		actor = "admin"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tag, err := h.db.Exec(ctx, `
		UPDATE credentials
		SET manual_disabled = $1, updated_at = now()
		WHERE id = $2 AND provider_id = $3
	`, req.ManualDisabled, credID, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}

	action := "disable"
	reasonCode := "credential_manual_disabled"
	if !req.ManualDisabled {
		action = "enable"
		reasonCode = "credential_manual_enabled"
	}
	actorCopy := actor
	reqCopy := req
	h.db.Exec(ctx, `
		INSERT INTO model_offer_events
		    (source, action, credential_id, provider_id, raw_model_name, reason_code, reason_detail)
		VALUES ('admin', $1, $2, $3, '', $4, $5)
	`, action, credID, providerID, reasonCode, actorCopy+": "+reqCopy.Reason)

	writeJSON(w, http.StatusOK, map[string]any{
		"message":         "updated",
		"manual_disabled": req.ManualDisabled,
		"actor":           actor,
	})
}

// setDefaultProbeModel manually pins a credential's probe model
// Audit: writes both model_offer_events and credential_probe_model_log
func (h *Handler) setDefaultProbeModel(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	var req struct {
		Model  *string `json:"model"`
		Reason string  `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	actor := r.Header.Get("X-Admin-User")
	if actor == "" {
		actor = "admin"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var oldModel string
	if err := h.db.QueryRow(ctx,
		`SELECT COALESCE(default_probe_model, '') FROM credentials WHERE id = $1 AND provider_id = $2`,
		credID, providerID,
	).Scan(&oldModel); err != nil {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}

	var newModel string
	var source string
	if req.Model != nil {
		newModel = *req.Model
		source = "manual"
	} else {
		source = "cleared"
	}

	if _, err := h.db.Exec(ctx, `
		UPDATE credentials
		SET default_probe_model = $1, default_probe_model_source = $2, default_probe_model_picked_at = now()
		WHERE id = $3 AND provider_id = $4
	`, newModel, source, credID, providerID); err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}

	actorCopy := actor
	reqCopy := req
	h.db.Exec(ctx, `
		INSERT INTO credential_probe_model_log
		    (tenant_id, credential_id, source, old_model, new_model, actor, reason)
		VALUES ('default', $1, $2, $3, $4, $5, $6)
	`, credID, source, oldModel, newModel, actorCopy, reqCopy.Reason)

	writeJSON(w, http.StatusOK, map[string]any{
		"message":   "updated",
		"old_model": oldModel,
		"new_model": newModel,
		"source":    source,
		"actor":     actor,
	})
}

// pickDefaultProbeModel triggers an immediate auto-pick (skips daily cron)
// Looks up via request_logs (7d most-used client_model) and falls back to
// domestic provider random pick. Manual source is preserved.
func (h *Handler) pickDefaultProbeModel(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	actor := r.Header.Get("X-Admin-User")
	if actor == "" {
		actor = "admin"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	result, err := bgPickProbeModel(ctx, h.db, credID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "pick failed: "+err.Error())
		return
	}

	if result.Model == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"message": "no candidate found",
			"model":   "",
			"source":  "",
		})
		return
	}

	var oldModel string
	if err := h.db.QueryRow(ctx,
		`SELECT COALESCE(default_probe_model, '') FROM credentials WHERE id = $1`,
		credID,
	).Scan(&oldModel); err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	if _, err := h.db.Exec(ctx, `
		UPDATE credentials
		SET default_probe_model = $1, default_probe_model_source = $2, default_probe_model_picked_at = now()
		WHERE id = $3
	`, result.Model, result.Source, credID); err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}

	actorCopy := actor
	h.db.Exec(ctx, `
		INSERT INTO credential_probe_model_log
		    (tenant_id, credential_id, source, old_model, new_model, actor, reason)
		VALUES ('default', $1, $2, $3, $4, $5, $6)
	`, credID, result.Source, oldModel, result.Model, actorCopy, "admin immediate pick")

	writeJSON(w, http.StatusOK, map[string]any{
		"message":   "picked",
		"model":     result.Model,
		"source":    result.Source,
		"old_model": oldModel,
	})
}

// getRoutableSummary returns aggregate counts of routable vs unavailable bindings
// for a provider, grouped by unavailable_reason. Backed by v_routable_credential_models.
func (h *Handler) getRoutableSummary(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT
		    COALESCE(unavailable_reason, 'routable') AS reason,
		    count(*) AS cnt
		FROM v_routable_credential_models
		WHERE provider_id = $1 AND tenant_id = 'default'
		GROUP BY unavailable_reason
		ORDER BY cnt DESC
	`, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	breakdown := make(map[string]int)
	var total, routable int
	for rows.Next() {
		var reason string
		var cnt int
		if err := rows.Scan(&reason, &cnt); err != nil {
			continue
		}
		breakdown[reason] = cnt
		total += cnt
		if reason == "routable" {
			routable = cnt
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"provider_id":            providerID,
		"total_bindings":         total,
		"routable_bindings":      routable,
		"unavailable_bindings":   total - routable,
		"unavailable_breakdown":  breakdown,
		"routable_ratio":         float64(routable) / float64(maxInt(total, 1)),
	})
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// =============================================================================
// Probe model picker helpers (shared with bg package)
// =============================================================================

// pickProbeResult is the shared return type for picker functions.
type pickProbeResult struct {
	Model  string
	Source string
}

// bgPickProbeModel is a thin adapter to the bg-package picker.
// Defined here to avoid an import cycle (admin does not import bg).
func bgPickProbeModel(ctx context.Context, db *pgxpool.Pool, credID int) (pickProbeResult, error) {
	return pickProbeModelForCredentialAdapter(ctx, db, credID)
}

// ── Per-provider model list refresh (force fetch from vendor API) ───────
//
// POST /api/providers/{id}/refresh-models
//   202 Accepted    : { "accepted": true, "reason": "started", "run": {...} }
//   404             : provider not found
//   503             : discovery service not available
//
// GET /api/providers/{id}/refresh-models
//   200             : { "running": {...} | null, "latest": {...} | null }
//
// The endpoint differs from the global /api/models/discover in two ways:
//   1. scope is restricted to a single provider (and its credentials)
//   2. it tracks per-provider status so the UI can show a live progress
//      message ("正在从供应商读取数据…") and re-fetch offers on success.
type providerRefreshStatus string

const (
	providerRefreshIdle    providerRefreshStatus = "idle"
	providerRefreshRunning providerRefreshStatus = "running"
	providerRefreshSucceed providerRefreshStatus = "succeeded"
	providerRefreshFailed  providerRefreshStatus = "failed"
)

type providerRefreshRun struct {
	RunID             string                `json:"run_id"`
	ProviderID        int                   `json:"provider_id"`
	Status            providerRefreshStatus `json:"status"`
	StartedAt         time.Time             `json:"started_at"`
	FinishedAt        *time.Time            `json:"finished_at,omitempty"`
	HeartbeatAt       *time.Time            `json:"heartbeat_at,omitempty"`
	CredentialsScanned int                  `json:"credentials_scanned"`
	ModelsUpserted     int                  `json:"models_upserted"`
	CredentialsFailed  int                  `json:"credentials_failed"`
	Errors             []string             `json:"errors,omitempty"`
	Message            string               `json:"message,omitempty"`
}

type providerRefreshState struct {
	mu     sync.Mutex
	latest map[int]*providerRefreshRun
}

// getProviderRefreshState lazily initialises the in-memory state tracker
// the first time a refresh endpoint touches it.
func (h *Handler) getProviderRefreshState() *providerRefreshState {
	h.refreshMu.Lock()
	defer h.refreshMu.Unlock()
	if h.refreshState == nil {
		h.refreshState = &providerRefreshState{latest: make(map[int]*providerRefreshRun)}
	}
	return h.refreshState
}

func (h *Handler) recordProviderRefresh(providerID int, run *providerRefreshRun) {
	st := h.getProviderRefreshState()
	st.mu.Lock()
	defer st.mu.Unlock()
	st.latest[providerID] = run
}

func (h *Handler) getProviderRefresh(providerID int) *providerRefreshRun {
	st := h.getProviderRefreshState()
	st.mu.Lock()
	defer st.mu.Unlock()
	if run, ok := st.latest[providerID]; ok {
		copy := *run
		return &copy
	}
	return nil
}

func (h *Handler) startRefreshProviderModels(w http.ResponseWriter, r *http.Request, providerID int) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var (
		providerCode    string
		providerName    string
		providerEnabled bool
	)
	err := h.db.QueryRow(ctx, `
		SELECT COALESCE(code,''), COALESCE(display_name,''), enabled
		FROM providers WHERE id = $1 AND tenant_id = 'default'
	`, providerID).Scan(&providerCode, &providerName, &providerEnabled)
	if err != nil {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	if !providerEnabled {
		writeError(w, http.StatusConflict, "provider is disabled")
		return
	}

	runID := fmt.Sprintf("provider-refresh-%d-%d", providerID, time.Now().UnixNano())
	now := time.Now()
	run := &providerRefreshRun{
		RunID:       runID,
		ProviderID:  providerID,
		Status:      providerRefreshRunning,
		StartedAt:   now,
		HeartbeatAt: &now,
		Message:     "从供应商接口读取模型列表",
	}
	h.recordProviderRefresh(providerID, run)

	slog.Info("provider refresh started",
		"run_id", runID,
		"provider_id", providerID,
		"provider_code", providerCode,
		"provider_name", providerName,
	)

	// Run in background so the POST returns 202 immediately.  We use a
	// detached context so a client disconnect does not abort the
	// upstream calls mid-flight.
	go func() {
		bgCtx, bgCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer bgCancel()

		creds, _ := h.fetchActiveCredentialsForProvider(bgCtx, providerID)

		var (
			totalUpserted int
			totalFailed   int
			errs          []string
		)
		for _, cred := range creds {
			heartbeat := time.Now()
			run.HeartbeatAt = &heartbeat
			h.recordProviderRefresh(providerID, run)

			upserted, failed, err := h.discoverAndUpsertForCredential(bgCtx, cred)
			if err != nil {
				totalFailed++
				errs = append(errs, fmt.Sprintf("credential #%d %s: %s", cred.id, cred.label, err.Error()))
				slog.Warn("provider refresh: credential failed",
					"run_id", runID,
					"provider_id", providerID,
					"credential_id", cred.id,
					"error", err,
				)
				continue
			}
			totalUpserted += upserted
			totalFailed += failed
		}

		finishedAt := time.Now()
		run.FinishedAt = &finishedAt
		run.CredentialsScanned = len(creds)
		run.ModelsUpserted = totalUpserted
		run.CredentialsFailed = totalFailed
		run.Errors = errs
		if totalFailed > 0 && totalUpserted == 0 {
			run.Status = providerRefreshFailed
			run.Message = "刷新失败：所有凭据都返回错误"
		} else {
			run.Status = providerRefreshSucceed
			run.Message = fmt.Sprintf("新增/更新 %d 个模型（凭据 %d 个，失败 %d 个）",
				totalUpserted, len(creds), totalFailed)
		}
		h.recordProviderRefresh(providerID, run)

		slog.Info("provider refresh finished",
			"run_id", runID,
			"provider_id", providerID,
			"credentials", len(creds),
			"upserted", totalUpserted,
			"failed", totalFailed,
		)
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"accepted": true,
		"reason":   "started",
		"run":      run,
	})
}

func (h *Handler) getRefreshProviderModelsStatus(w http.ResponseWriter, r *http.Request, providerID int) {
	run := h.getProviderRefresh(providerID)
	resp := map[string]any{
		"running": nil,
		"latest":  nil,
	}
	if run != nil {
		if run.Status == providerRefreshRunning {
			resp["running"] = run
		} else {
			resp["latest"] = run
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// credentialRowLite is a slim credential view used only by the per-provider
// refresh path; we keep the columns tight so the background goroutine does
// not pull fields it never reads.
type credentialRowLite struct {
	id                 int
	label              string
	providerID         int
	providerName       string
	baseURL            string
	protocol           string
	catalogCode        string
	secretCipher       []byte
	modelsEndpointTpl  *string
	discoveryStrategy  string
	modelsManifestJSON *string
}

func (h *Handler) fetchActiveCredentialsForProvider(ctx context.Context, providerID int) ([]credentialRowLite, error) {
	rows, err := h.db.Query(ctx, `
		SELECT
			c.id, COALESCE(c.label,''), p.id, p.display_name,
			COALESCE(p.base_url,''), COALESCE(p.protocol,''),
			COALESCE(p.catalog_code, ''),
			c.secret_ciphertext,
			pc.models_endpoint_template,
			COALESCE(pc.discovery_strategy, 'auto'),
			pc.models_manifest_json
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN provider_catalog pc ON pc.code = COALESCE(NULLIF(p.catalog_code, ''), p.code)
		WHERE c.provider_id = $1
		  AND c.status = 'active'
		  AND COALESCE(c.lifecycle_status, 'active') NOT IN ('suspended', 'retired', 'disabled')
		  AND COALESCE(c.availability_state, 'ready') = 'ready'
		  AND (c.quota_state IS NULL OR c.quota_state NOT IN ('permanently_exhausted', 'balance_exhausted'))
		  AND p.enabled = TRUE
		ORDER BY c.id
	`, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []credentialRowLite
	for rows.Next() {
		var c credentialRowLite
		if err := rows.Scan(&c.id, &c.label, &c.providerID, &c.providerName,
			&c.baseURL, &c.protocol, &c.catalogCode,
			&c.secretCipher, &c.modelsEndpointTpl, &c.discoveryStrategy, &c.modelsManifestJSON); err != nil {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func (h *Handler) loadCredentialRowLite(ctx context.Context, providerID, credID int) (credentialRowLite, error) {
	var c credentialRowLite
	err := h.db.QueryRow(ctx, `
		SELECT
			c.id, COALESCE(c.label,''), p.id, p.display_name,
			COALESCE(p.base_url,''), COALESCE(p.protocol,''),
			COALESCE(p.catalog_code, ''),
			c.secret_ciphertext,
			pc.models_endpoint_template,
			COALESCE(pc.discovery_strategy, 'auto'),
			pc.models_manifest_json
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN provider_catalog pc ON pc.code = COALESCE(NULLIF(p.catalog_code, ''), p.code)
		WHERE c.id = $1 AND c.provider_id = $2
	`, credID, providerID).Scan(&c.id, &c.label, &c.providerID, &c.providerName,
		&c.baseURL, &c.protocol, &c.catalogCode,
		&c.secretCipher, &c.modelsEndpointTpl, &c.discoveryStrategy, &c.modelsManifestJSON)
	return c, err
}

// loadCredentialRowLiteAny loads a credential by id alone (no provider
// scoping); used only by diagnostic probes that already know the cred id.
func (h *Handler) loadCredentialRowLiteAny(ctx context.Context, credID int) (credentialRowLite, error) {
	var c credentialRowLite
	err := h.db.QueryRow(ctx, `
		SELECT
			c.id, COALESCE(c.label,''), p.id, p.display_name,
			COALESCE(p.base_url,''), COALESCE(p.protocol,''),
			COALESCE(p.catalog_code, ''),
			c.secret_ciphertext,
			pc.models_endpoint_template,
			COALESCE(pc.discovery_strategy, 'auto'),
			pc.models_manifest_json
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN provider_catalog pc ON pc.code = COALESCE(NULLIF(p.catalog_code, ''), p.code)
		WHERE c.id = $1
	`, credID).Scan(&c.id, &c.label, &c.providerID, &c.providerName,
		&c.baseURL, &c.protocol, &c.catalogCode,
		&c.secretCipher, &c.modelsEndpointTpl, &c.discoveryStrategy, &c.modelsManifestJSON)
	return c, err
}

// resolveModelsEndpointURL maps catalog template + base_url to the models list URL.
func resolveModelsEndpointURL(baseURL string, template *string) (url string, explicitTemplate bool) {
	if template == nil {
		return "", false
	}
	tpl := strings.TrimSpace(*template)
	if tpl == "" {
		return "", true
	}
	explicitTemplate = true
	if strings.HasPrefix(tpl, "http://") || strings.HasPrefix(tpl, "https://") {
		return tpl, true
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return base + tpl, true
}

// resolveModelsForCredential loads model IDs from vendor API and/or manifest.
// forceAPI=true (manual refresh / health probe): always try vendor API first
// even when discovery_strategy is "manifest"; manifest is fallback only.
// forceAPI=false (scheduled discovery): manifest_only and empty template skip API.
func (h *Handler) resolveModelsForCredential(ctx context.Context, cred credentialRowLite, apiKey string, forceAPI bool) (models []string, source string, err error) {
	desc := providercap.Resolve(cred.protocol, cred.catalogCode)
	if !desc.SupportsModelsEndpoint {
		// Anthropic-compatible upstreams (e.g. minimax /anthropic) have no /models
		// listing; use catalog manifest when operator forces a refresh or probe.
		models, mErr := extractManifestModels(cred.modelsManifestJSON)
		if mErr != nil || len(models) == 0 {
			return nil, "none", fmt.Errorf("provider has no /models endpoint and manifest is empty")
		}
		return models, "manifest_only", nil
	}

	modelsURL, explicitTemplate := resolveModelsEndpointURL(cred.baseURL, cred.modelsEndpointTpl)
	skipAPI := !forceAPI && (cred.discoveryStrategy == "manifest_only" || (explicitTemplate && modelsURL == ""))
	if skipAPI {
		models, err = extractManifestModels(cred.modelsManifestJSON)
		if err != nil || len(models) == 0 {
			return nil, "manifest_only", err
		}
		return models, "manifest_only", nil
	}

	var fetchErr error
	if explicitTemplate && modelsURL != "" {
		models, fetchErr = h.fetchVendorModelsFromURLs(ctx, []string{modelsURL}, cred, apiKey)
	} else {
		models, fetchErr = h.fetchVendorModelsFromURLs(ctx, modelsURLCandidatesForCred(cred.baseURL, cred.modelsEndpointTpl, desc), cred, apiKey)
	}
	if fetchErr == nil && len(models) > 0 {
		// Merge in catalog-manifest models that the live /models list omits.
		// Some vendors publish a model (e.g. zhipu glm-5.2) before their
		// /models endpoint lists it; the model is callable but invisible to
		// discovery. Catalogs register these "known but unlisted" models so a
		// refresh still surfaces them without dropping the live list.
		manifestModels, _ := extractManifestModels(cred.modelsManifestJSON)
		if len(manifestModels) > 0 {
			models = mergeModelIDs(models, manifestModels)
			return models, "api+manifest", nil
		}
		return models, "api", nil
	}

	// Fallback to manifest when API fails or returns empty.
	fallback, _ := extractManifestModels(cred.modelsManifestJSON)
	if len(fallback) > 0 {
		return fallback, "manifest", nil
	}
	if fetchErr != nil {
		return nil, "api", fetchErr
	}
	return nil, "api", fmt.Errorf("no models found from vendor API or manifest")
}

// mergeModelIDs appends manifest entries that are not already present in the
// live list, preserving order and de-duplicating case-insensitively on the
// normalized model id. Returns the live list unchanged when manifest is empty.
func mergeModelIDs(live, manifest []string) []string {
	if len(manifest) == 0 {
		return live
	}
	seen := make(map[string]bool, len(live))
	for _, m := range live {
		seen[strings.ToLower(strings.TrimSpace(m))] = true
	}
	for _, m := range manifest {
		key := strings.ToLower(strings.TrimSpace(m))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		live = append(live, m)
	}
	return live
}

// CredentialModelFetchResult is one row from VerifyAllCredentialModelFetches.
type CredentialModelFetchResult struct {
	ProviderID   int    `json:"provider_id"`
	ProviderCode string `json:"provider_code"`
	ProviderName string `json:"provider_name"`
	CredentialID int    `json:"credential_id"`
	Label        string `json:"label"`
	BaseURL      string `json:"base_url"`
	CatalogCode  string `json:"catalog_code"`
	Template     string `json:"models_endpoint_template"`
	Strategy     string `json:"discovery_strategy"`
	ResolvedURL  string `json:"resolved_url"`
	Source       string `json:"source"`
	ModelCount   int    `json:"model_count"`
	SampleModels []string `json:"sample_models"`
	OK           bool   `json:"ok"`
	Error        string `json:"error,omitempty"`
}

// VerifyAllCredentialModelFetches probes every active credential using the
// same resolveModelsForCredential path as manual refresh (forceAPI=true).
func (h *Handler) VerifyAllCredentialModelFetches(ctx context.Context, providerIDFilter int) ([]CredentialModelFetchResult, error) {
	query := `
		SELECT
			p.id, COALESCE(p.code,''), COALESCE(p.display_name,''),
			c.id, COALESCE(c.label,''),
			COALESCE(p.base_url,''), COALESCE(p.protocol,''),
			COALESCE(p.catalog_code, ''),
			c.secret_ciphertext,
			COALESCE(pc.models_endpoint_template, ''),
			COALESCE(pc.discovery_strategy, 'auto'),
			pc.models_manifest_json
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN provider_catalog pc ON pc.code = COALESCE(NULLIF(p.catalog_code, ''), p.code)
		WHERE c.status = 'active'
		  AND COALESCE(c.lifecycle_status, 'active') NOT IN ('suspended', 'retired', 'disabled')
		  AND p.enabled = TRUE
		  AND p.tenant_id = 'default'
	`
	args := []any{}
	if providerIDFilter > 0 {
		query += ` AND p.id = $1`
		args = append(args, providerIDFilter)
	}
	query += ` ORDER BY p.id, c.id`

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CredentialModelFetchResult
	for rows.Next() {
		var (
			providerID, credID                                    int
			providerCode, providerName, label, baseURL, protocol string
			catalogCode, template, strategy                       string
			secretCipher                                          []byte
			manifestJSON                                          *string
		)
		if err := rows.Scan(&providerID, &providerCode, &providerName,
			&credID, &label, &baseURL, &protocol, &catalogCode, &secretCipher,
			&template, &strategy, &manifestJSON); err != nil {
			continue
		}

		res := CredentialModelFetchResult{
			ProviderID:   providerID,
			ProviderCode: providerCode,
			ProviderName: providerName,
			CredentialID: credID,
			Label:        label,
			BaseURL:      baseURL,
			CatalogCode:  catalogCode,
			Template:     template,
			Strategy:     strategy,
		}

		var tplPtr *string
		if template != "" {
			tplPtr = &template
		}
		if u, explicit := resolveModelsEndpointURL(baseURL, tplPtr); explicit && u != "" {
			res.ResolvedURL = u
		} else if u := modelsURLCandidatesForCred(baseURL, tplPtr, providercap.Resolve(protocol, catalogCode)); len(u) > 0 {
			res.ResolvedURL = strings.Join(u, " | ")
		}

		apiKey, decErr := h.decryptCredStr(string(secretCipher))
		if decErr != nil {
			res.Error = "decrypt: " + decErr.Error()
			out = append(out, res)
			continue
		}

		cred := credentialRowLite{
			id: credID, label: label, providerID: providerID, providerName: providerName,
			baseURL: baseURL, protocol: protocol, catalogCode: catalogCode,
			secretCipher: secretCipher, discoveryStrategy: strategy,
			modelsManifestJSON: manifestJSON,
		}
		if tplPtr != nil {
			cred.modelsEndpointTpl = tplPtr
		}

		models, source, fetchErr := h.resolveModelsForCredential(ctx, cred, apiKey, true)
		res.Source = source
		res.ModelCount = len(models)
		if fetchErr != nil {
			res.Error = fetchErr.Error()
		} else if len(models) == 0 {
			res.Error = "no models returned"
		} else {
			res.OK = source == "api" || source == "api+manifest" || source == "manifest_only"
			limit := 3
			if len(models) < limit {
				limit = len(models)
			}
			res.SampleModels = models[:limit]
			if source == "manifest" {
				res.Error = "vendor API unavailable; manifest fallback only"
			}
		}
		out = append(out, res)
	}
	return out, rows.Err()
}

// CredentialModelUpsertResult reports the outcome of discoverAndUpsertForCredential
// (same code path as POST /api/providers/{id}/refresh-models).
type CredentialModelUpsertResult struct {
	ProviderID   int    `json:"provider_id"`
	ProviderCode string `json:"provider_code"`
	CredentialID int    `json:"credential_id"`
	Label        string `json:"label"`
	Upserted     int    `json:"upserted"`
	Failed       int    `json:"failed"`
	OK           bool   `json:"ok"`
	Error        string `json:"error,omitempty"`
}

// VerifyAllCredentialModelUpserts runs the full refresh upsert path for each
// active credential (without clearing existing bindings first).
func (h *Handler) VerifyAllCredentialModelUpserts(ctx context.Context, providerIDFilter int) ([]CredentialModelUpsertResult, error) {
	fetches, err := h.VerifyAllCredentialModelFetches(ctx, providerIDFilter)
	if err != nil {
		return nil, err
	}
	var out []CredentialModelUpsertResult
	for _, f := range fetches {
		res := CredentialModelUpsertResult{
			ProviderID:   f.ProviderID,
			ProviderCode: f.ProviderCode,
			CredentialID: f.CredentialID,
			Label:        f.Label,
		}
		if strings.HasPrefix(f.Error, "decrypt:") {
			res.Error = f.Error
			out = append(out, res)
			continue
		}
		cred, lerr := h.loadCredentialRowLite(ctx, f.ProviderID, f.CredentialID)
		if lerr != nil {
			res.Error = "load credential: " + lerr.Error()
			out = append(out, res)
			continue
		}
		upserted, failed, uerr := h.discoverAndUpsertForCredential(ctx, cred)
		res.Upserted = upserted
		res.Failed = failed
		if uerr != nil {
			res.Error = uerr.Error()
		} else {
			res.OK = true
		}
		out = append(out, res)
	}
	return out, nil
}

// discoverAndUpsertForCredential replicates the vendor-API fetch +
// upsert logic from discovery.discoverForCredential but stays in the
// admin package so it can write to a per-provider status record and
// remain decoupled from the global discovery Service.  Behavior must
// match discovery.discoverForCredential: existing rows are kept, new
// model names are inserted, and the credential health is updated.
// Manual refresh always forceAPI so catalog "manifest" strategy still
// hits the live /models endpoint. Duplicate upserts preserve manual
// disable state via modelcatalog.UpsertCredentialModel.
func (h *Handler) discoverAndUpsertForCredential(ctx context.Context, cred credentialRowLite) (upserted int, failed int, err error) {
	apiKey, decErr := h.decryptCredStr(string(cred.secretCipher))
	if decErr != nil {
		return 0, 0, fmt.Errorf("decrypt credential: %w", decErr)
	}

	models, source, fErr := h.resolveModelsForCredential(ctx, cred, apiKey, true)
	if len(models) == 0 {
		msg := "no models returned"
		if fErr != nil {
			msg = fErr.Error()
		} else {
			msg = fmt.Sprintf("no models returned (source=%s)", source)
		}
		h.updateCredHealth(ctx, cred.id, "unreachable", msg)
		return 0, 0, fmt.Errorf("%s", msg)
	}
	// Manual refresh requires a live vendor API list; manifest-only fallback
	// is useful for health diagnostics but must not masquerade as a refresh.
	// Exception: anthropic-messages suppliers never expose /models — manifest
	// is the authoritative source for those bindings.
	// "api+manifest" is a successful live fetch with extra known-but-unlisted
	// models merged in, so it counts as a real refresh.
	if source != "api" && source != "api+manifest" && source != "manifest_only" {
		h.updateCredHealth(ctx, cred.id, "unreachable", "vendor API failed; manifest fallback only")
		return 0, 0, fmt.Errorf("vendor API failed; only manifest fallback available (%d models)", len(models))
	}

	for _, m := range models {
		stdName := modelname.StandardizeName(m)
		if stdName != "" {
			h.db.Exec(ctx, `
				INSERT INTO models_canonical (canonical_name, family, source, status)
				VALUES ($1, 'unknown', 'provider_refresh', 'active')
				ON CONFLICT (canonical_name) DO NOTHING
			`, stdName)
		}
		if uErr := h.upsertModelForProvider(ctx, cred.id, m); uErr != nil {
			failed++
			continue
		}
		upserted++
	}
	h.updateCredHealth(ctx, cred.id, "healthy", "")
	return upserted, failed, nil
}

func modelsURLCandidatesForBase(baseURL string) []string {
	return modelsURLCandidatesForCred(baseURL, nil, providercap.Resolve("", ""))
}

func modelsURLCandidatesForCred(baseURL string, template *string, desc providercap.Descriptor) []string {
	return providercap.ModelsURLCandidates(baseURL, template, desc)
}

func setModelsAuthHeaders(req *http.Request, protocol, apiKey string) {
	providercap.ApplyAuthHeaders(req, providercap.Resolve(protocol, ""), apiKey)
}

func (h *Handler) applyCatalogHeaderProfile(ctx context.Context, req *http.Request, catalogCode string) {
	if h.db == nil || strings.TrimSpace(catalogCode) == "" {
		return
	}
	var headersJSON []byte
	err := h.db.QueryRow(ctx, `
		SELECT php.headers_json
		FROM provider_catalog pc
		JOIN provider_header_profiles php ON php.profile_code = pc.header_profile_code
		WHERE pc.code = $1
	`, catalogCode).Scan(&headersJSON)
	if err != nil || len(headersJSON) == 0 {
		return
	}
	var headers map[string]string
	if json.Unmarshal(headersJSON, &headers) != nil {
		return
	}
	for k, v := range headers {
		if strings.TrimSpace(v) != "" {
			req.Header.Set(k, v)
		}
	}
}

func (h *Handler) fetchVendorModels(ctx context.Context, url string, cred credentialRowLite, apiKey string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	setModelsAuthHeaders(req, cred.protocol, apiKey)
	h.applyCatalogHeaderProfile(ctx, req, cred.catalogCode)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("models endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	return parseVendorModelsBody(body)
}

// DebugFetchVendorModelsRaw is a diagnostic helper: it fetches the given URL
// with the credential's auth headers and returns the HTTP status, the raw
// response body, and the parsed model IDs. It exists purely to support
// the verify-model-fetch CLI probe and is not wired to any HTTP route.
func (h *Handler) DebugFetchVendorModelsRaw(ctx context.Context, credID int, url string) (status int, rawBody []byte, models []string, err error) {
	cred, err := h.loadCredentialRowLiteAny(ctx, credID)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("load credential: %w", err)
	}
	apiKey, decErr := h.decryptCredStr(string(cred.secretCipher))
	if decErr != nil {
		return 0, nil, nil, fmt.Errorf("decrypt credential: %w", decErr)
	}
	req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if rerr != nil {
		return 0, nil, nil, rerr
	}
	setModelsAuthHeaders(req, cred.protocol, apiKey)
	h.applyCatalogHeaderProfile(ctx, req, cred.catalogCode)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	status = resp.StatusCode
	rawBody, _ = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if status == http.StatusOK {
		models, err = parseVendorModelsBody(rawBody)
	}
	return status, rawBody, models, err
}

// DebugChatProbe issues a minimal chat completion against the credential's
// base URL for the given model name, returning the HTTP status and a short
// body preview. It is used to detect models that are callable but not listed
// by the vendor's /models endpoint.
func (h *Handler) DebugChatProbe(ctx context.Context, credID int, model string) (status int, body []byte, err error) {
	cred, err := h.loadCredentialRowLiteAny(ctx, credID)
	if err != nil {
		return 0, nil, fmt.Errorf("load credential: %w", err)
	}
	apiKey, decErr := h.decryptCredStr(string(cred.secretCipher))
	if decErr != nil {
		return 0, nil, fmt.Errorf("decrypt credential: %w", decErr)
	}
	base := strings.TrimRight(strings.TrimSpace(cred.baseURL), "/")
	payload := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}],"max_tokens":1,"stream":false}`)
	req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(payload))
	if rerr != nil {
		return 0, nil, rerr
	}
	setModelsAuthHeaders(req, cred.protocol, apiKey)
	h.applyCatalogHeaderProfile(ctx, req, cred.catalogCode)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	status = resp.StatusCode
	body, _ = io.ReadAll(io.LimitReader(resp.Body, 4096))
	return status, body, nil
}

// fetchVendorModelsFromURLs tries catalog-resolved candidate URLs in order;
// requires HTTP 200 with a parseable model list (used by refresh + health probe).
func (h *Handler) fetchVendorModelsFromURLs(ctx context.Context, urls []string, cred credentialRowLite, apiKey string) ([]string, error) {
	var lastErr error
	for _, u := range urls {
		models, err := h.fetchVendorModels(ctx, u, cred, apiKey)
		if err == nil && len(models) > 0 {
			return models, nil
		}
		if err != nil {
			lastErr = err
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no models found from any candidate URL")
}

func parseVendorModelsBody(data []byte) ([]string, error) {
	// Standard OpenAI format: {"data": [{"id": "..."}]}
	var openai struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &openai); err == nil && len(openai.Data) > 0 {
		var ids []string
		for _, m := range openai.Data {
			if m.ID != "" {
				ids = append(ids, m.ID)
			}
		}
		if len(ids) > 0 {
			return ids, nil
		}
	}

	// Alt format: {"models": [{"id": "..."}]}
	var alt struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &alt); err == nil && len(alt.Models) > 0 {
		var ids []string
		for _, m := range alt.Models {
			if m.ID != "" {
				ids = append(ids, m.ID)
			}
		}
		if len(ids) > 0 {
			return ids, nil
		}
	}

	// Bare array
	var bare []string
	if err := json.Unmarshal(data, &bare); err == nil && len(bare) > 0 {
		return bare, nil
	}

	// Array of objects
	var objArray []map[string]any
	if err := json.Unmarshal(data, &objArray); err == nil && len(objArray) > 0 {
		var ids []string
		for _, m := range objArray {
			if id, ok := m["id"].(string); ok && id != "" {
				ids = append(ids, id)
			} else if name, ok := m["name"].(string); ok && name != "" {
				ids = append(ids, name)
			} else if model, ok := m["model"].(string); ok && model != "" {
				ids = append(ids, model)
			}
		}
		if len(ids) > 0 {
			return ids, nil
		}
	}

	return nil, fmt.Errorf("unrecognized models response format")
}

func extractManifestModels(manifest *string) ([]string, error) {
	if manifest == nil || *manifest == "" {
		return nil, nil
	}
	data := []byte(*manifest)

	// Try {"models": [{"id": "..."}]}
	var wrap struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &wrap); err == nil && len(wrap.Models) > 0 {
		var ids []string
		for _, m := range wrap.Models {
			if m.ID != "" {
				ids = append(ids, m.ID)
			}
		}
		return ids, nil
	}

	// Try {"data": [{"id": "..."}]}
	var openai struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &openai); err == nil && len(openai.Data) > 0 {
		var ids []string
		for _, m := range openai.Data {
			if m.ID != "" {
				ids = append(ids, m.ID)
			}
		}
		return ids, nil
	}

	// Try bare array
	var bare []string
	if err := json.Unmarshal(data, &bare); err == nil && len(bare) > 0 {
		return bare, nil
	}

	// Try array of objects
	var objArray []map[string]any
	if err := json.Unmarshal(data, &objArray); err == nil && len(objArray) > 0 {
		var ids []string
		for _, m := range objArray {
			if id, ok := m["id"].(string); ok && id != "" {
				ids = append(ids, id)
			} else if name, ok := m["name"].(string); ok && name != "" {
				ids = append(ids, name)
			}
		}
		return ids, nil
	}
	return nil, nil
}

// upsertModelForProvider upserts one binding directly on base tables.
// Manual disables (reason LIKE 'manual%') are preserved; legacy soft-deletes
// and auto disables are re-enabled when the vendor still lists the model.
func (h *Handler) upsertModelForProvider(ctx context.Context, credentialID int, rawName string) error {
	return modelcatalog.UpsertCredentialModel(ctx, h.db, credentialID, rawName, modelname.StandardizeName(rawName), nil)
}

func (h *Handler) updateCredHealth(ctx context.Context, credentialID int, status, errMsg string) {
	_, err := h.db.Exec(ctx, `
		UPDATE credentials
		SET health_status = $1, health_error = $2, health_checked_at = NOW()
		WHERE id = $3
	`, status, errMsg, credentialID)
	if err != nil {
		slog.Debug("updateCredHealth failed", "credential_id", credentialID, "error", err)
	}
}
