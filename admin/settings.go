package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// registerSettingsRoutes installs the 4 platform endpoints under /api/admin/settings
// and the 4 tenant endpoints under /api/admin/tenants/.
func (h *Handler) registerSettingsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/admin/settings", h.admin(h.settingsList))
	mux.HandleFunc("/api/admin/settings/", h.admin(h.settingsRouter))
	mux.HandleFunc("/api/admin/tenants/", h.admin(h.tenantSettingsRouter))
}

// settingsRouter dispatches /api/admin/settings/{key}[/history|/rollback].
func (h *Handler) settingsRouter(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/admin/settings/")
	parts := strings.Split(rest, "/")
	key := parts[0]
	sub := ""
	if len(parts) > 1 {
		sub = parts[1]
	}
	switch {
	case r.Method == http.MethodGet && sub == "":
		h.settingsGet(w, r, key, false, "")
	case r.Method == http.MethodPut && sub == "":
		h.settingsPut(w, r, key, false, "")
	case r.Method == http.MethodGet && sub == "history":
		h.settingsHistory(w, r, key, "")
	case r.Method == http.MethodPost && sub == "rollback":
		h.settingsRollback(w, r, key, false, "")
	default:
		writeError(w, http.StatusNotFound, "unknown settings endpoint")
	}
}

// tenantSettingsRouter handles /api/admin/tenants/{tid}/settings/{key}.
func (h *Handler) tenantSettingsRouter(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/admin/tenants/")
	parts := strings.Split(rest, "/")
	if len(parts) < 3 || parts[1] != "settings" {
		writeError(w, http.StatusNotFound, "unknown tenant settings endpoint")
		return
	}
	tid := parts[0]
	key := parts[2]
	sub := ""
	if len(parts) > 3 {
		sub = parts[3]
	}
	// Tenant settings require super_admin (Q3).
	if !IsSuperAdminOrLegacy(r) {
		writeError(w, http.StatusForbidden, "super_admin required for tenant settings")
		return
	}
	switch {
	case r.Method == http.MethodGet && sub == "":
		h.settingsGet(w, r, key, true, tid)
	case r.Method == http.MethodPut && sub == "":
		h.settingsPut(w, r, key, true, tid)
	case r.Method == http.MethodGet && sub == "history":
		h.settingsHistory(w, r, key, tid)
	case r.Method == http.MethodPost && sub == "rollback":
		h.settingsRollback(w, r, key, true, tid)
	default:
		writeError(w, http.StatusNotFound, "unknown tenant settings endpoint")
	}
}

// settingsList returns all registered Specs with their effective values.
func (h *Handler) settingsList(w http.ResponseWriter, r *http.Request) {
	if settings.Global == nil {
		writeError(w, http.StatusServiceUnavailable, "settings registry not initialised")
		return
	}
	category := r.URL.Query().Get("category")
	items := []map[string]any{}
	for _, sp := range settings.Global.AllSpecs() {
		if category != "" && string(sp.Category) != category {
			continue
		}
		// For tenant-scope specs, list shows spec only; values are per-tenant.
		var v json.RawMessage
		var src string
		if sp.Scope == settings.ScopePlatform {
			raw, s, err := settings.Global.EffectiveValue(sp.Scope, sp.Key, "")
			if err != nil {
				continue
			}
			v = json.RawMessage(raw)
			src = s
		}
		items = append(items, map[string]any{
			"key":            sp.Key,
			"env_name":       sp.EnvName,
			"type":           sp.Type,
			"scope":          sp.Scope,
			"category":       sp.Category,
			"default":        sp.Default,
			"value":          v,
			"source":         src,
			"options":        sp.Options,
			"min":            sp.Min,
			"max":            sp.Max,
			"description":    sp.Description,
			"danger_level":   sp.DangerLevel,
			"hot_reload":     sp.HotReload,
			"observability":  sp.Observability,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// settingsGet returns a single setting with its effective value.
func (h *Handler) settingsGet(w http.ResponseWriter, r *http.Request, key string, tenant bool, tid string) {
	if settings.Global == nil {
		writeError(w, http.StatusServiceUnavailable, "settings registry not initialised")
		return
	}
	sp := settings.Global.Spec(key)
	if sp == nil {
		writeError(w, http.StatusNotFound, "unknown setting "+key)
		return
	}
	scope := sp.Scope
	if tenant {
		scope = settings.ScopeTenant
	}
	raw, src, err := settings.Global.EffectiveValue(scope, key, tid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"spec":   sp,
		"value":  json.RawMessage(raw),
		"source": src,
	})
}

// settingsPut writes a value with validation and audit.
func (h *Handler) settingsPut(w http.ResponseWriter, r *http.Request, key string, tenant bool, tid string) {
	if settings.Global == nil || h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "settings not available")
		return
	}
	sp := settings.Global.Spec(key)
	if sp == nil {
		writeError(w, http.StatusNotFound, "unknown setting "+key)
		return
	}
	// Permission gate.
	if sp.DangerLevel >= settings.Dangerous && !IsSuperAdminOrLegacy(r) {
		writeError(w, http.StatusForbidden, "super_admin required for this setting")
		return
	}

	var body struct {
		Value        json.RawMessage `json:"value"`
		ConfirmToken string          `json:"confirm_token,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var v any
	if err := json.Unmarshal(body.Value, &v); err != nil {
		writeError(w, http.StatusBadRequest, "value is not valid JSON")
		return
	}
	if err := sp.Validate(v); err != nil {
		writeError(w, http.StatusBadRequest, "validation: "+err.Error())
		return
	}

	store, ok := h.dbSettingsStore()
	if !ok {
		writeError(w, http.StatusInternalServerError, "settings store not wired")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	scope := sp.Scope
	if tenant {
		scope = settings.ScopeTenant
	}
	var oldVal []byte
	var err error
	if tenant {
		oldVal, err = store.SetTenant(tid, key, v)
	} else {
		oldVal, err = store.Set(scope, key, v)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "save failed: "+err.Error())
		return
	}

	// Audit.
	user, role, ip := authIdentity(r)
	settings.WriteAudit(ctx, h.db, settings.AuditEntry{
		SettingKey:   key,
		TenantID:     tid,
		Action:       "update",
		OldValue:     json.RawMessage(oldVal),
		NewValue:     body.Value,
		OperatorUser: user,
		OperatorRole: role,
		ClientIP:     ip,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"old_value":  json.RawMessage(oldVal),
		"new_value":  body.Value,
		"applied_at": time.Now().UTC(),
	})
}

// settingsHistory returns the last 50 audit entries for a setting.
func (h *Handler) settingsHistory(w http.ResponseWriter, r *http.Request, key, tid string) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not wired")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	entries, err := settings.ListAudit(ctx, h.db, key, tid, 50)
	if err != nil {
		slog.Warn("settings: history query failed", "err", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": entries})
}

// settingsRollback reverts a setting to its previous value.
func (h *Handler) settingsRollback(w http.ResponseWriter, r *http.Request, key string, tenant bool, tid string) {
	if settings.Global == nil || h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "settings not available")
		return
	}
	store, ok := h.dbSettingsStore()
	if !ok {
		writeError(w, http.StatusInternalServerError, "settings store not wired")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	sp := settings.Global.Spec(key)
	if sp == nil {
		writeError(w, http.StatusNotFound, "unknown setting "+key)
		return
	}
	scope := sp.Scope
	if tenant {
		scope = settings.ScopeTenant
	}
	newVal, err := store.Rollback(scope, key, tid)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	user, role, ip := authIdentity(r)
	settings.WriteAudit(ctx, h.db, settings.AuditEntry{
		SettingKey:   key,
		TenantID:     tid,
		Action:       "rollback",
		OldValue:     nil,
		NewValue:     newVal,
		OperatorUser: user,
		OperatorRole: role,
		ClientIP:     ip,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"status":          "ok",
		"rolled_back_to":  newVal,
	})
}

// dbSettingsStore returns the DB-backed settings store, or (nil, false).
func (h *Handler) dbSettingsStore() (*settings.StoreDB, bool) {
	if h.db == nil {
		return nil, false
	}
	// We constructed the StoreDB during init and cached it.
	if h.settingsStore == nil {
		return nil, false
	}
	return h.settingsStore, true
}

// authIdentity extracts user, role, client_ip from the admin auth context.
func authIdentity(r *http.Request) (user, role, ip string) {
	user = "unknown"
	role = "anonymous"
	ip = r.RemoteAddr
	if claims := GetAuthContext(r); claims != nil {
		if claims.Username != "" {
			user = claims.Username
		}
		if claims.Role != "" {
			role = claims.Role
		}
	}
	return
}