package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/kaixuan/llm-gateway-go/admin"
)

// r1039_jwt_middleware_cookie_test.go — 2026-10-20 逐页验收查出的真缺陷的判据。
//
// ## 缺陷
//
// `newJWTMiddleware`（Echo 侧）过去**只认 `Authorization: Bearer` 头**，
// 而 ServeMux 侧的 admin 中间件用 `extractBearerOrCookieToken`（**头或 cookie 都认**）。
// 而前端 web/src/api/_core.ts **刻意不发** Authorization 头
// （登录成功后靠 HttpOnly cookie 鉴权）⇒
// **凡是在 Echo 上注册的 `/api/admin/*` 端点，对浏览器 cookie 会话一律 401**：
//
//	/api/admin/tiers /api/admin/licenses /api/admin/vibecoding/*
//	/api/admin/faults /api/admin/releases /api/admin/autoupdate
//	/api/admin/center/* /api/admin/routing-opt/ml
//
// 实测（本地网关，cookie-only vs Bearer）：8 个端点 cookie=401 / bearer=200。
//
// ## 判据为什么打在这一层
//
// 打**中间件本身**（Echo + 真实 JWT + 真实 cookie），不是打 helper：
// 断言「cookie 能过」若只在 helper 层断言，中间件没调用它就照样全绿。
//
// 判据纪律：
//   - **正向**断言（cookie 请求必须到达 handler 且拿到 role），
//     不是「不含 401」那种否定式断言；
//   - 每条放行用例都配**对照**：同一串凭据坏掉必须被拒
//     （否则「什么都放行」也会让放行类判据全绿）。

const r1039JWTSecret = "r1039-test-secret-value"

// r1039Echo 造一个只挂本中间件 + 一个记录身份的 handler 的 Echo。
func r1039Echo(t *testing.T, secret string) *echo.Echo {
	t.Helper()
	e := echo.New()
	e.Use(newJWTMiddleware(secret))
	e.GET("/probe", func(c echo.Context) error {
		role, _ := c.Get("role").(string)
		uid, _ := c.Get("user_id").(int)
		return c.JSON(http.StatusOK, map[string]any{"role": role, "user_id": uid})
	})
	return e
}

// r1039Token 签一个测试用 JWT。
func r1039Token(t *testing.T, role string) string {
	t.Helper()
	tok, _, err := admin.SignToken(42, "acme", "tester", role, r1039JWTSecret, false)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	return tok
}

// TestR1039JWTMiddlewareAcceptsSessionCookie 钉住**核心修复**：
// 只带 `llmgw_session` cookie（不带 Authorization 头）的请求必须通过，
// 且身份（role / user_id）必须正确注入。
//
// ⚠ 这是本轮缺陷的**唯一**可观测差异：把中间件改回「只认 Bearer 头」，
// 本用例必须变红（其余用例在两种实现下都绿）。
func TestR1039JWTMiddlewareAcceptsSessionCookie(t *testing.T) {
	e := r1039Echo(t, r1039JWTSecret)
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(&http.Cookie{Name: admin.CookieName, Value: r1039Token(t, "super_admin")})
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("带会话 cookie 的请求必须通过，实际 HTTP %d：%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !contains(body, `"role":"super_admin"`) || !contains(body, `"user_id":42`) {
		t.Fatalf("身份没有正确注入：%s", body)
	}
}

// TestR1039JWTMiddlewareRejectsBadCredentials 钉住**拒绝侧**：
// 无凭据 / 坏 cookie / 错签名 / 空 token 一律 401。
//
// ⇒ 这组用例保证「cookie 能过」不是因为「什么都放行」。
func TestR1039JWTMiddlewareRejectsBadCredentials(t *testing.T) {
	e := r1039Echo(t, r1039JWTSecret)

	cases := []struct {
		name   string
		cookie string
		header string
	}{
		{name: "完全没有凭据"},
		{name: "cookie 是垃圾", cookie: "not-a-jwt"},
		{name: "cookie 是别处签的", cookie: "eyJhbGciOiJIUzI1NiJ9.eyJ1c2VyX2lkIjo0Mn0.bad"},
		{name: "Bearer 头是垃圾", header: "Bearer not-a-jwt"},
		{name: "Bearer 头为空", header: "Bearer "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/probe", nil)
			if tc.cookie != "" {
				req.AddCookie(&http.Cookie{Name: admin.CookieName, Value: tc.cookie})
			}
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("必须被拒（401），实际 HTTP %d：%s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestR1039JWTMiddlewareStillAcceptsBearer 钉住**既有行为不回归**：
// Bearer 头必须照旧可用。
func TestR1039JWTMiddlewareStillAcceptsBearer(t *testing.T) {
	e := r1039Echo(t, r1039JWTSecret)
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+r1039Token(t, "tenant_admin"))
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Bearer 头必须照旧可用，实际 HTTP %d：%s", rec.Code, rec.Body.String())
	}
	if !contains(rec.Body.String(), `"role":"tenant_admin"`) {
		t.Fatalf("角色注入不对：%s", rec.Body.String())
	}
}

// TestR1039JWTMiddlewareSecretUnconfigured 钉住**未配置**语义：
// secret 为空 ⇒ 503（运维信号），不是 401。
func TestR1039JWTMiddlewareSecretUnconfigured(t *testing.T) {
	e := r1039Echo(t, "")
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("secret 未配置必须是 503，实际 HTTP %d：%s", rec.Code, rec.Body.String())
	}
}

// TestR1039RequireSuperAdminRejectsTenantViaCookie 钉住**角色门仍然生效**：
// 租户管理员的 cookie 不得进入超管专属端点。
//
// ⇒ cookie 通道不能变成「绕过角色门」的后门。
func TestR1039RequireSuperAdminRejectsTenantViaCookie(t *testing.T) {
	e := echo.New()
	e.Use(newJWTMiddleware(r1039JWTSecret))
	e.Use(newRequireSuperAdminMiddleware())
	e.GET("/probe", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(&http.Cookie{Name: admin.CookieName, Value: r1039Token(t, "tenant_admin")})
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("租户管理员进超管端点必须被拒（403），实际 HTTP %d：%s", rec.Code, rec.Body.String())
	}
}

// contains 是本文件内的最小子串判断（避免为一个断言拉 strings 进 import）。
func contains(haystack, needle string) bool {
	return len(needle) <= len(haystack) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
