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
// Deps: stdlib + internal/jsonbody + provider/catalog (for the adaptive
// protocol declaration key and its value validation).

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/jsonbody"
	providercatalog "github.com/kaixuan/llm-gateway-go/provider/catalog"
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
			warnRowSkip("providerSettings.list", err)
			continue
		}
		settings = append(settings, s)
	}
	// 设置项被截断 → 前端以为某项"没配"（回落默认值），比显式失败危险。
	if writeAggRowsErr(w, "providerSettings.list", rows.Err()) {
		return
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

// writableSettingKeys 是 PUT /settings/{key} 接受的键集合。
//
// 这是一个**封闭枚举**，不是通用 KV 通道：每个键都要有读取方（生产调用点）
// 与值校验规则，否则写进去就是一个无人消费的死配置——本仓已多次登记「登记
// 了却没投递」同型缺陷。新增键时必须三件事一起做：本表 + validateSettingValue
// 分支 + 读取方接线，缺一件就等于没做。
var writableSettingKeys = map[string]bool{
	"compression.mode":          true,
	"cache.enabled":             true,
	"format_conversion.enabled": true,
	// 2026-11-28 协议自适应：声明该上游真实可用的协议（有序数组，首项为基协议）。
	// 读取方 provider/adaptive_protocol.go:adaptiveAllowedByProvider。
	ProviderSettingKeyAdaptiveAllowed: true,
}

// isWritableSettingKey reports whether the key is accepted on the write path.
func isWritableSettingKey(key string) bool {
	return writableSettingKeys[key]
}

// setProviderSetting sets or updates a provider-level setting override.
func (h *Handler) setProviderSetting(w http.ResponseWriter, r *http.Request, providerID int, settingKey string) {
	if !isWritableSettingKey(settingKey) {
		writeError(w, http.StatusBadRequest, "invalid setting key: "+settingKey)
		return
	}

	var body struct {
		Value   json.RawMessage `json:"value"`
		Enabled *bool           `json:"enabled,omitempty"`
	}
	if err := jsonbody.DecodeRequest(r, &body, jsonbody.MaxRequiredBody, true); err != nil {
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
	case ProviderSettingKeyAdaptiveAllowed:
		// 有序字符串数组，每项必须是 canonical 协议或已知别名。
		// 拒绝空串、未知值、以及选择器本身（'adaptive' 作为「可用协议」会让
		// 消解器自我引用，永远消解不出具体协议）。
		//
		// 用 *[]string 而非 []string 以区分 `null` 与 `[]`：encoding/json 把
		// `null` 解进 slice 是不报错的（结果 nil），若直接接 []string 就会
		// 静默通过——`[]` 是合法的「清空声明」，`null` 是形状错误。
		var declared *[]string
		if err := json.Unmarshal(valueJSON, &declared); err != nil {
			return fmt.Errorf("must be an array of protocol strings")
		}
		if declared == nil {
			return fmt.Errorf("must be an array of protocol strings, not null")
		}
		for i, entry := range *declared {
			if strings.TrimSpace(entry) == "" {
				return fmt.Errorf("entry %d is blank", i)
			}
			if providercatalog.IsAdaptiveProtocol(entry) {
				return fmt.Errorf("entry %d: %q is a selection strategy, not a wire protocol", i, entry)
			}
			if _, err := providercatalog.NormalizeProviderProtocol(entry); err != nil {
				return fmt.Errorf("entry %d: %v", i, err)
			}
		}
	default:
		return fmt.Errorf("unknown setting key")
	}
	return nil
}
