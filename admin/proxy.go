package admin

// 代理管理 Admin API。
//
// 重要背景：真实订阅（NPS/机场）返回的是 Clash/Mihomo YAML，节点绝大多数为
// trojan/vless。Go 的 net/http 只能通过 http/https/socks5 代理拨号，因此这些节点
// 只能作为库存记录，无法直接使用；必须先由本地 mihomo/xray 暴露一个
// http 或 socks5 入口，再把该入口作为节点登记（POST /api/proxy/nodes）。
// 所有列表与汇总接口都会显式给出 dialable 标记，避免"导入了 116 个节点却一个都用不了"
// 这种情况被静默掩盖。

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/proxy"
)

// 审计修复 (2026-08-29)：问题 2 - 订阅刷新超时过长。
// 降低为 20 秒，parser 层面已有 20 秒拉取超时，外层再加缓冲足够。
const proxyRefreshTimeout = 20 * time.Second

// proxyRuntime 惰性初始化代理管理器，避免改动所有 Handler 构造点。
func (h *Handler) proxyRuntime() (*proxy.Manager, *proxy.PgStore) {
	h.proxyOnce.Do(func() {
		if h.db == nil {
			return
		}
		store := proxy.NewPgStore(h.db, h.encryptCred, h.decryptCredStr)
		h.proxyStore = store
		// 注意：不在请求路径里调用 Manager.Start()，避免 HTTP 请求拉起后台 goroutine。
		h.proxyMgr = proxy.NewManager(store, proxy.NewMultiFormatParser(), proxy.NewHTTPHealthChecker(10*time.Second))
	})
	return h.proxyMgr, h.proxyStore
}

// StartProxyRuntime starts the proxy manager when the admin handler has a DB.
// It is safe to call on a nil handler or repeatedly.
func (h *Handler) StartProxyRuntime() {
	if h == nil {
		return
	}
	mgr, _ := h.proxyRuntime()
	if mgr != nil {
		mgr.Start()
	}
}

// StopProxyRuntime stops the already-created proxy manager. It does not lazily
// initialize the runtime, and is safe to call on a nil handler or repeatedly.
func (h *Handler) StopProxyRuntime() {
	if h == nil {
		return
	}
	if h.proxyMgr != nil {
		h.proxyMgr.Stop()
	}
}

// ── 响应视图 ──────────────────────────────────────────────────────────────

// proxySubscriptionView 是订阅的对外视图：保留现有响应字段，但订阅地址只返回
// scheme、host 与固定脱敏路径。完整地址仍仅用于创建/更新与后台刷新。
type proxySubscriptionView struct {
	ID              int        `json:"id"`
	Name            string     `json:"name"`
	SubscribeURL    string     `json:"subscribe_url"`
	Status          string     `json:"status"`
	LastFetchAt     *time.Time `json:"last_fetch_at"`
	LastFetchStatus string     `json:"last_fetch_status"`
	LastError       string     `json:"last_error"`
	NodeCount       int        `json:"node_count"`
	Priority        int        `json:"priority"`
	Notes           string     `json:"notes"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func sanitizeSubscribeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		// Persisted URLs are validated on write; do not echo malformed legacy data.
		return "[redacted]"
	}
	u.User = nil
	u.Path = "/redacted"
	u.RawPath = ""
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
}

func toProxySubscriptionView(s *proxy.Subscription) proxySubscriptionView {
	var lastFetchAt *time.Time
	if !s.LastFetchAt.IsZero() {
		lastFetchAt = &s.LastFetchAt
	}
	return proxySubscriptionView{
		ID:              s.ID,
		Name:            s.Name,
		SubscribeURL:    sanitizeSubscribeURL(s.SubscribeURL),
		Status:          s.Status,
		LastFetchAt:     lastFetchAt,
		LastFetchStatus: s.LastFetchStatus,
		LastError:       proxy.SanitizeSubscriptionError(s.LastError, s.SubscribeURL),
		NodeCount:       s.NodeCount,
		Priority:        s.Priority,
		Notes:           s.Notes,
		CreatedAt:       s.CreatedAt,
		UpdatedAt:       s.UpdatedAt,
	}
}

func toProxySubscriptionViews(subs []*proxy.Subscription) []proxySubscriptionView {
	views := make([]proxySubscriptionView, 0, len(subs))
	for _, sub := range subs {
		if sub != nil {
			views = append(views, toProxySubscriptionView(sub))
		}
	}
	return views
}

// proxyNodeView 是节点的对外视图：绝不泄露密码明文。
type proxyNodeView struct {
	ID                    int        `json:"id"`
	SubscriptionID        int        `json:"subscription_id"`
	Name                  string     `json:"name"`
	Protocol              string     `json:"protocol"`
	Server                string     `json:"server"`
	Port                  int        `json:"port"`
	Username              string     `json:"username,omitempty"`
	HasPassword           bool       `json:"has_password"`
	Dialable              bool       `json:"dialable"`
	Location              string     `json:"location,omitempty"`
	Status                string     `json:"status"`
	HealthCheckURL        string     `json:"health_check_url,omitempty"`
	LastHealthCheckAt     *time.Time `json:"last_health_check_at"`
	LastHealthCheckStatus string     `json:"last_health_check_status,omitempty"`
	ResponseTimeMs        int        `json:"response_time_ms"`
	SuccessRate           float64    `json:"success_rate"`
	ConsecutiveFailures   int        `json:"consecutive_failures"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

func toProxyNodeView(n *proxy.Node) proxyNodeView {
	var lastHealthCheckAt *time.Time
	if !n.LastHealthCheckAt.IsZero() {
		lastHealthCheckAt = &n.LastHealthCheckAt
	}
	return proxyNodeView{
		ID:                    n.ID,
		SubscriptionID:        n.SubscriptionID,
		Name:                  n.Name,
		Protocol:              n.Protocol,
		Server:                n.Server,
		Port:                  n.Port,
		Username:              n.Username,
		HasPassword:           n.Password != "",
		Dialable:              n.Dialable(),
		Location:              n.Location,
		Status:                n.Status,
		HealthCheckURL:        n.HealthCheckURL,
		LastHealthCheckAt:     lastHealthCheckAt,
		LastHealthCheckStatus: n.LastHealthCheckStatus,
		ResponseTimeMs:        n.ResponseTimeMs,
		SuccessRate:           n.SuccessRate,
		ConsecutiveFailures:   n.ConsecutiveFailures,
		CreatedAt:             n.CreatedAt,
		UpdatedAt:             n.UpdatedAt,
	}
}

// undialableWarning 在存在节点但没有任何可拨号节点时给出可操作提示。
func undialableWarning(total, dialable int) string {
	if total == 0 || dialable > 0 {
		return ""
	}
	return "导入的 " + strconv.Itoa(total) + " 个节点均为 trojan/vless/vmess/ss 协议，" +
		"Go 的 HTTP 客户端无法直接使用。请先用本地 mihomo/xray 将其暴露为 http 或 socks5 入口" +
		"（例如 Clash 的 mixed-port 127.0.0.1:7897），再通过 POST /api/proxy/nodes 登记该入口节点。"
}

// ── 路由分发 ──────────────────────────────────────────────────────────────

func (h *Handler) handleProxySubscriptionsRoot(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.listProxySubscriptions(w, r)
	case http.MethodPost:
		h.createProxySubscription(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleProxySubscriptions(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	remaining := strings.Trim(r.URL.Path[len("/api/proxy/subscriptions/"):], "/")
	if remaining == "" {
		h.handleProxySubscriptionsRoot(w, r)
		return
	}

	parts := strings.Split(remaining, "/")
	id, err := parsePositiveID(parts[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid subscription id: "+parts[0])
		return
	}

	// /api/proxy/subscriptions/{id}/refresh
	if len(parts) == 2 && parts[1] == "refresh" {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.refreshProxySubscription(w, r, id)
		return
	}
	if len(parts) != 1 {
		writeError(w, http.StatusNotFound, "unknown proxy subscription route")
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.getProxySubscription(w, r, id)
	case http.MethodPut:
		h.updateProxySubscription(w, r, id)
	case http.MethodDelete:
		h.deleteProxySubscription(w, r, id)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleProxyNodesRoot(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.listProxyNodes(w, r)
	case http.MethodPost:
		h.createProxyNode(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleProxyNodes(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	remaining := strings.Trim(r.URL.Path[len("/api/proxy/nodes/"):], "/")
	if remaining == "" {
		h.handleProxyNodesRoot(w, r)
		return
	}

	parts := strings.Split(remaining, "/")
	id, err := parsePositiveID(parts[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid node id: "+parts[0])
		return
	}

	// /api/proxy/nodes/{id}/health-check
	if len(parts) == 2 && parts[1] == "health-check" {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.healthCheckProxyNode(w, r, id)
		return
	}
	if len(parts) != 1 {
		writeError(w, http.StatusNotFound, "unknown proxy node route")
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.getProxyNode(w, r, id)
	case http.MethodDelete:
		h.deleteProxyNode(w, r, id)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ── 订阅 ──────────────────────────────────────────────────────────────────

func (h *Handler) listProxySubscriptions(w http.ResponseWriter, r *http.Request) {
	_, store := h.proxyRuntime()
	subs, err := store.ListSubscriptions(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list subscriptions: "+err.Error())
		return
	}
	items := toProxySubscriptionViews(subs)
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (h *Handler) createProxySubscription(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string `json:"name"`
		SubscribeURL string `json:"subscribe_url"`
		Priority     int    `json:"priority"`
		Notes        string `json:"notes"`
		Status       string `json:"status"`
	}
	if err := readJSONRequired(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.SubscribeURL = strings.TrimSpace(req.SubscribeURL)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := validateSubscribeURL(req.SubscribeURL); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	status := strings.TrimSpace(req.Status)
	if status == "" {
		status = "active"
	}
	if !isValidSubscriptionStatus(status) {
		writeError(w, http.StatusBadRequest, "status must be one of active/disabled/error")
		return
	}

	_, store := h.proxyRuntime()
	sub := &proxy.Subscription{
		Name:         req.Name,
		SubscribeURL: req.SubscribeURL,
		Status:       status,
		Priority:     req.Priority,
		Notes:        req.Notes,
	}
	if err := store.CreateSubscription(r.Context(), sub); err != nil {
		writeError(w, http.StatusInternalServerError, "create subscription: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toProxySubscriptionView(sub))
}

func (h *Handler) getProxySubscription(w http.ResponseWriter, r *http.Request, id int) {
	_, store := h.proxyRuntime()
	sub, err := store.GetSubscription(r.Context(), id)
	if err != nil {
		writeProxyLookupError(w, err, "subscription")
		return
	}
	writeJSON(w, http.StatusOK, toProxySubscriptionView(sub))
}

func (h *Handler) updateProxySubscription(w http.ResponseWriter, r *http.Request, id int) {
	var req struct {
		Name         *string `json:"name"`
		SubscribeURL *string `json:"subscribe_url"`
		Status       *string `json:"status"`
		Priority     *int    `json:"priority"`
		Notes        *string `json:"notes"`
	}
	if err := readJSONRequired(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	_, store := h.proxyRuntime()
	sub, err := store.GetSubscription(r.Context(), id)
	if err != nil {
		writeProxyLookupError(w, err, "subscription")
		return
	}

	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "name must not be empty")
			return
		}
		sub.Name = name
	}
	if req.SubscribeURL != nil {
		u := strings.TrimSpace(*req.SubscribeURL)
		if err := validateSubscribeURL(u); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		sub.SubscribeURL = u
	}
	if req.Status != nil {
		st := strings.TrimSpace(*req.Status)
		if !isValidSubscriptionStatus(st) {
			writeError(w, http.StatusBadRequest, "status must be one of active/disabled/error")
			return
		}
		sub.Status = st
	}
	if req.Priority != nil {
		sub.Priority = *req.Priority
	}
	if req.Notes != nil {
		sub.Notes = *req.Notes
	}

	if err := store.UpdateSubscription(r.Context(), sub); err != nil {
		writeError(w, http.StatusInternalServerError, "update subscription: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toProxySubscriptionView(sub))
}

func (h *Handler) deleteProxySubscription(w http.ResponseWriter, r *http.Request, id int) {
	mgr, store := h.proxyRuntime()
	if err := store.DeleteSubscription(r.Context(), id); err != nil {
		writeProxyLookupError(w, err, "subscription")
		return
	}
	// 该订阅的缓存 Transport 一并失效，关闭其空闲连接。
	if mgr != nil {
		mgr.InvalidateTransport(id)
	}
	// 节点由外键 ON DELETE CASCADE 一并删除，缓存需同步失效。
	if mgr == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
		return
	}
	if err := mgr.ReloadCache(); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "cache_reload_error": proxy.SanitizeSecrets(err.Error())})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

func (h *Handler) refreshProxySubscription(w http.ResponseWriter, r *http.Request, id int) {
	mgr, store := h.proxyRuntime()
	if mgr == nil || store == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy runtime not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), proxyRefreshTimeout)
	defer cancel()

	var subscribeURL string
	if sub, err := store.GetSubscription(ctx, id); err == nil && sub != nil {
		subscribeURL = sub.SubscribeURL
	}
	if err := mgr.RefreshSubscription(ctx, id); err != nil {
		message := proxy.SanitizeSecrets(err.Error())
		if subscribeURL != "" {
			message = proxy.SanitizeSubscriptionError(message, subscribeURL)
		}
		writeError(w, http.StatusBadGateway, "refresh subscription: "+message)
		return
	}

	nodes, err := store.ListNodes(ctx, &id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list nodes: "+err.Error())
		return
	}
	byProtocol := map[string]int{}
	dialable := 0
	for _, n := range nodes {
		byProtocol[n.Protocol]++
		if n.Dialable() {
			dialable++
		}
	}
	if err := mgr.ReloadCache(); err != nil {
		writeError(w, http.StatusInternalServerError, "reload cache: "+err.Error())
		return
	}

	resp := map[string]any{
		"ok":              true,
		"subscription_id": id,
		"node_count":      len(nodes),
		"by_protocol":     byProtocol,
		"dialable_count":  dialable,
	}
	if warn := undialableWarning(len(nodes), dialable); warn != "" {
		resp["warning"] = warn
	}
	writeJSON(w, http.StatusOK, resp)
}

// ── 节点 ──────────────────────────────────────────────────────────────────

func (h *Handler) listProxyNodes(w http.ResponseWriter, r *http.Request) {
	_, store := h.proxyRuntime()

	var subFilter *int
	if raw := strings.TrimSpace(r.URL.Query().Get("subscription_id")); raw != "" {
		id, err := parsePositiveID(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid subscription_id: "+raw)
			return
		}
		subFilter = &id
	}

	nodes, err := store.ListNodes(r.Context(), subFilter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list nodes: "+err.Error())
		return
	}

	// dialable 过滤（可选）
	var dialableFilter *bool
	if raw := strings.TrimSpace(r.URL.Query().Get("dialable")); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid dialable filter: "+raw)
			return
		}
		dialableFilter = &v
	}

	items := make([]proxyNodeView, 0, len(nodes))
	byProtocol := map[string]int{}
	dialableTotal := 0
	for _, n := range nodes {
		byProtocol[n.Protocol]++
		if n.Dialable() {
			dialableTotal++
		}
		if dialableFilter != nil && n.Dialable() != *dialableFilter {
			continue
		}
		items = append(items, toProxyNodeView(n))
	}

	resp := map[string]any{
		"items":          items,
		"total":          len(items),
		"node_count":     len(nodes),
		"by_protocol":    byProtocol,
		"dialable_count": dialableTotal,
	}
	if warn := undialableWarning(len(nodes), dialableTotal); warn != "" {
		resp["warning"] = warn
	}
	writeJSON(w, http.StatusOK, resp)
}

// createProxyNode 手工登记节点。最主要的用途是登记本地 mihomo/xray 网桥入口
// （protocol=http/socks5, server=127.0.0.1, port=7897），让 trojan/vless 订阅可用。
func (h *Handler) createProxyNode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SubscriptionID int    `json:"subscription_id"`
		Name           string `json:"name"`
		Protocol       string `json:"protocol"`
		Server         string `json:"server"`
		Port           int    `json:"port"`
		Username       string `json:"username"`
		Password       string `json:"password"`
		Location       string `json:"location"`
		HealthCheckURL string `json:"health_check_url"`
		Status         string `json:"status"`
	}
	if err := readJSONRequired(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Protocol = strings.ToLower(strings.TrimSpace(req.Protocol))
	req.Server = strings.TrimSpace(req.Server)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Server == "" {
		writeError(w, http.StatusBadRequest, "server is required")
		return
	}
	if req.Port <= 0 || req.Port > 65535 {
		writeError(w, http.StatusBadRequest, "port must be in 1..65535")
		return
	}
	switch req.Protocol {
	case proxy.ProtocolHTTP, proxy.ProtocolHTTPS, proxy.ProtocolSOCKS5:
	default:
		writeError(w, http.StatusBadRequest,
			"protocol must be one of http/https/socks5 — 只有这三种能被 Go 的 HTTP 客户端直接使用；"+
				"trojan/vless 等请先经本地 mihomo/xray 转换为 http 或 socks5 入口")
		return
	}
	status := strings.TrimSpace(req.Status)
	if status == "" {
		status = "active"
	}
	if !isValidNodeStatus(status) {
		writeError(w, http.StatusBadRequest, "status must be one of active/disabled/unhealthy")
		return
	}
	healthCheckURL := strings.TrimSpace(req.HealthCheckURL)
	if healthCheckURL == "" {
		healthCheckURL = "https://www.google.com/generate_204"
	}

	mgr, store := h.proxyRuntime()
	node := &proxy.Node{
		SubscriptionID: req.SubscriptionID,
		Name:           req.Name,
		Protocol:       req.Protocol,
		Server:         req.Server,
		Port:           req.Port,
		Username:       req.Username,
		Password:       req.Password,
		Location:       req.Location,
		HealthCheckURL: healthCheckURL,
		Status:         status,
	}
	if err := store.CreateNode(r.Context(), node); err != nil {
		writeError(w, http.StatusInternalServerError, "create node: "+err.Error())
		return
	}
	if err := mgr.ReloadCache(); err != nil {
		writeError(w, http.StatusInternalServerError, "reload cache: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toProxyNodeView(node))
}

func (h *Handler) getProxyNode(w http.ResponseWriter, r *http.Request, id int) {
	_, store := h.proxyRuntime()
	node, err := store.GetNode(r.Context(), id)
	if err != nil {
		writeProxyLookupError(w, err, "node")
		return
	}
	writeJSON(w, http.StatusOK, toProxyNodeView(node))
}

func (h *Handler) deleteProxyNode(w http.ResponseWriter, r *http.Request, id int) {
	mgr, store := h.proxyRuntime()
	// 先取出节点以拿到其订阅 ID，供删除后失效对应 Transport。
	existing, gerr := store.GetNode(r.Context(), id)
	if gerr != nil {
		writeProxyLookupError(w, gerr, "node")
		return
	}
	if err := store.DeleteNode(r.Context(), id); err != nil {
		writeProxyLookupError(w, err, "node")
		return
	}
	if mgr != nil && existing != nil {
		mgr.InvalidateTransport(existing.SubscriptionID)
	}
	if mgr == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
		return
	}
	if err := mgr.ReloadCache(); err != nil {
		writeError(w, http.StatusInternalServerError, "reload cache: "+proxy.SanitizeSecrets(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

func (h *Handler) healthCheckProxyNode(w http.ResponseWriter, r *http.Request, id int) {
	mgr, store := h.proxyRuntime()
	if mgr == nil || store == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy runtime not configured")
		return
	}

	node, err := store.GetNode(r.Context(), id)
	if err != nil {
		writeProxyLookupError(w, err, "node")
		return
	}
	if !node.Dialable() {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       false,
			"id":       id,
			"dialable": false,
			"reason":   "undialable_protocol",
			"detail": "协议 " + node.Protocol + " 无法被 Go 的 HTTP 客户端直接探测，" +
				"请先经本地 mihomo/xray 暴露为 http 或 socks5 入口后再登记探测。",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	checkErr := mgr.HealthCheckNode(ctx, id)
	updated, err := store.GetNode(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "reload node: "+err.Error())
		return
	}
	_ = mgr.ReloadCache()

	resp := map[string]any{
		"ok":               updated.LastHealthCheckStatus == "success",
		"id":               id,
		"dialable":         true,
		"status":           updated.Status,
		"response_time_ms": updated.ResponseTimeMs,
		"health_status":    updated.LastHealthCheckStatus,
	}
	if checkErr != nil {
		resp["error"] = proxy.SanitizeSecrets(checkErr.Error())
	}
	writeJSON(w, http.StatusOK, resp)
}

// ── 汇总状态 ──────────────────────────────────────────────────────────────

func (h *Handler) handleProxyStatus(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	mgr, store := h.proxyRuntime()
	if mgr == nil || store == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy runtime not configured")
		return
	}
	ctx := r.Context()

	subs, err := store.ListSubscriptions(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list subscriptions: "+err.Error())
		return
	}
	nodes, err := store.ListNodes(ctx, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list nodes: "+err.Error())
		return
	}

	byProtocol := map[string]int{}
	dialable := 0
	unhealthy := 0
	healthy := 0
	for _, n := range nodes {
		byProtocol[n.Protocol]++
		if n.Dialable() {
			dialable++
		}
		switch n.Status {
		case "unhealthy":
			unhealthy++
		case "active":
			healthy++
		}
	}

	activeSubs := 0
	for _, s := range subs {
		if s.Status == "active" {
			activeSubs++
		}
	}

	resp := map[string]any{
		"subscription_count":   len(subs),
		"active_subscriptions": activeSubs,
		"node_count":           len(nodes),
		"by_protocol":          byProtocol,
		"dialable_count":       dialable,
		"unhealthy_count":      unhealthy,
		"healthy_count":        healthy,
	}
	if best, err := mgr.SelectBestNode(ctx, nil); err != nil {
		resp["selected_node"] = nil
		resp["selection_error"] = err.Error()
	} else {
		resp["selected_node"] = toProxyNodeView(best)
	}
	if warn := undialableWarning(len(nodes), dialable); warn != "" {
		resp["warning"] = warn
	}
	writeJSON(w, http.StatusOK, resp)
}

// ── 辅助 ──────────────────────────────────────────────────────────────────

func parsePositiveID(raw string) (int, error) {
	id, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, err
	}
	if id <= 0 {
		return 0, errors.New("id must be positive")
	}
	return id, nil
}

func isValidSubscriptionStatus(status string) bool {
	switch status {
	case "active", "disabled", "error":
		return true
	default:
		return false
	}
}

func isValidNodeStatus(status string) bool {
	switch status {
	case "active", "disabled", "unhealthy":
		return true
	default:
		return false
	}
}

func validateSubscribeURL(raw string) error {
	if raw == "" {
		return errors.New("subscribe_url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("subscribe_url is not a valid URL: " + err.Error())
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("subscribe_url must use http or https")
	}
	if u.Host == "" {
		return errors.New("subscribe_url must include a host")
	}
	return nil
}

func writeProxyLookupError(w http.ResponseWriter, err error, kind string) {
	msg := err.Error()
	if strings.Contains(msg, "no rows") || strings.Contains(strings.ToLower(msg), "not found") {
		writeError(w, http.StatusNotFound, kind+" not found")
		return
	}
	writeError(w, http.StatusInternalServerError, kind+" lookup failed: "+msg)
}
