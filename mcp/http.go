package mcp

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Mount 是网关的 HTTP 装配面：把数据面、控制面与运维视图挂到同一个
// http.Handler 上。
type Mount struct {
	gateway *Gateway
}

// NewMount 构造装配面。
func NewMount(g *Gateway) *Mount {
	return &Mount{gateway: g}
}

// Handler 返回挂载点根路径（"/"）的处理器。
//
// 路由：
//
//	POST /mcp                    数据面（JSON-RPC over HTTP）
//	GET  /mcp/...                控制面（管理 API）
//	GET  /mcp/admin              运营看板（自包含 HTML）
//	GET  /.well-known/oauth-protected-resource   RFC 9728 受保护资源元数据
func (m *Mount) Handler() http.Handler {
	control := NewControlPlane(m.gateway)

	mux := http.NewServeMux()

	// 数据面：/mcp 与 /mcp/（POST）。
	mux.Handle("/mcp", http.HandlerFunc(m.gateway.HandleMCP))
	mux.Handle("/mcp/", http.HandlerFunc(m.gateway.HandleMCP))

	// 控制面：/mcp/control/...（避免与数据面路径冲突）。
	mux.Handle("/mcp/control/", http.StripPrefix("/mcp/control", control.Handler()))

	// 运营看板。
	mux.HandleFunc("GET /mcp/admin", m.handleDashboard)
	mux.HandleFunc("GET /mcp/admin/", m.handleDashboard)

	// RFC 9728 受保护资源元数据。
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", m.handleProtectedResourceMetadata)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", m.handleProtectedResourceMetadata)

	return mux
}

// handleProtectedResourceMetadata 输出 RFC 9728 受保护资源元数据。
//
// 企业价值：MCP 客户端（如 VS Code、Claude Desktop）据此发现授权服务器，
// 从而走 OAuth 2.1 授权码 + PKCE 流程接入，而不是退回到裸 token。
func (m *Mount) handleProtectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimSuffix(r.Header.Get("X-Forwarded-Proto"), "")
	if base == "" {
		if r.TLS != nil {
			base = "https"
		} else {
			base = "http"
		}
	}
	host := r.Host
	origin := base + "://" + host
	meta := map[string]any{
		"resource":                 origin + "/mcp",
		"authorization_servers":    []string{},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         ControlScopes(),
		"resource_name":            "llm-gateway-go MCP Gateway",
		"resource_documentation":   "https://github.com/kaixuan/llm-gateway-go",
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(meta)
}

// handleDashboard 输出自包含的运营看板页面。
//
// 页面用原生 JS 拉取 /mcp/control/health 与 /mcp/control/audit（带管理令牌），
// 展示调用量、成功率、Top 工具与最近事件 —— 对应 PRD 的「运营看板」。
func (m *Mount) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(dashboardHTML))
}

// UptimeSeconds 返回网关运行时长（秒）。
func (g *Gateway) UptimeSeconds() int {
	return int(time.Since(g.startedAt).Seconds())
}
