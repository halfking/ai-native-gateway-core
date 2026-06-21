package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/providercap"
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

// ── Probe endpoints ──────────────────────────────────────────────────────────


// probeURL (POST /api/providers/probe-url) probes a base_url WITHOUT
// requiring an existing provider. Used by the "add provider" form:
// the user fills in base_url + optional api_key and clicks "探测".
//
// Body: { "base_url": "...", "api_key": "..." (optional) }
func (h *Handler) probeURL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key,omitempty"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.BaseURL == "" {
		writeError(w, http.StatusBadRequest, "base_url required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	result := h.doProbeURL(ctx, req.BaseURL, req.APIKey)
	writeJSON(w, http.StatusOK, result)
}

// probeProviderURL (POST /api/providers/{id}/probe-url) probes a
// provider's base_url using its existing credentials. Used by the
// "edit provider" form: user sees current base_url + protocol and
// clicks "探测".
func (h *Handler) probeProviderURL(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	var baseURL, protocol string
	err := h.db.QueryRow(ctx, `SELECT COALESCE(base_url,''), COALESCE(protocol,'openai-completions') FROM providers WHERE id = $1 AND tenant_id = 'default'`, providerID).Scan(&baseURL, &protocol)
	if err != nil {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	if baseURL == "" {
		writeJSON(w, http.StatusOK, probeURLResult{Reachable: false, Error: "provider has no base_url"})
		return
	}

	result := h.doProbeURL(ctx, baseURL, "")
	// If the provider has active credentials, try probing with each api_key
	// to get auth_ok status.
	var apiKey string
	rows, err := h.db.Query(ctx, `SELECT secret_ciphertext FROM credentials WHERE provider_id = $1 AND status = 'active' LIMIT 1`, providerID)
	if err == nil {
		defer rows.Close()
		if rows.Next() {
			var ciphertext []byte
			if rows.Scan(&ciphertext) == nil {
				if dec, decErr := h.decryptCredStr(string(ciphertext)); decErr == nil {
					apiKey = dec
				}
			}
		}
	}
	if apiKey != "" {
		result2 := h.doProbeURL(ctx, baseURL, apiKey)
		result.AuthOK = result2.AuthOK
		if result2.Reachable {
			result = result2
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// doProbeURL is the shared probe logic. It tries the URL with each
// known protocol (openai-completions first, then anthropic-messages)
// and returns which protocol worked.
func (h *Handler) doProbeURL(ctx context.Context, baseURL, apiKey string) probeURLResult {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	protocols := []string{"openai-completions", "anthropic-messages"}

	hasKey := strings.TrimSpace(apiKey) != ""

	for _, protocol := range protocols {
		desc := providercap.Resolve(protocol, "")
		probeURL := providercap.ProbeEndpointURL(baseURL, desc)
		if probeURL == "" {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, "GET", probeURL, nil)
		if err != nil {
			continue
		}
		providercap.ApplyAuthHeaders(req, desc, apiKey)
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{Timeout: 8 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()

		// Accept 200, 401, 403 (auth required but reachable)
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			result := probeURLResult{
				Reachable:  true,
				Protocol:   protocol,
				HTTPStatus: resp.StatusCode,
				AuthOK:     resp.StatusCode == http.StatusOK || !hasKey,
			}
			if hasKey && resp.StatusCode == http.StatusOK {
				var modelsData []map[string]any
				var mResp struct {
					Data   []map[string]any `json:"data"`
					Models []map[string]any `json:"models"`
				}
				if json.Unmarshal(body, &mResp) == nil {
					modelsData = mResp.Data
					if len(modelsData) == 0 {
						modelsData = mResp.Models
					}
				}
				result.ModelsCount = len(modelsData)
				limit := 3
				if len(modelsData) < limit {
					limit = len(modelsData)
				}
				for i := 0; i < limit; i++ {
					if id, ok := modelsData[i]["id"].(string); ok {
						result.SampleModels = append(result.SampleModels, id)
					} else if name, ok := modelsData[i]["name"].(string); ok {
						result.SampleModels = append(result.SampleModels, name)
					}
				}
				if result.SampleModels == nil {
					result.SampleModels = []string{}
				}
			}
			return result
		}
	}

	// None of the known protocols worked
	msg := "无法连接到该 base_url"
	if hasKey {
		// Try again without auth to distinguish "unreachable" from "auth failed"
		if result := h.doProbeURL(ctx, baseURL, ""); result.Reachable {
			return probeURLResult{Reachable: true, Protocol: result.Protocol, AuthOK: false, HTTPStatus: 401, Error: "API Key 无效或凭据错误"}
		}
	}
	return probeURLResult{Reachable: false, Error: msg}
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

	// ── Root-level probe endpoints (no provider ID) ──
	if remaining == "probe-url" {
		if r.Method == http.MethodPost {
			h.probeURL(w, r)
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
	case "probe-url":
		if r.Method == http.MethodPost {
			h.probeProviderURL(w, r, providerID)
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
	case "probe-history/trigger-all":
		h.handleProviderProbeHistoryTriggerAll(w, r, providerID)
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
	case "settings":
		if r.Method == http.MethodGet {
			h.getProviderSettings(w, r, providerID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "settings/":
		// Handle /api/providers/:id/settings/:key
		settingKey := strings.TrimPrefix(subPath, "settings/")
		if settingKey != "" {
			h.handleProviderSetting(w, r, providerID, settingKey)
		} else {
			http.NotFound(w, r)
		}
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
		if len(subPath) > 9 && subPath[:9] == "settings/" {
			settingKey := subPath[9:]
			h.handleProviderSetting(w, r, providerID, settingKey)
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
		QualityFixMode *string  `json:"quality_fix_mode"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Track whether base_url or protocol changed — if so, auto-reprobe all active credentials.
	needsReprobe := false

	if req.DisplayName != nil {
		h.db.Exec(ctx, `UPDATE providers SET display_name = $1, updated_at = now() WHERE id = $2`, *req.DisplayName, id)
	}
	if req.BaseURL != nil {
		h.db.Exec(ctx, `UPDATE providers SET base_url = $1, updated_at = now() WHERE id = $2`, *req.BaseURL, id)
		needsReprobe = true
	}
	if req.Protocol != nil {
		h.db.Exec(ctx, `UPDATE providers SET protocol = $1, updated_at = now() WHERE id = $2`, *req.Protocol, id)
		needsReprobe = true
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

	// ── Auto reprobe on base_url/protocol change ──
	if needsReprobe {
		ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel2()
		rows, err := h.db.Query(ctx2, `SELECT id FROM credentials WHERE provider_id = $1 AND status = 'active'`, id)
		if err == nil {
			var credIDs []int
			for rows.Next() {
				var cid int
				if rows.Scan(&cid) == nil {
					credIDs = append(credIDs, cid)
				}
			}
			rows.Close()
			for _, cid := range credIDs {
				cid := cid
				go func(pid, cid int) {
					taskID, taskErr := insertBackgroundTask(context.Background(), h.db, "health_check", &pid, &cid,
						map[string]any{"provider_id": pid, "credential_id": cid, "source": "auto_on_provider_update"})
					if taskErr != nil {
						slog.Warn("auto-reprobe: task insert failed", "provider_id", pid, "credential_id", cid, "error", taskErr)
						return
					}
					h.runHealthCheck(pid, cid, taskID)
				}(id, cid)
			}
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
		ON CONFLICT (tenant_id, code) DO NOTHING
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

		// 2026-06-19 audit: provider refresh re-writes the rows
		// /api/routing/available-models aggregates.  Invalidate the
		// process-wide cache so the next admin page render sees the
		// new model list.
		InvalidateAvailableModelsCache()

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

// getProviderSettings returns all provider-level setting overrides.
// GET /api/providers/:id/settings
func (h *Handler) getProviderSettings(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT setting_key, setting_value, enabled, created_by, created_at, updated_at
		FROM provider_settings
		WHERE provider_id = $1
		ORDER BY setting_key
	`, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database query failed")
		return
	}
	defer rows.Close()

	type settingRow struct {
		Key       string          `json:"key"`
		Value     json.RawMessage `json:"value"`
		Enabled   bool            `json:"enabled"`
		CreatedBy string          `json:"created_by"`
		CreatedAt time.Time       `json:"created_at"`
		UpdatedAt time.Time       `json:"updated_at"`
	}

	var settings []settingRow
	for rows.Next() {
		var s settingRow
		if err := rows.Scan(&s.Key, &s.Value, &s.Enabled, &s.CreatedBy, &s.CreatedAt, &s.UpdatedAt); err != nil {
			continue
		}
		settings = append(settings, s)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"provider_id": providerID,
		"settings":    settings,
	})
}

// handleProviderSetting handles CRUD operations on a specific setting key.
// GET    /api/providers/:id/settings/:key - get one setting
// PUT    /api/providers/:id/settings/:key - set/update setting
// DELETE /api/providers/:id/settings/:key - delete setting override
func (h *Handler) handleProviderSetting(w http.ResponseWriter, r *http.Request, providerID int, settingKey string) {
	switch r.Method {
	case http.MethodGet:
		h.getProviderSetting(w, r, providerID, settingKey)
	case http.MethodPut:
		h.setProviderSetting(w, r, providerID, settingKey)
	case http.MethodDelete:
		h.deleteProviderSetting(w, r, providerID, settingKey)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// getProviderSetting returns a specific setting override.
func (h *Handler) getProviderSetting(w http.ResponseWriter, r *http.Request, providerID int, settingKey string) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	var value json.RawMessage
	var enabled bool
	err := h.db.QueryRow(ctx, `
		SELECT setting_value, enabled
		FROM provider_settings
		WHERE provider_id = $1 AND setting_key = $2
	`, providerID, settingKey).Scan(&value, &enabled)

	if err != nil {
		writeError(w, http.StatusNotFound, "setting not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"key":     settingKey,
		"value":   value,
		"enabled": enabled,
	})
}

// setProviderSetting sets or updates a provider-level setting override.
func (h *Handler) setProviderSetting(w http.ResponseWriter, r *http.Request, providerID int, settingKey string) {
	// Validate settingKey is in allowed list
	allowedKeys := map[string]bool{
		"compression.mode":            true,
		"cache.enabled":               true,
		"format_conversion.enabled":   true,
	}

	if !allowedKeys[settingKey] {
		writeError(w, http.StatusBadRequest, "invalid setting key: "+settingKey)
		return
	}

	var body struct {
		Value   json.RawMessage `json:"value"`
		Enabled *bool           `json:"enabled,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(body.Value) == 0 {
		writeError(w, http.StatusBadRequest, "value is required")
		return
	}

	// Validate value format based on key
	if err := validateSettingValue(settingKey, body.Value); err != nil {
		writeError(w, http.StatusBadRequest, "invalid value: "+err.Error())
		return
	}

	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	user := r.Header.Get("X-User-ID")
	if user == "" {
		user = "admin"
	}

	_, err := h.db.Exec(ctx, `
		INSERT INTO provider_settings (provider_id, setting_key, setting_value, enabled, created_by)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (provider_id, setting_key)
		DO UPDATE SET
			setting_value = EXCLUDED.setting_value,
			enabled = EXCLUDED.enabled,
			updated_at = NOW()
	`, providerID, settingKey, body.Value, enabled, user)

	if err != nil {
		slog.Error("setProviderSetting failed", "error", err, "provider_id", providerID, "key", settingKey)
		writeError(w, http.StatusInternalServerError, "database update failed")
		return
	}

	// Clear cache for this provider
	if h.providerSettingsResolver != nil {
		h.providerSettingsResolver.ClearProviderCache(providerID)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"message": "setting updated",
		"key":     settingKey,
	})
}

// deleteProviderSetting removes a provider-level setting override.
func (h *Handler) deleteProviderSetting(w http.ResponseWriter, r *http.Request, providerID int, settingKey string) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	result, err := h.db.Exec(ctx, `
		DELETE FROM provider_settings
		WHERE provider_id = $1 AND setting_key = $2
	`, providerID, settingKey)

	if err != nil {
		writeError(w, http.StatusInternalServerError, "database delete failed")
		return
	}

	rowsAffected := result.RowsAffected()
	if rowsAffected == 0 {
		writeError(w, http.StatusNotFound, "setting not found")
		return
	}

	// Clear cache for this provider
	if h.providerSettingsResolver != nil {
		h.providerSettingsResolver.ClearProviderCache(providerID)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"message": "setting deleted",
		"key":     settingKey,
	})
}

// validateSettingValue validates the JSON value based on the setting key.
func validateSettingValue(key string, valueJSON json.RawMessage) error {
	switch key {
	case "compression.mode":
		var mode string
		if err := json.Unmarshal(valueJSON, &mode); err != nil {
			return fmt.Errorf("must be a string")
		}
		validModes := map[string]bool{"off": true, "auto_threshold": true, "on_4xx": true}
		if !validModes[mode] {
			return fmt.Errorf("must be one of: off, auto_threshold, on_4xx")
		}
	case "cache.enabled", "format_conversion.enabled":
		var enabled bool
		if err := json.Unmarshal(valueJSON, &enabled); err != nil {
			return fmt.Errorf("must be a boolean")
		}
	default:
		return fmt.Errorf("unknown setting key")
	}
	return nil
}
