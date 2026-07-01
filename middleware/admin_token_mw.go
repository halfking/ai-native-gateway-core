package middleware

import (
	"crypto/subtle"
	"net/http"
)

// AdminTokenMiddleware 用静态 LLM_GATEWAY_ADMIN_API_KEY 校验 Bearer Token。
//
// 用于给内部 ops 端点（/metrics, /healthz?full=true, /admin/config/reload）
// 加一道简单鉴权，避免生产 DB 模式下依赖 admin.AdminMiddleware 的 DB
// 查询（admin.AdminMiddleware 在 dbConn 不可用时直接放行或返回 503，
// 不适合作为"硬鉴权"层）。
//
// 行为：
//   - token 为空（env 未配置）时，**fail-open**（与 LLM_GATEWAY_API_KEY 一致）
//     并打 slog.Warn；适合本地 dev 场景
//   - token 非空：必须 Header Authorization: Bearer <token>，否则 401
//   - 比较使用 crypto/subtle.ConstantTimeCompare 防 timing attack
//
// NET-007 / NET-008 fix 的关键组件。
type AdminTokenMiddleware struct {
	BaseMiddleware
	token string
}

func NewAdminTokenMiddleware(token string) *AdminTokenMiddleware {
	return &AdminTokenMiddleware{
		BaseMiddleware: BaseMiddleware{name: "admin_token"},
		token:          token,
	}
}

func (m *AdminTokenMiddleware) Wrap(next http.Handler) http.Handler {
	if m.token == "" {
		// fail-open（本地 dev）—— 直接 pass-through
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
