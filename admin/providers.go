package admin

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
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
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var code, displayName string
	var enabled bool
	err := h.db.QueryRow(ctx, `SELECT code, display_name, enabled FROM providers WHERE id = $1 AND tenant_id = 'default'`, providerID).Scan(&code, &displayName, &enabled)
	if err != nil {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"provider_id":   providerID,
		"code":          code,
		"display_name":  displayName,
		"enabled":       enabled,
		"message":       "provider health check not yet fully implemented (stub)",
	})
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
	default:
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
		       p.egress_profile, p.domestic, p.discount_rate, p.enabled, p.notes,
		       COALESCE(pc.vendor_name, pc.code) as vendor_name
		FROM providers p
		LEFT JOIN provider_catalog pc ON pc.code = p.catalog_code
		WHERE p.tenant_id = 'default'
		ORDER BY p.id
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type provider struct {
		ID           int     `json:"id"`
		TenantID     string  `json:"tenant_id"`
		Code         string  `json:"code"`
		DisplayName  string  `json:"display_name"`
		Kind         string  `json:"kind"`
		Category     string  `json:"category"`
		Protocol     string  `json:"protocol"`
		BaseURL      string  `json:"base_url"`
		EgressProfile string `json:"egress_profile"`
		Domestic     bool    `json:"domestic"`
		DiscountRate float64 `json:"discount_rate"`
		Enabled      bool    `json:"enabled"`
		Notes        *string `json:"notes"`
		VendorName   string  `json:"vendor_name"`
	}
	var providers []provider
	for rows.Next() {
		var p provider
		if err := rows.Scan(&p.ID, &p.TenantID, &p.Code, &p.DisplayName, &p.Kind,
			&p.Category, &p.Protocol, &p.BaseURL, &p.EgressProfile, &p.Domestic,
			&p.DiscountRate, &p.Enabled, &p.Notes, &p.VendorName); err != nil {
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
		ID           int     `json:"id"`
		Code         string  `json:"code"`
		DisplayName  string  `json:"display_name"`
		Kind         string  `json:"kind"`
		Category     string  `json:"category"`
		Protocol     string  `json:"protocol"`
		BaseURL      string  `json:"base_url"`
		Enabled      bool    `json:"enabled"`
	}
	err := h.db.QueryRow(ctx, `
		SELECT id, COALESCE(code,''), COALESCE(display_name,''), COALESCE(kind,''),
		       COALESCE(category,''), COALESCE(protocol,''), COALESCE(base_url,''), enabled
		FROM providers WHERE id = $1 AND tenant_id = 'default'
	`, id).Scan(&p.ID, &p.Code, &p.DisplayName, &p.Kind, &p.Category, &p.Protocol, &p.BaseURL, &p.Enabled)
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
		ID                int     `json:"id"`
		Label             string  `json:"label"`
		Status            string  `json:"status"`
		ConcurrencyLimit  int     `json:"concurrency_limit"`
		BalanceUSD        float64 `json:"balance_usd"`
		CircuitState      string  `json:"circuit_state"`
		LifecycleStatus   string  `json:"lifecycle_status"`
		AvailabilityState string  `json:"availability_state"`
		QuotaState        string  `json:"quota_state"`
	}
	var creds []cred
	for rows.Next() {
		var c cred
		if err := rows.Scan(&c.ID, &c.Label, &c.Status, &c.ConcurrencyLimit, &c.BalanceUSD,
			&c.CircuitState, &c.LifecycleStatus, &c.AvailabilityState, &c.QuotaState); err != nil {
			continue
		}
		creds = append(creds, c)
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
