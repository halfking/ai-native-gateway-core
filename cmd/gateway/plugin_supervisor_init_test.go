package main

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

func TestScanAndStartPlugins_DoesNotPanicOnBadEntrypoint(t *testing.T) {
	sup := pluginruntime.NewSupervisor(pluginruntime.SupervisorConfig{SocketDir: t.TempDir()})
	m := &pluginruntime.Manifest{PluginID: "x", PluginVersion: "1", Runtime: pluginruntime.Runtime{Entrypoint: "/nonexistent/bin"}}
	// Should log a warning and continue, not panic. Returns a base map (empty
	// when no plugin starts successfully — the bad-entrypoint case here).
	bases := ScanAndStartPlugins(sup, t.TempDir(), []*pluginruntime.Manifest{m})
	if len(bases) != 0 {
		t.Fatalf("expected empty bases map for bad entrypoint, got %v", bases)
	}
}
