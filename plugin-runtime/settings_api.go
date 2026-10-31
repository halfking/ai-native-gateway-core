package pluginruntime

import (
	"encoding/json"
	"net/http"
	"strings"
)

// SettingsHandler exposes GET /api/v1/plugins/{id}/settings — schema-driven
// form payload. Host UI renders EffectiveSettingsSchema; no plugin-specific
// branches. Path shape: /api/v1/plugins/<id>/settings
func SettingsHandler(lookup func(pluginID string) *Manifest) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/plugins/")
		id = strings.TrimSuffix(id, "/settings")
		id = strings.Trim(id, "/")
		if id == "" || strings.Contains(id, "/") {
			http.Error(w, "plugin id required", http.StatusBadRequest)
			return
		}
		var m *Manifest
		if lookup != nil {
			m = lookup(id)
		}
		if m == nil {
			http.Error(w, "plugin not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plugin_id":       m.PluginID,
			"settings_schema": m.EffectiveSettingsSchema(),
			"defaults":        m.Defaults,
			"client_assets":   m.ClientAssets,
		})
	})
}
