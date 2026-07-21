package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

// writeTestManifest writes a minimal valid manifest for pluginID/version at
// path. The manifest passes pluginruntime.LoadManifest validate (non-empty
// handshake/health paths, api_contract=gateway-plugin-v1).
func writeTestManifest(t *testing.T, path, pluginID, version string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	doc := map[string]any{
		"schema_version": 1,
		"plugin_id":      pluginID,
		"plugin_version": version,
		"gateway_compatibility": map[string]any{
			"api_contract": "gateway-plugin-v1",
		},
		"runtime": map[string]any{
			"entrypoint":      "bin/" + pluginID,
			"protocol":        "http-unix-socket",
			"health_path":     "/plugin/healthz",
			"handshake_path":  "/plugin/handshake",
			"shutdown_grace_seconds": 5,
		},
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestScanPlugins_PopulatesRegistry(t *testing.T) {
	dir := t.TempDir()
	data, _ := os.ReadFile(filepath.Join("..", "..", "plugin-runtime", "testdata", "ai-session-manager.json"))
	os.MkdirAll(filepath.Join(dir, "ai-session-manager"), 0755)
	os.WriteFile(filepath.Join(dir, "ai-session-manager", "plugin-manifest.json"), data, 0644)

	reg := pluginruntime.NewRegistry()
	manifests, err := ScanPlugins(dir, reg)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(manifests) < 1 {
		t.Fatalf("expected at least 1 manifest, got %d", len(manifests))
	}
	entries := reg.NavEntries(pluginruntime.ViewerOpts{IsSuper: true})
	if len(entries) == 0 {
		t.Fatal("no nav entries after scan")
	}
	// the manifest has 2 pages with nav (sessions + settings); both visible to super
	if len(entries) != 2 {
		t.Fatalf("expected 2 nav entries, got %d", len(entries))
	}
}

func TestNavHandler_Wired(t *testing.T) {
	reg := pluginruntime.NewRegistry()
	reg.SetPlugin(&pluginruntime.PluginState{PluginID: "p", PluginVersion: "1", Status: "ready"})
	reg.SetNav("p", "1", []pluginruntime.Page{{Path: "s", Type: "data", Nav: &pluginruntime.Nav{Group: "g", LabelKey: "k"}}})
	h := pluginruntime.NavHandler(reg, func(*http.Request) pluginruntime.ViewerOpts {
		return pluginruntime.ViewerOpts{IsSuper: true}
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/plugin-nav", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
}

// TestPluginNavAuthGate verifies that /api/v1/plugin-nav is auth-gated:
// an unauthenticated request must be rejected (401), and a request with
// a valid super-admin JWT must reach NavHandler and return role-filtered nav.
// This is the P1 regression test for the auth-gate (P0 returned 200 to everyone).
func TestPluginNavAuthGate(t *testing.T) {
	// Wire the real extractor (admin.AuthContext → pluginruntime.AuthInfo).
	wirePluginAuthExtractor()

	reg := pluginruntime.NewRegistry()
	reg.SetPlugin(&pluginruntime.PluginState{PluginID: "p", PluginVersion: "1", Status: "ready"})
	reg.SetNav("p", "1", []pluginruntime.Page{{
		Path: "s", Type: "data",
		Nav: &pluginruntime.Nav{Group: "g", LabelKey: "k", Super: true},
	}})

	// Wrap NavHandler with admin.AdminMiddleware exactly as main.go does.
	const secret = "test-secret"
	handler := admin.AdminMiddleware(
		func(w http.ResponseWriter, r *http.Request) {
			pluginruntime.NavHandler(reg, pluginruntime.ViewerFromRequest).ServeHTTP(w, r)
		},
		nil, secret, // db=nil: AdminMiddleware only needs it for downstream lookups; auth check itself doesn't touch db
	)

	// 1) No auth → 401 (the regression fix).
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plugin-nav", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-auth: expected 401, got %d (body=%s)", rec.Code, rec.Body.String())
	}

	// 2) Valid super-admin JWT → 200 with nav items.
	tok, _, err := admin.SignToken(1, "default", "super", "super_admin", secret, false)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/plugin-nav", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("super-admin: expected 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestScanPluginsPrefersCurrentSymlink verifies that ScanPlugins prefers the
// P12 versioned layout <id>/current/plugin-manifest.json (symlink →
// <id>/<version>) over the legacy flat <id>/plugin-manifest.json, while still
// picking up flat-layout plugins that lack a current/ symlink. This is the
// regression test for P12 restart-after-install.
func TestScanPluginsPrefersCurrentSymlink(t *testing.T) {
	dir := t.TempDir()

	// Versioned: asm 0.2.0 reached through a `current` symlink.
	writeTestManifest(t, filepath.Join(dir, "asm", "0.2.0", "plugin-manifest.json"), "asm", "0.2.0")
	if err := os.Symlink(filepath.Join(dir, "asm", "0.2.0"), filepath.Join(dir, "asm", "current")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// Legacy flat layout (P0–P8 manual installs): manifest directly under <id>/.
	writeTestManifest(t, filepath.Join(dir, "legacy", "plugin-manifest.json"), "legacy", "0.1.0")

	reg := pluginruntime.NewRegistry()
	manifests, err := ScanPlugins(dir, reg)
	if err != nil {
		t.Fatalf("ScanPlugins: %v", err)
	}

	byVersion := map[string]string{}
	pathByID := map[string]string{}
	for _, m := range manifests {
		byVersion[m.PluginID] = m.PluginVersion
		pathByID[m.PluginID] = m.ManifestPath
	}
	if byVersion["asm"] != "0.2.0" {
		t.Errorf("asm version = %q, want 0.2.0 (via current symlink)", byVersion["asm"])
	}
	if byVersion["legacy"] != "0.1.0" {
		t.Errorf("legacy version = %q, want 0.1.0 (flat fallback)", byVersion["legacy"])
	}
	if !strings.Contains(pathByID["asm"], "current") {
		t.Errorf("asm manifest path = %q, should be under current/ so entrypoint resolves against the active version", pathByID["asm"])
	}
	if strings.Contains(pathByID["legacy"], "current") {
		t.Errorf("legacy manifest path = %q, should NOT be under current/ (flat layout)", pathByID["legacy"])
	}
}

// TestScanPluginsCurrentShadowsFlat ensures that when BOTH a current/ symlink
// and a flat manifest exist for the same plugin, the versioned current/ one
// wins (it is the source of truth after an installer-managed upgrade).
func TestScanPluginsCurrentShadowsFlat(t *testing.T) {
	dir := t.TempDir()

	// Flat layout says 0.1.0, but current/ symlink points at 0.3.0.
	writeTestManifest(t, filepath.Join(dir, "asm", "plugin-manifest.json"), "asm", "0.1.0")
	writeTestManifest(t, filepath.Join(dir, "asm", "0.3.0", "plugin-manifest.json"), "asm", "0.3.0")
	if err := os.Symlink(filepath.Join(dir, "asm", "0.3.0"), filepath.Join(dir, "asm", "current")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	reg := pluginruntime.NewRegistry()
	manifests, err := ScanPlugins(dir, reg)
	if err != nil {
		t.Fatalf("ScanPlugins: %v", err)
	}
	if len(manifests) != 1 {
		t.Fatalf("expected exactly 1 manifest (no double-counting), got %d", len(manifests))
	}
	if manifests[0].PluginVersion != "0.3.0" {
		t.Errorf("version = %q, want 0.3.0 (current/ must shadow flat)", manifests[0].PluginVersion)
	}
}
