package main

// mobile_static.go — serves the web-mobile SPA (Hyper-type mobile ops
// frontend) and its assets under /m/* and /m-assets/*. Mirrors the
// maintain_static.go dual-SPA precedent: the Gateway stays the single edge
// and the single port for both the desktop web/dist SPA and web-mobile.
//
// Design (NBJL docs/UI规范/17 + docs/plan/2026-10-04-web-mobile-hyper-unified-entry.md):
//   - /m-assets/* → MOBILE_WEB_DIST/assets/* (whitelisted exts only)
//   - /m/*        → file if present, else index.html (SPA fallback, base '/m/')
//   - /m/api/*, /m/v1/* → 404 (must not be shadowed by the SPA)
//
// Unified entry: the mobile SPA's /m/entry-switch.js bounces large
// (≥1280px) root entries back to '/', and the desktop web/ gains
// /entry-switch.js which forwards compact (<600px) entries to '/m'.
// The gateway itself performs no UA sniffing — window class is the truth.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/streaming"
)

// MobileStaticHandler serves the web-mobile build. It deliberately reuses
// streaming's extension whitelist so both SPAs apply the same NET-010
// static-leak protection.
type MobileStaticHandler struct {
	distDir   string
	assetsDir string
	indexFile string
}

// NewMobileStaticHandler returns nil when distDir is empty or missing,
// matching streaming.NewStaticHandler's convention so callers can treat a
// nil result as "web-mobile not configured" (deploy bundles without a
// mobile build keep working unchanged).
//
// When distDir is empty it mirrors config.defaultStaticDir's probe pattern:
// "web-mobile/dist" (dev checkout) then "web-mobile" (deploy bundles stage
// the dist contents flat, exactly like web/dist → bundle/web), each checked
// for a real index.html.
func NewMobileStaticHandler(distDir string) *MobileStaticHandler {
	if distDir == "" {
		for _, cand := range []string{filepath.Join("web-mobile", "dist"), "web-mobile"} {
			if info, err := os.Stat(filepath.Join(cand, "index.html")); err == nil && !info.IsDir() {
				distDir = cand
				break
			}
		}
	}
	if distDir == "" {
		return nil
	}
	info, err := os.Stat(distDir)
	if err != nil || !info.IsDir() {
		return nil
	}
	return &MobileStaticHandler{
		distDir:   distDir,
		assetsDir: filepath.Join(distDir, "assets"),
		indexFile: filepath.Join(distDir, "index.html"),
	}
}

// ServeAssets serves /m-assets/* from <dist>/assets/*. Anything that isn't
// a whitelisted static extension 404s.
func (h *MobileStaticHandler) ServeAssets(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/m-assets/")
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

// ServeSPA handles /m/*: serve a real file if it exists (after stripping the
// /m mount prefix — the SPA is built with vite base '/m/', so its asset
// URLs are /m/assets/...), otherwise fall back to index.html for
// client-side routing. API-ish paths under /m/ never fall back — they 404
// so a typo can't masquerade as a successful SPA route that then calls the
// wrong backend.
func (h *MobileStaticHandler) ServeSPA(w http.ResponseWriter, r *http.Request) {
	upath := r.URL.Path
	if !strings.HasPrefix(upath, "/") {
		upath = "/" + upath
	}

	// Reject /m/api/* and /m/v1/* — not SPA routes, must not be masked by
	// index.html (same guard as the maintain SPA).
	rel := strings.TrimPrefix(upath, "/m")
	if rel == "" {
		rel = "/"
	}
	if strings.HasPrefix(rel, "/api/") || strings.HasPrefix(rel, "/v1/") {
		http.NotFound(w, r)
		return
	}

	fpath := filepath.Join(h.distDir, filepath.Clean(rel))
	if info, err := os.Stat(fpath); err == nil && !info.IsDir() {
		if !streaming.IsAllowedStaticExt(filepath.Ext(fpath)) {
			http.NotFound(w, r)
			return
		}
		// ServeFile（而非 FileServer）：FileServer 按 r.URL.Path（含 /m 前缀）
		// 找文件，会绕过真实路径；ServeFile 直接用已剥前缀的 fpath。
		http.ServeFile(w, r, fpath)
		return
	}

	if _, err := os.Stat(h.indexFile); err == nil {
		http.ServeFile(w, r, h.indexFile)
		return
	}
	http.NotFound(w, r)
}
