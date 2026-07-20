package main

import (
	"net/http"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

// registerPluginAPIProxy mounts GET /plugins/{pluginId}/api/{rest...} which
// reverse-proxies to the plugin process. The {rest...} tail makes it
// unambiguously more specific than the static-file route
// GET /plugins/{pluginId}/{rest...} (Go ServeMux picks the more specific
// pattern, no conflict). pluginBaseFor returns the plugin's listen URL for a
// given pluginID ("" => plugin not running => 502).
func registerPluginAPIProxy(mux *http.ServeMux, secret []byte, pluginBaseFor func(pluginID string) string) {
	mux.Handle("GET /plugins/{pluginId}/api/{rest...}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pluginID := r.PathValue("pluginId")
		base := pluginBaseFor(pluginID)
		if base == "" {
			http.Error(w, "plugin not running", http.StatusBadGateway)
			return
		}
		pluginruntime.PluginAPIProxy(base, secret).ServeHTTP(w, r)
	}))
}
