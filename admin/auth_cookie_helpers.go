// Package admin — auth cookie helpers (补全 b68718e7 提交遗漏的 3 个函数)
//
// b68718e7 commit 引用了 clearSessionCookie / setSessionCookie / extractBearerOrCookieToken
// 但未提交函数定义。本文件提供 v2 实现, 配套 web 端 fetch credentials:'same-origin'。
//
// Cookie 名 `llmgw_session` 必须与 web/src/api/_core.ts 的 Set-Cookie 头一致
// (前端 fetch 注释里也写了这个名字)。v1 stub 用的是 `kx_session`, 前端拿不到
// 后端的 cookie, 导致浏览器 fetch 永远走不到 cookie 路径, 静默认证失败。
//
// Secure 标志受 LLM_GATEWAY_COOKIE_SECURE 环境变量控制: dev compose (HTTP :8782)
// 必须为 false 才能 set cookie; production nginx (HTTPS 443) 必须为 true 否则
// 浏览器不会持久化 cookie。
package admin

import (
	"net/http"
	"os"
	"strings"
	"time"
)

// sessionCookieName 是 session cookie 的名称 (与前端 web/src/api/_core.ts 同步)。
const sessionCookieName = "llmgw_session"

// sessionCookieMaxAge 是 cookie 有效期 (默认 7 天, 与 JWT 一致)。
const sessionCookieMaxAge = 7 * 24 * time.Hour

// cookieSecure 读 LLM_GATEWAY_COOKIE_SECURE, 默认 false (dev 安全)。
//
// 接受的 truthy 值: "1", "true", "yes", "on" (大小写不敏感)。
// 其他值视为 false。空字符串也视为 false。
func cookieSecure() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_GATEWAY_COOKIE_SECURE")))
	switch v {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// clearSessionCookie 通过 Set-Cookie 头清除 session cookie (Max-Age=-1)。
//
// 附加 HttpOnly + Secure + SameSite=Lax flags, 与 setSessionCookie 对称。
func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   cookieSecure(),
		SameSite: http.SameSiteLaxMode,
	})
}

// setSessionCookie 写入 session cookie (HttpOnly + Secure + SameSite=Lax)。
//
// expiresAt 决定 Expires 头; 零值时使用默认 sessionCookieMaxAge 作为 Max-Age。
func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expiresAt time.Time) {
	maxAge := int(sessionCookieMaxAge.Seconds())
	if !expiresAt.IsZero() {
		remaining := time.Until(expiresAt)
		if remaining > 0 {
			maxAge = int(remaining.Seconds())
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   cookieSecure(),
		SameSite: http.SameSiteLaxMode,
	})
}

// extractBearerOrCookieToken 优先从 Authorization Bearer header 取 token,
// 否则从 session cookie 取。返回 token 和 ok 标志。
//
// 大小写不敏感地匹配 "Bearer " 前缀 (RFC 7235 case-insensitive scheme)。
func extractBearerOrCookieToken(r *http.Request) (string, bool) {
	// 1. 优先 Authorization: Bearer <token>
	if auth := r.Header.Get("Authorization"); auth != "" {
		const prefix = "Bearer "
		if len(auth) > len(prefix) && strings.EqualFold(auth[:len(prefix)], prefix) {
			return auth[len(prefix):], true
		}
	}
	// 2. fallback: llmgw_session cookie (与 web 端 Set-Cookie 名字一致)
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		return c.Value, true
	}
	return "", false
}
