package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Config 是网关配置。
type Config struct {
	// Store 注册面持久化；为空时使用内存实现。
	Store Store
	// Ledger 凭证账本；为空时按 Store 自动构造本地账本。
	Ledger Ledger
	// Audit 审计日志；为空时创建内存审计。
	Audit *AuditLog
	// Capability 能力路由器；为空时创建默认实现。
	Capability *CapabilityRouter
	// Limiter 限流器；为空时创建默认实现。
	Limiter *Limiter
	// Breaker 熔断器；为空时创建默认实现。
	Breaker *CircuitBreaker
	// Redactor 脱敏器；为空时创建默认实现。
	Redactor *Redactor
	// Upstream 上游客户端；为空时创建默认实现。
	Upstream *UpstreamClient
	// Logger 日志。
	Logger *slog.Logger
	// AdminToken 控制面管理令牌（留空则控制面不可用）。
	AdminToken string
	// DefaultTenantID 默认租户。
	DefaultTenantID string
	// DefaultRateLimitPerMin 每个 key 的默认每分钟上限（0 = 不限）。
	DefaultRateLimitPerMin int
	// ClassRateLimits 按工具类别的每分钟上限。
	ClassRateLimits map[ToolClass]int
	// ToolTimeout 上游调用默认超时。
	ToolTimeout time.Duration
	// MaxBodyBytes 请求体上限。
	MaxBodyBytes int64
	// AutoRegisterTools tools/list 时是否把发现的工具自动登记进注册面。
	AutoRegisterTools bool
	// ToolCacheTTL 上游工具清单缓存时长。
	ToolCacheTTL time.Duration
	// Bootstrap 密钥引导面（D2/D3）配置。
	Bootstrap BootstrapConfig
	// Now 时钟（测试可注入）。
	Now func() time.Time
}

// Gateway 是 MCP 网关。
type Gateway struct {
	cfg        Config
	store      Store
	ledger     Ledger
	audit      *AuditLog
	capability *CapabilityRouter
	limiter    *Limiter
	breaker    *CircuitBreaker
	redactor   *Redactor
	upstream   *UpstreamClient
	bootstrap  *BootstrapService
	logger     *slog.Logger
	now        func() time.Time
	startedAt  time.Time

	catalogMu sync.RWMutex
	catalog   map[string]*Tool

	toolCacheMu sync.Mutex
	toolCache   map[string]toolCacheEntry
}

type toolCacheEntry struct {
	tools   []ToolView
	expires time.Time
}

// NewGateway 构造网关。
func NewGateway(cfg Config) (*Gateway, error) {
	if cfg.Store == nil {
		cfg.Store = NewMemoryStore()
	}
	if cfg.Ledger == nil {
		cfg.Ledger = NewLocalLedger(cfg.Store)
	}
	if cfg.Audit == nil {
		audit, err := NewAuditLog(200, "")
		if err != nil {
			return nil, err
		}
		cfg.Audit = audit
	}
	if cfg.Capability == nil {
		cfg.Capability = NewCapabilityRouter()
	}
	if cfg.Limiter == nil {
		cfg.Limiter = NewLimiter()
	}
	if cfg.Breaker == nil {
		cfg.Breaker = NewCircuitBreaker(5, 30*time.Second)
	}
	if cfg.Redactor == nil {
		cfg.Redactor = NewRedactor(true)
	}
	if cfg.Upstream == nil {
		cfg.Upstream = NewUpstreamClient(30 * time.Second)
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.DefaultTenantID == "" {
		cfg.DefaultTenantID = "default"
	}
	if cfg.ToolTimeout <= 0 {
		cfg.ToolTimeout = 30 * time.Second
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 1 << 20
	}
	if cfg.ToolCacheTTL <= 0 {
		cfg.ToolCacheTTL = 30 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.ClassRateLimits == nil {
		cfg.ClassRateLimits = map[ToolClass]int{
			ClassRead:  120,
			ClassWrite: 30,
			ClassLLM:   10,
		}
	}

	g := &Gateway{
		cfg:        cfg,
		store:      cfg.Store,
		ledger:     cfg.Ledger,
		audit:      cfg.Audit,
		capability: cfg.Capability,
		limiter:    cfg.Limiter,
		breaker:    cfg.Breaker,
		redactor:   cfg.Redactor,
		upstream:   cfg.Upstream,
		logger:     cfg.Logger,
		now:        cfg.Now,
		startedAt:  cfg.Now(),
		catalog:    map[string]*Tool{},
		toolCache:  map[string]toolCacheEntry{},
	}
	g.bootstrap = NewBootstrapService(g, cfg.Bootstrap)
	return g, nil
}

// Store 返回注册面（供控制面与测试使用）。
func (g *Gateway) Store() Store { return g.store }

// Audit 返回审计日志。
func (g *Gateway) Audit() *AuditLog { return g.audit }

// ---------------------------------------------------------------------------
// 数据面
// ---------------------------------------------------------------------------

// HandleMCP 是 MCP 数据面入口（JSON-RPC over HTTP）。
//
// 无状态约定：协议元数据随请求携带（`MCP-Protocol-Version` / `Mcp-Method` /
// `Mcp-Name`），路由不依赖 transport session。
func (g *Gateway) HandleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeRPCError(w, http.StatusMethodNotAllowed, nil, Errorf(CodeInvalidRequest, "only POST is supported"))
		return
	}
	requestsTotal.Inc()
	capability := r.URL.Query().Get("capability")
	if capability != "" {
		requestsByCapability.WithLabelValues(capability).Inc()
	}
	method := r.Header.Get("Mcp-Method")
	if method != "" {
		requestsByMethod.WithLabelValues(method).Inc()
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, g.cfg.MaxBodyBytes))
	if err != nil {
		writeRPCError(w, http.StatusBadRequest, nil, Errorf(CodeParseError, "read body: %s", err.Error()))
		return
	}

	var req jsonrpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPCError(w, http.StatusBadRequest, nil, Errorf(CodeParseError, "invalid JSON: %s", err.Error()))
		return
	}
	if req.Method == "" {
		req.Method = method
	}
	if req.Method == "" {
		writeRPCError(w, http.StatusBadRequest, req.ID, Errorf(CodeInvalidRequest, "method is required"))
		return
	}

	id, err := g.authenticate(r.Context(), r)
	if err != nil {
		g.audit.Record(AuditEvent{
			Kind:    AuditKindAuthz,
			Outcome: OutcomeDenied,
			Method:  req.Method,
			Reason:  "unauthorized",
		})
		writeRPCError(w, http.StatusUnauthorized, req.ID, Errorf(CodeInvalidRequest, "unauthorized"))
		return
	}
	g.touchKey(r.Context(), id)

	result, rpcErr, status := g.dispatch(r.Context(), id, r, req)
	if rpcErr != nil {
		g.audit.Record(AuditEvent{
			Kind:     AuditKindCall,
			Outcome:  OutcomeDenied,
			TenantID: id.TenantID,
			OrgID:    id.OrgID,
			UserID:   id.UserID,
			KeyID:    id.KeyID,
			AgentID:  id.AgentID,
			Method:   req.Method,
			Reason:   rpcErr.Message,
			Status:   status,
		})
		writeRPCError(w, status, req.ID, rpcErr)
		return
	}
	writeRPCResult(w, http.StatusOK, req.ID, result)
}

// dispatch 把 JSON-RPC 方法分发到具体处理器。
func (g *Gateway) dispatch(ctx context.Context, id *Identity, r *http.Request, req jsonrpcRequest) (json.RawMessage, *RPCError, int) {
	switch req.Method {
	case "initialize":
		return g.handleInitialize(), nil, http.StatusOK
	case "ping":
		return json.RawMessage(`{}`), nil, http.StatusOK
	case "tools/list":
		return g.handleToolsList(ctx, id, r)
	case "tools/call":
		return g.handleToolsCall(ctx, id, r, req)
	case "resources/list", "resources/read", "prompts/list":
		return g.handleForward(ctx, id, req)
	default:
		return nil, Errorf(CodeMethodNotFound, "method %q not supported", req.Method), http.StatusOK
	}
}

func (g *Gateway) handleInitialize() json.RawMessage {
	payload := map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities": map[string]any{
			"tools":     map[string]any{"listChanged": true},
			"resources": map[string]any{"subscribe": false, "listChanged": true},
			"prompts":   map[string]any{"listChanged": true},
			// 无状态路由：网关不签发 transport session。
			"logging": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "llm-gateway-go mcp-gateway",
			"version": Version,
		},
	}
	raw, _ := json.Marshal(payload)
	return raw
}

// handleToolsList 聚合所有适配器的工具清单，并按身份过滤。
func (g *Gateway) handleToolsList(ctx context.Context, id *Identity, r *http.Request) (json.RawMessage, *RPCError, int) {
	group := r.URL.Query().Get("group")
	adapters, err := g.store.ListAdapters(ctx, id.TenantID)
	if err != nil {
		return nil, Errorf(CodeInternalError, "list adapters: %s", err.Error()), http.StatusInternalServerError
	}
	views := make([]ToolView, 0, 32)
	for _, a := range adapters {
		if !a.Enabled {
			continue
		}
		if !keyAllowsAdapter(g.keyOf(id), a.Name) {
			continue
		}
		for _, tv := range g.adapterTools(ctx, a) {
			meta, err := g.lookupTool(ctx, id.TenantID, tv.Name)
			if err != nil {
				continue
			}
			if group != "" && (meta == nil || meta.Group != group) {
				continue
			}
			if !g.visibleTo(ctx, id, tv, meta) {
				continue
			}
			views = append(views, tv)
		}
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	raw, _ := json.Marshal(map[string]any{"tools": views})
	return raw, nil, http.StatusOK
}

// lookupTool 取工具元数据，并处理「同名工具已被别的租户私有化」的情形：
// 若该名在别的租户下已被注册（可见性为 private/team），对当前租户按未登记
// 处理会把它泄露出去，因此这种情况直接视为「不可见」。
func (g *Gateway) lookupTool(ctx context.Context, tenantID, name string) (*Tool, error) {
	meta, err := g.store.GetTool(ctx, tenantID, name)
	if err != nil {
		return nil, err
	}
	if meta != nil {
		return meta, nil
	}
	other, err := g.store.GetToolAnyTenant(ctx, name)
	if err != nil {
		return nil, err
	}
	if other != nil {
		// 名已被别的租户占用：标记为不可见（调用方按 meta==nil 且 hidden 处理）。
		return &Tool{Name: name, Visibility: VisibilityPrivate, TenantID: other.TenantID, Enabled: false}, nil
	}
	return nil, nil
}

// visibleTo 判断工具是否对该身份可见且被允许。
//
// meta 是工具在注册面里的元数据（可能为 nil：未显式登记的工具按公共读工具
// 处理，但仍受 key 的允许表与适配器白名单约束）。
func (g *Gateway) visibleTo(ctx context.Context, id *Identity, tv ToolView, meta *Tool) bool {
	if !id.HasScope(ScopeRead) && !id.HasScope(ScopeExecute) {
		return false
	}
	if meta != nil {
		if !meta.Enabled {
			return false
		}
		if !toolVisible(meta, id) {
			return false
		}
		if !keyAllowsTool(g.keyOf(id), meta.Name) {
			return false
		}
	}
	return true
}

// keyOf 从身份还原 key 视图（远端账本下 Record 同样提供允许表）。
func (g *Gateway) keyOf(id *Identity) *Key {
	if id == nil {
		return nil
	}
	if id.Record == nil {
		return nil
	}
	return &Key{
		ID:               id.Record.ID,
		TenantID:         id.Record.TenantID,
		OrgID:            id.Record.OrgID,
		UserID:           id.Record.UserID,
		AgentID:          id.Record.AgentID,
		Scopes:           id.Record.Scopes,
		AllowedTools:     id.Record.AllowedTools,
		DeniedTools:      id.Record.DeniedTools,
		AllowedAdapters:  id.Record.AllowedAdapters,
		Bindings:         id.Record.Bindings,
		ToolAllowEnforce: id.Record.Enforce,
		ExpiresAt:        id.Record.ExpiresAt,
	}
}

// handleToolsCall 执行一次工具调用（治理链在此汇聚）。
func (g *Gateway) handleToolsCall(ctx context.Context, id *Identity, r *http.Request, req jsonrpcRequest) (json.RawMessage, *RPCError, int) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, Errorf(CodeInvalidParams, "invalid params: %s", err.Error()), http.StatusOK
		}
	}
	if params.Name == "" {
		params.Name = r.Header.Get("Mcp-Name")
	}
	if params.Name == "" {
		return nil, Errorf(CodeInvalidParams, "tool name is required"), http.StatusOK
	}

	args, err := decodeArgs(params.Arguments)
	if err != nil {
		return nil, RPCErrorOf(err), http.StatusOK
	}

	tool, adapter, err := g.resolveTool(ctx, id.TenantID, params.Name)
	if err != nil {
		return nil, RPCErrorOf(err), http.StatusNotFound
	}

	// ── 授权链（顺序固定，便于验收与审计）──
	if !id.HasScope(ScopeExecute) {
		authzDeniedTotal.WithLabelValues("scope").Inc()
		return nil, Errorf(CodeToolNotAllowed, "missing scope %s", ScopeExecute), http.StatusForbidden
	}
	if meta := tool; meta != nil {
		if !toolVisible(meta, id) {
			authzDeniedTotal.WithLabelValues("visibility").Inc()
			g.auditToolDenied(id, meta.Name, "visibility")
			return nil, Errorf(CodeToolNotAllowed, "tool is not visible to this principal"), http.StatusForbidden
		}
		if !keyAllowsTool(g.keyOf(id), meta.Name) {
			authzDeniedTotal.WithLabelValues("tool_allow").Inc()
			toolAllowChecksTotal.WithLabelValues("denied").Inc()
			g.auditToolDenied(id, meta.Name, "tool_allow")
			return nil, Errorf(CodeToolNotAllowed, "tool is not in the allowed list"), http.StatusForbidden
		}
	}
	if !bindingEnabled(g.keyOf(id), adapter.Name) {
		bindingsChecksTotal.WithLabelValues("denied").Inc()
		g.auditToolDenied(id, params.Name, "binding_disabled")
		return nil, Errorf(CodeToolNotAllowed, "backend %q is disabled for this credential", adapter.Name), http.StatusForbidden
	}
	bindingsChecksTotal.WithLabelValues("allowed").Inc()
	toolAllowChecksTotal.WithLabelValues("allowed").Inc()

	// ── 敏感度审批 ──
	if tool != nil && (tool.RequireApproval || tool.Sensitivity == SensitivityHigh) {
		if err := g.checkApproval(r, id, tool); err != nil {
			approvalsRequiredTotal.Inc()
			g.auditToolDenied(id, params.Name, "approval_required")
			return nil, RPCErrorOf(err), http.StatusForbidden
		}
	}

	// ── 参数校验 ──
	if tool != nil {
		if err := validateParams(tool, args); err != nil {
			authzDeniedTotal.WithLabelValues("params").Inc()
			g.auditToolDenied(id, params.Name, "invalid_params")
			return nil, RPCErrorOf(err), http.StatusForbidden
		}
	}

	// ── 限流 ──
	limit := g.rateLimitFor(tool)
	if ok, wait := g.limiter.Allow("key:"+id.KeyID+":class:"+string(classOf(tool)), limit); !ok {
		rateLimitedTotal.Inc()
		g.audit.Record(AuditEvent{
			Kind:     AuditKindRate,
			Outcome:  OutcomeDenied,
			TenantID: id.TenantID,
			OrgID:    id.OrgID,
			UserID:   id.UserID,
			KeyID:    id.KeyID,
			AgentID:  id.AgentID,
			Tool:     params.Name,
			Adapter:  adapter.Name,
			Reason:   "rate_limited",
			Detail:   map[string]any{"limit_per_min": limit, "retry_after_sec": int(wait.Seconds())},
		})
		return nil, Errorf(CodeRateLimited, "rate limit exceeded"), http.StatusTooManyRequests
	}
	if adapter.RateLimitPerMin > 0 {
		if ok, _ := g.limiter.Allow("adapter:"+adapter.Name, adapter.RateLimitPerMin); !ok {
			rateLimitedTotal.Inc()
			return nil, Errorf(CodeRateLimited, "adapter %q rate limit exceeded", adapter.Name), http.StatusTooManyRequests
		}
	}

	// ── 熔断 ──
	if !g.breaker.Allow(adapter.Name) {
		circuitOpenTotal.Inc()
		g.audit.Record(AuditEvent{
			Kind:     AuditKindUpstream,
			Outcome:  OutcomeDenied,
			TenantID: id.TenantID,
			OrgID:    id.OrgID,
			UserID:   id.UserID,
			KeyID:    id.KeyID,
			Tool:     params.Name,
			Adapter:  adapter.Name,
			Reason:   "circuit_open",
		})
		return nil, Errorf(CodeCircuitOpen, "upstream %q circuit is open", adapter.Name), http.StatusServiceUnavailable
	}

	// ── 上游调用 ──
	rawName := params.Name
	if tool != nil && tool.RawName != "" {
		rawName = tool.RawName
	}
	callArgs, _ := json.Marshal(args)
	upParams, _ := json.Marshal(map[string]any{"name": rawName, "arguments": json.RawMessage(callArgs)})
	timeout := adapter.TimeoutMS
	if timeout <= 0 {
		timeout = int(g.cfg.ToolTimeout / time.Millisecond)
	}
	res, err := g.upstream.Call(ctx, CallContext{
		Adapter:         adapter,
		Method:          "tools/call",
		Params:          upParams,
		ToolName:        rawName,
		Capability:      adapter.Capability,
		ProtocolVersion: ProtocolVersion,
		Timeout:         time.Duration(timeout) * time.Millisecond,
		Retries:         g.retriesFor(tool),
	})
	if err != nil {
		g.breaker.Failure(adapter.Name)
		upstreamErrorsTotal.Inc()
		g.audit.Record(AuditEvent{
			Kind:     AuditKindUpstream,
			Outcome:  OutcomeError,
			TenantID: id.TenantID,
			OrgID:    id.OrgID,
			UserID:   id.UserID,
			KeyID:    id.KeyID,
			AgentID:  id.AgentID,
			Tool:     params.Name,
			Adapter:  adapter.Name,
			Reason:   "upstream_error",
			Detail:   map[string]any{"error": err.Error()},
		})
		var rpcErr *RPCError
		if uerr, ok := err.(*UpstreamHTTPError); ok && uerr.Fatal {
			rpcErr = Errorf(CodeUpstreamError, "upstream error: %s", uerr.Error())
			return nil, rpcErr, http.StatusBadGateway
		}
		return nil, Errorf(CodeUpstreamError, "upstream call failed: %s", err.Error()), http.StatusBadGateway
	}
	g.breaker.Success(adapter.Name)

	// ── 出参脱敏 ──
	out := res.Result
	if len(out) > 0 && g.cfg.Redactor != nil {
		masked := g.redactor.MaskToolResult(string(out))
		if masked != string(out) {
			redactionsTotal.Inc()
			out = json.RawMessage(masked)
		}
	}

	latency := res.Latency.Milliseconds()
	toolCallsTotal.WithLabelValues(params.Name, "ok").Inc()
	toolLatency.WithLabelValues(params.Name).Observe(res.Latency.Seconds())
	g.audit.Record(AuditEvent{
		Kind:      AuditKindCall,
		Outcome:   OutcomeAllowed,
		TenantID:  id.TenantID,
		OrgID:     id.OrgID,
		UserID:    id.UserID,
		KeyID:     id.KeyID,
		AgentID:   id.AgentID,
		Tool:      params.Name,
		Adapter:   adapter.Name,
		Method:    "tools/call",
		LatencyMS: latency,
	})
	return out, nil, http.StatusOK
}

// resolveTool 解析工具与其归属适配器。
//
// 优先命中注册面（显式注册名）；未命中时按 `<adapter>__<tool>` 规范化名回退。
func (g *Gateway) resolveTool(ctx context.Context, tenantID, name string) (*Tool, Adapter, error) {
	tool, err := g.store.GetTool(ctx, tenantID, name)
	if err != nil {
		return nil, Adapter{}, err
	}
	if tool != nil {
		adapter, err := g.store.GetAdapter(ctx, tenantID, tool.Adapter)
		if err != nil {
			return nil, Adapter{}, err
		}
		if adapter == nil {
			return nil, Adapter{}, fmt.Errorf("%w: adapter %q of tool %q not found", ErrNotFound, tool.Adapter, name)
		}
		return tool, *adapter, nil
	}
	// 规范化名回退：<adapter>__<tool>
	if idx := strings.Index(name, "__"); idx > 0 {
		adapterName := name[:idx]
		adapter, err := g.store.GetAdapter(ctx, tenantID, adapterName)
		if err != nil {
			return nil, Adapter{}, err
		}
		if adapter != nil {
			return &Tool{
				Name:        name,
				RawName:     name[idx+2:],
				Adapter:     adapterName,
				TenantID:    adapter.TenantID,
				Visibility:  VisibilityPublic,
				Sensitivity: SensitivityRead,
				Enabled:     true,
			}, *adapter, nil
		}
	}
	return nil, Adapter{}, fmt.Errorf("%w: tool %q", ErrNotFound, name)
}

// adapterTools 取某个适配器的工具清单（带短缓存）。
func (g *Gateway) adapterTools(ctx context.Context, a Adapter) []ToolView {
	g.toolCacheMu.Lock()
	if entry, ok := g.toolCache[a.Name]; ok && g.now().Before(entry.expires) {
		g.toolCacheMu.Unlock()
		return entry.tools
	}
	g.toolCacheMu.Unlock()

	res, err := g.upstream.Call(ctx, CallContext{
		Adapter:         a,
		Method:          "tools/list",
		Capability:      a.Capability,
		ProtocolVersion: ProtocolVersion,
		Timeout:         g.cfg.ToolTimeout,
	})
	if err != nil {
		healthCheckFailuresTotal.Inc()
		upstreamHealthy.WithLabelValues(a.Name).Set(0)
		g.logger.Warn("mcp: tools/list failed", "adapter", a.Name, "error", err)
		return nil
	}
	upstreamHealthy.WithLabelValues(a.Name).Set(1)

	var payload struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
			Annotations map[string]any  `json:"annotations"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(res.Result, &payload); err != nil {
		return nil
	}

	views := make([]ToolView, 0, len(payload.Tools))
	for _, t := range payload.Tools {
		view := ToolView{
			Name:        NormalizeToolName(a.Name, t.Name),
			Description: t.Description,
			InputSchema: t.InputSchema,
			Annotations: t.Annotations,
			Meta: map[string]any{
				"adapter":    a.Name,
				"raw_name":   t.Name,
				"capability": a.Capability,
			},
		}
		views = append(views, view)
		if g.cfg.AutoRegisterTools {
			g.autoRegister(ctx, a, t.Name, t.Description, t.InputSchema, view.Name)
		}
	}

	g.toolCacheMu.Lock()
	g.toolCache[a.Name] = toolCacheEntry{tools: views, expires: g.now().Add(g.cfg.ToolCacheTTL)}
	g.toolCacheMu.Unlock()
	return views
}

// autoRegister 把上游发现的工具登记进注册面（可搜索、可治理）。
func (g *Gateway) autoRegister(ctx context.Context, a Adapter, rawName, description string, schema json.RawMessage, exposedName string) {
	existing, err := g.store.GetTool(ctx, a.TenantID, exposedName)
	if err != nil || existing != nil {
		return
	}
	tool := Tool{
		Name:        exposedName,
		RawName:     rawName,
		Adapter:     a.Name,
		TenantID:    a.TenantID,
		Description: description,
		InputSchema: schema,
		Visibility:  VisibilityPublic,
		Sensitivity: SensitivityRead,
		Class:       ClassRead,
		Enabled:     true,
		CreatedAt:   g.now(),
		UpdatedAt:   g.now(),
	}
	if err := g.store.PutTool(ctx, tool); err != nil {
		return
	}
	g.catalogMu.Lock()
	g.catalog[exposedName] = &tool
	g.catalogMu.Unlock()
}

// NormalizeToolName 生成聚合后的规范化工具名（`<adapter>__<tool>`）。
func NormalizeToolName(adapter, raw string) string {
	if raw == "" {
		return adapter
	}
	if strings.Contains(raw, "__") {
		// 上游已带命名空间，保持原样（避免二次加前缀）。
		return raw
	}
	return adapter + "__" + raw
}

// handleForward 把 resources/prompts 类只读方法广播到各适配器并合并。
func (g *Gateway) handleForward(ctx context.Context, id *Identity, req jsonrpcRequest) (json.RawMessage, *RPCError, int) {
	adapters, err := g.store.ListAdapters(ctx, id.TenantID)
	if err != nil {
		return nil, Errorf(CodeInternalError, "list adapters: %s", err.Error()), http.StatusInternalServerError
	}
	merged := map[string]any{}
	for _, a := range adapters {
		if !a.Enabled || !keyAllowsAdapter(g.keyOf(id), a.Name) {
			continue
		}
		res, err := g.upstream.Call(ctx, CallContext{
			Adapter:         a,
			Method:          req.Method,
			Params:          req.Params,
			Capability:      a.Capability,
			ProtocolVersion: ProtocolVersion,
			Timeout:         g.cfg.ToolTimeout,
		})
		if err != nil {
			continue
		}
		mergeListResult(merged, res.Result)
	}
	raw, _ := json.Marshal(merged)
	return raw, nil, http.StatusOK
}

// mergeListResult 把上游的列表型结果合并进聚合结果。
func mergeListResult(dst map[string]any, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}
	for _, key := range []string{"resources", "prompts"} {
		items, ok := payload[key]
		if !ok {
			continue
		}
		var list []any
		if err := json.Unmarshal(items, &list); err != nil {
			continue
		}
		existing, _ := dst[key].([]any)
		dst[key] = append(existing, list...)
	}
}

// rateLimitFor 返回工具的每分钟限额（工具级 > 类别级 > 默认级）。
func (g *Gateway) rateLimitFor(tool *Tool) int {
	if tool != nil && tool.RateLimitPerMin > 0 {
		return tool.RateLimitPerMin
	}
	if limit, ok := g.cfg.ClassRateLimits[classOf(tool)]; ok {
		return limit
	}
	return g.cfg.DefaultRateLimitPerMin
}

// retriesFor 返回工具的重试次数（写类不重试）。
func (g *Gateway) retriesFor(tool *Tool) int {
	if tool == nil {
		return 0
	}
	switch classOf(tool) {
	case ClassRead:
		return 1
	default:
		return 0
	}
}

// checkApproval 校验审批凭据。
//
// 审批凭据通过 `X-MCP-Approval: <approvalID>` 头传递；必须是已批准、未过期、
// 且主体匹配的审批记录。
func (g *Gateway) checkApproval(r *http.Request, id *Identity, tool *Tool) error {
	grantID := r.Header.Get("X-MCP-Approval")
	if grantID == "" {
		return fmt.Errorf("%w: tool %q requires an approval grant", ErrForbidden, tool.Name)
	}
	approval, err := g.store.GetApproval(r.Context(), grantID)
	if err != nil {
		return err
	}
	if approval == nil {
		return fmt.Errorf("%w: approval %q not found", ErrForbidden, grantID)
	}
	if approval.Status != ApprovalApproved {
		return fmt.Errorf("%w: approval %q is %s", ErrForbidden, grantID, approval.Status)
	}
	if approval.Tool != "" && approval.Tool != tool.Name && !matchPattern(approval.Tool, tool.Name) {
		return fmt.Errorf("%w: approval %q does not cover tool %q", ErrForbidden, grantID, tool.Name)
	}
	if approval.ExpiresAt != nil && g.now().After(*approval.ExpiresAt) {
		return fmt.Errorf("%w: approval %q expired", ErrForbidden, grantID)
	}
	if approval.Requester != "" && id.UserID != "" && approval.Requester != id.UserID {
		return fmt.Errorf("%w: approval %q belongs to another principal", ErrForbidden, grantID)
	}
	return nil
}

// auditToolDenied 记录一次授权拒绝。
func (g *Gateway) auditToolDenied(id *Identity, tool, reason string) {
	g.audit.Record(AuditEvent{
		Kind:     AuditKindAuthz,
		Outcome:  OutcomeDenied,
		TenantID: id.TenantID,
		OrgID:    id.OrgID,
		UserID:   id.UserID,
		KeyID:    id.KeyID,
		AgentID:  id.AgentID,
		Tool:     tool,
		Reason:   reason,
	})
}

// ---------------------------------------------------------------------------
// 响应写出
// ---------------------------------------------------------------------------

func writeRPCResult(w http.ResponseWriter, status int, id json.RawMessage, result json.RawMessage) {
	payload := map[string]any{"jsonrpc": "2.0", "result": result}
	if len(id) > 0 {
		payload["id"] = id
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeRPCError(w http.ResponseWriter, status int, id json.RawMessage, rpcErr *RPCError) {
	payload := map[string]any{"jsonrpc": "2.0", "error": rpcErr}
	if len(id) > 0 {
		payload["id"] = id
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
