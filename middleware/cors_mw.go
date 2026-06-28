package middleware

import (
	"net/http"
	"strings"
)

type CORSMiddleware struct {
	BaseMiddleware
	origins      string
	allowMethods string
	allowHeaders string
	maxAge       string
}

func NewCORSMiddleware(origins string) *CORSMiddleware {
	// NET-001 fix: 不再为“未配置”自动注入 "*" —— 以前默认值导致任意
	// Origin + Authorization 跨域被放行。改为 panic 要求显式 allowlist。
	//
	// 如需明确开启 "*" 通配（仅限内网 CLI 客户端，无凭证场景），请在
	// 配置中显式写 LLM_GATEWAY_CORS_ORIGINS="*"。
	if origins == "" {
		panic("CORS origins must be explicitly configured (LLM_GATEWAY_CORS_ORIGINS); " +
			"fail-closed: an empty list will block all cross-origin requests, " +
			"use \"*\" only if you understand the risk and explicitly opt-in.")
	}
	return &CORSMiddleware{
		BaseMiddleware: BaseMiddleware{name: "cors"},
		origins:        origins,
		allowMethods:   "GET, POST, PUT, DELETE, OPTIONS",
		// NET-001 fix: Authorization 移出默认 allow-headers —— 跨域携带
		// 认证凭证必须由调用方额外 CORS 反代/前端代理明确要求。
		allowHeaders: "Content-Type, X-Request-Id, X-Device-Seed, X-Machine-Id, X-Runtime-Name, X-Runtime-Version, X-OS-Name, X-OS-Arch, X-Client-Profile",
		maxAge:       "86400",
	}
}

func (m *CORSMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		if m.origins == "*" {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		} else if origin != "" {
			for _, o := range strings.Split(m.origins, ",") {
				if strings.TrimSpace(o) == origin {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					break
				}
			}
		}

		w.Header().Set("Access-Control-Allow-Methods", m.allowMethods)
		w.Header().Set("Access-Control-Allow-Headers", m.allowHeaders)
		w.Header().Set("Access-Control-Max-Age", m.maxAge)

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
