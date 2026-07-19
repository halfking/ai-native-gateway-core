package pluginruntime

import (
	"encoding/json"
	"net/http"
)

// ViewerFunc 从请求解析当前用户角色。Gateway wiring 时接到现有 auth context。
type ViewerFunc func(*http.Request) ViewerOpts

// NavHandler 暴露 GET /api/v1/plugin-nav，返回当前用户可见的菜单条目。
func NavHandler(reg *Registry, viewer ViewerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		opts := ViewerOpts{}
		if viewer != nil {
			opts = viewer(r)
		}
		entries := reg.NavEntries(opts)
		if entries == nil {
			entries = []NavEntry{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": entries})
	})
}
