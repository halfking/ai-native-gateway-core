package admin

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
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
		var label, ciphertext, baseURL, protocol string
		if err := rows.Scan(&credID, &label, &ciphertext, &baseURL, &protocol); err != nil {
			continue
		}

		healthStatus := "unknown"
		errMsg := ""

		decrypted, decErr := decryptFernet([]byte(ciphertext), h.encKey)
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
		"provider_id":    providerID,
		"code":           code,
		"display_name":   displayName,
		"enabled":        enabled,
		"checked":        checked,
		"healthy":        healthy,
		"credentials":    results,
		"message":        "health check completed",
	})
}

func probeProvider(ctx context.Context, baseURL, protocol, apiKey string) (bool, error) {
	if baseURL == "" {
		return false, fmt.Errorf("empty base URL")
	}
	baseURL = strings.TrimRight(baseURL, "/")
	for _, suffix := range []string{"/v1/chat/completions", "/v1/completions", "/v1/responses", "/v1/messages"} {
		if strings.HasSuffix(baseURL, suffix) {
			baseURL = baseURL[:len(baseURL)-len(suffix)]
			break
		}
	}

	modelsURL := baseURL + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, "GET", modelsURL, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return true, nil
	}
	return false, fmt.Errorf("status %d", resp.StatusCode)
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
			h.diagnoseProvider(w, r, providerID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "models":
		if r.Method == http.MethodGet {
			h.getProviderModels(w, r, providerID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "credentials":
		if r.Method == http.MethodGet {
			h.listCredentials(w, r, providerID)
		} else if r.Method == http.MethodPost {
			h.addCredential(w, r, providerID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	default:
		if len(subPath) > 7 && subPath[:7] == "models/" {
			modelsPath := subPath[7:]
			h.handleProviderModels(w, r, providerID, modelsPath)
			return
		}
		if len(subPath) > 12 && subPath[:12] == "credentials/" {
			credPath := subPath[12:]
			h.handleProviderCredentials(w, r, providerID, credPath)
			return
		}
		http.NotFound(w, r)
	}
}

func (h *Handler) listProviders(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT p.id, p.tenant_id, p.code, p.display_name, p.kind, p.category, p.protocol, p.base_url,
		       p.egress_profile, p.domestic, p.discount_rate::float8, p.enabled, COALESCE(p.notes, ''),
		       COALESCE(pc.vendor_name, pc.code, '') as vendor_name,
		       COALESCE(ac.cnt, 0),
		       COALESCE(er.rate, 0)
		FROM providers p
		LEFT JOIN provider_catalog pc ON pc.code = p.catalog_code
		LEFT JOIN (SELECT c.provider_id, count(*) cnt FROM model_offers mo JOIN credentials c ON c.id=mo.credential_id WHERE mo.available=true GROUP BY c.provider_id) ac ON ac.provider_id=p.id
		LEFT JOIN (SELECT provider_id, count(*) FILTER (WHERE NOT success) * 1.0 / NULLIF(count(*), 0) AS rate FROM request_logs WHERE ts >= now() - interval '24 hours' GROUP BY provider_id) er ON er.provider_id=p.id
		WHERE p.tenant_id = 'default'
		ORDER BY p.id
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type provider struct {
		ID              int     `json:"id"`
		TenantID        string  `json:"tenant_id"`
		Code            string  `json:"code"`
		DisplayName     string  `json:"display_name"`
		Kind            string  `json:"kind"`
		Category        string  `json:"category"`
		Protocol        string  `json:"protocol"`
		BaseURL         string  `json:"base_url"`
		EgressProfile   string  `json:"egress_profile"`
		Domestic        bool    `json:"domestic"`
		DiscountRate    float64 `json:"discount_rate"`
		Enabled         bool    `json:"enabled"`
		Notes           string  `json:"notes"`
		VendorName      string  `json:"vendor_name"`
		AvailModelCount int     `json:"available_model_count"`
		ErrorRate24h    float64 `json:"error_rate_24h"`
	}
	providers := make([]provider, 0)
	for rows.Next() {
		var p provider
		if err := rows.Scan(&p.ID, &p.TenantID, &p.Code, &p.DisplayName, &p.Kind,
			&p.Category, &p.Protocol, &p.BaseURL, &p.EgressProfile, &p.Domestic,
			&p.DiscountRate, &p.Enabled, &p.Notes, &p.VendorName,
			&p.AvailModelCount, &p.ErrorRate24h); err != nil {
			slog.Warn("listProviders scan failed", "error", err)
			continue
		}
		providers = append(providers, p)
	}
	writeJSON(w, http.StatusOK, providers)
}

func (h *Handler) getProvider(w http.ResponseWriter, r *http.Request, id int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var p struct {
		ID                int     `json:"id"`
		Code              string  `json:"code"`
		DisplayName       string  `json:"display_name"`
		Kind              string  `json:"kind"`
		Category          string  `json:"category"`
		Protocol          string  `json:"protocol"`
		BaseURL           string  `json:"base_url"`
		Enabled           bool    `json:"enabled"`
		VendorName        string  `json:"vendor_name"`
		HealthyCount      int     `json:"healthy_count"`
		WarningCount      int     `json:"warning_count"`
		CoolingCount      int     `json:"cooling_count"`
		AvailModelCount   int     `json:"available_model_count"`
		UnavailModelCount int     `json:"unavailable_model_count"`
	}
	err := h.db.QueryRow(ctx, `
		SELECT p.id, COALESCE(p.code,''), COALESCE(p.display_name,''), COALESCE(p.kind,''),
		       COALESCE(p.category,''), COALESCE(p.protocol,''), COALESCE(p.base_url,''), p.enabled,
		       COALESCE(pc.vendor_name, pc.code, ''),
		       COALESCE(hc.cnt,0), COALESCE(wc.cnt,0), COALESCE(cc.cnt,0),
		       COALESCE(ac.cnt,0), COALESCE(uc.cnt,0)
		FROM providers p
		LEFT JOIN provider_catalog pc ON pc.code = p.catalog_code
		LEFT JOIN (SELECT provider_id, count(*) cnt FROM credentials WHERE status='active' GROUP BY provider_id) hc ON hc.provider_id=p.id
		LEFT JOIN (SELECT provider_id, count(*) cnt FROM credentials WHERE health_status='warning' GROUP BY provider_id) wc ON wc.provider_id=p.id
		LEFT JOIN (SELECT provider_id, count(*) cnt FROM credentials WHERE circuit_state='open' GROUP BY provider_id) cc ON cc.provider_id=p.id
		LEFT JOIN (SELECT c.provider_id, count(*) cnt FROM model_offers mo JOIN credentials c ON c.id=mo.credential_id WHERE mo.available=true GROUP BY c.provider_id) ac ON ac.provider_id=p.id
		LEFT JOIN (SELECT c.provider_id, count(*) cnt FROM model_offers mo JOIN credentials c ON c.id=mo.credential_id WHERE mo.available=false GROUP BY c.provider_id) uc ON uc.provider_id=p.id
		WHERE p.id = $1 AND p.tenant_id = 'default'
	`, id).Scan(&p.ID, &p.Code, &p.DisplayName, &p.Kind, &p.Category, &p.Protocol, &p.BaseURL, &p.Enabled,
		&p.VendorName,
		&p.HealthyCount, &p.WarningCount, &p.CoolingCount,
		&p.AvailModelCount, &p.UnavailModelCount)
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

	displayName := req.Code
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
	`, req.Code, displayName, baseURL, protocol).Scan(&id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "message": "ok"})
}

func (h *Handler) updateProvider(w http.ResponseWriter, r *http.Request, id int) {
	var req struct {
		DisplayName *string `json:"display_name"`
		BaseURL     *string `json:"base_url"`
		Protocol    *string `json:"protocol"`
		Notes       *string `json:"notes"`
		Enabled     *bool   `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if req.DisplayName != nil {
		h.db.Exec(ctx, `UPDATE providers SET display_name = $1 WHERE id = $2`, *req.DisplayName, id)
	}
	if req.BaseURL != nil {
		h.db.Exec(ctx, `UPDATE providers SET base_url = $1 WHERE id = $2`, *req.BaseURL, id)
	}
	if req.Protocol != nil {
		h.db.Exec(ctx, `UPDATE providers SET protocol = $1 WHERE id = $2`, *req.Protocol, id)
	}
	if req.Notes != nil {
		h.db.Exec(ctx, `UPDATE providers SET notes = $1 WHERE id = $2`, *req.Notes, id)
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
		INSERT INTO providers (tenant_id, code, display_name, catalog_code, protocol, base_url, enabled)
		SELECT
			'default',
			pc.code,
			pc.display_name,
			pc.code,
			COALESCE(pc.protocol, 'openai-completions'),
			COALESCE(pc.base_url_template, ''),
			TRUE
		FROM provider_catalog pc
		WHERE NOT EXISTS (
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
		"created": len(created),
		"total":   total,
		"providers": created,
	})
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
	case "check":
		if r.Method == http.MethodPost {
			h.checkCredentialHealth(w, r, providerID, credID)
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

	encrypted, err := encryptFernet([]byte(req.APIKey), h.encKey)
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
		SELECT c.id, c.label, c.status, c.concurrency_limit, c.balance_usd::float8,
		       COALESCE(c.circuit_state, 'closed'), COALESCE(c.lifecycle_status, 'active'),
		       COALESCE(c.availability_state, 'ready'), COALESCE(c.quota_state, 'ok')
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
		ID                int      `json:"id"`
		Label             string   `json:"label"`
		Status            string   `json:"status"`
		ConcurrencyLimit  *int     `json:"concurrency_limit"`
		BalanceUSD        *float64 `json:"balance_usd"`
		CircuitState      string   `json:"circuit_state"`
		LifecycleStatus   string   `json:"lifecycle_status"`
		AvailabilityState string   `json:"availability_state"`
		QuotaState        string   `json:"quota_state"`
	}
	var creds []cred
	for rows.Next() {
		var c cred
		if err := rows.Scan(&c.ID, &c.Label, &c.Status, &c.ConcurrencyLimit, &c.BalanceUSD,
			&c.CircuitState, &c.LifecycleStatus, &c.AvailabilityState, &c.QuotaState); err != nil {
			slog.Warn("listCredentials scan failed", "error", err, "provider_id", providerID)
			continue
		}
		creds = append(creds, c)
	}
	if creds == nil {
		creds = []cred{}
	}
	writeJSON(w, http.StatusOK, creds)
}

func (h *Handler) updateCredential(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	var req struct {
		Label            *string `json:"label"`
		Status           *string `json:"status"`
		ConcurrencyLimit *int    `json:"concurrency_limit"`
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

	plaintext, err := decryptFernet(ciphertext, h.encKey)
	if err != nil {
		slog.Warn("credential decrypt failed", "credential_id", credID, "error", err)
		writeError(w, http.StatusInternalServerError, "decryption failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"api_key": plaintext})
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
	h.db.Exec(ctx, `UPDATE credentials SET availability_state = 'ready', availability_recover_at = NULL WHERE id = $1 AND provider_id = $2`, credID, providerID)
	writeJSON(w, http.StatusOK, map[string]string{"message": "reset"})
}

func (h *Handler) resetCredentialQuota(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	h.db.Exec(ctx, `UPDATE credentials SET quota_state = 'ok', quota_recover_at = NULL WHERE id = $1 AND provider_id = $2`, credID, providerID)
	writeJSON(w, http.StatusOK, map[string]string{"message": "reset"})
}

func (h *Handler) checkCredentialHealth(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var exists int
	err := h.db.QueryRow(ctx, `SELECT 1 FROM credentials WHERE id = $1 AND provider_id = $2`, credID, providerID).Scan(&exists)
	if err != nil {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"credential_id": credID,
		"status":        "ok",
		"message":       "health check not yet fully implemented (stub)",
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

type credDiag struct {
	ID              int     `json:"credential_id"`
	Label           string  `json:"label"`
	State           string  `json:"state"`
	LastError       *string `json:"last_error"`
	CircuitState    string  `json:"circuit_state"`
	Healthy         bool    `json:"healthy"`
	Recommendation  string  `json:"recommendation"`
}

func (h *Handler) diagnoseProvider(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var baseURL, protocol string
	var enabled bool
	err := h.db.QueryRow(ctx, `
		SELECT COALESCE(base_url,''), COALESCE(protocol,'openai-completions'), enabled
		FROM providers WHERE id=$1 AND tenant_id='default'
	`, providerID).Scan(&baseURL, &protocol, &enabled)
	if err != nil {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}

	rows, err := h.db.Query(ctx, `
		SELECT c.id, COALESCE(c.label,''), COALESCE(c.status,''), c.health_error,
		       COALESCE(c.circuit_state,'closed')
		FROM credentials c
		WHERE c.provider_id=$1 AND c.tenant_id='default'
		ORDER BY c.id
	`, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	var diags []credDiag
	for rows.Next() {
		var d credDiag
		if err := rows.Scan(&d.ID, &d.Label, &d.State, &d.LastError, &d.CircuitState); err != nil {
			continue
		}
		d.Healthy = d.State == "active" && d.LastError == nil && d.CircuitState != "open"
		switch {
		case !enabled:
			d.Recommendation = "provider_disabled"
		case d.CircuitState == "open":
			d.Recommendation = "wait_for_cooling"
		case d.State == "quarantine":
			d.Recommendation = "manual_recovery_needed"
		case d.State == "active" && d.LastError != nil:
			d.Recommendation = "transient_error_monitor"
		default:
			d.Recommendation = "ok"
		}
		diags = append(diags, d)
	}
	if diags == nil {
		diags = []credDiag{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"provider_id": providerID,
		"enabled":     enabled,
		"base_url":    baseURL,
		"protocol":    protocol,
		"credentials": diags,
		"summary": map[string]any{
			"total":     len(diags),
			"healthy":   countByRec(diags, "ok"),
			"degraded":  countByRec(diags, "transient_error_monitor"),
			"disabled":  countByRec(diags, "provider_disabled"),
			"cooling":   countByRec(diags, "wait_for_cooling"),
			"quarantine": countByRec(diags, "manual_recovery_needed"),
		},
	})
}

func countByRec(diags []credDiag, rec string) int {
	n := 0
	for _, d := range diags {
		if d.Recommendation == rec {
			n++
		}
	}
	return n
}

func (h *Handler) getProviderModels(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT mo.id, mo.credential_id, COALESCE(c.label,''),
		       mo.raw_model_name, mo.standardized_name, mo.canonical_id,
		       mo.outbound_model_name,
		       mo.available, mo.unavailable_reason, mo.unavailable_at
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		WHERE c.provider_id=$1
		ORDER BY mo.standardized_name, mo.id
	`, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type ModelOffer struct {
		ID                int      `json:"id"`
		CredentialID      int      `json:"credential_id"`
		CredentialLabel   string   `json:"credential_label"`
		RawModelName      string   `json:"raw_model_name"`
		StandardizedName  *string  `json:"standardized_name"`
		CanonicalID       *int     `json:"canonical_id"`
		OutboundModelName *string  `json:"display_name"`
		Available         bool     `json:"available"`
		UnavailableReason *string  `json:"unavailable_reason"`
		UnavailableAt     *string  `json:"unavailable_at"`
	}
	var offers []ModelOffer
	for rows.Next() {
		var o ModelOffer
		if err := rows.Scan(&o.ID, &o.CredentialID, &o.CredentialLabel,
			&o.RawModelName, &o.StandardizedName, &o.CanonicalID,
			&o.OutboundModelName,
			&o.Available, &o.UnavailableReason, &o.UnavailableAt); err != nil {
			continue
		}
		offers = append(offers, o)
	}
	if offers == nil {
		offers = []ModelOffer{}
	}
	writeJSON(w, http.StatusOK, offers)
}

func (h *Handler) handleProviderModels(w http.ResponseWriter, r *http.Request, providerID int, modelsPath string) {
	if strings.Contains(modelsPath, "/state") {
		parts := strings.SplitN(modelsPath, "/", 2)
		if len(parts) == 2 && parts[1] == "state" {
			offerID, err := strconv.Atoi(parts[0])
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid offer id")
				return
			}
			h.toggleModelOfferState(w, r, providerID, offerID)
			return
		}
	}
	http.NotFound(w, r)
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

	var reason sql.NullString
	if !req.Available {
		reason = sql.NullString{String: "manual_override", Valid: true}
	}

	tag, err := h.db.Exec(ctx, `
		UPDATE model_offers SET available=$1,
			unavailable_reason=CASE WHEN $1 THEN NULL ELSE $2 END,
			unavailable_at=CASE WHEN $1 THEN NULL ELSE now() END
		WHERE id=$3 AND credential_id IN (SELECT id FROM credentials WHERE provider_id=$4)
	`, req.Available, reason, offerID, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "offer not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message":   "ok",
		"available": req.Available,
	})
}
