package main

import (
	"net/http"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

// registerPluginStaticRoutes mounts /plugins/{pluginId}/{rest...} serving each
// installed plugin's bundled web/ directory (consumed by the PluginMount iframe).
func registerPluginStaticRoutes(mux *http.ServeMux, pluginsDir string) {
	mux.Handle("GET /plugins/{pluginId}/{rest...}", pluginruntime.PluginStaticHandler(pluginsDir))
}
