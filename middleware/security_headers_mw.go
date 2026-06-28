package middleware

import (
	"net/http"
	"os"
	"strings"
)

// SecurityHeadersMiddleware 给所有 HTTP 响应附加现代浏览器安全头。
//
// NET-005 fix: 修复前整个仓库 0 个安全响应头（HSTS / CSP / X-Frame-Options /
// X-Content-Type-Options / Referrer-Policy / Permissions-Policy），攻击者可：
//   - iframe 嵌入 admin SPA → clickjack
//   - 通过 MIME sniff 执行上传内容
//   - referer 泄露跨站请求路径（含 tenant_id / api 路径）
//   - 浏览器侧未禁用多余 API（camera / geolocation 等）
//
// 行为：
//   - 通用头（所有响应）：X-Content-Type-Options / X-Frame-Options / Referrer-Policy / Permissions-Policy
//   - HSTS：仅在 LLM_GATEWAY_BEHIND_TLS=true 时输出，避免 HTTP-only 部署下 HSTS 不可撤销
//   - CSP：仅对 HTML 响应（text/html）输出，避免破坏 SSE / JSON / Prometheus metrics
type SecurityHeadersMiddleware struct {
	BaseMiddleware
	behindTLS bool
}

func NewSecurityHeadersMiddleware() *SecurityHeadersMiddleware {
	return &SecurityHeadersMiddleware{
		BaseMiddleware: BaseMiddleware{name: "security_headers"},
		behindTLS:      os.Getenv("LLM_GATEWAY_BEHIND_TLS") == "true",
	}
}

func (m *SecurityHeadersMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// 通用头 —— 所有响应都设
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=(), payment=(), usb=(), magnetometer=(), gyroscope=(), accelerometer=()")

		// HSTS —— 仅在反代已知为 HTTPS 时输出
		if m.behindTLS {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}

		// CSP —— 仅对 HTML 响应输出。SSE / JSON / Prometheus metrics 都不该有 CSP
		// 限制（否则会破坏浏览器侧的预检或工具链解析）。
		// 判断条件：URL 路径是 / 或是 .html 结尾，或 Content-Type 已是 text/html。
		ct := h.Get("Content-Type")
		isHTML := strings.HasPrefix(ct, "text/html") ||
			r.URL.Path == "/" ||
			strings.HasSuffix(r.URL.Path, ".html") ||
			strings.HasPrefix(r.URL.Path, "/admin")
		if isHTML {
			h.Set("Content-Security-Policy",
				"default-src 'self'; "+
					"script-src 'self'; "+
					"style-src 'self' 'unsafe-inline'; "+
					"img-src 'self' data: https:; "+
					"connect-src 'self'; "+
					"frame-ancestors 'none'; "+
					"base-uri 'self'; "+
					"form-action 'self'")
		}

		next.ServeHTTP(w, r)
	})
}