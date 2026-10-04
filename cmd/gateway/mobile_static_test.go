package main

// mobile_static_test.go — MobileStaticHandler 契约测试（对齐
// maintain_static.go 的双 SPA 先例）：
//   - /m/* SPA fallback（index.html 服务未知路由）
//   - /m/api/*、/m/v1/* 404 防遮蔽
//   - /m-assets/* 白名单扩展外 404
//   - 缺省探测 web-mobile/dist；MOBILE_WEB_DIST 缺失/坏路径 → nil（不注册）

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
	assets := filepath.Join(dir, "assets")
	if err := os.MkdirAll(assets, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!DOCTYPE html><div id=app>mobile</div>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assets, "app.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assets, "app.css"), []byte("a{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "entry-switch.js"), []byte("// switch"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.env"), []byte("KEY=x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestMobileStaticHandler_SPAServed(t *testing.T) {
	h := NewMobileStaticHandler(writeMobileDist(t))
	if h == nil {
		t.Fatal("handler should be configured for a valid dist")
	}
	rec := httptest.NewRecorder()
	h.ServeSPA(rec, httptest.NewRequest(http.MethodGet, "/m/nodes", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("SPA fallback: want 200, got %d", rec.Code)
	}
	if body := rec.Body.String(); body != "<!DOCTYPE html><div id=app>mobile</div>" {
		t.Fatalf("SPA fallback should serve index.html verbatim, got %q", body)
	}
}

func TestMobileStaticHandler_RootIndexAndRealFiles(t *testing.T) {
	h := NewMobileStaticHandler(writeMobileDist(t))
	// /m/ 根与真实文件（entry-switch.js、assets/*.js）都直出。
	for _, path := range []string{"/m", "/m/", "/m/entry-switch.js", "/m/assets/app.js", "/m/assets/app.css"} {
		rec := httptest.NewRecorder()
		h.ServeSPA(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: want 200, got %d", path, rec.Code)
		}
	}
	// 内容断言（2026-10-04 实测教训：只断言 200 会被 SPA fallback 假绿——
	// 前缀未剥时真实文件全部回落 index.html，MIME text/html 让 SPA 启动
	// 失败，而状态码仍然 200）。
	rec := httptest.NewRecorder()
	h.ServeSPA(rec, httptest.NewRequest(http.MethodGet, "/m/entry-switch.js", nil))
	if body := rec.Body.String(); body != "// switch" {
		t.Fatalf("entry-switch.js must serve the real file, got %q", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "" && !strings.Contains(ct, "javascript") {
		t.Fatalf("entry-switch.js content-type: %q", ct)
	}
	rec = httptest.NewRecorder()
	h.ServeSPA(rec, httptest.NewRequest(http.MethodGet, "/m/assets/app.js", nil))
	if body := rec.Body.String(); body != "console.log(1)" {
		t.Fatalf("assets/app.js must serve the real file, got %q", body)
	}
	// 未带 Accept 时 http.ServeFile 对 .js 不主动嗅探 Content-Type；带浏览器
	// Accept 头必须得到 javascript MIME（浏览器 strict MIME 检查的硬约束）。
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/m/assets/app.js", nil)
	req.Header.Set("Accept", "*/*")
	h.ServeSPA(rec, req)
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("assets/app.js with browser Accept must be javascript, got %q", ct)
	}
}

func TestMobileStaticHandler_APIPathsNotMasked(t *testing.T) {
	h := NewMobileStaticHandler(writeMobileDist(t))
	for _, path := range []string{"/m/api/keys", "/m/v1/chat"} {
		rec := httptest.NewRecorder()
		h.ServeSPA(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: want 404 (never masked by SPA), got %d", path, rec.Code)
		}
	}
}

func TestMobileStaticHandler_AssetsWhitelist(t *testing.T) {
	h := NewMobileStaticHandler(writeMobileDist(t))
	// 白名单外扩展（.env）即便存在也 404（NET-010 同款防泄露）。
	rec := httptest.NewRecorder()
	h.ServeAssets(rec, httptest.NewRequest(http.MethodGet, "/m-assets/../secret.env", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("non-whitelisted ext: want 404, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeAssets(rec, httptest.NewRequest(http.MethodGet, "/m-assets/app.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("whitelisted asset: want 200, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeAssets(rec, httptest.NewRequest(http.MethodGet, "/m-assets/app.css", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("whitelisted css: want 200, got %d", rec.Code)
	}
}

func TestMobileStaticHandler_NilWhenUnconfigured(t *testing.T) {
	if got := NewMobileStaticHandler("/definitely/not/there"); got != nil {
		t.Fatal("missing dist should yield nil (routes unregistered)")
	}
	if got := NewMobileStaticHandler(""); got != nil {
		// 空路径走 cwd 探测；测试运行目录（cmd/gateway）没有 web-mobile/dist
		// → 应得 nil。若仓库根跑测试导致探测命中，跳过该断言分支。
		wd, _ := os.Getwd()
		if _, err := os.Stat(filepath.Join(wd, "web-mobile", "dist")); err != nil {
			t.Fatalf("empty distDir should probe cwd and be nil here, got %v", got)
		}
	}
}
