package main

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

// registerPluginAPIProxy mounts GET /plugins/{pluginId}/api/{rest...}, admin-auth-gated.
// The {rest...} tail makes it unambiguously more specific than the static-file route
// GET /plugins/{pluginId}/{rest...} (Go ServeMux picks the more specific pattern, no
// conflict). After auth, the verified tenant from AuthContext is injected as
// X-Caller-Tenant so the proxy signs the forwarded request with the REAL tenant (not a
// client-forged/default one). pluginBaseFor returns the plugin's listen URL for a
// pluginID ("" => not running => 502).
func registerPluginAPIProxy(mux *http.ServeMux, secret []byte, pluginBaseFor func(pluginID string) string, pool *pgxpool.Pool, adminSecret string) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pluginID := r.PathValue("pluginId")
		base := pluginBaseFor(pluginID)
		if base == "" {
			http.Error(w, "plugin not running", http.StatusBadGateway)
			return
		}
		// Inject the verified tenant (set by AdminMiddleware into AuthContext) so the
		// proxy's SignHeaders uses the real tenant, not a client-forged header.
		if auth := admin.GetAuthContext(r); auth != nil && auth.TenantID != "" {
			r.Header.Set("X-Caller-Tenant", auth.TenantID)
		}
		pluginruntime.PluginAPIProxy(base, secret).ServeHTTP(w, r)
	})
	mux.Handle("GET /plugins/{pluginId}/api/{rest...}", admin.AdminMiddleware(handler, pool, adminSecret))
}
