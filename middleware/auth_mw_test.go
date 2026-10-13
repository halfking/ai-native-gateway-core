package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthMiddleware_BypassesAPIAdminPaths(t *testing.T) {
	// PR-3 (2026-06-30): /api/* must bypass global API-key auth so
	// cookie-authenticated browser sessions reach admin.AdminMiddleware
	// (which independently handles Bearer JWT / cookie / API key).
	called := false
	mw := NewAuthMiddleware("secret-key")
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	cases := []string{
		"/api/auth/token",
		"/api/auth/logout",
		// 登录验证码必须在**未登录**时就能取到（admin/auth_captcha.go）：
		// 否则「连续失败 N 次后才要验证码」自相矛盾——用户被要求验证，
		// 却因为没有登录态而拿不到验证码，永远登不进去。
		"/api/auth/captcha",
		"/api/auth/me",
		"/api/auth/change-password",
		"/api/users",
		"/api/users/42",
		"/api/agents/health",
		"/api/admin/probe/cache-state",
		"/api/routing/overview",
		"/api/telemetry/quality/heatmap",
	}

	for _, path := range cases {
		called = false
		req := httptest.NewRequest(http.MethodGet, path, nil)
		// NO Authorization header (cookie-only auth)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if !called {
			t.Errorf("%s: handler should be called (bypass expected), got status=%d", path, rr.Code)
			continue
		}
		if rr.Code == http.StatusUnauthorized {
			t.Errorf("%s: should NOT return 401, got %d body=%s", path, rr.Code, rr.Body.String())
		}
	}
}

func TestAuthMiddleware_StillRequiresBearerForDataPaths(t *testing.T) {
	// /v1/* data paths (chat/messages/responses) must STILL require
	// the global API key.
	called := false
	mw := NewAuthMiddleware("secret-key")
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	cases := []string{
		"/v1/chat/completions",
		"/v1/messages",
		"/v1/responses",
		"/v1/models",
	}

	for _, path := range cases {
		called = false
		req := httptest.NewRequest(http.MethodPost, path, nil)
		// NO Authorization header
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if called {
			t.Errorf("%s: handler should NOT be called without API key, got status=%d", path, rr.Code)
		}
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s: expected 401, got %d body=%s", path, rr.Code, rr.Body.String())
		}
	}
}

func TestAuthMiddleware_AcceptsValidBearerForDataPaths(t *testing.T) {
	called := false
	mw := NewAuthMiddleware("secret-key")
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer secret-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !called {
		t.Fatalf("handler should be called with valid Bearer, got status=%d", rr.Code)
	}
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestAuthMiddleware_RejectsInvalidBearer(t *testing.T) {
	called := false
	mw := NewAuthMiddleware("secret-key")
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if called {
		t.Fatalf("handler should NOT be called with wrong API key")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestAuthMiddleware_BypassesHealthAndMetrics(t *testing.T) {
	called := false
	mw := NewAuthMiddleware("secret-key")
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	for _, path := range []string{"/healthz", "/healthz/full", "/readyz", "/version", "/metrics", "/"} {
		called = false
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if !called {
			t.Errorf("%s: handler should be called (bypass expected), got status=%d", path, rr.Code)
		}
	}
}

func TestAuthMiddlewareRejectionPreservesRequestID(t *testing.T) {
	h := NewRequestIDMiddleware().Wrap(NewAuthMiddleware("secret-key").Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	if rr.Code != http.StatusUnauthorized || rr.Header().Get("X-Request-Id") == "" {
		t.Fatalf("status=%d request_id=%q", rr.Code, rr.Header().Get("X-Request-Id"))
	}
}

func TestAuthMiddleware_AcceptsXApiKeyStaticKey(t *testing.T) {
	// Anthropic Messages clients (Claude Code with ANTHROPIC_API_KEY) send
	// x-api-key instead of Authorization: Bearer. The static deployed key
	// delivered via x-api-key must pass the gate exactly like a Bearer.
	called := false
	mw := NewAuthMiddleware("secret-key")
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("x-api-key", "secret-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !called {
		t.Fatalf("handler should be called with valid x-api-key, got status=%d", rr.Code)
	}
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestAuthMiddleware_PassesSkXApiKeyToVerifier(t *testing.T) {
	// sk-* data-plane keys via x-api-key are not validated here; they pass
	// through to the per-request DB verifier (same contract as the Bearer
	// sk-* branch).
	called := false
	mw := NewAuthMiddleware("secret-key")
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("x-api-key", "sk-user-data-plane-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !called {
		t.Fatalf("handler should be called for sk-* x-api-key passthrough, got status=%d", rr.Code)
	}
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestAuthMiddleware_RejectsNonSkXApiKey(t *testing.T) {
	called := false
	mw := NewAuthMiddleware("secret-key")
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("x-api-key", "wrong-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if called {
		t.Fatalf("handler should NOT be called with wrong x-api-key")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAuthMiddleware_BearerTakesPrecedenceOverXApiKey(t *testing.T) {
	// When both headers are present, Authorization wins — matching the
	// precedence of extractBearerToken on the handler side. A malformed
	// Bearer must NOT be rescued by a valid x-api-key static key.
	called := false
	mw := NewAuthMiddleware("secret-key")
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	req.Header.Set("x-api-key", "secret-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if called {
		t.Fatalf("handler should NOT be called when Bearer is invalid even if x-api-key is valid")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestAuthMiddleware_SPANavigationDeepLinksBypass(t *testing.T) {
	// 2026-10-11 白页事故：控制台 SPA 从 /admin/* 旧路由迁到顶层路由后，
	// 深链/刷新（/dashboard 等）不带 API key，在全局门前 401 missing_key，
	// 静态处理器的 index.html 回退永远走不到。浏览器导航（Accept:
	// text/html）到非数据面路径必须放行给 mux。
	called := false
	mw := NewAuthMiddleware("secret-key")
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	cases := []string{
		"/dashboard",
		"/login",
		"/bootstrap",
		"/forbidden",
		"/routing-v2/credentials",
		"/tenant/models",
		"/keys/42",
		"/user-agreement.html",
	}

	for _, path := range cases {
		called = false
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		// NO Authorization header — 深链导航本就无 key
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if !called {
			t.Errorf("%s: SPA navigation should bypass auth, got status=%d body=%s", path, rr.Code, rr.Body.String())
		}
	}
}

func TestAuthMiddleware_SPABypassNeverCoversDataPlane(t *testing.T) {
	// 数据面 / 自守卫前缀不参与 SPA 导航放行：即使 Accept 带上 text/html，
	// 无 key 的 /v1/*、/mock/*、/plugins/* 仍必须 401 missing_key。
	mw := NewAuthMiddleware("secret-key")
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("%s: handler must not be reached for data-plane path", r.URL.Path)
	}))

	// 注：/mock/*、/plugins/* 在既有 BypassRule.PathPrefixes 里本就放行
	//（自守卫子系统，端点自带 auth.MockEndpoint 等守卫），不属本改动守卫
	// 范围；数据面前门只断言 /v1*。
	cases := []string{
		"/v1/chat/completions",
		"/v1/messages",
		"/v1",
		"/v1/models",
	}

	for _, path := range cases {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s: expected 401 for keyless data-plane request, got %d body=%s", path, rr.Code, rr.Body.String())
		}
	}
}

func TestAuthMiddleware_NonNavigationRequestsKeepGate(t *testing.T) {
	// 非 text/html 的 Accept（SDK/curl 的 application/json、*/*）保持原门：
	// 无 key 访问 SPA 路由仍 401，不放水。
	mw := NewAuthMiddleware("secret-key")
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("%s: handler must not be reached without navigation Accept", r.URL.Path)
	}))

	accepts := []string{"application/json", "*/*", ""}

	for _, accept := range accepts {
		req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("Accept=%q: expected 401 for keyless /dashboard, got %d", accept, rr.Code)
		}
	}
}

func TestAuthMiddleware_BypassesRootPublicAssets(t *testing.T) {
	// 2026-10-12（2547 轮 GUI 验收抓到）：web/public 根级公共资产必须匿名可达。
	// /assets/ 前缀只覆盖 Vite 哈希产物，public/ 根文件（与 index.html 同级）
	// 此前对活跃的全局 API-key 门一律 401：logo/favicon 破图（含已登录用户——
	// 本门不读 session cookie，JWT 也非数据面 key）、theme-init.js 失效
	// （暗色首帧白闪）、entry-switch.js 失效。<img>/<script> 请求不携带
	// API key，Accept 也不是 text/html，SPA 导航豁免救不了它们。
	called := false
	mw := NewAuthMiddleware("secret-key")
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	cases := []string{
		"/logo-icon.png",
		"/logo-icon-dark.png",
		"/favicon.png",
		"/theme-init.js",
		"/entry-switch.js",
		"/donation-qr.png",
		"/user-agreement.html",
		"/index.html",
	}

	for _, path := range cases {
		called = false
		req := httptest.NewRequest(http.MethodGet, path, nil)
		// 浏览器 <img>/<script> 的典型 Accept（非 text/html 导航）
		req.Header.Set("Accept", "*/*")
		// NO Authorization header — 静态资产请求本就无 key，cookie 不被本门读取
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if !called {
			t.Errorf("%s: root public asset should bypass auth, got status=%d body=%s", path, rr.Code, rr.Body.String())
			continue
		}
		if rr.Code == http.StatusUnauthorized {
			t.Errorf("%s: should NOT return 401, got %d body=%s", path, rr.Code, rr.Body.String())
		}
	}
}

func TestAuthMiddleware_RootAssetBypassDoesNotLeakNeighbors(t *testing.T) {
	// 反向钉子：根级豁免按**精确路径**收口，不得外溢到未登记的相邻路径。
	// （形状钉桩纪律：只断言「登记的在场」不够，必须同时钉住「未登记的仍被拒」，
	// 否则清单被删一半时判据依然全绿。）
	mw := NewAuthMiddleware("secret-key")
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("%s: unregistered neighbor must not ride the root-asset bypass", r.URL.Path)
	}))

	cases := []string{
		"/logo-icon.png.bak",        // 同名近似路径
		"/logo-icon-dark.png/x",     // 尾巴路径
		"/not-registered.png",       // 未来误放进 public 根且未登记的文件
		"/logo-archive/logo-icon.bak.20260709.svg", // public 子目录不在豁免内
	}

	for _, path := range cases {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Accept", "*/*")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s: expected 401 for unregistered neighbor, got %d body=%s", path, rr.Code, rr.Body.String())
		}
	}
}
