package main

// maintain_proxy.go — Gateway edge integration for the maintain service.
//
// Two responsibilities live here (docs/优化v1/01 + 02):
//
//  1. Reverse-proxy /maintain-api/* to the maintain backend (the canonical
//     API surface). The proxy strips client-supplied X-Tenant-ID so tenant
//     scope can only come from the authenticated session, forwards
//     X-Request-ID, and returns a unified 503 envelope on upstream failure.
//
//  2. Keep the legacy /api/* prefixes working as a deprecation shim: they
//     still proxy to maintain but every response is tagged
//     `Deprecation: true` + a successor `Link`. This lets old clients and
//     bookmarks migrate without a hard cutover.
//
// When MAINTAIN_SERVICE_URL is unset the Gateway falls back to its legacy
// in-process handlers, preserving the pre-migration rollback path.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// maintainCompatPrefixes are the legacy /api/* prefixes that used to be
// served by the Gateway's own ops handlers. During migration they are
// proxied to maintain and tagged deprecated.
var maintainCompatPrefixes = []string{
	"/api/admin/licenses",
	"/api/admin/downloads",
	"/api/admin/faults",
	"/api/admin/releases",
	"/api/admin/autoupdate",
	"/api/admin/center",
	"/api/admin/vibecoding",
	"/api/downloads",
	"/api/donations",
	"/api/public/offline-activation",
	"/api/system/license",
	"/api/system/upgrade",
	"/api/setup",
	"/api/consents",
	"/api/tenant/telemetry",
	"/api/feedback",
}

// maintainAPIPrefix is the canonical maintain API mount point.
const maintainAPIPrefix = "/maintain-api"

// maintainReverseProxy is the shared *httputil.ReverseProxy used for both
// the canonical /maintain-api/* path and the legacy /api/* shim. perPath
// controls whether responses are tagged deprecated.
func maintainReverseProxy(target *url.URL, perPath bool) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(r *http.Request) {
		originalDirector(r)
		// Tenant scope must come from the authenticated session, never from
		// a client header. Drop any client-supplied X-Tenant-ID before
		// forwarding.
		r.Header.Del("X-Tenant-ID")
		// ?token= must not be promoted to a credential on the backend.
		q := r.URL.Query()
		q.Del("token")
		r.URL.RawQuery = q.Encode()
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		if perPath {
			response.Header.Set("Deprecation", "true")
			response.Header.Set("Link", "<https://maintain.kxpms.cn>; rel=\"successor-version\"")
			response.Header.Set("Referrer-Policy", "no-referrer")
		}
		if response.StatusCode < http.StatusInternalServerError {
			return nil
		}
		requestID := ""
		if response.Request != nil {
			requestID = response.Request.Header.Get("X-Request-ID")
		}
		if requestID == "" {
			requestID = response.Header.Get("X-Request-ID")
		}
		body, err := json.Marshal(map[string]any{
			"code":       "maintain.upstream_unavailable",
			"message":    "运维服务暂时不可用，请稍后重试或联系支持",
			"request_id": requestID,
			"retryable":  true,
		})
		if err != nil {
			return err
		}
		if response.Body != nil {
			_ = response.Body.Close()
		}
		response.StatusCode = http.StatusServiceUnavailable
		response.Status = "503 Service Unavailable"
		response.Header.Set("Content-Type", "application/json")
		response.Header.Set("Content-Length", strconv.Itoa(len(body)))
		response.Header.Set("Retry-After", "10")
		response.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}
	proxy.ErrorHandler = func(writer http.ResponseWriter, request *http.Request, err error) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Retry-After", "10")
		writer.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"code":       "maintain.proxy_unavailable",
			"message":    "运维服务暂时不可用，请稍后重试或联系支持",
			"request_id": request.Header.Get("X-Request-ID"),
			"retryable":  true,
		})
		slog.Warn("maintain proxy request failed", "path", request.URL.Path, "error", err)
	}
	return proxy
}

// newMaintainGatewayHandler composes the legacy Gateway handler with the
// maintain reverse proxy and (optionally) the maintain-web static handler.
// Maintain routes are always intercepted so they cannot fall through to the
// Gateway SPA when the optional maintain service is absent.
func newMaintainGatewayHandler(legacy http.Handler, static *MaintainStaticHandler) http.Handler {
	target, err := parseMaintainServiceURL(os.Getenv("MAINTAIN_SERVICE_URL"))
	compat := http.NewServeMux()
	if err != nil {
		slog.Warn("maintain proxy disabled", "error", err)
		apiUnavailable := http.HandlerFunc(serveMaintainAPIUnavailable)
		pageUnavailable := http.HandlerFunc(serveMaintainUnavailablePage)
		compat.Handle(maintainAPIPrefix, apiUnavailable)
		compat.Handle(maintainAPIPrefix+"/", apiUnavailable)
		compat.Handle("/maintain", pageUnavailable)
		compat.Handle("/maintain/", pageUnavailable)
		compat.Handle("/maintain-assets/", pageUnavailable)
		compat.Handle("/", legacy)
		return compat
	}

	canonicalProxy := maintainReverseProxy(target, false)
	deprecProxy := maintainReverseProxy(target, true)
	compat.Handle(maintainAPIPrefix+"/", canonicalProxy)
	compat.Handle(maintainAPIPrefix, canonicalProxy)
	for _, prefix := range maintainCompatPrefixes {
		// ★ 2026-11-23（211 配置优化遗留 L1）：legacy 前缀走「**尽力迁移**」代理 ——
		//	路径改写成 maintain 的 canonical 前缀（/api/X → /maintain-api/X），
		//	**只有 maintain 真的服务成功（2xx）时才用它的响应**；
		//	任何非 2xx（404 没实现 / 401 鉴权模型不同 / 5xx 上游故障）
		//	⇒ 丢弃上游响应，回落到网关自己的 in-process handler。
		//
		//	为什么必须有回落（211 实测）：maintain 侧对 legacy 那 17 个前缀
		//	**全是 404**（它的路由挂在 /maintain-api/* 下），而鉴权模型也不同
		//	（maintain 认自己的会话，不认网关的 admin key ⇒ 401）。
		//	⇒ 没有回落的话，仅仅是「设了 MAINTAIN_SERVICE_URL」就会把
		//	  /api/admin/licenses、/api/downloads/catalog … 一批**现在能用**的端点
		//	  变成 404/401 ⇒ 接线的副作用比不接还大。
		//
		//	⇒ 口径：**maintain 能服务就由 maintain 服务，不能服务网关自己顶上**。
		//	  迁移是渐进的，不是开关式的。
		compat.Handle(prefix, maintainCompatFallbackHandler(deprecProxy, legacy))
		compat.Handle(prefix+"/", maintainCompatFallbackHandler(deprecProxy, legacy))
	}
	if static != nil {
		compat.Handle("/maintain-assets/", http.HandlerFunc(static.ServeAssets))
		compat.Handle("/maintain", http.HandlerFunc(static.ServeSPA))
		compat.Handle("/maintain/", http.HandlerFunc(static.ServeSPA))
	} else {
		pageUnavailable := http.HandlerFunc(serveMaintainUnavailablePage)
		compat.Handle("/maintain-assets/", pageUnavailable)
		compat.Handle("/maintain", pageUnavailable)
		compat.Handle("/maintain/", pageUnavailable)
	}
	compat.Handle("/", legacy)
	slog.Info("maintain proxy enabled", "target", target.String(), "static_configured", static != nil)
	return compat
}

func serveMaintainAPIUnavailable(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "10")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":       "maintain.not_configured",
		"message":    "运维服务未配置或未启动，请稍后重试或联系支持",
		"request_id": r.Header.Get("X-Request-ID"),
		"retryable":  true,
	})
}

func serveMaintainUnavailablePage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>运维平台不可用</title></head><body><main><h1>运维平台暂时不可用</h1><p>请稍后重试或联系支持。</p></main></body></html>`))
}

// existing tests and any external callers; it builds a gateway handler
// without the static SPA mount.
func newMaintainCompatHandler(legacy http.Handler) http.Handler {
	return newMaintainGatewayHandler(legacy, nil)
}

// ─────────────────────────────────────────────────────────────────────────
// legacy 兼容前缀的「尽力迁移」代理（2026-11-23，211 遗留 L1）
//
// 口径：**maintain 能服务就由 maintain 服务，不能服务网关自己顶上**。
//
// 为什么不是「一律反代」：211 实测 maintain 侧对 legacy 那 17 个前缀全是
// 404（它的路由挂在 /maintain-api/* 下），且鉴权模型不同（maintain 认自己的
// 会话，不认网关的 admin key ⇒ 401）⇒ 一律反代会把这批**现在能用**的端点
// 变成 404/401。
//
// 为什么路径要改写：maintain 的 canonical 前缀是 /maintain-api/*，
// 所以 /api/X → /maintain-api/X；不改写的话回落逻辑会让 legacy 端点
// **永远**留在网关侧（等价于「不迁移」）。
//
// 为什么只认 2xx：上游 401/403/404/5xx 都不是「maintain 服务了这个端点」
// 的证据 ⇒ 一律回落，避免把「上游故障」当成「迁移完成」。
// ---------------------------------------------------------------------------

// maintainCompatFallbackHandler 把 legacy 前缀的请求先给 maintain（路径改写），
// 只有 2xx 才采用它的响应；否则回落到 legacy（网关自己的 in-process handler）。
func maintainCompatFallbackHandler(proxy http.Handler, legacy http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if legacy == nil {
			proxy.ServeHTTP(w, r)
			return
		}
		// 路径改写：/api/... → /maintain-api/...
		rewritten := r.Clone(r.Context())
		if strings.HasPrefix(r.URL.Path, "/api/") {
			rewritten.URL.Path = maintainAPIPrefix + strings.TrimPrefix(r.URL.Path, "/api")
		} else if r.URL.Path == "/api" {
			rewritten.URL.Path = maintainAPIPrefix
		}

		buffer := &maintainResponseBuffer{header: http.Header{}}
		proxy.ServeHTTP(buffer, rewritten)

		if maintainEndpointNotServed(buffer.status) {
			// 上游没能服务这个端点 ⇒ 回落到网关自己的实现。
			slog.Debug("maintain compat: upstream did not serve the endpoint; falling back to in-process handler",
				"path", r.URL.Path, "upstream_status", buffer.status)
			legacy.ServeHTTP(w, r)
			return
		}
		buffer.flushTo(w)
	})
}

// maintainEndpointNotServed 判断上游状态码是否意味着「maintain 没有服务这个
// 端点」⇒ 应当回落到网关自己的实现。
//
// ★ 回落范围刻意**不含 5xx**：上游故障时既有契约是「统一 503 信封」
//
//	（maintain.upstream_unavailable + Retry-After），不是静默回落到旧实现
//	—— 否则运维看到的会是「端点偶尔正常」而不是「维护面挂了」。
//
// ★ 404：maintain 没实现这个端点（211 实测 legacy 那批前缀全是 404）。
// ★ 401/403：maintain 实现了它，但认的是**自己的会话**（不认网关的 admin key）
//
//	⇒ 对 legacy 客户端就是「拒绝」；回落到网关自己的 handler 后，
//	鉴权仍由网关自己的逻辑把关（不构成越权），且端点不会被打断。
func maintainEndpointNotServed(status int) bool {
	switch status {
	case http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden:
		return true
	default:
		return false
	}
}

// maintainResponseBuffer 是一个只写内存的 ResponseWriter：先接住上游响应，
// 由调用方决定「采用」还是「丢弃」。
type maintainResponseBuffer struct {
	header      http.Header
	status      int
	body        bytes.Buffer
	wroteHeader bool
}

func (b *maintainResponseBuffer) Header() http.Header { return b.header }

func (b *maintainResponseBuffer) Write(p []byte) (int, error) {
	if !b.wroteHeader {
		b.WriteHeader(http.StatusOK)
	}
	return b.body.Write(p)
}

func (b *maintainResponseBuffer) WriteHeader(status int) {
	if b.wroteHeader {
		return
	}
	b.wroteHeader = true
	b.status = status
}

// flushTo 把缓冲的响应原样写给真实 writer。
func (b *maintainResponseBuffer) flushTo(w http.ResponseWriter) {
	dst := w.Header()
	for k, vs := range b.header {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
	status := b.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(b.body.Bytes())
}

func parseMaintainServiceURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("MAINTAIN_SERVICE_URL is not set")
	}
	target, err := url.Parse(raw)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return nil, errors.New("MAINTAIN_SERVICE_URL must be an absolute HTTP(S) URL")
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return nil, errors.New("MAINTAIN_SERVICE_URL scheme must be http or https")
	}
	if target.User != nil || target.RawQuery != "" || target.Fragment != "" {
		return nil, errors.New("MAINTAIN_SERVICE_URL cannot contain credentials, query, or fragment")
	}
	return target, nil
}
