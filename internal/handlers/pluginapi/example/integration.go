// Package main provides example integration of plugin API into gateway-v2
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/kaixuan/llm-gateway-go/internal/handlers/pluginapi"
)

// IntegratePluginAPI shows how to integrate plugin API into gateway-v2
func IntegratePluginAPI(mux *http.ServeMux, pluginDir string) {
	// The standalone example has no process supervisor; lifecycle requests remain unavailable.
	handler := pluginapi.NewPluginAPIHandler(pluginDir, nil)
	
	// Register routes
	mux.HandleFunc("/api/v1/plugins", handler.ListPluginsHandler)
	mux.HandleFunc("/api/v1/plugins/", func(w http.ResponseWriter, r *http.Request) {
		// Extract plugin ID from path
		if r.URL.Path == "/api/v1/plugins" || r.URL.Path == "/api/v1/plugins/" {
			handler.ListPluginsHandler(w, r)
		} else {
			handler.GetPluginHandler(w, r)
		}
	})
	
	log.Printf("✅ Plugin API registered at /api/v1/plugins")
}

// Example usage in gateway-v2/main.go:
//
// import "github.com/kaixuan/llm-gateway-go/internal/handlers/pluginapi/example"
//
// func main() {
//     mux := http.NewServeMux()
//     pluginDir := getEnv("PLUGIN_DIR", "/var/lib/llm-gateway/plugins")
//     example.IntegratePluginAPI(mux, pluginDir)
//     // ... rest of gateway setup
// }

func main() {
	// Demo server
	pluginDir := os.Getenv("PLUGIN_DIR")
	if pluginDir == "" {
		pluginDir = "./tests/integration/demo-plugins"
	}
	
	mux := http.NewServeMux()
	IntegratePluginAPI(mux, pluginDir)
	
	// Add health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"healthy"}`)
	})
	
	addr := ":8080"
	log.Printf("🚀 Demo Plugin API Server listening on %s", addr)
	log.Printf("📂 Plugin directory: %s", pluginDir)
	log.Printf("Try: curl http://localhost:8080/api/v1/plugins")
	
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
