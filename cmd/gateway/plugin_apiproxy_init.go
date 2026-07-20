package main

import (
	"net/http"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

// registerPluginAPIProxy 挂载 /plugins/{pluginId}/api/，反向代理到对应的插件进程。
// pluginBaseFor 返回指定 pluginID 对应的插件监听 URL（空字符串表示插件未运行 → 502）。
func registerPluginAPIProxy(mux *http.ServeMux, secret []byte, pluginBaseFor func(pluginID string) string) {
	mux.Handle("/plugins/{pluginId}/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pluginID := r.PathValue("pluginId")
		base := pluginBaseFor(pluginID)
		if base == "" {
			http.Error(w, "plugin not running", http.StatusBadGateway)
			return
		}
		pluginruntime.PluginAPIProxy(base, secret).ServeHTTP(w, r)
	}))
}
