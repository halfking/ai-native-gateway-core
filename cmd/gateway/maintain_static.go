package main

// maintain_static.go — serves the maintain-web SPA and its assets under
// /maintain/* and /maintain-assets/*. The Gateway remains the single edge
// for both SPAs; this handler owns the maintain-web dist directory while
// streaming.StaticHandler continues to own the Gateway's web/dist.
//
// Design (docs/优化v1/01-同域前端与统一认证 §3, §5):
//   - /maintain-assets/* → MAINTAIN_WEB_DIST/assets/* (whitelisted exts only)
//   - /maintain/*        → file if present, else index.html (SPA fallback)
//   - /maintain/api/*, /maintain/v1/* → 404 (must not be shadowed by SPA)

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/streaming"
)

// MaintainStaticHandler serves the maintain-web build. It deliberately
// reuses streaming's extension whitelist so the two SPAs apply the same
// NET-010 static-leak protection.
type MaintainStaticHandler struct {
	distDir  string
	assetsDir string
	indexFile string
	fs       http.Handler
}

// NewMaintainStaticHandler returns nil when distDir is empty or missing,
// matching streaming.NewStaticHandler's convention so callers can treat a
// nil result as "maintain-web not configured".
func NewMaintainStaticHandler(distDir string) *MaintainStaticHandler {
	if distDir == "" {
		return nil
	}
	info, err := os.Stat(distDir)
	if err != nil || !info.IsDir() {
		return nil
	}
	return &MaintainStaticHandler{
		distDir:   distDir,
		assetsDir: filepath.Join(distDir, "assets"),
		indexFile: filepath.Join(distDir, "index.html"),
		fs:        http.FileServer(http.Dir(distDir)),
	}
}

// ServeAssets serves /maintain-assets/* from <dist>/assets/*. Anything that
// isn't a whitelisted static extension 404s.
func (h *MaintainStaticHandler) ServeAssets(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/maintain-assets/")
	rel = strings.TrimPrefix(rel, "assets/")
	fpath := filepath.Join(h.assetsDir, filepath.Clean("/"+rel))
	if !streaming.IsAllowedStaticExt(filepath.Ext(fpath)) {
		http.NotFound(w, r)
		return
	}
	if info, err := os.Stat(fpath); err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, fpath)
}

// ServeSPA handles /maintain/*: serve a real file if it exists, otherwise
// fall back to index.html for client-side routing. API-ish paths under
// /maintain/ never fall back — they 404 so a typo can't masquerade as a
// successful SPA route that then calls the wrong backend.
func (h *MaintainStaticHandler) ServeSPA(w http.ResponseWriter, r *http.Request) {
	upath := r.URL.Path
	if !strings.HasPrefix(upath, "/") {
		upath = "/" + upath
	}

	// Reject /maintain/api/* and /maintain/v1/* — these are not SPA routes
	// and must not be masked by index.html.
	stripped := strings.TrimPrefix(upath, "/maintain")
	if strings.HasPrefix(stripped, "/api/") || strings.HasPrefix(stripped, "/v1/") {
		http.NotFound(w, r)
		return
	}

	fpath := filepath.Join(h.distDir, filepath.Clean(upath))
	if info, err := os.Stat(fpath); err == nil && !info.IsDir() {
		if !streaming.IsAllowedStaticExt(filepath.Ext(fpath)) {
			http.NotFound(w, r)
			return
		}
		h.fs.ServeHTTP(w, r)
		return
	}

	if _, err := os.Stat(h.indexFile); err == nil {
		http.ServeFile(w, r, h.indexFile)
		return
	}
	http.NotFound(w, r)
}
