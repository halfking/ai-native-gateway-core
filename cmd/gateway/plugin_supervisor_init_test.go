package main

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

func TestScanAndStartPlugins_DoesNotPanicOnBadEntrypoint(t *testing.T) {
	sup := pluginruntime.NewSupervisor(pluginruntime.SupervisorConfig{SocketDir: t.TempDir()})
	m := &pluginruntime.Manifest{PluginID: "x", PluginVersion: "1", Runtime: pluginruntime.Runtime{Entrypoint: "/nonexistent/bin"}}
	// Should log a warning and continue, not panic.
	ScanAndStartPlugins(sup, t.TempDir(), []*pluginruntime.Manifest{m})
}
