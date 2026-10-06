package main

// mobile_static_servespa_asset_regression_test.go — 整合轮补的门。
//
// 背景（来自 feat/web-mobile-hyper 首部署实测记录，docs/plan/2026-10-04-
// web-mobile-hyper-unified-entry.md §2.6 缺陷 1）：ServeSPA 用
// filepath.Join(distDir, filepath.Clean(upath)) 拼真实路径时，upath 仍带
// "/m" 挂载前缀。filepath.Join 把 "/m/assets/app.js" 当相对段处理，实际
// stat 的是 distDir/m/assets/app.js —— 而 vite 产物在 distDir/assets/。
// 于是资源请求永远 stat 不到，回落 index.html，且因为回落路径用的是
// http.ServeFile(index.html)，Content-Type 变成 text/html，浏览器按
// module script 加载直接失败，整个 /m SPA 起不来。
//
// 既有 TestMobileGatewayHandlerRouting 只打 "/m-assets/app.js"（ServeAssets
// 那条腿，路径拼接本来就是对的），从未打 "/m/assets/*"（ServeSPA 这条腿），
// 所以这条缺陷在 main 上是绿的 —— 门缺失，不是判据错。
//
// 这条测试断言的是**真实文件内容与 MIME**，不是 200：只断言 200 正是当初
// 假绿的成因。

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditMobileServeSPAServesRealAssetContent(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	const wantBody = "console.log('app')"
	if err := os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte(wantBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("MOBILE-INDEX"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := NewMobileStaticHandler(dir)
	if h == nil {
		t.Fatal("NewMobileStaticHandler returned nil for a dir containing index.html")
	}

	rec := httptest.NewRecorder()
	h.ServeSPA(rec, httptest.NewRequest(http.MethodGet, "/m/assets/app.js", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// 回落 index.html 时 MIME 是 text/html；真实 .js 资源必须带 JS MIME。
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type = %q, want it to contain \"javascript\"；若为 text/html 说明请求被 index.html 回落遮蔽（/m 前缀未从真实路径上剥掉）", ct)
	}
	if got := rec.Body.String(); got != wantBody {
		t.Errorf("body = %q, want %q —— SPA fallback 遮蔽了真实静态资源", got, wantBody)
	}
}

// 前缀剥离不能顺手引入穿越：/m/../../x 必须 404，且不得命中 dist 外的文件。
func TestAuditMobileServeSPARejectsTraversalOutsideDist(t *testing.T) {
	root := t.TempDir()
	dist := filepath.Join(root, "dist")
	if err := os.MkdirAll(filepath.Join(dist, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("MOBILE-INDEX"), 0o644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP-SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := NewMobileStaticHandler(dist)
	if h == nil {
		t.Fatal("NewMobileStaticHandler returned nil for a dir containing index.html")
	}

	rec := httptest.NewRecorder()
	h.ServeSPA(rec, httptest.NewRequest(http.MethodGet, "/m/../secret.txt", nil))

	if rec.Code == http.StatusOK {
		t.Errorf("status = 200 with body %q —— ServeSPA served a file outside distDir", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "TOP-SECRET") {
		t.Fatal("ServeSPA leaked a file from outside distDir")
	}
}

// web-mobile 的首帧脚本（public/entry-switch.js）现在走 /m/entry-switch.js ——
// index.html 里的内联 <script> 因 CSP 无 'unsafe-inline' 被拦，主题防 FOUC 与
// large→/ 反向切换都改由这个外链文件承担。它一旦取不到就会回落 index.html，
// 于是「主题首帧 + 反向入口」两条能力静默消失且无任何报错。故单独钉一条。
func TestAuditMobileServeSPAServesFirstFrameEntrySwitch(t *testing.T) {
	dir := t.TempDir()
	const wantBody = "/* entry-switch.js — web-mobile 首帧脚本 */"
	if err := os.WriteFile(filepath.Join(dir, "entry-switch.js"), []byte(wantBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("MOBILE-INDEX"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := NewMobileStaticHandler(dir)
	if h == nil {
		t.Fatal("NewMobileStaticHandler returned nil for a dir containing index.html")
	}

	rec := httptest.NewRecorder()
	h.ServeSPA(rec, httptest.NewRequest(http.MethodGet, "/m/entry-switch.js", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type = %q, want it to contain \"javascript\"", ct)
	}
	if got := rec.Body.String(); got != wantBody {
		t.Errorf("body = %q, want %q —— 首帧脚本被 index.html 回落遮蔽，主题防 FOUC 与反向入口切换会静默失效", got, wantBody)
	}
}
