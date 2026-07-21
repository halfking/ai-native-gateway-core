// Plugin health-check helpers extracted from main() during the P0 main.go split.
//
// See docs/refactor-plans/main-go-split.md for the full plan.
//
// The plugin runtime health-check closure was an 11-line anonymous function
// inside main() that:
//   - resolved the plugin's unix socket path via supervisor
//   - chose the per-plugin health path (manifest override → "/plugin/healthz")
//   - called pluginruntime.PreciseHealthCheck with a 2s timeout
//
// Hoisted to a top-level function so it can be reused from anywhere in the
// package (e.g. tests, on-demand health probes) and the captured supervisor
// is now an explicit parameter rather than a closure variable.
package main

import (
	"fmt"
	"time"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

// defaultPluginHealthPath is the fallback health-check endpoint a plugin
// exposes over its unix socket. Matches the path used by the inline
// closure that previously lived in main().
const defaultPluginHealthPath = "/plugin/healthz"

// pluginHealthCheckTimeout bounds each individual probe attempt.
// Matches the 2-second timeout used by the inline closure.
const pluginHealthCheckTimeout = 2 * time.Second

// newPluginHealthCheck returns a closure that issues an HTTP GET against
// the plugin's per-plugin health endpoint over its unix socket. Used by
// pluginruntime.NewHealthLoop to periodically probe each started plugin.
//
// Behaviour is identical to the inline closure that previously lived in
// main(): resolves socket path via sup.SocketPathOf, applies the manifest's
// per-plugin health path override if set, and delegates the actual HTTP
// to pluginruntime.PreciseHealthCheck with a 2s timeout.
func newPluginHealthCheck(sup *pluginruntime.Supervisor) func(pluginID string) error {
	return func(pluginID string) error {
		socketPath := sup.SocketPathOf(pluginID)
		if socketPath == "" {
			return fmt.Errorf("plugin %s not started", pluginID)
		}
		healthPath := defaultPluginHealthPath
		if m := sup.ManifestOf(pluginID); m != nil && m.Runtime.HealthPath != "" {
			healthPath = m.Runtime.HealthPath
		}
		return pluginruntime.PreciseHealthCheck(socketPath, healthPath, pluginHealthCheckTimeout)()
	}
}
