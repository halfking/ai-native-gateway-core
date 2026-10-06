package main

// mobile_static.go — serves the web-mobile (Hyper 移动前端) build under
// /m/* and /m-assets/*. Mirrors maintain_static.go (the Gateway's second-SPA
// precedent) so both SPAs apply the same NET-010 static-leak protection.
//
// Mount contract (web-mobile/README.md + docs/UI规范/17 §7):
//   - /m-assets/* → MOBILE_WEB_DIST/assets/* (whitelisted exts only)
//   - /m/*        → file if present, else index.html (SPA fallback)
//   - /m/api/*, /m/v1/* → 404 (must not be shadowed by SPA)
//
// Unified entry (2026-10-04): with the mount configured, GET/HEAD "/" from
// a mobile-class User-Agent gets a 302 to /m/ — one port, one entry, the
// device picks the surface. Deep links keep their surface on purpose (a
// mobile hit on a PC route still renders the PC SPA); only the bare entry
// switches. web/index.html adds a client-side coarse-pointer guard for the
// inverse gap (iPadOS desktop-class UA); its ?desktop escape must match this
// side's "param present = stay on PC" reading. MOBILE_WEB_ENTRY_REDIRECT=
// false|0|off disables the redirect while keeping /m mounted.
//
// Configuration: MOBILE_WEB_DIST env wins; when unset we probe
// web-mobile/dist then web-mobile next to the process cwd (same two-candidate
// shape as config.defaultStaticDir: repo-root runs hit the former, release
// bundles that stage dist contents at web-mobile/ hit the latter).

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/streaming"
)

// Mount paths (mount contract above). They are constants rather than inline
// literals because they are a **cross-artifact contract** with the frontend
// build: web-mobile/vite.config.ts sets the production `base` to this same
// prefix, and every asset URL in the built index.html is derived from it.
// Changing one side without the other does not fail any Go test — it makes
// every asset 404 in production, i.e. a blank mobile page.
// mobile_mount_contract_test.go cross-checks the two, and (when a build is
// present) asserts every /m-assets/* reference in the built index.html really
// resolves through this handler.
const (
	mobileSPAMountPath    = "/m"
	mobileAssetsMountPath = "/m-assets/"
)

// MobileStaticHandler serves the web-mobile build.
type MobileStaticHandler struct {
	distDir   string
	assetsDir string
	indexFile string
	fs        http.Handler
}

// NewMobileStaticHandler returns nil when the dist directory (or its
// index.html) is missing so callers can treat a nil result as
// "mobile-web not configured".
func NewMobileStaticHandler(distDir string) *MobileStaticHandler {
	if distDir == "" {
		distDir = defaultMobileDistProbe()
	}
	if distDir == "" {
		return nil
	}
	info, err := os.Stat(distDir)
	if err != nil || !info.IsDir() {
		return nil
	}
	indexFile := filepath.Join(distDir, "index.html")
	if idxInfo, err := os.Stat(indexFile); err != nil || idxInfo.IsDir() {
		return nil
	}
	return &MobileStaticHandler{
		distDir:   distDir,
		assetsDir: filepath.Join(distDir, "assets"),
		indexFile: indexFile,
		fs:        http.FileServer(http.Dir(distDir)),
	}
}

// defaultMobileDistProbe checks ./web-mobile/dist then ./web-mobile relative
// to the process working directory (mirrors config.defaultStaticDir's
// web/dist→web two-candidate probing so release-bundle layouts are found).
func defaultMobileDistProbe() string {
	for _, candidate := range []string{filepath.Join("web-mobile", "dist"), "web-mobile"} {
		if info, err := os.Stat(filepath.Join(candidate, "index.html")); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// ServeAssets serves /m-assets/* from <dist>/assets/*.
func (h *MobileStaticHandler) ServeAssets(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, mobileAssetsMountPath)
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

// ServeSPA handles /m/*: real whitelisted file if present, else index.html.
func (h *MobileStaticHandler) ServeSPA(w http.ResponseWriter, r *http.Request) {
	upath := r.URL.Path
	if !strings.HasPrefix(upath, "/") {
		upath = "/" + upath
	}

	// /m/api/* and /m/v1/* are not SPA routes and must not be masked by
	// index.html (same rule as the maintain SPA).
	stripped := strings.TrimPrefix(upath, mobileSPAMountPath)
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

// newMobileGatewayHandler composes the legacy handler with the mobile static
// mount. /m and /m-assets are owned here; everything else passes through.
// With the mount configured it also owns the unified entry: GET/HEAD "/"
// (or /index.html) from a mobile-class UA redirects to /m/ — see the file
// header for the deep-link and kill-switch contract.
func newMobileGatewayHandler(next http.Handler, static *MobileStaticHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static == nil {
			next.ServeHTTP(w, r)
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, mobileAssetsMountPath):
			static.ServeAssets(w, r)
		case r.URL.Path == mobileSPAMountPath || strings.HasPrefix(r.URL.Path, mobileSPAMountPath+"/"):
			static.ServeSPA(w, r)
		case isMobileEntryRedirect(r):
			target := mobileSPAMountPath + "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			// no-store: the same URL must re-evaluate if the operator flips
			// MOBILE_WEB_ENTRY_REDIRECT or the device changes its UA class.
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, target, http.StatusFound)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// isMobileEntryRedirect reports whether this request is a bare-entry
// navigation that should land on the mobile surface. Narrow by design:
// GET/HEAD only, exact "/" or "/index.html" only, ?desktop=1 opts out, and
// the MOBILE_WEB_ENTRY_REDIRECT kill-switch (false|0|off) must not have
// disabled the switch. Deep links and API paths never redirect.
func isMobileEntryRedirect(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		return false
	}
	switch strings.ToLower(os.Getenv("MOBILE_WEB_ENTRY_REDIRECT")) {
	case "false", "0", "off":
		return false
	}
	if r.URL.Query().Has("desktop") {
		return false
	}
	return isMobileUserAgent(r.UserAgent())
}

// mobileUserAgentRe — device-class sniff for the unified entry. Matches the
// classic mobile tokens; desktop Chrome/Safari/Firefox/Edge UAs contain none
// of them. iPadOS 13+ masquerades as desktop Safari and is deliberately NOT
// matched here — web/public/entry-switch.js covers it client-side via
// pointer:coarse, because touch capability is invisible server-side.
var mobileUserAgentRe = regexp.MustCompile(
	`(?i)iphone|ipod|ipad|android|windows phone|iemobile|blackberry|bb10|opera mini|opera mobi|mobile safari`)

func isMobileUserAgent(ua string) bool {
	return ua != "" && mobileUserAgentRe.MatchString(ua)
}
