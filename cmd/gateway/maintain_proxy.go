package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
)

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

// newMaintainCompatHandler adds a migration-only proxy in front of the old
// mux. An unset URL deliberately preserves the existing Gateway handlers.
func newMaintainCompatHandler(legacy http.Handler) http.Handler {
	target, err := parseMaintainServiceURL(os.Getenv("MAINTAIN_SERVICE_URL"))
	if err != nil {
		slog.Warn("maintain compatibility proxy disabled", "error", err)
		return legacy
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(r *http.Request) {
		originalDirector(r)
		query := r.URL.Query()
		query.Del("token")
		r.URL.RawQuery = query.Encode()
		if requestID := r.Header.Get("X-Request-ID"); requestID != "" {
			r.Header.Set("X-Request-ID", requestID)
		}
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		response.Header.Set("Deprecation", "true")
		response.Header.Set("Link", "<https://maintain.kxpms.cn>; rel=\"successor-version\"")
		response.Header.Set("Referrer-Policy", "no-referrer")
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
		slog.Warn("maintain compatibility proxy request failed", "path", request.URL.Path, "error", err)
	}

	compat := http.NewServeMux()
	for _, prefix := range maintainCompatPrefixes {
		compat.Handle(prefix, proxy)
		compat.Handle(prefix+"/", proxy)
	}
	compat.Handle("/", legacy)
	slog.Info("maintain compatibility proxy enabled", "target", target.String())
	return compat
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
