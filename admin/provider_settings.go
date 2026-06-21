package admin

// provider_settings.go — extracted from providers.go (2026-06-22 audit §3
// single-file-bloat remediation, ninth and final cut, settings slice).
// Owns the per-provider settings_kv CRUD endpoints introduced in Round 48.
//
// Endpoints:
//   GET    /api/providers/{id}/settings                  getProviderSettings
//   GET    /api/providers/{id}/settings/{key}            getProviderSetting
//   PUT    /api/providers/{id}/settings/{key}            setProviderSetting
//   DELETE /api/providers/{id}/settings/{key}            deleteProviderSetting
//   ANY    /api/providers/{id}/settings/{key}            handleProviderSetting (dispatcher)
//
// validateSettingValue enforces the per-key JSON-shape rules defined in
// settings/specs.go (e.g. compression.mode is one of "off"/"auto"/"force",
// cache.enabled is a boolean, format_conversion.enabled is a boolean).
// Unknown keys are rejected; known keys are schema-validated.
//
// Self-contained: stdlib only + same-package helpers (writeJSON / writeError
// / h.db). No internal/* deps; no new third-party imports.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

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
