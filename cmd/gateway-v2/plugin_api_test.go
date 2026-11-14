package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/handlers/pluginapi"
)

// TestPluginAPIIntegration tests plugin API routing in gateway-v2.
func TestPluginAPIIntegration(t *testing.T) {
	tmpDir := t.TempDir()
	createMinimalManifest(t, tmpDir, "test-plugin", "Test Plugin", "1.0.0")

	cfg := &v2Config{
		Listen:     ":0",
		PluginsDir: tmpDir,
	}
	deps := newDeps(cfg)
	handler := authChain(deps, cfg)

	t.Run("list plugins without trailing slash", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/plugins", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp pluginapi.PluginListResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if !resp.Success || resp.Total != 1 {
			t.Fatalf("expected success=true and total=1, got %+v", resp)
		}
		if len(resp.Data) != 1 || resp.Data[0].PluginID != "test-plugin" {
			t.Fatalf("expected test-plugin in data, got %+v", resp.Data)
		}
		// Verify ManifestPath is not leaked
		if resp.Data[0].Metadata.ManifestPath != "" {
			t.Errorf("manifest_path should be empty to avoid leaking server paths, got %q",
				resp.Data[0].Metadata.ManifestPath)
		}
	})

	t.Run("list plugins with trailing slash", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/plugins/", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var resp pluginapi.PluginListResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to decode: %v", err)
		}
		if resp.Total != 1 {
			t.Errorf("expected total=1, got %d", resp.Total)
		}
	})

	t.Run("manifests endpoint", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/plugins/manifests", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for /manifests, got %d", w.Code)
		}
		var resp pluginapi.PluginListResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to decode manifests: %v", err)
		}
		if resp.Total != 1 {
			t.Errorf("manifests should return same data as list, got total=%d", resp.Total)
		}
	})

	t.Run("get single plugin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/plugins/test-plugin", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var resp pluginapi.PluginListResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to decode: %v", err)
		}
		if len(resp.Data) != 1 || resp.Data[0].PluginID != "test-plugin" {
			t.Fatalf("expected test-plugin detail, got %+v", resp.Data)
		}
	})

	t.Run("unknown plugin returns 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/plugins/nonexistent", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404 for unknown plugin, got %d", w.Code)
		}
	})

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/plugins", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("expected 405 for POST, got %d", w.Code)
		}
	})
}

// TestPluginAPINotRegisteredWhenDirEmpty verifies that plugin API
// returns 404 when PluginsDir is not configured.
func TestPluginAPINotRegisteredWhenDirEmpty(t *testing.T) {
	cfg := &v2Config{
		Listen:     ":0",
		PluginsDir: "", // not configured
	}
	deps := newDeps(cfg)
	handler := authChain(deps, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/plugins", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 when plugins dir not configured, got %d", w.Code)
	}
}

// TestPluginDirPriority verifies LLM_GATEWAY_PLUGINS_DIR takes precedence
// over PLUGIN_DIR.
func TestPluginDirPriority(t *testing.T) {
	canonicalDir := t.TempDir()
	legacyDir := t.TempDir()

	createMinimalManifest(t, canonicalDir, "canonical-plugin", "Canonical", "1.0.0")
	createMinimalManifest(t, legacyDir, "legacy-plugin", "Legacy", "1.0.0")

	t.Setenv("LLM_GATEWAY_PLUGINS_DIR", canonicalDir)
	t.Setenv("PLUGIN_DIR", legacyDir)

	cfg := loadConfig()
	if cfg.PluginsDir != canonicalDir {
		t.Errorf("expected canonical dir %q, got %q", canonicalDir, cfg.PluginsDir)
	}

	// Verify only canonical plugin is loaded
	deps := newDeps(cfg)
	handler := authChain(deps, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/plugins", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	var resp pluginapi.PluginListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("expected 1 plugin from canonical dir, got %d", resp.Total)
	}
	if resp.Data[0].PluginID != "canonical-plugin" {
		t.Errorf("expected canonical-plugin, got %s", resp.Data[0].PluginID)
	}
}

// TestPluginAPIAuth verifies X-API-Key authentication.
func TestPluginAPIAuth(t *testing.T) {
	tmpDir := t.TempDir()
	createMinimalManifest(t, tmpDir, "auth-test", "Auth Test", "1.0.0")

	cfg := &v2Config{
		Listen:     ":0",
		APIKey:     "test-secret-key",
		PluginsDir: tmpDir,
	}
	deps := newDeps(cfg)
	handler := authChain(deps, cfg)

	t.Run("missing key returns 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/plugins", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 without key, got %d", w.Code)
		}
	})

	t.Run("correct key returns 200", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/plugins", nil)
		req.Header.Set("X-API-Key", "test-secret-key")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200 with correct key, got %d", w.Code)
		}
	})

	t.Run("wrong key returns 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/plugins", nil)
		req.Header.Set("X-API-Key", "wrong-key")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 with wrong key, got %d", w.Code)
		}
	})
}

// createMinimalManifest creates a minimal valid plugin manifest for testing.
func createMinimalManifest(t *testing.T, dir, pluginID, displayName, version string) {
	t.Helper()
	pluginDir := filepath.Join(dir, pluginID)
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	manifest := map[string]any{
		"schema_version": 2,
		"plugin_id":      pluginID,
		"display_name":   displayName,
		"plugin_version": version,
		"build_seq":      1,
		"channel":        "stable",
		"api":            "^1",
		"essential":      false,
		"requires":       []string{},
		"provides":       []string{pluginID + ".capability"},
		"gateway_compatibility": map[string]string{
			"min_version":  "0.0.0",
			"max_version":  "",
			"api_contract": "gateway-plugin-v2",
		},
		"runtime": map[string]any{
			"entrypoint":             "bin/plugin",
			"protocol":               "http-unix-socket",
			"health_path":            "/plugin/healthz",
			"handshake_path":         "/plugin/handshake",
			"shutdown_grace_seconds": 5,
		},
		"capabilities": []string{},
		"bindings":     []map[string]any{},
		"pages":        []map[string]any{},
		"web": map[string]string{
			"mount":     "iframe",
			"base_path": "/plugins/" + pluginID + "/",
			"entry":     "web/index.html",
		},
		"activation": map[string]any{
			"module_key":       pluginID,
			"license_required": false,
			"license": map[string]string{
				"mode": "open",
			},
		},
	}

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	manifestPath := filepath.Join(pluginDir, "plugin-manifest.json")
	if err := os.WriteFile(manifestPath, data, 0644); err != nil {
		t.Fatalf("write manifest failed: %v", err)
	}
}
