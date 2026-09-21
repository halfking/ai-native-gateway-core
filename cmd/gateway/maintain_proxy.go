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
		compat.Handle(prefix, deprecProxy)
		compat.Handle(prefix+"/", deprecProxy)
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
