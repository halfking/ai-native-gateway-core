package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginControllerDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway.yaml")
	data := fmt.Sprintf("server:\n  host: 0.0.0.0\n  port: 8781\nplugins:\n  directory: %q\n  enabled: []\n", dir)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := pluginControllerDirectory(path, "", ":8781"); err != nil || got != dir {
		t.Fatalf("dir=%q err=%v", got, err)
	}
	for _, tc := range []struct{ path, dir, listen string }{{path, dir, ":8782"}, {path, filepath.Join(dir, "different"), ":8781"}, {path + ".missing", dir, ":8781"}} {
		if _, err := pluginControllerDirectory(tc.path, tc.dir, tc.listen); err == nil {
			t.Fatalf("invalid explicit config accepted: %+v", tc)
		}
	}
	if got, err := pluginControllerDirectory("", dir, ":8781"); err != nil || got != dir {
		t.Fatal("legacy directory changed")
	}
}

func TestConfigManagedEndpointsCannotRaceController(t *testing.T) {
	control := configManagedPluginControl{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /plugins/{id}/activate", makePluginActivateHandler(control))
	mux.HandleFunc("POST /plugins/{id}/deactivate", makePluginDeactivateHandler(control))
	mux.HandleFunc("POST /plugins/{id}/uninstall", makePluginUninstallHandler(control))
	mux.HandleFunc("POST /install", makePluginInstallHandler(control))
	for _, path := range []string{"/plugins/one/activate", "/plugins/one/deactivate", "/plugins/one/uninstall", "/install"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"plugin_id":"one","version":"2.0.0"}`))
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "plugin.managed_by_runtime_config") {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
}

func TestPluginControllerExplicitStartupErrors(t *testing.T) {
	if _, err := startPluginController(context.Background(), filepath.Join(t.TempDir(), "missing.yaml"), nil, nil); err == nil {
		t.Fatal("invalid runtime config fell back to legacy mode")
	}
}
