package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestWaitForPluginReady_RetriesUntilSocketListens(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "asm-sock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "p.sock")
	go func() {
		time.Sleep(250 * time.Millisecond)
		ln, err := net.Listen("unix", sock)
		if err != nil {
			return
		}
		mux := http.NewServeMux()
		write := func(w http.ResponseWriter, body any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(body)
		}
		mux.HandleFunc("/plugin/handshake", func(w http.ResponseWriter, _ *http.Request) {
			write(w, map[string]any{
				"plugin_id": "ai-session-manager", "plugin_version": "0.2.0",
				"api_contract": "gateway-plugin-v2", "status": "ready",
			})
		})
		mux.HandleFunc("/plugin/healthz", func(w http.ResponseWriter, _ *http.Request) {
			write(w, map[string]any{"status": "ready"})
		})
		_ = http.Serve(ln, mux)
	}()
	manifest := &pluginruntime.Manifest{
		PluginID: "ai-session-manager", PluginVersion: "0.2.0",
		GatewayCompatibility: pluginruntime.GatewayCompatibility{APIContract: "gateway-plugin-v2"},
		Runtime: pluginruntime.Runtime{
			HandshakePath: "/plugin/handshake", HealthPath: "/plugin/healthz",
		},
	}
	if err := waitForPluginReady(context.Background(), sock, manifest); err != nil {
		t.Fatal(err)
	}
}
