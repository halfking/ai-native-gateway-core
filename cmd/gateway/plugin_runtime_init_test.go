package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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
