package pluginruntime

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// PluginAPIProxy 返回一个反向代理 handler，把请求转发到 pluginBase。
//
// pluginBase 支持两种 scheme：
//   - "unix:///path/to/sock"（生产：plugin 进程监听 unix domain socket，
//     由 asm 通过 AI_SESSION_MANAGER_PLUGIN_SOCKET 注入）。本代理使用自定义
//     Transport 的 DialContext 拨号到该 socket。
//   - "http://host:port"（开发/测试：tcp）。走默认 Transport。
//
// 转发前，通过 SignHeaders 注入签名后的内部上下文（HMAC），以 {pluginId} 路径值
// 和 X-Caller-Tenant header（由 gateway 上游 admin middleware 写入）为 key。
// /plugins/{pluginId}/api 前缀被剥离，plugin 看到 /v1/... 。
func PluginAPIProxy(pluginBase string, secret []byte) http.Handler {
	u, err := url.Parse(pluginBase)
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "invalid plugin base url", http.StatusBadGateway)
		})
	}
	var proxy *httputil.ReverseProxy
	if u.Scheme == "unix" {
		socketPath := u.Path
		// IMPORTANT: target.Path must be empty, else NewSingleHostReverseProxy's
		// Director prepends the socket path to every request path (singleJoiningSlash),
		// corrupting the upstream URL (C1 fix). Use a bare target with no path.
		target := &url.URL{Scheme: "http", Host: "unix"}
		proxy = httputil.NewSingleHostReverseProxy(target)
		proxy.Transport = &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				d := net.Dialer{Timeout: 5 * time.Second}
				return d.DialContext(ctx, "unix", socketPath)
			},
		}
	} else {
		proxy = httputil.NewSingleHostReverseProxy(u)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pluginID := r.PathValue("pluginId")
		if pluginID == "" {
			http.Error(w, "missing plugin id", http.StatusBadRequest)
			return
		}
		tenant := r.Header.Get("X-Caller-Tenant")
		if tenant == "" {
			tenant = "default"
		}
		r.Header.Del("X-Caller-Tenant")
		SignHeaders(r.Header, secret, pluginID, tenant)
		// 在转发前直接修改 r.URL.Path 剥离 /plugins/{pluginId}/api 前缀，
		// plugin 看到 /v1/...。Director 只负责设置 Host/Scheme，会保留 Path。
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/plugins/"+pluginID+"/api")
		if r.URL.Path == "" {
			r.URL.Path = "/"
		}
		proxy.ServeHTTP(w, r)
	})
}
