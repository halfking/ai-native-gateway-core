package pluginruntime

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginStaticHandler_ServesIndex(t *testing.T) {
	dir := t.TempDir()
	webDir := filepath.Join(dir, "ai-session-manager", "web")
	os.MkdirAll(webDir, 0755)
	os.WriteFile(filepath.Join(webDir, "index.html"), []byte("<h1>plugin web</h1>"), 0644)

	h := PluginStaticHandler(dir)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/plugins/ai-session-manager/index.html", nil)
	req.SetPathValue("pluginId", "ai-session-manager")
	req.SetPathValue("rest", "index.html")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "plugin web") {
		t.Fatalf("body = %s", body)
	}
}

func TestPluginStaticHandler_ServesIndexForDirPath(t *testing.T) {
	dir := t.TempDir()
	webDir := filepath.Join(dir, "asm", "web")
	os.MkdirAll(webDir, 0755)
	os.WriteFile(filepath.Join(webDir, "index.html"), []byte("home"), 0644)

	h := PluginStaticHandler(dir)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/plugins/asm/", nil)
	req.SetPathValue("pluginId", "asm")
	req.SetPathValue("rest", "") // empty rest → serve index.html
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "home") {
		t.Fatalf("body = %s", body)
	}
}

func TestPluginStaticHandler_RejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	h := PluginStaticHandler(dir)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/plugins/..%2f..%2fetc/passwd", nil)
	req.SetPathValue("pluginId", "..")
	req.SetPathValue("rest", "../../etc/passwd")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestPluginStaticHandler_NotFound(t *testing.T) {
	dir := t.TempDir()
	h := PluginStaticHandler(dir)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/plugins/unknown/index.html", nil)
	req.SetPathValue("pluginId", "unknown")
	req.SetPathValue("rest", "index.html")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}
