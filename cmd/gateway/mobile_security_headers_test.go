package main

// mobile_security_headers_test.go — R49-A1（2026-10-07）回归门。
//
// 缺陷原型：maintain 与 mobile 两个挂载此前包在中间件链**外**（先
// Build().Then(mux) 再往外叠网关），/m、/m-assets、/maintain 的响应全部
// 不带安全响应头（CSP/X-Frame-Options/nosniff/HSTS）、不进日志/追踪/
// Recovery——而 /m 是带登录态的运维面（main.go 的 desktop SPA 在链内，
// 同类页面两头待遇）。修复：publicSurface 先组合 maintain+mobile，再由
// 链包裹；AuthMiddleware bypass 名单补 /m、/m/、/m-assets/。
//
// 本门两层：
//  1. 行为面——按 main.go 同样方式组合链，断言 /m 响应带安全头且不被
//     全局 auth 401（bypass 生效）；
//  2. 接线面——main.go 源码必须以 Then(publicSurface) 收链，且不得再把
//     mobile 网关包在 finalHandler 链外（文本锚定，防回归挪位）。

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/middleware"
)

func mobileStaticForTest(t *testing.T) *MobileStaticHandler {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &MobileStaticHandler{
		distDir:   dir,
		assetsDir: filepath.Join(dir, "assets"),
		indexFile: filepath.Join(dir, "index.html"),
		fs:        http.FileServer(http.Dir(dir)),
	}
}

func chainForTest(t *testing.T, inner http.Handler) http.Handler {
	t.Helper()
	return middleware.NewBuilder().
		Add(middleware.NewRecoveryMiddleware()).
		Add(middleware.NewRequestIDMiddleware()).
		Add(middleware.NewAuthMiddleware("test-global-key")).
		Add(middleware.NewLoggingMiddleware()).
		Add(middleware.NewSecurityHeadersMiddleware()).
		Build().
		Then(inner)
}

// 行为面：/m 挂载进链后，安全头必须出现，且全局 auth 必须放行。
func TestMobileSPAResponsesCarrySecurityHeaders(t *testing.T) {
	mobile := mobileStaticForTest(t)
	inner := newMobileGatewayHandler(http.NotFoundHandler(), mobile)
	h := chainForTest(t, inner)

	cases := []struct {
		name string
		path string
	}{
		{"spa root", "/m/"},
		{"spa mount bare", "/m"},
		{"spa deep link falls to index", "/m/nodes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code == http.StatusUnauthorized {
				t.Fatalf("%s 被 auth 中间件 401：bypass 名单缺 /m 条目（R49-A1 回归）", tc.path)
			}
			for _, hdr := range []string{"X-Frame-Options", "X-Content-Type-Options", "Referrer-Policy"} {
				if rec.Header().Get(hdr) == "" {
					t.Fatalf("%s 响应缺 %s —— /m 挂载不在安全头链内（R49-A1 回归）", tc.path, hdr)
				}
			}
		})
	}
}

// 行为面：/m-assets 静态资产同样必须带安全头（同链）。
func TestMobileAssetsCarrySecurityHeaders(t *testing.T) {
	mobile := mobileStaticForTest(t)
	inner := newMobileGatewayHandler(http.NotFoundHandler(), mobile)
	h := chainForTest(t, inner)

	req := httptest.NewRequest(http.MethodGet, "/m-assets/assets/nope.js", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Fatal("/m-assets 被 auth 401：bypass 名单缺 /m-assets/ 条目（R49-A1 回归）")
	}
	if rec.Header().Get("X-Content-Type-Options") == "" {
		t.Fatal("/m-assets 响应缺 X-Content-Type-Options —— 挂载不在安全头链内（R49-A1 回归）")
	}
}

// 接线面：main.go 必须把 publicSurface 收进链内。
// 文本锚定的理由与 mobile_mount_contract_test 相同：挪回链外不会让任何
// 行为测试红（组合函数本身仍是正确实现），只有钉住调用点形状才能防回归。
func TestMobileMountSitsInsideMiddlewareChain(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if !strings.Contains(s, "Then(publicSurface)") {
		t.Fatal("main.go 中间件链没有以 Then(publicSurface) 收链 —— maintain/mobile 挂载可能被挪回链外（R49-A1）")
	}
	if strings.Contains(s, "newMobileGatewayHandler(finalHandler") ||
		strings.Contains(s, "newMaintainGatewayHandler(handler, maintainStatic)") {
		t.Fatal("main.go 又把 maintain/mobile 网关包在链外（finalHandler/handler 之上）—— R49-A1 回归：安全头与日志将不再覆盖 /m 与 /maintain")
	}
}
