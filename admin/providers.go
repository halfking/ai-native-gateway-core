package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/providercap"
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

		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `
			UPDATE credentials
			SET health_status = $1, health_error = $2, health_checked_at = NOW()
			WHERE id = $3
		`, healthStatus, errMsg, credID)

		results = append(results, credResult{CredentialID: credID, Label: label, Status: healthStatus, Error: errMsg})
		checked++
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"accepted": true,
		"reason":   "health check completed",
		"checked":  checked,
		"healthy":  healthy,
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
		//nolint:errcheck // best-effort close
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
		ID                   int        `json:"id"`
		TenantID             string     `json:"tenant_id"`
		Code                 string     `json:"code"`
		DisplayName          string     `json:"display_name"`
		CatalogCode          *string    `json:"catalog_code"`
		IsCustom             bool       `json:"is_custom"`
		Kind                 string     `json:"kind"`
		Category             string     `json:"category"`
		Protocol             string     `json:"protocol"`
		BaseURL              string     `json:"base_url"`
		EgressProfile        string     `json:"egress_profile"`
		Domestic             bool       `json:"domestic"`
		DiscountRate         float64    `json:"discount_rate"`
		Enabled              bool       `json:"enabled"`
		Notes                string     `json:"notes"`
		NetworkQualityScore  *string    `json:"network_quality_score"`
		OwnerUser            *string    `json:"owner_user"`
		CreatedAt            *time.Time `json:"created_at"`
		UpdatedAt            *time.Time `json:"updated_at"`
		VendorName           string     `json:"vendor_name"`
		HeaderProfileCode    string     `json:"header_profile_code"`
		ActiveCredCount      int        `json:"active_credential_count"`
		HealthyCredCount     int        `json:"healthy_credential_count"`
		WarningCredCount     int        `json:"warning_credential_count"`
		UnreachableCredCount int        `json:"unreachable_credential_count"`
		HealthCheckedAt      *time.Time `json:"health_checked_at"`
		FreeModelCount       int        `json:"free_model_count"`
		HealthStatus         string     `json:"health_status"`
		RoutableBindingCount int        `json:"routable_binding_count"`
		TotalBindingCount    int        `json:"total_binding_count"`
		QualityFixMode       string     `json:"quality_fix_mode"`
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
		ID                    int        `json:"id"`
		Code                  string     `json:"code"`
		DisplayName           string     `json:"display_name"`
		CatalogCode           *string    `json:"catalog_code"`
		Kind                  string     `json:"kind"`
		Category              string     `json:"category"`
		Protocol              string     `json:"protocol"`
		BaseURL               string     `json:"base_url"`
		EgressProfile         *string    `json:"egress_profile"`
		Domestic              bool       `json:"domestic"`
		DiscountRate          float64    `json:"discount_rate"`
		Enabled               bool       `json:"enabled"`
		ManualDisabled        bool       `json:"manual_disabled"`
		HeaderProfileCode     string     `json:"header_profile_code"`
		Notes                 *string    `json:"notes"`
		VendorName            *string    `json:"vendor_name"`
		ActiveCredCount       int        `json:"active_cred_count"`
		HealthyCredCount      int        `json:"healthy_cred_count"`
		WarningCredCount      int        `json:"warning_cred_count"`
		CoolingCredCount      int        `json:"cooling_cred_count"`
		UnreachableCredCount  int        `json:"unreachable_cred_count"`
		AvailableModelCount   int        `json:"available_model_count"`
		UnavailableModelCount int        `json:"unavailable_model_count"`
		ErrorRate24h          float64    `json:"error_rate_24h"`
		CreatedAt             *time.Time `json:"created_at"`
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
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `UPDATE providers SET display_name = $1, updated_at = now() WHERE id = $2`, *req.DisplayName, id)
	}
	if req.BaseURL != nil {
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `UPDATE providers SET base_url = $1, updated_at = now() WHERE id = $2`, *req.BaseURL, id)
		needsReprobe = true
	}
	if req.Protocol != nil {
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `UPDATE providers SET protocol = $1, updated_at = now() WHERE id = $2`, *req.Protocol, id)
		needsReprobe = true
	}
	if req.Kind != nil {
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `UPDATE providers SET kind = $1, updated_at = now() WHERE id = $2`, *req.Kind, id)
	}
	if req.Category != nil {
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `UPDATE providers SET category = $1, updated_at = now() WHERE id = $2`, *req.Category, id)
	}
	if req.DiscountRate != nil {
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `UPDATE providers SET discount_rate = $1, updated_at = now() WHERE id = $2`, *req.DiscountRate, id)
	}
	if req.EgressProfile != nil {
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `UPDATE providers SET egress_profile = $1, updated_at = now() WHERE id = $2`, *req.EgressProfile, id)
	}
	if req.Notes != nil {
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `UPDATE providers SET notes = $1, updated_at = now() WHERE id = $2`, *req.Notes, id)
	}
	if req.Enabled != nil {
		//nolint:errcheck // best-effort exec, non-critical
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
	//nolint:errcheck // scan error non-critical
	h.db.QueryRow(ctx, `SELECT COUNT(*) FROM providers WHERE tenant_id = 'default'`).Scan(&total)

	writeJSON(w, http.StatusCreated, map[string]any{
		"message":   fmt.Sprintf("Seeded %d new providers from catalog", len(created)),
		"created":   len(created),
		"total":     total,
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
	case "reset-fp-slots":
		if r.Method == http.MethodPost {
			h.resetCredentialFpSlots(w, r, providerID, credID)
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
// getProviderSettings returns all provider-level setting overrides.
// GET /api/providers/:id/settings
