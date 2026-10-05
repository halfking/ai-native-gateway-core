package main

// mobile_static_test.go — /m 挂载行为门（UI规范 17 §7）：
// SPA fallback、assets 白名单、/m/api 防遮蔽、无配置时零影响透传。

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMobileDist(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><html><body>MOBILE-INDEX</body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("console.log('app')"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestMobileStaticNilWhenMissing(t *testing.T) {
	if h := NewMobileStaticHandler(filepath.Join(t.TempDir(), "does-not-exist")); h != nil {
		t.Fatalf("expected nil handler for missing dist, got %+v", h)
	}
	// 空目录（无 index.html）也不挂载
	if h := NewMobileStaticHandler(t.TempDir()); h != nil {
		t.Fatalf("expected nil handler for dist without index.html, got %+v", h)
	}
}

func TestMobileGatewayHandlerRouting(t *testing.T) {
	dist := writeMobileDist(t)
	static := NewMobileStaticHandler(dist)
	if static == nil {
		t.Fatal("expected configured handler")
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		w.Write([]byte("LEGACY"))
	})
	handler := newMobileGatewayHandler(next, static)

	cases := []struct {
		name       string
		path       string
		wantStatus int
		wantBody   string
	}{
		{"root mounts index", "/m/", http.StatusOK, "MOBILE-INDEX"},
		{"bare /m mounts index", "/m", http.StatusOK, "MOBILE-INDEX"},
		{"SPA fallback for client route", "/m/nodes", http.StatusOK, "MOBILE-INDEX"},
		{"assets served", "/m-assets/app.js", http.StatusOK, "console.log('app')"},
		{"asset traversal blocked", "/m-assets/../../etc/passwd", http.StatusNotFound, ""},
		{"unknown asset ext 404", "/m-assets/notes.txt", http.StatusNotFound, ""},
		{"/m/api must not be masked", "/m/api/keys", http.StatusNotFound, ""},
		{"/m/v1 must not be masked", "/m/v1/chat/completions", http.StatusNotFound, ""},
		{"legacy passthrough", "/healthz", http.StatusTeapot, "LEGACY"},
		{"api passthrough", "/api/auth/token", http.StatusTeapot, "LEGACY"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("GET %s: status = %d, want %d", tc.path, rec.Code, tc.wantStatus)
			}
			if tc.wantBody != "" && !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("GET %s: body = %q, want substring %q", tc.path, rec.Body.String(), tc.wantBody)
			}
		})
	}
}

func TestMobileGatewayHandlerNilStaticPassThrough(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})
	handler := newMobileGatewayHandler(next, nil)
	req := httptest.NewRequest(http.MethodGet, "/m/nodes", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "OK" {
		t.Fatalf("nil static must pass /m through, got %d %q", rec.Code, rec.Body.String())
	}
	// 统一入口依赖挂载存在：未配置 mobile dist 时移动端 UA 也直落 PC 端。
	mreq := httptest.NewRequest(http.MethodGet, "/", nil)
	mreq.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X)")
	mrec := httptest.NewRecorder()
	handler.ServeHTTP(mrec, mreq)
	if mrec.Code != http.StatusOK {
		t.Fatalf("nil static must not redirect the entry, got %d", mrec.Code)
	}
}

// TestMobileEntryRedirect — 统一入口分流矩阵（2026-10-04）。
// 入口只认 GET/HEAD + 精确 "/"（含 /index.html）+ 移动端 UA；
// 深链、POST、?desktop=1、env kill-switch 一律不切。
func TestMobileEntryRedirect(t *testing.T) {
	dist := writeMobileDist(t)
	static := NewMobileStaticHandler(dist)
	if static == nil {
		t.Fatal("expected configured handler")
	}
	const iphoneUA = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148 Safari/604.1"
	const desktopUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36"

	cases := []struct {
		name       string
		method     string
		path       string
		ua         string
		wantStatus int
		wantLoc    string
	}{
		{"mobile UA on / redirects", http.MethodGet, "/", iphoneUA, http.StatusFound, "/m/"},
		{"mobile UA on /index.html redirects", http.MethodGet, "/index.html", iphoneUA, http.StatusFound, "/m/"},
		{"query preserved", http.MethodGet, "/?view=nodes", iphoneUA, http.StatusFound, "/m/?view=nodes"},
		{"desktop UA on / passes through", http.MethodGet, "/", desktopUA, http.StatusTeapot, ""},
		{"empty UA passes through", http.MethodGet, "/", "", http.StatusTeapot, ""},
		{"android tablet UA redirects", http.MethodGet, "/", "Mozilla/5.0 (Linux; Android 13; SM-X710) AppleWebKit/537.36 Chrome/129 Safari/537.36", http.StatusFound, "/m/"},
		{"deep link keeps PC surface", http.MethodGet, "/login", iphoneUA, http.StatusTeapot, ""},
		{"api never redirects", http.MethodGet, "/api/auth/token", iphoneUA, http.StatusTeapot, ""},
		{"POST never redirects", http.MethodPost, "/", iphoneUA, http.StatusTeapot, ""},
		{"desktop=1 opts out", http.MethodGet, "/?desktop=1", iphoneUA, http.StatusTeapot, ""},
		// 逃生口语义 = 参数存在即停（Query().Has），与 entry-switch.js 的
		// /[?&]desktop(?:=|&|$)/ 对齐——两条腿在同一份查询串上必须同判。
		{"desktop=0 also opts out", http.MethodGet, "/?desktop=0", iphoneUA, http.StatusTeapot, ""},
		{"bare desktop param opts out", http.MethodGet, "/?desktop", iphoneUA, http.StatusTeapot, ""},
		{"unrelated param does not opt out", http.MethodGet, "/?nodesktop=1", iphoneUA, http.StatusFound, "/m/?nodesktop=1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := newMobileGatewayHandler(teapotNext(), static)
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.ua != "" {
				req.Header.Set("User-Agent", tc.ua)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("%s %s (ua=%q): status = %d, want %d", tc.method, tc.path, tc.ua, rec.Code, tc.wantStatus)
			}
			if got := rec.Header().Get("Location"); got != tc.wantLoc {
				t.Fatalf("%s %s: Location = %q, want %q", tc.method, tc.path, got, tc.wantLoc)
			}
			if tc.wantLoc != "" && rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("redirect must carry Cache-Control: no-store, got %q", rec.Header().Get("Cache-Control"))
			}
		})
	}
}

// TestMobileEntryRedirectKillSwitch — MOBILE_WEB_ENTRY_REDIRECT=false|0|off
// 关掉入口分流但 /m 挂载照常。
func TestMobileEntryRedirectKillSwitch(t *testing.T) {
	dist := writeMobileDist(t)
	static := NewMobileStaticHandler(dist)
	for _, val := range []string{"false", "0", "off", "FALSE", "Off"} {
		t.Setenv("MOBILE_WEB_ENTRY_REDIRECT", val)
		handler := newMobileGatewayHandler(teapotNext(), static)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 14; Pixel 8) Mobile Safari/537.36")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusTeapot {
			t.Fatalf("MOBILE_WEB_ENTRY_REDIRECT=%s: entry must pass through, got %d", val, rec.Code)
		}
	}
	t.Setenv("MOBILE_WEB_ENTRY_REDIRECT", "on")
	handler := newMobileGatewayHandler(teapotNext(), static)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 14; Pixel 8) Mobile Safari/537.36")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/m/" {
		t.Fatalf("MOBILE_WEB_ENTRY_REDIRECT=on must restore the redirect, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// TestMobileDefaultDistProbeLayouts — repo 布局（web-mobile/dist）与 release
// bundle 布局（web-mobile/ 直装 dist 内容）都必须被缺省探测命中；两者并存时
// dist 优先；都没有时不挂载。
func TestMobileDefaultDistProbeLayouts(t *testing.T) {
	root := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(cwd) })

	if got := defaultMobileDistProbe(); got != "" {
		t.Fatalf("empty root must not probe anything, got %q", got)
	}
	if err := os.MkdirAll(filepath.Join(root, "web-mobile"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "web-mobile", "index.html"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := defaultMobileDistProbe(); got != "web-mobile" {
		t.Fatalf("flat web-mobile layout must be probed, got %q", got)
	}
	if err := os.MkdirAll(filepath.Join(root, "web-mobile", "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	// dist 存在但缺 index.html：不算命中，仍回落 flat 布局。
	if got := defaultMobileDistProbe(); got != "web-mobile" {
		t.Fatalf("dist without index.html must not win, got %q", got)
	}
	if err := os.WriteFile(filepath.Join(root, "web-mobile", "dist", "index.html"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := defaultMobileDistProbe(); got != filepath.Join("web-mobile", "dist") {
		t.Fatalf("dist layout must win when present, got %q", got)
	}
}

func teapotNext() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		w.Write([]byte("LEGACY"))
	})
}
