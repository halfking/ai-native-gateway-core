package streaming

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
	upath := r.URL.Path
	if !strings.HasPrefix(upath, "/") {
		upath = "/" + upath
	}

	fpath := filepath.Join(h.distDir, filepath.Clean(upath))
	if info, err := os.Stat(fpath); err == nil && !info.IsDir() {
		h.fs.ServeHTTP(w, r)
		return
	}

	if strings.HasPrefix(upath, "/v1/") || strings.HasPrefix(upath, "/api/") || strings.HasPrefix(upath, "/healthz") {
		http.NotFound(w, r)
		return
	}

	indexFile := filepath.Join(h.distDir, "index.html")
	if _, err := os.Stat(indexFile); err == nil {
		http.ServeFile(w, r, indexFile)
		return
	}

	http.NotFound(w, r)
}
