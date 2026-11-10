package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
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
	// ControlScopeDisabled 关闭「业务 key 按 scope 访问控制面」。
	//
	// ★ 零值 = false ⇒ **能力默认开启**（新能力不需要额外配置即可用）。
	//   需要回退到「控制面只认管理令牌」的既有口径时，用环境变量
	//   MCP_GATEWAY_CONTROL_SCOPE=off（或构造 Config 时置 true）。
	ControlScopeDisabled bool
	// Now 时钟（测试可注入）。
	Now func() time.Time
	// LocalTools 是网关本地工具注册表（由网关自己执行，不经上游适配器）。
	//
	// ★ 零值 / nil ⇒ 没有任何本地工具 ⇒ 行为与引入本字段前完全一致。
	//   用途：把「网关自有的记忆能力」（按步取回明细 / 语义召回长期记忆）
	//   以 MCP 工具的形式暴露给 MCP 客户端 —— 现状 mcp 包的 tools/call
	//   只支持上游适配器，网关自有能力无法被列出、更无法被调用。
	LocalTools *LocalToolRegistry
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
	toolCache   map[toolCacheKey]toolCacheEntry
	// Serialize registration existence checks with their writes so concurrent
	// register-only requests cannot turn a create into an unauthorized replacement.
	registrationMu sync.Mutex
}

type toolCacheEntry struct {
	tools   []ToolView
	expires time.Time
}

type toolCacheKey struct{ tenant, adapter, capability string }

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
		toolCache:  map[toolCacheKey]toolCacheEntry{},
	}
	g.bootstrap = NewBootstrapService(g, cfg.Bootstrap)
	// ★ 运行时长指标落一个初值：只声明不 Set ⇒ 它在 /metrics 上**恒为 0**
	//   （普通 Gauge 注册即存在、但永远不动 ⇒ 面板上是一条平的线；实测口径见
	//   mcp/metrics.go 的注释）⇒ internal/metricguard 门也会红。
	//   在这里落初值 ⇒ 进程一起来它就带上真实起点；之后由 control.go 的两个
	//   健康端点刷新（/healthz 是探针端点，部署里被周期性打 ⇒ 值不会长期陈旧）。
	uptimeSeconds.Set(float64(g.UptimeSeconds()))
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
	// ── 请求级能力域（?capability=X）──
	//
	// 契约与既有 tools/mcp-gateway 的 gateway.js 逐字对齐：
	//   · 合法 ⇒ 计入 requests_by_capability，并把 X-MCP-Capability /
	//     X-MCP-Capability-Tools 注入上游（**覆盖**适配器默认能力域）；
	//   · 非法 ⇒ HTTP 400 + JSON-RPC -32602 + "Unknown capability: X. Valid: ..."。
	//
	// ★ 校验必须在计数**之前**：非法能力域不该污染 requests_by_capability
	//   （既有实现也是先判合法再计数）。
	capability := r.URL.Query().Get("capability")
	if capability != "" {
		if !ValidCapability(capability) {
			writeRPCError(w, http.StatusBadRequest, nil, Errorf(CodeInvalidParams,
				"Unknown capability: %s. Valid: %s", capability, strings.Join(ValidCapabilities(), ", ")))
			return
		}
		requestsByCapability.WithLabelValues(capability).Inc()
	}
	ctx := withCapability(r.Context(), capability)
	method := r.Header.Get("Mcp-Method")
	if method != "" {
		requestsByMethod.WithLabelValues(method).Inc()
	}

	// 请求体上限：**显式拒绝**（413 + -32004，与既有 gateway.js:295 逐字一致）。
	//
	// ★ 此前用 LimitReader 静默截断 ⇒ 超限表现为「JSON 解析失败(-32700)」，
	//   把真因（体太大）掩盖成了客户端格式错误 ⇒ 既有客户端按 -32004 分支
	//   处理体超限，永远命中不到。
	body, err := io.ReadAll(io.LimitReader(r.Body, g.cfg.MaxBodyBytes+1))
	if err != nil {
		writeRPCError(w, http.StatusBadRequest, nil, Errorf(CodeParseError, "read body: %s", err.Error()))
		return
	}
	if int64(len(body)) > g.cfg.MaxBodyBytes {
		writeRPCError(w, http.StatusRequestEntityTooLarge, nil, Errorf(CodePayloadTooLarge,
			"Request body exceeds the inspection limit (%d bytes)", g.cfg.MaxBodyBytes).
			WithData(map[string]any{"limit_bytes": g.cfg.MaxBodyBytes}))
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

	id, err := g.authenticate(ctx, r)
	if err != nil {
		g.audit.Record(AuditEvent{
			Kind:    AuditKindAuthz,
			Outcome: OutcomeDenied,
			Method:  req.Method,
			Reason:  "unauthorized",
		})
		// ★ 凭证「失效/吊销」⇒ 401 + **-32001**（既有 bindings.js 逐字一致）；
		//   凭证「不存在/未知」⇒ 401 + -32600（既有对此不产生 JSON-RPC 错误，
		//   交由上游 401 ⇒ 本实现沿用标准段，不去占用既有自定义码）。
		if errors.Is(err, ErrCredentialInactive) {
			writeRPCError(w, http.StatusUnauthorized, req.ID,
				Errorf(CodeBindingDenied, "API key is inactive or revoked"))
			return
		}
		writeRPCError(w, http.StatusUnauthorized, req.ID, Errorf(CodeInvalidRequest, "unauthorized"))
		return
	}
	g.touchKey(ctx, id)

	result, rpcErr, status := g.dispatch(ctx, id, r, req)
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
		writeRPCErrorWithHeaders(w, status, req.ID, rpcErr, errorHeaders(rpcErr))
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
	// ★ 网关本地工具也要出现在清单里 —— 否则客户端根本不知道有这条路可走。
	//	group 过滤与上游分支同一口径（审计轮修复：本地分支原本无条件全列）。
	views = append(views, g.localToolViews(id, group)...)
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

	// ── 网关本地工具（由网关自己执行，不经上游适配器）──
	// ★ 必须放在 resolveTool **之前**：本地工具没有 Adapter 记录，
	//   resolveTool 对没有适配器的工具直接 ErrNotFound。
	if lt, ok := g.localTool(params.Name); ok {
		return g.handleLocalToolCall(ctx, id, r, lt, args)
	}

	tool, adapter, err := g.resolveTool(ctx, id.TenantID, params.Name)
	if err != nil {
		return nil, RPCErrorOf(err), http.StatusNotFound
	}
	if !adapter.Enabled || tool != nil && !tool.Enabled {
		return nil, Errorf(CodeToolNotAllowed, "tool or backend is disabled"), http.StatusForbidden
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
		// ★ 遗留旧名计数器：既有 tool-allow.js 每次判定都记 checks_total，
		//   拒绝记 denied_total，**空允许表（执法跳过）记 not_enforced_total**
		//   ⇒ 运维靠第三个数就能判断「收口根本没生效」。
		toolAllowChecksTotal.Inc()
		if k := g.keyOf(id); k != nil && len(k.AllowedTools) == 0 && len(k.DeniedTools) == 0 {
			toolAllowNotEnforcedTotal.Inc()
		}
		if !keyAllowsTool(g.keyOf(id), meta.Name) {
			authzDeniedTotal.WithLabelValues("tool_allow").Inc()
			toolAllowResultTotal.WithLabelValues("denied").Inc()
			toolAllowDeniedTotal.Inc() // 遗留旧名计数器
			g.auditToolDenied(id, meta.Name, "tool_allow")
			return nil, Errorf(CodeToolNotAllowed, "tool is not in the allowed list"), http.StatusForbidden
		}
	}
	bindingsChecksTotal.Inc() // 遗留旧名计数器（既有 bindings.js 同名）
	if !keyAllowsAdapter(g.keyOf(id), adapter.Name) {
		authzDeniedTotal.WithLabelValues("adapter_allow").Inc()
		g.auditToolDenied(id, toolNameOf(tool, params.Name), "adapter_allow")
		return nil, Errorf(CodeToolNotAllowed, "backend %q is not in the allowed list for this credential", adapter.Name), http.StatusForbidden
	}
	if !bindingEnabled(g.keyOf(id), adapter.Name) {
		bindingsResultTotal.WithLabelValues("denied").Inc()
		bindingsDeniedTotal.Inc() // 遗留旧名计数器
		g.auditToolDenied(id, toolNameOf(tool, params.Name), "binding_disabled")
		return nil, Errorf(CodeBindingDenied, "backend %q is disabled for this credential", adapter.Name), http.StatusForbidden
	}
	bindingsResultTotal.WithLabelValues("allowed").Inc()
	toolAllowResultTotal.WithLabelValues("allowed").Inc()

	// ── 敏感度审批 ──
	if tool != nil && (tool.RequireApproval || tool.Sensitivity == SensitivityHigh) {
		if err := g.checkApproval(r, id, tool); err != nil {
			approvalsRequiredTotal.Inc()
			g.auditToolDenied(id, params.Name, "approval_required")
			// ★ 审批缺失有专属码（-32007，新增、既有未使用）⇒ 不再借用
			//   -32003（既有语义是 tool-allow 拒绝）：审批闸与允许表闸是
			//   两件事，客户端要能分开分支。
			return nil, Errorf(CodeApprovalRequired, "%s", err.Error()), http.StatusForbidden
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
		retryMS := retryAfterMillis(wait)
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
			Detail:   map[string]any{"limit_per_min": limit, "retry_after_sec": int(math.Ceil(wait.Seconds()))},
		})
		return nil, Errorf(CodeRateLimited, "Rate limit exceeded").
			WithData(map[string]any{"retry_after_ms": retryMS, "limit": limit}), http.StatusTooManyRequests
	}
	if adapter.RateLimitPerMin > 0 {
		if ok, wait := g.limiter.Allow("adapter:"+adapter.Name, adapter.RateLimitPerMin); !ok {
			rateLimitedTotal.Inc()
			return nil, Errorf(CodeRateLimited, "Rate limit exceeded").
				WithData(map[string]any{
					"retry_after_ms": retryAfterMillis(wait),
					"limit":          adapter.RateLimitPerMin,
					"adapter":        adapter.Name,
				}), http.StatusTooManyRequests
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
		Capability:      effectiveCapability(ctx, adapter.Capability),
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
		// ★ 超时**不单列错误码**：既有的上游错误（含超时）统一是 -32000 ⇒
		//   拆码会打断既有客户端的 -32000 分支。超时用 data.timeout 区分。
		if isTimeout(err) {
			return nil, Errorf(CodeUpstreamError, "upstream call timed out: %s", err.Error()).
				WithData(map[string]any{"timeout": true}), http.StatusBadGateway
		}
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
	key := toolCacheKey{tenant: a.TenantID, adapter: a.Name, capability: effectiveCapability(ctx, a.Capability)}
	g.toolCacheMu.Lock()
	if entry, ok := g.toolCache[key]; ok && g.now().Before(entry.expires) {
		g.toolCacheMu.Unlock()
		return entry.tools
	}
	g.toolCacheMu.Unlock()

	res, err := g.upstream.Call(ctx, CallContext{
		Adapter:         a,
		Method:          "tools/list",
		Capability:      effectiveCapability(ctx, a.Capability),
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
	g.toolCache[key] = toolCacheEntry{tools: views, expires: g.now().Add(g.cfg.ToolCacheTTL)}
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
			Capability:      effectiveCapability(ctx, a.Capability),
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
	if approval.TenantID != id.TenantID {
		return fmt.Errorf("%w: approval belongs to another tenant", ErrForbidden)
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
	if approval.Requester != "" && approval.Requester != id.UserID {
		return fmt.Errorf("%w: approval %q belongs to another principal", ErrForbidden, grantID)
	}
	return nil
}

// isTimeout 判断上游错误是否为超时（context 截止 / 网络超时）。
//
// ★ 只用于**区分**（写进 data.timeout 与审计），**不改错误码** —— 既有的
//
//	上游错误（含超时）统一是 -32000。
func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}

// toolNameOf 取工具名（工具未登记时退回调用方给的名字）。
func toolNameOf(tool *Tool, fallback string) string {
	if tool != nil && tool.Name != "" {
		return tool.Name
	}
	return fallback
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
// 请求级能力域
// ---------------------------------------------------------------------------

// ctxKeyCapability 是请求级能力域在 context 里的键。
type ctxKeyCapability struct{}

// withCapability 把**已通过合法性校验**的请求级能力域写进上下文。
func withCapability(ctx context.Context, cap string) context.Context {
	if cap == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyCapability{}, cap)
}

// capabilityFromContext 取请求级能力域（空 = 本次请求未指定）。
func capabilityFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyCapability{}).(string)
	return v
}

// effectiveCapability 返回本次上游调用生效的能力域：
// **请求级 ?capability= 覆盖优先于适配器默认能力域**。
//
// 与既有 tools/mcp-gateway 的语义一致：既有实现把 x-mcp-capability 直接写在
// 入站请求头上再往下游透传 ⇒ 谁在最后改写头谁说了算；这里等价地让请求级
// 覆盖适配器默认。
func effectiveCapability(ctx context.Context, adapterCap string) string {
	if req := capabilityFromContext(ctx); req != "" {
		return req
	}
	return adapterCap
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
	writeRPCErrorWithHeaders(w, status, id, rpcErr, nil)
}

// writeRPCErrorWithHeaders 写 JSON-RPC 错误响应，并附带附加 HTTP 头。
func writeRPCErrorWithHeaders(w http.ResponseWriter, status int, id json.RawMessage, rpcErr *RPCError, headers map[string]string) {
	for k, v := range headers {
		w.Header().Set(k, v)
	}
	payload := map[string]any{"jsonrpc": "2.0", "error": rpcErr}
	if len(id) > 0 {
		payload["id"] = id
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// errorHeaders 由错误体推导需要附带的 HTTP 头。
//
// ★ 目前只有限流需要：既有 tools/mcp-gateway 的 429 契约是
// 「HTTP 头 Retry-After（秒）+ JSON-RPC 错误码 -32002 + data.retry_after_ms
// （毫秒）」三者同源。既有客户端按头决定退避、按 data 做精细提示，
// 缺任何一个都会被判成异常响应 ⇒ 这里统一由错误体推导，不允许只给一半。
func errorHeaders(rpcErr *RPCError) map[string]string {
	if sec, ok := retryAfterSeconds(rpcErr); ok {
		return map[string]string{"Retry-After": sec}
	}
	return nil
}

// retryAfterMillis 把等待时长转成毫秒（至少 1ms），用于 retry_after_ms。
func retryAfterMillis(wait time.Duration) int {
	ms := int(wait.Milliseconds())
	if ms < 1 {
		ms = 1
	}
	return ms
}

// retryAfterSeconds 从限流错误体推出 Retry-After 的秒数（向上取整，最小 1）。
func retryAfterSeconds(rpcErr *RPCError) (string, bool) {
	if rpcErr == nil || rpcErr.Code != CodeRateLimited || rpcErr.Data == nil {
		return "", false
	}
	ms, ok := rpcErr.Data["retry_after_ms"].(int)
	if !ok || ms <= 0 {
		return "", false
	}
	sec := int(math.Ceil(float64(ms) / 1000))
	if sec < 1 {
		sec = 1
	}
	return strconv.Itoa(sec), true
}
