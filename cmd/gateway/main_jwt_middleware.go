// JWT middleware helpers extracted from main() during the P0 main.go split.
//
// See docs/refactor-plans/main-go-split.md for the full plan.
//
// These were originally inline closures inside main() that captured a local
// `jwtSecret` variable. They now take their dependencies as explicit
// parameters so they can live at package scope and be reused / tested.
package main

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/kaixuan/llm-gateway-go/admin"
)

// resolveJWTSecret returns the JWT secret to use for admin endpoints.
// Priority: envValue (if non-empty) → fallback. Centralised so the env
// precedence (LLM_GATEWAY_JWT_SECRET → cfg.SecretKey) is testable and not
// buried inside a closure in main().
func resolveJWTSecret(envValue, fallback string) string {
	if s := envValue; s != "" {
		return s
	}
	return fallback
}

// jwtRevocationProbe is the test seam; production uses
// admin.RevokedByPasswordChange — the very same probe the ServeMux-side
// admin middlewares use (admin/auth.go). One implementation, one ruler.
//
// ★ 2026-10-27（r1039 后续轮，审计档 §8 第 9 条）：Echo 侧此前**完全没有**这个
// 探测 ⇒ 改密之后，旧 cookie 会话在 Echo 注册的 `/api/admin/*` 端点上继续全权可用
// （ServeMux 侧同一时刻已经 401）。本轮只做**对齐**：Echo 侧按 ServeMux 侧**既有**
// 策略执行，**不新增也不放宽任何撤销条件**。
var jwtRevocationProbe = admin.RevokedByPasswordChange

// newJWTMiddleware returns an echo middleware that enforces a bearer-token
// JWT signed with secret. On success it injects user_id / tenant_id /
// username / role into the Echo context. On failure it short-circuits with
// 401 (or 503 if secret is unconfigured, signalling a misconfig to operators).
//
// db may be nil (no database configured / closed pool): the password-change
// revocation probe then fails open, exactly like the ServeMux side does when
// settings_kv cannot be read.
//
// Behaviour is identical to the inline closure that previously lived in main();
// only the dependency on a closure-captured `jwtSecret` was replaced by an
// explicit parameter so the function can be hoisted to package scope.
//
// ★ 2026-10-20（r1039 逐页验收）：**同时接受 `llmgw_session` 会话 cookie**。
//
// 缺陷（实测）：本中间件过去**只认 `Authorization: Bearer` 头**，
// 而 ServeMux 侧的 admin 中间件（admin/auth.go 的 AdminMiddleware）
// 用 `extractBearerOrCookieToken` —— **头或 cookie 都认**。
// 两套传输的接受范围不一致 ⇒ **凡是在 Echo 上注册的 `/api/admin/*` 端点，
// 对「浏览器 cookie 会话」一律 401**：
//
//	/api/admin/tiers /api/admin/licenses /api/admin/vibecoding/*
//	/api/admin/faults /api/admin/releases /api/admin/autoupdate
//	/api/admin/center/* /api/admin/routing-opt/ml
//
// 而前端 web/src/api/_core.ts **刻意不发** Authorization 头
// （`const bearer = isAuthenticated() && store.userInfo ? '' : authBearer()`：
// 登录成功后靠 HttpOnly cookie 鉴权，避免内存里的 JWT 盖住别的标签页更新的 cookie）
// ⇒ 这些管理页在浏览器里**恒 401**（页面能开、数据全空）。
//
// 修法与安全边界：
//   - cookie 里装的就是 `SignToken` 签出来的那串 JWT（admin/auth_cookie_helpers.go
//     的 setSessionCookieRemember 直接把 token 写进 cookie），
//     **校验器、issuer、audience 全部不变**（仍是 admin.VerifyToken）；
//   - ⇒ 不是新增一类凭据，只是让**同一串凭据**多一种传输方式；
//   - cookie 是 HttpOnly + SameSite=Strict ⇒ 跨站请求不会带上它，不构成 CSRF 面；
//   - ★ 2026-10-27：补上**改密吊销探测**（见上）⇒ Echo 侧与 ServeMux 侧
//     对「改密即踢出」这一既有策略**口径一致**。
//
// ★ 仍未对齐的**既有差异**（本轮**刻意不扩大**改动面，属安全策略变更，需单独裁决）：
// ServeMux 侧的 AdminMiddleware 还带 `MustChangePassword` 闸
// （必须改密的账号只能访问改密路径），Echo 侧**没有**这道闸。
func newJWTMiddleware(secret string, db *pgxpool.Pool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if secret == "" {
				return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "admin authentication is not configured"})
			}
			// 头优先、cookie 兜底（admin.ExtractBearerOrCookieToken 就是这个口径，
			// 与 ServeMux 侧 AdminMiddleware 共用同一个实现）。
			tokenStr, ok := admin.ExtractBearerOrCookieToken(c.Request())
			if !ok {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "authentication required"})
			}
			claims, err := admin.VerifyToken(tokenStr, secret)
			if err != nil || claims.UserID <= 0 {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid or expired token"})
			}
			// ★ 改密吊销（与 ServeMux 侧 admin/auth.go 同一把尺子）：
			// token 的签发时间早于该用户的改密纪元 ⇒ 这串会话已被改密作废。
			// 本中间件用的是**本地签发器**（admin.VerifyToken）⇒ 没有跨项目
			// principal 的 user_id 语义问题 ⇒ 与 ServeMux 侧对 legacy principal
			// 的处理一致（那边对 multi_issuer principal 跳过本探测）。
			issuedAt := time.Time{}
			if claims.IssuedAt != nil {
				issuedAt = claims.IssuedAt.Time
			}
			if jwtRevocationProbe(c.Request().Context(), db, claims.UserID, issuedAt) {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "session revoked: password changed"})
			}
			// 将用户信息注入 Echo context
			c.Set("user_id", claims.UserID)
			c.Set("tenant_id", claims.TenantID)
			c.Set("username", claims.Username)
			c.Set("role", claims.Role)
			return next(c)
		}
	}
}

// newRequireSuperAdminMiddleware returns an echo middleware that 403s when
// the role injected by newJWTMiddleware is anything other than "super_admin".
// Must be installed AFTER newJWTMiddleware in the chain.
func newRequireSuperAdminMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if role, _ := c.Get("role").(string); role != "super_admin" {
				return c.JSON(http.StatusForbidden, map[string]string{"error": "super_admin required"})
			}
			return next(c)
		}
	}
}
