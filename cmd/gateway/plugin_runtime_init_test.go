package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

func TestScanPlugins_PopulatesRegistry(t *testing.T) {
	dir := t.TempDir()
	data, _ := os.ReadFile(filepath.Join("..", "..", "plugin-runtime", "testdata", "ai-session-manager.json"))
	os.MkdirAll(filepath.Join(dir, "ai-session-manager"), 0755)
	os.WriteFile(filepath.Join(dir, "ai-session-manager", "plugin-manifest.json"), data, 0644)

	reg := pluginruntime.NewRegistry()
	if err := ScanPlugins(dir, reg); err != nil {
		t.Fatalf("scan: %v", err)
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
