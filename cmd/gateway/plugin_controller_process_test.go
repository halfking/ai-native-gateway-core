package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pluginruntime "github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

// The production initializer must enforce the same readiness contract as the
// controller. This helper is a real subprocess, with the supervisor's own env.
func TestPluginControllerProductionHelper(t *testing.T) {
	socket := os.Getenv("AI_SESSION_MANAGER_PLUGIN_SOCKET")
	if socket == "" {
		return
	}
	m, err := pluginruntime.LoadManifest(os.Getenv("AI_SESSION_MANAGER_MANIFEST"))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	var markerOnce sync.Once
	mux.HandleFunc("/handshake", func(w http.ResponseWriter, r *http.Request) {
		status, version := "ready", m.PluginVersion
		if m.PluginID == "starting" {
			status = "starting"
		}
		if m.PluginID == "empty" {
			version = ""
		}
		response := map[string]string{"plugin_id": m.PluginID, "plugin_version": version, "api_contract": "gateway-plugin-v1", "status": status}
		data, err := json.Marshal(response)
		if err != nil {
			t.Error(err)
			return
		}
		markerOnce.Do(func() {
			if err := os.WriteFile(os.Getenv("GATEWAY_CONTROLLER_TEST_HANDSHAKE"), data, 0600); err != nil {
				t.Error(err)
			}
		})
		_, _ = w.Write(data)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	if err := http.Serve(listener, mux); err != nil {
		t.Fatal(err)
	}
}

func TestPluginControllerProductionReadiness(t *testing.T) {
	for _, id := range []string{"ready", "starting", "empty"} {
		t.Run(id, func(t *testing.T) {
			dir := t.TempDir()
			bundle := filepath.Join(dir, id)
			if err := os.Mkdir(bundle, 0700); err != nil {
				t.Fatal(err)
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(dir, "handshake.json")
			script := fmt.Sprintf("#!/bin/sh\nexport GATEWAY_CONTROLLER_TEST_HANDSHAKE='%s'\nexec '%s' -test.run='^TestPluginControllerProductionHelper$'\n", strings.ReplaceAll(marker, "'", "'\\''"), strings.ReplaceAll(exe, "'", "'\\''"))
			if err := os.WriteFile(filepath.Join(bundle, "run"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			manifest := map[string]any{"plugin_id": id, "plugin_version": "1.0.0", "gateway_compatibility": map[string]string{"api_contract": "gateway-plugin-v1"}, "runtime": map[string]string{"entrypoint": "run", "protocol": "http-unix-socket", "handshake_path": "/handshake", "health_path": "/healthz"}}
			data, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bundle, "plugin-manifest.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "gateway.yaml")
			yaml := fmt.Sprintf("server:\n  host: localhost\n  port: 8080\nplugins:\n  directory: %q\n  enabled: [%s]\n", dir, id)
			if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c, err := startPluginController(ctx, path, nil, nil)
			if c != nil {
				defer c.Close()
			}
			handshakeData, markerErr := os.ReadFile(marker)
			if markerErr != nil {
				t.Fatalf("child never completed a handshake: %v (startup: %v)", markerErr, err)
			}
			var hs pluginruntime.HandshakeResponse
			if decodeErr := json.Unmarshal(handshakeData, &hs); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			wantStatus, wantVersion := "ready", "1.0.0"
			if id == "starting" {
				wantStatus = "starting"
			}
			if id == "empty" {
				wantVersion = ""
			}
			if hs.Status != wantStatus || hs.PluginVersion != wantVersion {
				t.Fatalf("unexpected child handshake: %+v", hs)
			}
			if id != "ready" {
				if err == nil {
					t.Fatal("production initializer accepted an unready or unversioned plugin")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.BaseURL(id) == "" {
				t.Fatal("ready process has no route")
			}
		})
	}
}
