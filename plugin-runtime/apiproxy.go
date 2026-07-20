package pluginruntime

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// PluginAPIProxy 返回一个反向代理 handler，把请求转发到 pluginBase（P4 为 tcp URL；
// P5 改为 unix socket dial）。
//
// 转发前，通过 SignHeaders 注入签名后的内部上下文（HMAC），以 {pluginId} 路径值
// 和 X-Caller-Tenant header（由 gateway 上游 admin middleware 写入）为 key。
// /plugins/{pluginId}/api 前缀被剥离，plugin 看到 /v1/... 。
//
// P5 TODO: 用一个 Transport dial unix socket 的 ReverseProxy 替换 url.Parse +
// NewSingleHostReverseProxy，以实现真正的 socket 转发。
func PluginAPIProxy(pluginBase string, secret []byte) http.Handler {
	target, err := url.Parse(pluginBase)
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "invalid plugin base url", http.StatusBadGateway)
		})
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
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
		// plugin 看到 /v1/...。默认 Director 只负责设置 Host/Scheme 到 target，
		// 会保留 Path，因此无需 reassign proxy.Director（避免对共享 proxy 的并发写）。
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/plugins/"+pluginID+"/api")
		if r.URL.Path == "" {
			r.URL.Path = "/"
		}
		proxy.ServeHTTP(w, r)
	})
}
