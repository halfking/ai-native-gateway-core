package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/httpx"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// ControlPlane 实现 MCP 网关的管理面（控制面）。
//
// 鉴权：管理令牌（Authorization: Bearer <AdminToken> 或 X-Admin-Token）
// **或** 持有对应 scope 的业务 key（详见 mcp/control_scope.go）。
// AdminToken 未配置时：管理令牌不可用，但带 scope 的 key 仍可按权限访问；
// 若连 key 都无法判定（无凭据/凭据无效/账本不可达）⇒ 控制面不可用（503），
// 避免无鉴权的管理入口暴露。
//
// 例外：密钥引导面（keys/apply · keys/status · keys/claim）在
// Bootstrap.Enabled=true 时**免管理令牌**可达 —— 这是既有 tools/mcp-gateway 的
// D2「零人工介入接入」语义；未显式开启时仍走管理令牌。
type ControlPlane struct {
	gateway   *Gateway
	bootstrap *BootstrapService
}

// NewControlPlane 构造控制面。
func NewControlPlane(g *Gateway) *ControlPlane {
	return &ControlPlane{gateway: g, bootstrap: g.bootstrap}
}

// tenantOf 取请求的租户（缺省默认租户）。
//
// ★ 租户收口：业务 key（控制面身份非管理令牌）**只能操作自己所属租户** ⇒
//
//	这里直接返回该租户，忽略请求头里的 X-Tenant-Id（跨租户请求在鉴权阶段
//	就被 403 拒绝，见 control_scope.go 的 authorize）。管理令牌保持既有语义
//	（可带 X-Tenant-Id 指定租户）。
func (c *ControlPlane) tenantOf(r *http.Request) string {
	if id := controlIdentity(r); id != nil && !id.Admin {
		return id.TenantID
	}
	tenant := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenant == "" {
		tenant = strings.TrimSpace(r.URL.Query().Get("tenant"))
	}
	if tenant == "" {
		tenant = c.gateway.cfg.DefaultTenantID
	}
	return tenant
}

// Handler 返回控制面路由处理器。
//
// ★ 鉴权（2026-11-09 起扩展）：每个路由标注**所需能力位**，
//
//	管理令牌（Authorization: Bearer / X-Admin-Token）⇒ 全权（超集）；
//	业务 key（Authorization: Bearer / X-Api-Key）⇒ 持有该路由所需 scope 才放行。
//
// 能力位与路由的对应（详见 mcp/control_scope.go 与 docs/mcp-gateway/03 §6.7）：
//
//	mcp.register  注册面：适配器/工具/智能体/技能的创建与查看
//	mcp.manage    管理面：注册项的变更与删除、凭证签发/吊销/轮换、审批裁决
//	mcp.analyze   统计分析面：审计查询与导出、路由清单、能力目录、指标快照
//	mcp.topology  内部拓扑面：/health 的上游端点与熔断态（加严子位）
//
// 路由（相对挂载点）：
//
//	GET    /health            健康与上游状态（需 mcp.topology）
//	GET    /healthz           简易自检（公开）
//	GET    /routes            路由清单（适配器 + 工具）（需 mcp.analyze）
//	GET    /capabilities      能力目录与分层统计（需 mcp.analyze）
//	GET    /recommend         工具检索推荐（需 mcp.analyze）
//	GET    /stats             MCP 网关指标快照（需 mcp.analyze）
//	GET    /adapters          列出适配器（需 mcp.register）
//	POST   /adapters          注册适配器（需 mcp.register）
//	GET    /adapters/{name}   适配器详情（需 mcp.register）
//	PUT    /adapters/{name}   更新适配器（需 mcp.manage）
//	DELETE /adapters/{name}   删除适配器（需 mcp.manage）
//	GET    /tools             列出工具（需 mcp.register）
//	POST   /tools             注册工具（需 mcp.register）
//	GET    /tools/{name}      工具详情（需 mcp.register）
//	PUT    /tools/{name}      更新工具（需 mcp.manage）
//	DELETE /tools/{name}      删除工具（需 mcp.manage）
//	GET    /agents            列出智能体（需 mcp.register）
//	POST   /agents            注册智能体（需 mcp.register）
//	DELETE /agents/{id}       删除智能体（需 mcp.manage）
//	GET    /skills            列出技能（需 mcp.register）
//	POST   /skills            注册技能（需 mcp.register）
//	DELETE /skills/{id}       删除技能（需 mcp.manage）
//	GET    /keys              列出凭证（需 mcp.manage）
//	POST   /keys              签发凭证（需 mcp.manage；不可签出超出自身的权限）
//	POST   /keys/{id}/revoke  吊销凭证（需 mcp.manage）
//	POST   /keys/{id}/rotate  轮换凭证（需 mcp.manage；不可轮换更高权限的凭证）
//	GET    /approvals         列出审批（需 mcp.manage）
//	POST   /approvals         创建审批（需 mcp.manage）
//	POST   /approvals/{id}    裁决审批（approve/reject）（需 mcp.manage）
//	GET    /audit             审计查询（需 mcp.analyze）
//	GET    /audit/export      审计导出（JSONL）（需 mcp.analyze）
func (c *ControlPlane) Handler() http.Handler {
	mux := http.NewServeMux()

	// 健康与只读视图。
	//
	// /health 含上游端点、熔断态等内部拓扑 ⇒ 需要 mcp.topology（比
	// mcp.analyze 更严的一位）；/healthz 是给探针用的公开自检（只回 "ok"，
	// 不泄露任何拓扑信息）。
	mux.HandleFunc("GET /health", c.scoped(ScopeTopology, c.handleHealth))
	mux.HandleFunc("GET /healthz", c.handleHealthz)

	// 统计与只读视图（需 mcp.analyze）。
	mux.HandleFunc("GET /routes", c.scoped(ScopeAnalyze, c.handleRoutes))
	mux.HandleFunc("GET /capabilities", c.scoped(ScopeAnalyze, c.handleCapabilities))
	mux.HandleFunc("GET /recommend", c.scoped(ScopeAnalyze, c.handleRecommend))
	mux.HandleFunc("GET /stats", c.scoped(ScopeAnalyze, c.handleStats))

	// 注册面（需 mcp.register）：创建 + 查看。
	mux.HandleFunc("GET /adapters", c.scoped(ScopeRegister, c.handleListAdapters))
	mux.HandleFunc("POST /adapters", c.scoped(ScopeRegister, c.handleCreateAdapter))
	mux.HandleFunc("GET /adapters/{name}", c.scoped(ScopeRegister, c.handleGetAdapter))
	// 管理面（需 mcp.manage）：变更 + 删除。
	mux.HandleFunc("PUT /adapters/{name}", c.scoped(ScopeManage, c.handleUpdateAdapter))
	mux.HandleFunc("DELETE /adapters/{name}", c.scoped(ScopeManage, c.handleDeleteAdapter))

	mux.HandleFunc("GET /tools", c.scoped(ScopeRegister, c.handleListTools))
	mux.HandleFunc("POST /tools", c.scoped(ScopeRegister, c.handleCreateTool))
	mux.HandleFunc("GET /tools/{name}", c.scoped(ScopeRegister, c.handleGetTool))
	mux.HandleFunc("PUT /tools/{name}", c.scoped(ScopeManage, c.handleUpdateTool))
	mux.HandleFunc("DELETE /tools/{name}", c.scoped(ScopeManage, c.handleDeleteTool))

	mux.HandleFunc("GET /agents", c.scoped(ScopeRegister, c.handleListAgents))
	mux.HandleFunc("POST /agents", c.scoped(ScopeRegister, c.handleCreateAgent))
	mux.HandleFunc("DELETE /agents/{id}", c.scoped(ScopeManage, c.handleDeleteAgent))

	mux.HandleFunc("GET /skills", c.scoped(ScopeRegister, c.handleListSkills))
	mux.HandleFunc("POST /skills", c.scoped(ScopeRegister, c.handleCreateSkill))
	mux.HandleFunc("DELETE /skills/{id}", c.scoped(ScopeManage, c.handleDeleteSkill))

	// 凭证与审批（需 mcp.manage）。
	mux.HandleFunc("GET /keys", c.scoped(ScopeManage, c.handleListKeys))
	// 密钥引导面（D2/D3）：免认证面（受 IP/设备限流与幂等保护）或管理令牌面。
	mux.HandleFunc("POST /keys/apply", c.bootstrapOrAdmin(c.handleKeyApply))
	mux.HandleFunc("GET /keys/status/{id}", c.bootstrapOrAdmin(c.handleKeyStatus))
	mux.HandleFunc("POST /keys/claim", c.bootstrapOrAdmin(c.handleKeyClaim))
	mux.HandleFunc("POST /keys/requests/{id}/approve", c.scoped(ScopeManage, c.handleKeyApprove))
	mux.HandleFunc("POST /keys", c.scoped(ScopeManage, c.handleCreateKey))
	mux.HandleFunc("POST /keys/{id}/revoke", c.scoped(ScopeManage, c.handleRevokeKey))
	mux.HandleFunc("POST /keys/{id}/rotate", c.scoped(ScopeManage, c.handleRotateKey))

	mux.HandleFunc("GET /approvals", c.scoped(ScopeManage, c.handleListApprovals))
	mux.HandleFunc("POST /approvals", c.scoped(ScopeManage, c.handleCreateApproval))
	mux.HandleFunc("POST /approvals/{id}", c.scoped(ScopeManage, c.handleDecideApproval))

	// 审计与统计（需 mcp.analyze）。
	mux.HandleFunc("GET /audit", c.scoped(ScopeAnalyze, c.handleAudit))
	mux.HandleFunc("GET /audit/export", c.scoped(ScopeAnalyze, c.handleAuditExport))

	return mux
}

// bootstrapOrAdmin：引导面开启时免令牌，否则走管理令牌（或 mcp.admin 权限位）。
func (c *ControlPlane) bootstrapOrAdmin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c.bootstrap != nil && c.bootstrap.cfg.Enabled {
			h(w, r)
			return
		}
		c.scoped(ScopeAdmin, h)(w, r)
	}
}

// ---------------------------------------------------------------------------
// 密钥引导面（D2/D3）
// ---------------------------------------------------------------------------

func (c *ControlPlane) handleKeyApply(w http.ResponseWriter, r *http.Request) {
	if c.bootstrap == nil {
		_ = httpx.WriteJSON(w, http.StatusServiceUnavailable, "application/json", map[string]any{
			"ok": false, "error": "bootstrap disabled",
		})
		return
	}
	var req ApplyRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 32<<10)).Decode(&req); err != nil && err != io.EOF {
		_ = httpx.WriteJSON(w, http.StatusBadRequest, "application/json", map[string]any{
			"ok": false, "error": "invalid JSON body",
		})
		return
	}
	result, status := c.bootstrap.Apply(r.Context(), req, ClientIP(r))
	if status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", result.RetryAfterSeconds))
	}
	_ = httpx.WriteJSON(w, status, "application/json", result)
}

func (c *ControlPlane) handleKeyStatus(w http.ResponseWriter, r *http.Request) {
	if c.bootstrap == nil {
		_ = httpx.WriteJSON(w, http.StatusServiceUnavailable, "application/json", map[string]any{
			"ok": false, "error": "bootstrap disabled",
		})
		return
	}
	body, status := c.bootstrap.Status(r.Context(), r.PathValue("id"), ClientIP(r))
	_ = httpx.WriteJSON(w, status, "application/json", body)
}

func (c *ControlPlane) handleKeyClaim(w http.ResponseWriter, r *http.Request) {
	if c.bootstrap == nil {
		_ = httpx.WriteJSON(w, http.StatusServiceUnavailable, "application/json", map[string]any{
			"ok": false, "error": "bootstrap disabled",
		})
		return
	}
	var req struct {
		RequestID  string `json:"requestId"`
		ClaimToken string `json:"claimToken"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 32<<10)).Decode(&req); err != nil && err != io.EOF {
		_ = httpx.WriteJSON(w, http.StatusBadRequest, "application/json", map[string]any{
			"ok": false, "error": "invalid JSON body",
		})
		return
	}
	body, status := c.bootstrap.Claim(r.Context(), req.RequestID, req.ClaimToken, ClientIP(r))
	_ = httpx.WriteJSON(w, status, "application/json", body)
}

func (c *ControlPlane) handleKeyApprove(w http.ResponseWriter, r *http.Request) {
	if c.bootstrap == nil {
		_ = httpx.WriteJSON(w, http.StatusServiceUnavailable, "application/json", map[string]any{
			"ok": false, "error": "bootstrap disabled",
		})
		return
	}
	body, status := c.bootstrap.ApproveApplication(r.Context(), r.PathValue("id"), "")
	_ = httpx.WriteJSON(w, status, "application/json", body)
}

// ---------------------------------------------------------------------------
// 健康与只读视图
// ---------------------------------------------------------------------------

func (c *ControlPlane) handleHealth(w http.ResponseWriter, r *http.Request) {
	tenant := c.tenantOf(r)
	adapters, err := c.gateway.store.ListAdapters(r.Context(), tenant)
	if err != nil {
		_ = httpx.WriteJSON(w, http.StatusInternalServerError, "application/json", map[string]any{"error": err.Error()})
		return
	}
	items := make([]map[string]any, 0, len(adapters))
	healthy := 0
	for _, a := range adapters {
		ok := a.Enabled
		if ok {
			healthy++
		}
		items = append(items, map[string]any{
			"name":       a.Name,
			"endpoint":   a.Endpoint,
			"enabled":    a.Enabled,
			"healthy":    ok,
			"circuit":    c.gateway.breaker.State(a.Name),
			"capability": a.Capability,
		})
	}
	_ = httpx.WriteJSON(w, http.StatusOK, "application/json", map[string]any{
		"status":           "ok",
		"version":          Version,
		"uptime_seconds":   c.gateway.UptimeSeconds(),
		"adapters":         items,
		"adapters_total":   len(adapters),
		"adapters_healthy": healthy,
	})
}

func (c *ControlPlane) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (c *ControlPlane) handleRoutes(w http.ResponseWriter, r *http.Request) {
	tenant := c.tenantOf(r)
	adapters, err := c.gateway.store.ListAdapters(r.Context(), tenant)
	if err != nil {
		_ = httpx.WriteJSON(w, http.StatusInternalServerError, "application/json", map[string]any{"error": err.Error()})
		return
	}
	tools, err := c.gateway.store.ListTools(r.Context(), tenant)
	if err != nil {
		_ = httpx.WriteJSON(w, http.StatusInternalServerError, "application/json", map[string]any{"error": err.Error()})
		return
	}
	routes := make([]map[string]any, 0, len(adapters))
	for _, a := range adapters {
		routes = append(routes, map[string]any{
			"path":       "/mcp/adapters/" + a.Name + "/mcp",
			"adapter":    a.Name,
			"endpoint":   a.Endpoint,
			"enabled":    a.Enabled,
			"capability": a.Capability,
		})
	}
	toolList := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		toolList = append(toolList, map[string]any{
			"name":        t.Name,
			"raw_name":    t.RawName,
			"adapter":     t.Adapter,
			"enabled":     t.Enabled,
			"visibility":  string(t.Visibility),
			"sensitivity": string(t.Sensitivity),
			"group":       t.Group,
		})
	}
	_ = httpx.WriteJSON(w, http.StatusOK, "application/json", map[string]any{
		"data_plane": "/mcp",
		"routes":     routes,
		"tools":      toolList,
	})
}

func (c *ControlPlane) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	_ = httpx.WriteJSON(w, http.StatusOK, "application/json", map[string]any{
		"capabilities": c.gateway.capability.AllCapabilities(),
		"stats":        c.gateway.capability.CapabilityStats(),
		"mapping":      capabilityToTools,
	})
}

func (c *ControlPlane) handleRecommend(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	topK := 5
	_ = httpx.WriteJSON(w, http.StatusOK, "application/json", map[string]any{
		"query":           query,
		"recommendations": c.gateway.capability.RecommendTools(query, topK),
	})
}

// ---------------------------------------------------------------------------
// 指标快照（统计分析出口）
// ---------------------------------------------------------------------------

// mcpMetricPrefixes 是 MCP 网关指标族的命名前缀。
//
// ★ 只收前缀白名单：主网关的 /metrics 里还有大量其它子系统的族，不该混进
//
//	「MCP 网关的统计出口」里造成误读。
var mcpMetricPrefixes = []string{"mcp_gateway_", "tool_allow_", "bindings_"}

// mcpMetricExact 是「前缀会误伤」而单独点名的族。
//
// ★ `keys_*` 在主网关注册表里可能与其它子系统重名 ⇒ 只认 MCP 网关自己注册
//
//	的这五个（其余同前缀族不收）。
var mcpMetricExact = map[string]bool{
	"keys_apply_total":              true,
	"keys_apply_replay_total":       true,
	"keys_apply_rate_limited_total": true,
	"keys_claim_total":              true,
	"keys_status_total":             true,
}

// isMCPMetricFamily 报告指标族是否属于 MCP 网关。
func isMCPMetricFamily(name string) bool {
	if mcpMetricExact[name] {
		return true
	}
	for _, p := range mcpMetricPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// handleStats 输出 MCP 网关自身的指标快照（JSON）。
//
// ★ 为什么不是 /metrics：全局 /metrics 是 Prometheus 抓取面（属于运维网络
//
//	域，不在业务 key 的 scope 体系内）；这里是**给业务 key 用的**统计出口
//	⇒ 需要 mcp.analyze，且只返回 MCP 网关自己的指标族。
func (c *ControlPlane) handleStats(w http.ResponseWriter, r *http.Request) {
	// ★ 抓取时刻求值的指标（mcp_gateway_uptime_seconds）先刷新再收集：
	//   否则快照里那份是上一次的读数（甚至压根没有该族）。
	c.gateway.RefreshUptimeMetric()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		_ = httpx.WriteJSON(w, http.StatusInternalServerError, "application/json", map[string]any{"error": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(families))
	for _, mf := range families {
		name := mf.GetName()
		if !isMCPMetricFamily(name) {
			continue
		}
		items := make([]map[string]any, 0, len(mf.GetMetric()))
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			items = append(items, map[string]any{
				"labels": labels,
				"value":  metricSampleValue(m, mf.GetType()),
			})
		}
		out = append(out, map[string]any{
			"name":    name,
			"type":    mf.GetType().String(),
			"help":    mf.GetHelp(),
			"metrics": items,
		})
	}
	_ = httpx.WriteJSON(w, http.StatusOK, "application/json", map[string]any{
		"families":       out,
		"family_count":   len(out),
		"uptime_seconds": int(c.gateway.UptimeSeconds()),
		"audit":          c.gateway.audit.Stats(),
	})
}

// metricSampleValue 取指标样本的标量值（直方图/摘要取样本数）。
//
// ★ 指标**类型**取自族（family），不是样本本身（dto.Metric 没有类型字段）。
func metricSampleValue(m *dto.Metric, kind dto.MetricType) float64 {
	switch kind {
	case dto.MetricType_COUNTER:
		return m.GetCounter().GetValue()
	case dto.MetricType_GAUGE:
		return m.GetGauge().GetValue()
	case dto.MetricType_HISTOGRAM:
		return float64(m.GetHistogram().GetSampleCount())
	case dto.MetricType_SUMMARY:
		return float64(m.GetSummary().GetSampleCount())
	default:
		return 0
	}
}

// ---------------------------------------------------------------------------
// 适配器
// ---------------------------------------------------------------------------

func (c *ControlPlane) handleListAdapters(w http.ResponseWriter, r *http.Request) {
	tenant := c.tenantOf(r)
	adapters, err := c.gateway.store.ListAdapters(r.Context(), tenant)
	if err != nil {
		_ = httpx.WriteJSON(w, http.StatusInternalServerError, "application/json", map[string]any{"error": err.Error()})
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, "application/json", map[string]any{"adapters": adapters})
}

func (c *ControlPlane) handleCreateAdapter(w http.ResponseWriter, r *http.Request) {
	var in Adapter
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		_ = httpx.WriteJSON(w, http.StatusBadRequest, "application/json", map[string]any{"error": err.Error()})
		return
	}
	if in.Name == "" || in.Endpoint == "" {
		_ = httpx.WriteJSON(w, http.StatusBadRequest, "application/json", map[string]any{
			"error": "name and endpoint are required",
		})
		return
	}
	if in.TenantID == "" {
		in.TenantID = c.tenantOf(r)
	}
	now := c.gateway.now()
	in.CreatedAt = now
	in.UpdatedAt = now
	if in.ProtocolVersion == "" {
		in.ProtocolVersion = ProtocolVersion
	}
	if err := c.gateway.store.PutAdapter(r.Context(), in); err != nil {
		_ = httpx.WriteJSON(w, http.StatusConflict, "application/json", map[string]any{"error": err.Error()})
		return
	}
	c.gateway.invalidateToolCache(in.Name)
	c.gateway.audit.Record(AuditEvent{
		Kind:     AuditKindAdmin,
		Outcome:  OutcomeAllowed,
		TenantID: in.TenantID,
		Adapter:  in.Name,
		Reason:   "adapter_created",
	})
	_ = httpx.WriteJSON(w, http.StatusCreated, "application/json", in)
}

func (c *ControlPlane) handleGetAdapter(w http.ResponseWriter, r *http.Request) {
	tenant := c.tenantOf(r)
	adapter, err := c.gateway.store.GetAdapter(r.Context(), tenant, r.PathValue("name"))
	if err != nil {
		_ = httpx.WriteJSON(w, http.StatusInternalServerError, "application/json", map[string]any{"error": err.Error()})
		return
	}
	if adapter == nil {
		_ = httpx.WriteJSON(w, http.StatusNotFound, "application/json", map[string]any{"error": "adapter not found"})
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, "application/json", adapter)
}

func (c *ControlPlane) handleUpdateAdapter(w http.ResponseWriter, r *http.Request) {
	tenant := c.tenantOf(r)
	name := r.PathValue("name")
	existing, err := c.gateway.store.GetAdapter(r.Context(), tenant, name)
	if err != nil {
		_ = httpx.WriteJSON(w, http.StatusInternalServerError, "application/json", map[string]any{"error": err.Error()})
		return
	}
	if existing == nil {
		_ = httpx.WriteJSON(w, http.StatusNotFound, "application/json", map[string]any{"error": "adapter not found"})
		return
	}
	var in Adapter
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		_ = httpx.WriteJSON(w, http.StatusBadRequest, "application/json", map[string]any{"error": err.Error()})
		return
	}
	in.Name = name
	in.TenantID = existing.TenantID
	in.CreatedAt = existing.CreatedAt
	in.UpdatedAt = c.gateway.now()
	if in.AuthSecret == "" {
		in.AuthSecret = existing.AuthSecret
	}
	if err := c.gateway.store.PutAdapter(r.Context(), in); err != nil {
		_ = httpx.WriteJSON(w, http.StatusConflict, "application/json", map[string]any{"error": err.Error()})
		return
	}
	c.gateway.invalidateToolCache(name)
	_ = httpx.WriteJSON(w, http.StatusOK, "application/json", in)
}

func (c *ControlPlane) handleDeleteAdapter(w http.ResponseWriter, r *http.Request) {
	tenant := c.tenantOf(r)
	name := r.PathValue("name")
	if err := c.gateway.store.DeleteAdapter(r.Context(), tenant, name); err != nil {
		_ = httpx.WriteJSON(w, http.StatusInternalServerError, "application/json", map[string]any{"error": err.Error()})
		return
	}
	c.gateway.invalidateToolCache(name)
	c.gateway.audit.Record(AuditEvent{
		Kind:     AuditKindAdmin,
		Outcome:  OutcomeAllowed,
		TenantID: tenant,
		Adapter:  name,
		Reason:   "adapter_deleted",
	})
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------

func (c *ControlPlane) handleListTools(w http.ResponseWriter, r *http.Request) {
	tenant := c.tenantOf(r)
	tools, err := c.gateway.store.ListTools(r.Context(), tenant)
	if err != nil {
		_ = httpx.WriteJSON(w, http.StatusInternalServerError, "application/json", map[string]any{"error": err.Error()})
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, "application/json", map[string]any{"tools": tools})
}

func (c *ControlPlane) handleCreateTool(w http.ResponseWriter, r *http.Request) {
	var in Tool
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		_ = httpx.WriteJSON(w, http.StatusBadRequest, "application/json", map[string]any{"error": err.Error()})
		return
	}
	if in.Name == "" || in.Adapter == "" {
		_ = httpx.WriteJSON(w, http.StatusBadRequest, "application/json", map[string]any{
			"error": "name and adapter are required",
		})
		return
	}
	if in.TenantID == "" {
		in.TenantID = c.tenantOf(r)
	}
	if in.Visibility == "" {
		in.Visibility = VisibilityPublic
	}
	if !in.Visibility.ValidVisibility() {
		_ = httpx.WriteJSON(w, http.StatusBadRequest, "application/json", map[string]any{
			"error": "visibility must be one of private/team/public",
		})
		return
	}
	if in.Sensitivity == "" {
		in.Sensitivity = SensitivityRead
	}
	if !in.Sensitivity.ValidSensitivity() {
		_ = httpx.WriteJSON(w, http.StatusBadRequest, "application/json", map[string]any{
			"error": "sensitivity must be one of read/write/high",
		})
		return
	}
	now := c.gateway.now()
	in.CreatedAt = now
	in.UpdatedAt = now
	if err := c.gateway.store.PutTool(r.Context(), in); err != nil {
		_ = httpx.WriteJSON(w, http.StatusConflict, "application/json", map[string]any{"error": err.Error()})
		return
	}
	c.gateway.registerCatalogTool(&in)
	_ = httpx.WriteJSON(w, http.StatusCreated, "application/json", in)
}

func (c *ControlPlane) handleGetTool(w http.ResponseWriter, r *http.Request) {
	tenant := c.tenantOf(r)
	tool, err := c.gateway.store.GetTool(r.Context(), tenant, r.PathValue("name"))
	if err != nil {
		_ = httpx.WriteJSON(w, http.StatusInternalServerError, "application/json", map[string]any{"error": err.Error()})
		return
	}
	if tool == nil {
		_ = httpx.WriteJSON(w, http.StatusNotFound, "application/json", map[string]any{"error": "tool not found"})
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, "application/json", tool)
}

func (c *ControlPlane) handleUpdateTool(w http.ResponseWriter, r *http.Request) {
	tenant := c.tenantOf(r)
	name := r.PathValue("name")
	existing, err := c.gateway.store.GetTool(r.Context(), tenant, name)
	if err != nil {
		_ = httpx.WriteJSON(w, http.StatusInternalServerError, "application/json", map[string]any{"error": err.Error()})
		return
	}
	if existing == nil {
		_ = httpx.WriteJSON(w, http.StatusNotFound, "application/json", map[string]any{"error": "tool not found"})
		return
	}
	var in Tool
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		_ = httpx.WriteJSON(w, http.StatusBadRequest, "application/json", map[string]any{"error": err.Error()})
		return
	}
	in.Name = name
	in.TenantID = existing.TenantID
	in.Adapter = existing.Adapter
	in.CreatedAt = existing.CreatedAt
	in.UpdatedAt = c.gateway.now()
	if err := c.gateway.store.PutTool(r.Context(), in); err != nil {
		_ = httpx.WriteJSON(w, http.StatusConflict, "application/json", map[string]any{"error": err.Error()})
		return
	}
	c.gateway.registerCatalogTool(&in)
	_ = httpx.WriteJSON(w, http.StatusOK, "application/json", in)
}

func (c *ControlPlane) handleDeleteTool(w http.ResponseWriter, r *http.Request) {
	tenant := c.tenantOf(r)
	name := r.PathValue("name")
	if err := c.gateway.store.DeleteTool(r.Context(), tenant, name); err != nil {
		_ = httpx.WriteJSON(w, http.StatusInternalServerError, "application/json", map[string]any{"error": err.Error()})
		return
	}
	c.gateway.catalogMu.Lock()
	delete(c.gateway.catalog, name)
	c.gateway.catalogMu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// 智能体与技能
// ---------------------------------------------------------------------------

func (c *ControlPlane) handleListAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := c.gateway.store.ListAgents(r.Context(), c.tenantOf(r))
	if err != nil {
		_ = httpx.WriteJSON(w, http.StatusInternalServerError, "application/json", map[string]any{"error": err.Error()})
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, "application/json", map[string]any{"agents": agents})
}

func (c *ControlPlane) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	var in Agent
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		_ = httpx.WriteJSON(w, http.StatusBadRequest, "application/json", map[string]any{"error": err.Error()})
		return
	}
	if in.Name == "" {
		_ = httpx.WriteJSON(w, http.StatusBadRequest, "application/json", map[string]any{"error": "name is required"})
		return
	}
	if in.TenantID == "" {
		in.TenantID = c.tenantOf(r)
	}
	now := c.gateway.now()
	in.ID = fmt.Sprintf("agent_%d", now.UnixNano())
	in.CreatedAt = now
	in.UpdatedAt = now
	if err := c.gateway.store.PutAgent(r.Context(), in); err != nil {
		_ = httpx.WriteJSON(w, http.StatusConflict, "application/json", map[string]any{"error": err.Error()})
		return
	}
	_ = httpx.WriteJSON(w, http.StatusCreated, "application/json", in)
}

func (c *ControlPlane) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	if err := c.gateway.store.DeleteAgent(r.Context(), c.tenantOf(r), r.PathValue("id")); err != nil {
		_ = httpx.WriteJSON(w, http.StatusInternalServerError, "application/json", map[string]any{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *ControlPlane) handleListSkills(w http.ResponseWriter, r *http.Request) {
	skills, err := c.gateway.store.ListSkills(r.Context(), c.tenantOf(r))
	if err != nil {
		_ = httpx.WriteJSON(w, http.StatusInternalServerError, "application/json", map[string]any{"error": err.Error()})
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, "application/json", map[string]any{"skills": skills})
}

func (c *ControlPlane) handleCreateSkill(w http.ResponseWriter, r *http.Request) {
	var in Skill
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		_ = httpx.WriteJSON(w, http.StatusBadRequest, "application/json", map[string]any{"error": err.Error()})
		return
	}
	if in.Name == "" {
		_ = httpx.WriteJSON(w, http.StatusBadRequest, "application/json", map[string]any{"error": "name is required"})
		return
	}
	if in.TenantID == "" {
		in.TenantID = c.tenantOf(r)
	}
	now := c.gateway.now()
	in.ID = fmt.Sprintf("skill_%d", now.UnixNano())
	in.CreatedAt = now
	in.UpdatedAt = now
	if err := c.gateway.store.PutSkill(r.Context(), in); err != nil {
		_ = httpx.WriteJSON(w, http.StatusConflict, "application/json", map[string]any{"error": err.Error()})
		return
	}
	_ = httpx.WriteJSON(w, http.StatusCreated, "application/json", in)
}

func (c *ControlPlane) handleDeleteSkill(w http.ResponseWriter, r *http.Request) {
	if err := c.gateway.store.DeleteSkill(r.Context(), c.tenantOf(r), r.PathValue("id")); err != nil {
		_ = httpx.WriteJSON(w, http.StatusInternalServerError, "application/json", map[string]any{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// 凭证
// ---------------------------------------------------------------------------

func (c *ControlPlane) handleListKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := c.gateway.store.ListKeys(r.Context(), c.tenantOf(r))
	if err != nil {
		_ = httpx.WriteJSON(w, http.StatusInternalServerError, "application/json", map[string]any{"error": err.Error()})
		return
	}
	// 列表不暴露哈希与明文。
	for i := range keys {
		keys[i].Hash = ""
	}
	_ = httpx.WriteJSON(w, http.StatusOK, "application/json", map[string]any{"keys": keys})
}

func (c *ControlPlane) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	var spec KeySpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		_ = httpx.WriteJSON(w, http.StatusBadRequest, "application/json", map[string]any{"error": err.Error()})
		return
	}
	if spec.TenantID == "" {
		spec.TenantID = c.tenantOf(r)
	}
	// ★ 权限放大防护：业务 key 签发的凭证**不得超出签发者自身权限**。
	//   管理令牌不受限（超集）；业务 key 逐个 scope 校验（含省略 scopes 时
	//   的既有默认档 [mcp.read, mcp.execute]）。
	if issuer := controlIdentity(r); issuer != nil && !issuer.Admin {
		if spec.TenantID != issuer.TenantID {
			c.writeTenantMismatch(w, issuer.TenantID)
			return
		}
		if exceeded, err := delegateScopesOK(issuer, spec.Scopes); err != nil {
			_ = httpx.WriteJSON(w, http.StatusForbidden, "application/json", map[string]any{
				"error":          "forbidden",
				"reason":         "scope_escalation",
				"exceeded_scope": exceeded,
				"granted_scopes": issuer.Scopes,
			})
			c.gateway.audit.Record(AuditEvent{
				Kind:     AuditKindAuthz,
				Outcome:  OutcomeDenied,
				TenantID: issuer.TenantID,
				KeyID:    issuer.KeyID,
				Reason:   "scope_escalation",
				Detail:   map[string]any{"exceeded_scope": exceeded, "granted_scopes": issuer.Scopes},
			})
			return
		}
	}
	key, secret, err := c.gateway.IssueKey(r.Context(), spec)
	if err != nil {
		_ = httpx.WriteJSON(w, http.StatusInternalServerError, "application/json", map[string]any{"error": err.Error()})
		return
	}
	_ = httpx.WriteJSON(w, http.StatusCreated, "application/json", map[string]any{
		"key":    key,
		"secret": secret,
		"note":   "secret is returned once and never stored in plaintext",
	})
}

func (c *ControlPlane) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	if err := c.gateway.RevokeKey(r.Context(), c.tenantOf(r), r.PathValue("id")); err != nil {
		status := StatusError(err)
		_ = httpx.WriteJSON(w, status, "application/json", map[string]any{"error": err.Error()})
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, "application/json", map[string]any{"status": "revoked"})
}

func (c *ControlPlane) handleRotateKey(w http.ResponseWriter, r *http.Request) {
	// ★ 轮换会返回**新 key 的明文** ⇒ 若目标 key 的权限高于轮换者，轮换者
	//   等于拿到了一把更 powerful 的凭证 ⇒ 业务 key 只能轮换「不超出自身
	//   权限」的凭证（权限放大防护）。
	if issuer := controlIdentity(r); issuer != nil && !issuer.Admin {
		target, err := c.gateway.store.GetKey(r.Context(), c.tenantOf(r), r.PathValue("id"))
		if err != nil {
			_ = httpx.WriteJSON(w, http.StatusInternalServerError, "application/json", map[string]any{"error": err.Error()})
			return
		}
		if target == nil {
			_ = httpx.WriteJSON(w, http.StatusNotFound, "application/json", map[string]any{"error": "key not found"})
			return
		}
		if !keyScopesWithinIssuer(issuer, target) {
			_ = httpx.WriteJSON(w, http.StatusForbidden, "application/json", map[string]any{
				"error":          "forbidden",
				"reason":         "scope_escalation",
				"granted_scopes": issuer.Scopes,
				"target_scopes":  target.Scopes,
			})
			c.gateway.audit.Record(AuditEvent{
				Kind:     AuditKindAuthz,
				Outcome:  OutcomeDenied,
				TenantID: issuer.TenantID,
				KeyID:    issuer.KeyID,
				Reason:   "scope_escalation",
				Detail:   map[string]any{"target_key_id": target.ID, "target_scopes": target.Scopes},
			})
			return
		}
	}
	key, secret, err := c.gateway.RotateKey(r.Context(), c.tenantOf(r), r.PathValue("id"))
	if err != nil {
		status := StatusError(err)
		_ = httpx.WriteJSON(w, status, "application/json", map[string]any{"error": err.Error()})
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, "application/json", map[string]any{
		"key":    key,
		"secret": secret,
		"note":   "old credential revoked; new secret is returned once",
	})
}

// ---------------------------------------------------------------------------
// 审批
// ---------------------------------------------------------------------------

func (c *ControlPlane) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	approvals, err := c.gateway.store.ListApprovals(r.Context(), c.tenantOf(r))
	if err != nil {
		_ = httpx.WriteJSON(w, http.StatusInternalServerError, "application/json", map[string]any{"error": err.Error()})
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, "application/json", map[string]any{"approvals": approvals})
}

func (c *ControlPlane) handleCreateApproval(w http.ResponseWriter, r *http.Request) {
	var in Approval
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		_ = httpx.WriteJSON(w, http.StatusBadRequest, "application/json", map[string]any{"error": err.Error()})
		return
	}
	if in.Tool == "" {
		_ = httpx.WriteJSON(w, http.StatusBadRequest, "application/json", map[string]any{"error": "tool is required"})
		return
	}
	if in.TenantID == "" {
		in.TenantID = c.tenantOf(r)
	}
	now := c.gateway.now()
	in.ID = fmt.Sprintf("apr_%d", now.UnixNano())
	in.Status = ApprovalPending
	in.CreatedAt = now
	if err := c.gateway.store.PutApproval(r.Context(), in); err != nil {
		_ = httpx.WriteJSON(w, http.StatusInternalServerError, "application/json", map[string]any{"error": err.Error()})
		return
	}
	_ = httpx.WriteJSON(w, http.StatusCreated, "application/json", in)
}

func (c *ControlPlane) handleDecideApproval(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	approval, err := c.gateway.store.GetApproval(r.Context(), id)
	if err != nil {
		_ = httpx.WriteJSON(w, http.StatusInternalServerError, "application/json", map[string]any{"error": err.Error()})
		return
	}
	if approval == nil {
		_ = httpx.WriteJSON(w, http.StatusNotFound, "application/json", map[string]any{"error": "approval not found"})
		return
	}
	var in struct {
		Decision  string     `json:"decision"`
		DecidedBy string     `json:"decided_by"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		_ = httpx.WriteJSON(w, http.StatusBadRequest, "application/json", map[string]any{"error": err.Error()})
		return
	}
	switch in.Decision {
	case ApprovalApproved, ApprovalRejected:
	default:
		_ = httpx.WriteJSON(w, http.StatusBadRequest, "application/json", map[string]any{
			"error": "decision must be approved or rejected",
		})
		return
	}
	now := c.gateway.now()
	approval.Status = in.Decision
	approval.DecidedAt = &now
	approval.DecidedBy = in.DecidedBy
	approval.ExpiresAt = in.ExpiresAt
	if err := c.gateway.store.PutApproval(r.Context(), *approval); err != nil {
		_ = httpx.WriteJSON(w, http.StatusInternalServerError, "application/json", map[string]any{"error": err.Error()})
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, "application/json", approval)
}

// ---------------------------------------------------------------------------
// 审计
// ---------------------------------------------------------------------------

func (c *ControlPlane) handleAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := AuditFilter{
		UserID:  q.Get("user_id"),
		KeyID:   q.Get("key_id"),
		Tool:    q.Get("tool"),
		Adapter: q.Get("adapter"),
		Kind:    q.Get("kind"),
		Outcome: q.Get("outcome"),
		Limit:   100,
	}
	if v := q.Get("limit"); v != "" {
		if n, err := parseInt(v); err == nil && n > 0 && n <= 1000 {
			filter.Limit = n
		}
	}
	events := c.gateway.audit.Query(filter)
	_ = httpx.WriteJSON(w, http.StatusOK, "application/json", map[string]any{
		"events": events,
		"stats":  c.gateway.audit.Stats(),
	})
}

func (c *ControlPlane) handleAuditExport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	bw := bufio.NewWriter(w)
	defer func() { _ = bw.Flush() }()
	if _, err := c.gateway.audit.ExportJSONL(bw); err != nil {
		c.gateway.logger.Warn("mcp: audit export failed", "error", err)
	}
}

func parseInt(s string) (int, error) {
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n); err != nil {
		return 0, err
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// 网关辅助
// ---------------------------------------------------------------------------

// invalidateToolCache 让某个适配器的工具清单缓存失效。
func (g *Gateway) invalidateToolCache(adapter string) {
	g.toolCacheMu.Lock()
	defer g.toolCacheMu.Unlock()
	delete(g.toolCache, adapter)
}

// registerCatalogTool 把工具登记进内存目录（授权时读取）。
func (g *Gateway) registerCatalogTool(t *Tool) {
	if t == nil {
		return
	}
	g.catalogMu.Lock()
	defer g.catalogMu.Unlock()
	cp := *t
	g.catalog[t.Name] = &cp
}

// LoadCatalog 从注册面加载工具目录（启动时调用）。
func (g *Gateway) LoadCatalog(ctx context.Context, tenantID string) error {
	tools, err := g.store.ListTools(ctx, tenantID)
	if err != nil {
		return err
	}
	g.catalogMu.Lock()
	defer g.catalogMu.Unlock()
	g.catalog = map[string]*Tool{}
	for i := range tools {
		cp := tools[i]
		g.catalog[cp.Name] = &cp
	}
	return nil
}

// SortedAdapters 返回按名排序的适配器列表（面板用）。
func SortedAdapters(adapters []Adapter) []Adapter {
	out := make([]Adapter, len(adapters))
	copy(out, adapters)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
