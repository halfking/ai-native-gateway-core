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

// 2026-10-28: 桌面 SPA shell 必须带 Cache-Control: no-cache。index.html 引用
// 的资源名是内容哈希，每次部署都换；shell 不带缓存头时浏览器按启发式缓存
// （无 Cache-Control + Last-Modified ⇒ ≈10% 年龄当新鲜期），部署后新开的
// 标签页会继续拿旧 shell → 引用已消失的旧 bundle（404 → 白屏，或「已修复的
// 行为看起来还没修」）。哈希资产自身保持 FileServer 原样（文件名不可变，
// no-cache 只会白白加一轮协商开销）。移动端 ServeSPA 同款约束先例。
func TestStaticSPAShellSendsNoCache(t *testing.T) {
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<html>shell</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dist, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "assets", "index-abc123.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler := NewStaticHandler(dist)
	if handler == nil {
		t.Fatal("NewStaticHandler returned nil")
	}

	// 根路径与 SPA 深链都由回退路径交付同一份 shell，必须 no-cache。
	for _, path := range []string{"/", "/live"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("path %s should serve the SPA shell, got %d", path, response.Code)
		}
		if got := response.Header().Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("SPA shell %s: Cache-Control = %q, want no-cache", path, got)
		}
	}

	// /index.html 按契约 301 到 ./（FileServer 的 index 重定向），shell 由
	// 根路径的回退分支交付 —— 不会绕过 no-cache。
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/index.html", nil))
	if response.Code != http.StatusMovedPermanently {
		t.Fatalf("path /index.html should redirect to ./, got %d", response.Code)
	}

	// 哈希资产是不可变的：保持无缓存头（FileServer 原样），不许误伤。
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/assets/index-abc123.js", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("hashed asset should serve directly, got %d", response.Code)
	}
	if got := response.Header().Get("Cache-Control"); got != "" {
		t.Fatalf("hashed asset: Cache-Control = %q, want empty", got)
	}
}
