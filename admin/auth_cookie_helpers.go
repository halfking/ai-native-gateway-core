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

// clearSessionCookie expires the session cookie (MaxAge=-1) on the client.
func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   cookieSecure(nil),
		SameSite: http.SameSiteStrictMode,
	})
}

// setSessionCookie writes the JWT as an HttpOnly+Secure+SameSite=Strict cookie.
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
		Path:     "/",
		MaxAge:   maxAge,
		Expires:  expiresAt,
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
