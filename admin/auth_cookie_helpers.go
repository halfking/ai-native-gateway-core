// Package admin — auth cookie helpers (rule 20 §6.1 HttpOnly cookie support).
//
// Provides the cookie-backed JWT session functions that allow the frontend
// to authenticate without holding a JS-accessible token in localStorage.
// Cookie name `llmgw_session` must match web/src/api/_core.ts expectations.
package admin

import (
	"net/http"
	"os"
	"strings"
	"time"
)

// cookieSecure returns the Secure flag for the session cookie.
func cookieSecure(r *http.Request) bool {
	if v := os.Getenv("LLM_GATEWAY_COOKIE_SECURE"); v != "" {
		low := strings.ToLower(strings.TrimSpace(v))
		return low == "1" || low == "true" || low == "yes" || low == "on"
	}
	if r == nil {
		return true
	}
	if r.TLS == nil {
		if host := r.Host; host != "" {
			h := strings.SplitN(host, ":", 2)[0]
			if h == "localhost" || h == "127.0.0.1" || h == "::1" {
				return false
			}
		}
	}
	return true
}

// cookieDomain returns the Domain attribute for the session cookie so that
// sibling hosts under the same eTLD+1 (e.g. freellm.itestu.cn auth_request
// against llm.itestu.cn session) can share llmgw_session.
//
// Precedence:
//  1. LLM_GATEWAY_COOKIE_DOMAIN env (explicit override; empty string disables)
//  2. If request Host ends with .itestu.cn → ".itestu.cn"
//  3. Otherwise "" (host-only cookie; historical default)
func cookieDomain(r *http.Request) string {
	if v, ok := os.LookupEnv("LLM_GATEWAY_COOKIE_DOMAIN"); ok {
		return strings.TrimSpace(v)
	}
	if r == nil {
		return ""
	}
	host := r.Host
	if host == "" {
		return ""
	}
	h := strings.SplitN(host, ":", 2)[0]
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if strings.HasSuffix(h, ".itestu.cn") || h == "itestu.cn" {
		return ".itestu.cn"
	}
	return ""
}

// clearSessionCookie expires the session cookie (MaxAge=-1) on the client.
//
// ★★ 2026-11-03（12h 审计 C9）：**必须同时清「域作用域」与「host-only 作用域」两份**。
//
//	★ 背景：本仓把 `llmgw_session` 的作用域从 **host-only**（domain=llm.itestu.cn）
//	★ 改成了 **domain cookie**（Domain=.itestu.cn）⇒ ⇒ 而 cookie 的身份键是
//	★ **name + domain + path** ⇒ ⇒ 两种作用域是 cookie jar 里的**两个不同的键**
//	★ ⇒ ⇒ 升级前签发的 **host-only** 那条**不受**只带 `Domain=.itestu.cn` 的删除
//	★ 指令影响 ⇒ ⇒ 用户点了「退出登录」之后，`r.Cookie("llmgw_session")`
//	★ **仍能取到旧 token** ⇒ ⇒ **会话未失效**。
//	★ ⇒ 修法：**两条删除指令都发**（一条带 Domain、一条不带 Domain）。
//	★ ★ 诚实边界：host-only 那条**只在升级前签发过的会话里存在** ⇒ ⇒ 本条是
//	★ **升级期的一次性兼容**；新签发的 cookie 一律带 Domain ⇒ ⇒ 只发一条即可。
//	★ 但两条都发是**幂等且零成本**的 ⇒ ⇒ 没有理由只发一条。
func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	domain := cookieDomain(r)
	base := http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   cookieSecure(r),
		SameSite: http.SameSiteStrictMode,
	}
	// ① 域作用域（★ 仅当本部署**确实在用**域作用域时才发 ⇒ 否则它与②是同一条，
	//    发两条等价的删除指令只是噪音）。
	if domain != "" {
		withDomain := base
		withDomain.Domain = domain
		http.SetCookie(w, &withDomain)
	}
	// ② host-only 作用域（★ 升级前签发的存量会话用的就是这一把钥匙 ⇒ 必须清）。
	http.SetCookie(w, &base)
}

// setSessionCookie writes the JWT as an HttpOnly+Secure+SameSite=Strict cookie.
// Persistent variant: Max-Age follows the token expiry (default 7d — see
// auth_params.go CookieMaxAge; 2026-10-13 changed from 24h).
func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expiresAt time.Time) {
	maxAge := int(CookieMaxAge.Seconds())
	if !expiresAt.IsZero() {
		if remaining := time.Until(expiresAt); remaining > 0 {
			maxAge = int(remaining.Seconds())
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     CookiePath,
		Domain:   cookieDomain(r),
		MaxAge:   maxAge,
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   cookieSecure(r),
		SameSite: http.SameSiteStrictMode,
	})
}

// setSessionCookieRemember is the login-path entry point honouring 「保持登录」
// (2026-10-10).
//
//	remember=true  → 与 setSessionCookie 完全一致：持久 cookie，Max-Age 取 JWT 剩余时长。
//	remember=false → **会话级** cookie：MaxAge=0 且不写 Expires，
//	                 浏览器/WebView 会话一结束就丢弃。
//
// ⚠️ 这里刻意**不**缩短 JWT 本身的有效期。JWT 的 TTL 由 jwtExpiry() 统一管
// （auth_params.go §5：改 cookie 时长不得与 JWT TTL 脱钩），而「保持登录」的语义
// 是「关掉 App/浏览器后是否要保持登录态」，由 cookie 的存活期表达就足够了。
// 若把未勾选时的 JWT 也签短，会与 rule 20 §5 的单一 TTL 事实源打架。
//
// ⚠️ 会话级 cookie 依赖 Go 的一个反直觉语义：Cookie.MaxAge==0 表示
// 「不输出 Max-Age 属性」（MaxAge<0 才会输出 `Max-Age=0`，那是删除语义）。
// 配合同样保持零值的 Expires，两者都不输出 = 会话级 cookie。
// 见 TestSetSessionCookieRemember_FalseIsSessionScoped。
func setSessionCookieRemember(w http.ResponseWriter, r *http.Request, token string, expiresAt time.Time, remember bool) {
	if remember {
		setSessionCookie(w, r, token, expiresAt)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:   CookieName,
		Value:  token,
		Path:   CookiePath,
		Domain: cookieDomain(r),
		// MaxAge=0 → 不输出 Max-Age 属性；Expires 保持零值 → 不输出 Expires。
		// 两者都没有 = 会话级 cookie，浏览器退出即失效。
		HttpOnly: true,
		Secure:   cookieSecure(r),
		SameSite: http.SameSiteStrictMode,
	})
}

// ExtractBearerOrCookieToken returns the JWT candidate from the request.
// Exported for use in domains/streaming/handler.go health check.
func ExtractBearerOrCookieToken(r *http.Request) (string, bool) {
	if auth := r.Header.Get("Authorization"); auth != "" {
		const prefix = "Bearer "
		if len(auth) > len(prefix) && strings.EqualFold(auth[:len(prefix)], prefix) {
			return auth[len(prefix):], true
		}
	}
	if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
		return c.Value, true
	}
	return "", false
}

// extractBearerOrCookieToken is the internal alias for backward compatibility.
func extractBearerOrCookieToken(r *http.Request) (string, bool) {
	return ExtractBearerOrCookieToken(r)
}
