package streaming

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// 2026-08-27: /maintain* must never be masked by the Gateway SPA fallback.
// Serving index.html at /maintain-api/healthz (HTTP 200 + text/html) made the
// frontend maintain probe report "available" and full-page jump to
// /maintain/home, producing an infinite reload flicker.
func TestStaticFallbackNeverMasksMaintainPaths(t *testing.T) {
	dist := t.TempDir()
	indexFile := filepath.Join(dist, "index.html")
	if err := os.WriteFile(indexFile, []byte("<html>gateway</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dist, "maintain-api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "maintain-api", "healthz.js"), []byte("shadow"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "maintenance.html"), []byte("<html>legacy</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler := NewStaticHandler(dist)
	if handler == nil {
		t.Fatal("NewStaticHandler returned nil")
	}

	for _, path := range []string{"/maintain", "/maintain/home", "/maintain-api/healthz", "/maintain-assets/app.js"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("path %s should 404 instead of SPA fallback, got %d", path, response.Code)
		}
	}

	// A similarly named Gateway SPA path remains outside the reserved boundary.
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/maintenance", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("boundary path /maintenance should use SPA fallback, got %d", response.Code)
	}
	// Plain SPA routes keep falling back to index.html.
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/providers", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("SPA fallback broken, got %d", response.Code)
	}
}
