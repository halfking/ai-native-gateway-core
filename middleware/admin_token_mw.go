package middleware

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"os"
)

// AdminTokenMiddleware 用静态 LLM_GATEWAY_ADMIN_API_KEY 校验 Bearer Token。
//
// 用于给内部 ops 端点（/metrics, /healthz?full=true, /admin/config/reload）
// 加一道简单鉴权，避免生产 DB 模式下依赖 admin.AdminMiddleware 的 DB
// 查询（admin.AdminMiddleware 在 dbConn 不可用时直接放行或返回 503，
// 不适合作为"硬鉴权"层）。
//
// 行为：
//   - token 为空时（env 未配置）：根据 LLM_GATEWAY_ENV 决定 fail-open 还是
//     拒绝。production / staging 显式拒绝（503 Service Unavailable）以避免
//     /metrics 在生产中无鉴权暴露；dev / local 保留 fail-open 方便调试。
//     启动时根据 env 打印 ERROR / WARN 日志提示运维。
//   - token 非空：必须 Header Authorization: Bearer <token>，否则 401
//   - 比较使用 crypto/subtle.ConstantTimeCompare 防 timing attack
//
// NET-007 / NET-008 fix 的关键组件。
type AdminTokenMiddleware struct {
	BaseMiddleware
	token string
}

func NewAdminTokenMiddleware(token string) *AdminTokenMiddleware {
	m := &AdminTokenMiddleware{
		BaseMiddleware: BaseMiddleware{name: "admin_token"},
		token:          token,
	}
	if token == "" {
		switch os.Getenv("LLM_GATEWAY_ENV") {
		case "production", "staging", "prod":
			slog.Error("admin_token: LLM_GATEWAY_ADMIN_API_KEY unset in production-like env — /metrics etc. will be REJECTED, not fail-open")
		default:
			slog.Warn("admin_token: LLM_GATEWAY_ADMIN_API_KEY unset — /metrics etc. will be REJECTED unless LLM_GATEWAY_ENV=dev|local")
		}
	}
	return m
}

func (m *AdminTokenMiddleware) Wrap(next http.Handler) http.Handler {
	if m.token == "" {
		// Empty token: refuse in production-like envs to avoid exposing
		// /metrics unauthenticated. 503 (not 401) signals "service not
		// ready" — distinct from "you sent a wrong token".
		if env := os.Getenv("LLM_GATEWAY_ENV"); env == "production" || env == "staging" || env == "prod" {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":"admin token not configured; set LLM_GATEWAY_ADMIN_API_KEY"}`))
			})
		}
		// fail-open only for dev / local / unset env
		return next
	}
	expected := []byte(m.token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if len(auth) <= len(prefix) || auth[:len(prefix)] != prefix {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("WWW-Authenticate", `Bearer realm="admin"`)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"admin token required"}`))
			return
		}
		got := []byte(auth[len(prefix):])
		// 长度不匹配先拒绝（避免 panic），仍用 constant-time 的长度判断
		if subtle.ConstantTimeCompare(got, expected) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid admin token"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
