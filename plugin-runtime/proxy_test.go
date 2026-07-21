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

func TestPluginStaticHandler_ServesFromCurrentSymlink(t *testing.T) {
	dir := t.TempDir()
	// versioned layout (P12+): <dir>/asm/0.2.0/web/index.html + current symlink
	versioned := filepath.Join(dir, "asm", "0.2.0")
	os.MkdirAll(filepath.Join(versioned, "web"), 0o755)
	os.WriteFile(filepath.Join(versioned, "web", "index.html"), []byte("<html>versioned</html>"), 0o644)
	os.Symlink(versioned, filepath.Join(dir, "asm", "current"))

	handler := PluginStaticHandler(dir)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/plugins/asm/index.html", nil)
	req.SetPathValue("pluginId", "asm")
	req.SetPathValue("rest", "index.html")
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "<html>versioned</html>" {
		t.Errorf("body = %q, want versioned content", rec.Body.String())
	}
}

func TestPluginStaticHandler_FallsBackToFlatWhenNoCurrent(t *testing.T) {
	dir := t.TempDir()
	// flat layout (P0-P8 legacy): <dir>/legacy/web/index.html, NO current symlink
	os.MkdirAll(filepath.Join(dir, "legacy", "web"), 0o755)
	os.WriteFile(filepath.Join(dir, "legacy", "web", "index.html"), []byte("<html>flat</html>"), 0o644)

	handler := PluginStaticHandler(dir)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/plugins/legacy/index.html", nil)
	req.SetPathValue("pluginId", "legacy")
	req.SetPathValue("rest", "index.html")
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "<html>flat</html>" {
		t.Errorf("body = %q, want flat content", rec.Body.String())
	}
}
