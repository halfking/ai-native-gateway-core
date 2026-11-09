package mcp

import (
	"encoding/json"
	"net/http"
	"strings"
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
//
// ★ 用网关自己的时钟（g.now）而不是 time.Since：g.now 是本包统一的时间源
//
//	（测试可注入 ⇒ 运行时长可确定性断言）；生产下 cfg.Now 默认就是
//	time.Now，与 time.Since 等价（两者都吃单调时钟读数）。
func (g *Gateway) UptimeSeconds() int {
	return int(g.now().Sub(g.startedAt).Seconds())
}

// ScrapeHandler 把 next 包成「每次抓取前先刷新抓取时刻求值指标」的 handler。
//
// ★ 存在的理由：mcp_gateway_uptime_seconds 这类指标只有「抓取时刻」才有意义，
//
//	而 Prometheus 的抓取是**外部触发**的 ⇒ 刷新必须挂在抓取路径上；
//	挂在别处（定时器、事件点）都会让值滞后或直接缺失。
//
// ★ 凡是**暴露 mcp_gateway_* 指标族的 /metrics 端点**都必须经过它，
//
//	否则该族里「抓取时刻求值」的成员会在 /metrics 上恒为 ABSENT
//	（不是 0）⇒ 面板无数据、rate() 告警永不触发。
func (g *Gateway) ScrapeHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.RefreshUptimeMetric()
		next.ServeHTTP(w, r)
	})
}
