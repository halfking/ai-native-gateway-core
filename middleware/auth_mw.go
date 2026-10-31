package middleware

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/i18n"
	"github.com/kaixuan/llm-gateway-go/internal/auth"
)

type AuthMiddleware struct {
	BaseMiddleware
	expectedKey string
}

func NewAuthMiddleware(apiKey string) *AuthMiddleware {
	return &AuthMiddleware{
		BaseMiddleware: BaseMiddleware{
			name: "auth",
			// Global API-key auth runs BEFORE mux routing, but admin handlers
			// registered under /api/* are independently wrapped with
			// admin.AdminMiddleware (Bearer JWT / cookie / API key) via
			// wrapAdmin in cmd/gateway/main.go. The /api/* prefix bypass
			// here lets cookie-authenticated browser sessions reach those
			// wrapped admin handlers without sending the global API key
			// (rule 20 §6.1 cookie compliance).
			//
			// /admin/* is the Vue SPA path prefix (e.g. /admin/turns). Nginx
			// often proxies location /admin to the Go process (for
			// /admin/config/reload), so SPA navigations would otherwise be
			// rejected as missing_key. /admin/config/reload still has its
			// own AdminTokenMiddleware after this bypass.
			//
			// /m、/m/、/m-assets/（R49-A1，2026-10-07）：mobile 运维 SPA 挂载。
			// 自 R49-A1 起该挂载组合进中间件链内（main.go publicSurface），
			// 与桌面 SPA 的 / 同模型：页面静态资源匿名可达，数据面 /api/*
			// 由 wrapAdmin 端点级鉴权自守卫。此条缺失 ⇒ /m 全部 401 missing_key。
			//
			// /maintain（2026-10-07 审计补）：maintain_proxy.go 精确注册了
			// mux.Handle("/maintain", ...)（无尾斜杠根路径），前缀 /maintain/
			// 不匹配它；A1 根修后它进了链内，全局 API key 部署下裸 /maintain
			// 401 missing_key —— 与 /m 的 exact 条目对称补上。
			//
			// /maintain-api（2026-10-23）：**canonical 运维 API 前缀此前整体
			// 漏登记** ⇒ 匿名探针（访客未登录时前端先探 /maintain-api/healthz
			// 与 /maintain-api/menu/ops）被本门抢先拦成 **401 missing_key**，
			// 而不是 maintain_proxy.go 设计的 **503 maintain.not_configured**
			// 信封 ⇒ ⇒ 前端分不清「运维平台没配」与「运维平台真在维护态」
			//（两者都只是 res.ok=false），首页因此常驻一条黄色告警。
			// ★ 注意前缀歧义：PathPrefixes 里的 "/maintain/" **不匹配**
			// "/maintain-api/..."（"-" 不是 "/"）⇒ 必须显式登记两条。
			// ★ 安全性：本前缀只挂 maintain 反代（maintain_proxy.go 的
			// compat mux），而 maintain 侧自己认会话/凭据
			// （代理会剥掉客户端 X-Tenant-ID，租户范围只来自已认证会话）；
			// 未配置 MAINTAIN_SERVICE_URL 时命中的是 503 not_configured
			// 兜底，**不泄露任何数据**。端点级鉴权仍由 maintain 后端自守卫。
			//
			// SAFETY: every registered /api/* endpoint is wrapped by
			// wrapAdmin/superAdmin in cmd/gateway/main.go and
			// admin/handler.go. Verified 2026-06-30 via grep — see
			// docs/audit/2026-06-30-weekly-audit-report.md P0-3.
			//
			// /mock/* (Mock Probe 通道, 2026-09-24 设计 / 2026-09-26 生产入口
			// 接入) 同理自守卫：mock 端点未注册时 mux 自然 404（与未部署该
			// 子系统一致）；注册时每个端点挂 auth.MockEndpoint 守卫（强制
			// POST + Bearer mock-probe-client + 供应商 scope 白名单），鉴权
			// 不依赖本全局门。探测客户端凭证是公开的系统标记（非机密、不在
			// api_keys 表），与 gateway-v2 的 auth.MockProbeBypass 语义对齐
			// ——仅绕过鉴权层，链上其余中间件（Prometheus/Logging 等）照常
			// 生效。
			bypass: BypassRule{
			// 2026-08-29：加 /readyz + /version。供 scripts/lifecycle/preflight.sh 三段检查使用。
			// /readyz 返回 DB+Redis 是否就绪（K8s readiness），/version 暴露 build metadata。
			// 两者均无敏感信息，必须 anon 可达。
			//
			// 2026-10-12（2547 轮 GUI 验收抓到）：web/public **根级**公共资产。
			// /assets/ 前缀只覆盖 Vite 哈希产物；public/ 根文件部署在 web 根
			// （与 index.html 同级），此前对活跃的全局 API-key 门一律 401：
			// logo/favicon 破图（含已登录用户——本门不读 session cookie，
			// JWT 也不是数据面 key）、theme-init.js 失效（暗色首帧白闪）、
			// entry-switch.js 失效（移动端入口切换断腿）。自 2026-07-10
			// PNG logo 落地（8ec9fcc341）起即如此。均为公共品牌/引导资产
			// （theme-init/entry-switch 仅含客户端主题与入口判断逻辑，
			// 2026-10-12 过目无配置泄露）。
			// 维护契约：今后往 web/public 根新增文件，必须同步登记此处，
			// 或改放 /assets/（前缀已豁免）；**勿将任何敏感文件放进 web/public 根**。
			ExactPaths: []string{
				"/healthz", "/healthz/full", "/readyz", "/version", "/metrics",
				"/", "/index.html", "/m", "/maintain", "/maintain-api",
				"/logo-icon.png", "/logo-icon-dark.png", "/favicon.png",
				// 2026-10-19：应用图标标准（开轩 icon）落地——favicon.svg +
				// 32/180 两档 PNG + apple-touch-icon 全部在 public 根，浏览器
				// 运行期匿名请求。漏登记 ⇒ 全局 API-key 门 401 ⇒ 图标破图。
				// 两侧都有门盯着：web_public_bypass_contract_test.go 的正向腿
				// （被 index.html 引用却 401）与反向腿（public 根文件未表态）
				// 都会当场变红。
				"/favicon.svg", "/favicon-32.png", "/favicon-32-dark.png",
				"/favicon-180-dark.png", "/apple-touch-icon-180.png",
				"/theme-init.js", "/entry-switch.js", "/donation-qr.png",
				"/user-agreement.html",
			},
			PathPrefixes: []string{"/api/", "/admin/", "/assets/", "/maintain/", "/maintain-api/", "/plugins/", "/mock/", "/m/", "/m-assets/"},
			},
		},
		expectedKey: apiKey,
	}
}

func (m *AuthMiddleware) Wrap(next http.Handler) http.Handler {
	if m.expectedKey == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.ShouldBypass(r) {
			next.ServeHTTP(w, r)
			return
		}

		// SPA deep-link bypass (2026-10-11, white-page incident): browser
		// navigations carry Accept: text/html. The console SPA serves its
		// top-level routes (/dashboard, /login, /routing-v2/… — formerly
		// /admin/*) through the "/" static handler's index.html fallback;
		// a full page load of such a path carries no API key and must not
		// die here with 401 missing_key before the mux ever sees it (the
		// 2026-10-11 2538 deploy turned every refresh on /dashboard into a
		// bare JSON error page). Only navigations are exempted: SDK/curl
		// callers (Accept absent or JSON) keep the original gate, and the
		// data-plane / self-guarded prefixes are excluded outright in
		// isSPANavigation — /api/* and /admin/* stay wrapAdmin-guarded
		// behind this bypass either way.
		if m.isSPANavigation(r) {
			next.ServeHTTP(w, r)
			return
		}

		// Anthropic Messages clients authenticate with x-api-key per the
		// Anthropic spec (Claude Code with ANTHROPIC_API_KEY sends ONLY this
		// header). Without the fallback they die here with 401 missing_key
		// before the /v1/messages handler — whose extractBearerToken has
		// honored x-api-key since 2026-06-26 — ever sees the request
		// (observed 2026-09-21: claude-cli 401s on 154, zero rows in
		// session_turns_hot).
		//
		// ★ 2026-10-26（Gemini 原生认证修复轮）：本门此前**只认两种形态**
		// （Authorization Bearer + x-api-key），而厂方标准文档写明的 Gemini
		// 认证形态（x-goog-api-key 头 / ?key= query）**一张都没实现**
		// ⇒ 原生 Gemini 客户端一律 401 missing_key，且**死在 mux 之前**
		// （连 Gemini handler 都进不去）。本仓自己留的厂方文档就是判据：
		//   docs/vendor-formats/gemini.md §HTTP 形态（?key=）
		//   docs/archive/process/format-conversion/厂家数据格式/gemini/
		//     generate-content.md §认证（?key= 或 Bearer）
		//   docs/供应商协议优化.md §8.3（x-goog-api-key 或 ?key=）
		//   docs/QUICK_REFERENCE.md §Gemini协议（x-goog-api-key）
		//
		// ⇒ 提取逻辑收口到 internal/auth.ExtractClientKey（唯一事实源），
		//   与 domains/streaming 的 extractBearerToken 同源 ⇒ 两份实现
		//   不再各自漂移（此前它们对 "bearer" 小写写法就分叉过）。
		provided, _ := auth.ExtractClientKey(r)
		if provided == "" {
			// 2026-10-16（zcode/ZCode minimax-m3 401 missing_key 事故）：
			// 本分支此前**一条日志都不打**。修复前 AuthMiddleware 排在
			// LoggingMiddleware 外侧，被它拒掉的请求既不进 http_request
			// 记录，也没有 WARN——客户端拿到的 X-Request-Id（如 19d7d64a…）
			// 在网关两份实例日志里 grep 命中 0 次，只剩 nginx access log
			// 一条线索。链序已一并修复（见 cmd/gateway/main.go），但这条
			// 日志仍要留：它带 code，能把"没带凭据"与"凭据无效"分开。
			// 而本分支最容易被误判：边缘 nginx 对 :80 一律
			// `301 https://$host$request_uri`，客户端按 fetch 规范跟随时
			// Authorization 被跨源剥离、POST 被降级成 GET，落进来长得与
			// "调用方压根没配 key" 一模一样（那把 key 当时是有效的）。
			slog.Warn("auth: missing API key",
				"remote", r.RemoteAddr,
				"path", r.URL.Path,
				"code", "missing_key",
				"user_agent", r.UserAgent(),
			)
			writeAuthUnauthorized(r.Context(), w, i18n.MsgMissingAuth, "missing_key")
			return
		}

		// Exact-match on the deployed static key FIRST, even when it carries
		// an "sk-" prefix (deployments set LLM_GATEWAY_API_KEY=sk-gw* on
		// 154/245). Without this, the sk- branch below hands the static key
		// to the DB verifier, where it lands in tier "default" (12 RPM) —
		// every probe / self-check / ops caller then queues behind the
		// minute bucket, burning the caller's entire timeout budget
		// (observed 2026-08-26: kimi-k3 requests queued ~55-94s on 245 and
		// died with "context canceled" 502). Exact match cannot shadow
		// other users' keys — only the deployed key equal to expectedKey
		// ever takes this branch.
		if subtle.ConstantTimeCompare([]byte(m.expectedKey), []byte(provided)) == 1 {
			// Mark ctx with a sentinel that OriginMiddleware uses to decide
			// whether inbound X-LLM-Origin-Stage / X-LLM-Origin-Actor are
			// trustworthy. Also read by checkGatewayRateLimit to skip the
			// shared RPM bucket for the static data-plane key.
			ctx := RegisterAuthOwnerUser(r.Context(), "global-auth-passed")
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Data-plane sk-* keys are NOT validated here. They are verified
		// per-request by domains/authentication.KeyVerifier against
		// api_keys (key_hash + enabled + status + expires_at). The static
		// gate must not shadow that check: with replicas holding different
		// LLM_GATEWAY_API_KEY values (each accidentally set to a user's
		// sk-key), the gate rejected every OTHER valid DB key with the
		// same "Invalid or expired API key" 401 as a real auth failure —
		// and nginx failover to the backup replica rejected the primary's
		// key during every deploy/restart window (2026-08-24 incident).
		// Rule 20 §2: /v1/* accepts sk-* via the DB verifier; the static
		// key only covers non-sk- internal callers (health probes,
		// self-check, deploy scripts) and the exact-match case above.
		if strings.HasPrefix(provided, "sk-") {
			next.ServeHTTP(w, r)
			return
		}

		// 2026-08-24: log a non-sensitive key prefix (same convention as
		// api_keys.key_prefix) so operators can tell WHICH misconfigured
		// internal caller is being rejected — the 2026-08-24 incident
		// needed cross-replica experiments to trace this gate.
		// user_agent 同理补齐（2026-10-16）：这是把被拒请求指认回调用方的
		// 唯一字段——鉴权拒绝即使已进 http_request 记录，也要靠它才能定位
		// 是哪个客户端在用错钥匙。
		slog.Warn("auth: invalid API key",
			"remote", r.RemoteAddr,
			"path", r.URL.Path,
			"key_prefix", bearerKeyPrefixForLog(provided),
			"code", "invalid_key",
			"user_agent", r.UserAgent(),
		)
		writeAuthUnauthorized(r.Context(), w, i18n.MsgInvalidKey, "invalid_key")
	})
}

// spaProtectedPrefixes must never ride the SPA-navigation bypass: /v1 is
// the data plane, /mock and /plugins are self-guarded subsystems whose
// front door this gate is part of.
//
// 维护契约（2026-10-11 审计）：本 bypass 的安全性是隐式的——它依赖
// 「除上述前缀外，AuthMiddleware 后面的每个 handler 要么已在原生
// BypassRule 内且自带 AdminToken/authz 守卫（如 /internal/*、/api/v1/plugins/*），
// 要么在 handler 内自校验 API key（如 ChatHandler 的 keyVerifier 段，含经
// GeminiHandler 转发的 /v1beta|/v1/models/{m}:generateContent）」。
// 未来把任何无自守卫的新 handler 挂到顶层路径（或把现有自守卫 handler
// 挪出这些前缀）时，必须同步加进本清单，否则 text/html 请求将匿名直达。
var spaProtectedPrefixes = []string{"/v1", "/mock", "/plugins"}

// isSPANavigation reports whether r looks like a browser page navigation
// (Accept includes text/html) to a path outside the protected prefixes.
func (m *AuthMiddleware) isSPANavigation(r *http.Request) bool {
	if !strings.Contains(r.Header.Get("Accept"), "text/html") {
		return false
	}
	p := r.URL.Path
	for _, prefix := range spaProtectedPrefixes {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return false
		}
	}
	return true
}

// bearerKeyPrefixForLog returns a redacted prefix of a bearer token safe
// for structured logs: first 8 chars + "****" (mirrors api_keys.key_prefix).
// Never log the full token — invalid keys may still be near-valid secrets.
func bearerKeyPrefixForLog(key string) string {
	if len(key) <= 8 {
		return key + "****"
	}
	return key[:8] + "****"
}

// writeAuthUnauthorized emits the canonical 401 authentication-error envelope.
// messageKey is translated via i18n for the request's locale; code is the
// machine-readable token kept stable for SDKs.
func writeAuthUnauthorized(ctx context.Context, w http.ResponseWriter, messageKey, code string) {
	msg := i18n.T(ctx, messageKey)
	if requestID, ok := ctx.Value(requestIDContextKey{}).(string); ok && strings.TrimSpace(requestID) != "" {
		w.Header().Set("X-Request-Id", requestID)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	//nolint:errcheck // HTTP write error non-recoverable
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "authentication_error",
			"code":    code,
		},
	})
}
