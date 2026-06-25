
package relay

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// StaticHandler serves the Vue SPA from the web/dist directory.
// It falls back to index.html for SPA client-side routing.
type StaticHandler struct {
	distDir string
	fs      http.Handler
}

// NewStaticHandler creates a new static file handler.
// If distDir is empty or doesn't exist, it returns nil.
func NewStaticHandler(distDir string) *StaticHandler {
	if distDir == "" {
		return nil
	}
	info, err := os.Stat(distDir)
	if err != nil || !info.IsDir() {
		return nil
	}
	return &StaticHandler{
		distDir: distDir,
		fs:      http.FileServer(http.Dir(distDir)),
	}
}

func (h *StaticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Clean the path
	upath := r.URL.Path
	if !strings.HasPrefix(upath, "/") {
		upath = "/" + upath
	}

	// Try to serve the file directly
	fpath := filepath.Join(h.distDir, filepath.Clean(upath))
	if info, err := os.Stat(fpath); err == nil && !info.IsDir() {
		h.fs.ServeHTTP(w, r)
		return
	}

	// For API paths, don't serve index.html
	if strings.HasPrefix(upath, "/v1/") || strings.HasPrefix(upath, "/api/") || strings.HasPrefix(upath, "/healthz") {
		http.NotFound(w, r)
		return
	}

	// SPA fallback: serve index.html for all non-file, non-API paths
	indexFile := filepath.Join(h.distDir, "index.html")
	if _, err := os.Stat(indexFile); err == nil {
		http.ServeFile(w, r, indexFile)
		return
	}

	http.NotFound(w, r)
}
