package main

import (
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/admin"
	pluginruntime "github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

type pluginEntitlementAuthorizer interface {
	Allowed(ctx context.Context, tenantID, moduleID string) (bool, error)
}

// entitlementGateEnabled parses LLM_GATEWAY_PLUGIN_ENTITLEMENT_GATE once at
// wiring time so a typo'd value ("True", "1", "yes") is not silently treated
// as "off". Only the exact string "true" (case-insensitive) enables the gate.
func entitlementGateEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("LLM_GATEWAY_PLUGIN_ENTITLEMENT_GATE")), "true")
}

// registerPluginAPIProxy mounts GET /plugins/{pluginId}/api/{rest...}, admin-auth-gated.
// The {rest...} tail makes it unambiguously more specific than the static-file route
// GET /plugins/{pluginId}/{rest...} (Go ServeMux picks the more specific pattern, no
// conflict). After auth, the verified tenant from AuthContext is injected as
// X-Caller-Tenant so the proxy signs the forwarded request with the REAL tenant (not a
// client-forged/default one). pluginBaseFor returns the plugin's listen URL for a
// pluginID ("" => not running => 502).
func registerPluginAPIProxy(mux *http.ServeMux, secret []byte, pluginBaseFor func(pluginID string) string, moduleKeyFor func(pluginID string) (moduleID string, licenseRequired bool), authorizer pluginEntitlementAuthorizer, pool *pgxpool.Pool, adminSecret string) {
	gateEnabled := entitlementGateEnabled()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pluginID := r.PathValue("pluginId")
		base := pluginBaseFor(pluginID)
		if base == "" {
			http.Error(w, "plugin not running", http.StatusBadGateway)
			return
		}
		auth := admin.GetAuthContext(r)
		if gateEnabled {
			moduleID, licenseRequired := moduleKeyFor(pluginID)
			if licenseRequired {
				if authorizer == nil {
					http.Error(w, "plugin_entitlement_unavailable", http.StatusServiceUnavailable)
					return
				}
				active, err := authorizer.Allowed(r.Context(), auth.TenantID, moduleID)
				if err != nil {
					http.Error(w, "plugin_entitlement_unavailable", http.StatusServiceUnavailable)
					return
				}
				if !active {
					http.Error(w, "plugin_entitlement_required", http.StatusForbidden)
					return
				}
			}
		}
		// Inject the verified tenant (set by AdminMiddleware into AuthContext) so the
		// proxy's SignHeaders uses the real tenant, not a client-forged header.
		if auth != nil && auth.TenantID != "" {
			r.Header.Set("X-Caller-Tenant", auth.TenantID)
		}
		pluginruntime.PluginAPIProxy(base, secret).ServeHTTP(w, r)
	})
	mux.Handle("GET /plugins/{pluginId}/api/{rest...}", admin.AdminMiddleware(handler, pool, adminSecret))
}
