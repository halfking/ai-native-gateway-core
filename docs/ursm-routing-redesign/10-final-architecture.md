# URSM 最终完整方案 — Hook 驱动 + 插件化扩展

> 整合 08 (统一管线) + 09 (智能绑定) + Hook 体系 + 插件化架构
> 2026-07-13 最终完整版

## 1. 架构全景

### 1.1 核心设计原则

```
原则 1: Hook-Driven Architecture
  每个关键环节暴露 Hook 点，供会话治理、审计、监控等插件使用

原则 2: Plugin-Based Extension
  核心路由稳定，额外资源管理(限流/降级/缓存)以插件形式扩展

原则 3: Scenario-Driven Testing
  全场景业务模拟 → 拟合实际流量 → 细化处理逻辑

原则 4: Observability First
  每个决策点可追踪、可度量、可回放
```

### 1.2 分层架构

```
┌──────────────────────────────────────────────────────────────┐
│  Layer 0: Hook Registry & Plugin Manager                      │
│  - hooks.Registry: 5 Phase Hook 链                            │
│  - plugins.Manager: 动态加载/卸载插件                          │
└──────────────────────────────────────────────────────────────┘
                            │
┌──────────────────────────────────────────────────────────────┐
│  Layer 1: Request Pipeline (handler.go)                       │
│  - Auth + RPM Gate + API Key Concurrent                       │
│  - Hook: PhasePreRouting (audit, security, preprocessing)     │
└──────────────────────────────────────────────────────────────┘
                            │
┌──────────────────────────────────────────────────────────────┐
│  Layer 2: Routing Core (router.go + ursm)                     │
│  - Session Binding Lookup (强/软/无绑定)                       │
│  - DB/Redis Candidate Loading                                 │
│  - P2C + CostScorer Selection                                 │
│  - Hook: PhaseRouting (route_decision, cost_optimization)     │
└──────────────────────────────────────────────────────────────┘
                            │
┌──────────────────────────────────────────────────────────────┐
│  Layer 3: Resource Gating (executor.go)                       │
│  - Provider Gate Check                                         │
│  - FP Slot Acquire / AcquireUntil                             │
│  - Concurrency Slot Acquire / AcquireUntil                    │
│  - Circuit Breaker                                             │
│  - Hook: PhasePreUpstream (resource_acquire, transformation)  │
└──────────────────────────────────────────────────────────────┘
                            │
┌──────────────────────────────────────────────────────────────┐
│  Layer 4: Upstream Execution                                   │
│  - Protocol Conversion (IR)                                    │
│  - Upstream API Call                                           │
│  - Hook: PhasePostUpstream (response_processing, caching)     │
└──────────────────────────────────────────────────────────────┘
                            │
┌──────────────────────────────────────────────────────────────┐
│  Layer 5: Post-Processing                                      │
│  - StateRecorder.Record (状态+模型索引双写)                    │
│  - Session Binding Create/Update                              │
│  - Resource Release (Conc → FP → Limiter)                     │
│  - Hook: PhasePostResponse (telemetry, async_tasks)           │
└──────────────────────────────────────────────────────────────┘
```

## 2. Hook 体系设计

### 2.1 URSM 专用 Hook 点

在现有 5 Phase 基础上，增加 URSM 细粒度 Hook：

```go
// URSM Hook Points (注册到现有 Phase)
const (
    // PhasePreRouting 细分
    HookAuthCompleted        = "auth_completed"         // 认证完成
    HookRateLimitChecked     = "rate_limit_checked"     // 限流检查完成
    HookKeyConcurrentAcquired = "key_concurrent_acquired" // API Key 并发获取
    
    // PhaseRouting 细分
    HookBindingLookup        = "binding_lookup"         // 绑定查询
    HookBindingHit           = "binding_hit"            // 绑定命中
    HookBindingMiss          = "binding_miss"           // 绑定未命中
    HookCandidateLoaded      = "candidate_loaded"       // 候选加载完成
    HookCandidateFiltered    = "candidate_filtered"     // 候选过滤完成
    HookCostScored           = "cost_scored"            // 成本评分完成
    HookRouteSelected        = "route_selected"         // 路由选定
    HookBindingReEvaluate    = "binding_reevaluate"     // 软绑定重选触发
    
    // PhasePreUpstream 细分
    HookProviderGateCheck    = "provider_gate_check"    // 供应商门禁检查
    HookFPSlotAcquire        = "fp_slot_acquire"        // FP 槽获取
    HookConcSlotAcquire      = "conc_slot_acquire"      // 并发槽获取
    HookCircuitBreakerCheck  = "circuit_breaker_check"  // 熔断器检查
    HookResourceDegraded     = "resource_degraded"      // 资源降级
    HookUpstreamPrepare      = "upstream_prepare"       // 上游准备完成
    
    // PhasePostUpstream 细分
    HookUpstreamSuccess      = "upstream_success"       // 上游成功
    HookUpstreamFailure      = "upstream_failure"       // 上游失败
    HookStateRecorded        = "state_recorded"         // 状态记录完成
    HookBindingCreated       = "binding_created"        // 绑定创建
    HookBindingUpdated       = "binding_updated"        // 绑定更新
    HookBindingInvalidated   = "binding_invalidated"    // 绑定失效
    
    // PhasePostResponse 细分
    HookResourceReleased     = "resource_released"      // 资源释放
    HookTelemetryRecorded    = "telemetry_recorded"     // 遥测记录
    HookSessionPersisted     = "session_persisted"      // 会话持久化
)
```

### 2.2 Hook Environment 扩展

```go
// URSMEnvironment 扩展现有 hooks.Environment
type URSMEnvironment struct {
    *hooks.Environment
    
    // 路由上下文
    Routing *RoutingContext
    
    // 资源上下文
    Resources *ResourceContext
    
    // 绑定上下文
    Binding *BindingContext
    
    // 成本上下文
    Cost *CostContext
}

type RoutingContext struct {
    StdModel         string
    Candidates       []RouteNode
    SelectedNode     *RouteNode
    SelectionReason  string
    DBDegraded       bool
    RedisDegraded    bool
}

type ResourceContext struct {
    FPSlotAcquired    bool
    FPSlotIndex       int
    FPSlotDegraded    bool
    ConcSlotAcquired  bool
    ConcSlotCurrent   int
    ConcSlotLimit     int
    CircuitState      string
    ProviderGatePassed bool
}

type BindingContext struct {
    BindingLevel      int       // 1=强, 2=软, 3=无
    BindingHit        bool
    BindingCredID     int
    BindingProviderID int
    BindingRawModel   string
    BindingFPSlot     int
    ReEvaluateTriggered bool
    ReEvaluateReason  string
}

type CostContext struct {
    CandidateScores   map[int]float64
    SelectedScore     float64
    PricePerRequest   float64
    EstimatedLatency  int
    ResourcePressure  float64
}
```

### 2.3 Hook 调用时序

```go
// handler.go - Phase 1: PreRouting
func (h *ChatHandler) Handle(ctx context.Context, req *Request) (*Response, error) {
    env := NewURSMEnvironment(req.RequestID)
    
    // 1. 认证
    keyInfo, err := h.auth.Authenticate(ctx, req.APIKey)
    env.Emit(HookAuthCompleted, keyInfo)
    
    // 2. RPM 限流
    outcome := h.rateLimiter.CheckRPM(ctx, keyInfo.KeyID)
    env.Emit(HookRateLimitChecked, outcome)
    
    // 3. API Key 并发
    acquired := h.keyConcMgr.Acquire(ctx, keyInfo.KeyID)
    env.Emit(HookKeyConcurrentAcquired, acquired)
    defer h.keyConcMgr.Release(ctx, keyInfo.KeyID)
    
    // Execute PreRouting hooks
    h.hooks.Execute(ctx, hooks.PhasePreRouting, env)
    
    // Phase 2: Routing
    node, binding := h.router.SelectNode(ctx, req, env)
    
    // Phase 3: Resource + Upstream
    resp, err := h.executor.Execute(ctx, node, binding, env)
    
    // Phase 4: PostResponse
    h.hooks.Execute(ctx, hooks.PhasePostResponse, env)
    
    return resp, err
}

// router.go - Phase 2: Routing
func (r *Router) SelectNode(ctx context.Context, req *Request, env *URSMEnvironment) (*RouteNode, *SessionRouteBinding) {
    // 1. Binding Lookup
    binding := r.bindingCache.Get(req.SessionID, req.StdModel)
    env.Emit(HookBindingLookup, binding)
    
    if binding != nil {
        env.Binding.BindingHit = true
        env.Emit(HookBindingHit, binding)
        
        // 强绑定: 直接返回
        if binding.BindingLevel == 1 {
            return r.constructNode(binding), binding
        }
        
        // 软绑定: 判断是否需要重选
        if !r.shouldReEvaluate(binding, env) {
            return r.constructNode(binding), binding
        }
        env.Emit(HookBindingReEvaluate, env.Binding.ReEvaluateReason)
    } else {
        env.Emit(HookBindingMiss, nil)
    }
    
    // 2. Load Candidates
    candidates := r.loadCandidates(ctx, req.StdModel, env)
    env.Routing.Candidates = candidates
    env.Emit(HookCandidateLoaded, candidates)
    
    // 3. Filter Unavailable
    available := r.filterAvailable(ctx, candidates, env)
    env.Emit(HookCandidateFiltered, available)
    
    // 4. Cost Scoring
    scores := r.costScorer.Score(available, env)
    env.Cost.CandidateScores = scores
    env.Emit(HookCostScored, scores)
    
    // 5. P2C Selection
    selected := r.selectBest(available, scores, env)
    env.Routing.SelectedNode = selected
    env.Emit(HookRouteSelected, selected)
    
    // Execute Routing hooks
    r.hooks.Execute(ctx, hooks.PhaseRouting, env)
    
    return selected, nil
}

// executor.go - Phase 3: Resource + Upstream
func (e *Executor) Execute(ctx context.Context, node *RouteNode, binding *SessionRouteBinding, env *URSMEnvironment) (*Response, error) {
    // 1. Provider Gate
    gate := e.providerGates.Get(node.ProviderID)
    env.Emit(HookProviderGateCheck, gate)
    
    // 2. FP Slot
    if gate.FpSlotEnabled {
        lease, err := e.fpSlots.AcquireUntil(ctx, node.CredentialID, binding, deadline)
        env.Resources.FPSlotAcquired = (err == nil)
        env.Resources.FPSlotIndex = lease.SlotIndex
        env.Emit(HookFPSlotAcquire, lease)
        defer e.fpSlots.Release(ctx, lease)
    }
    
    // 3. Concurrency Slot
    if gate.ConcSlotEnabled {
        acquired, err := e.concSlots.AcquireUntil(ctx, node.CredentialID, binding, deadline)
        env.Resources.ConcSlotAcquired = acquired
        env.Emit(HookConcSlotAcquire, acquired)
        defer e.concSlots.Release(ctx, node.CredentialID)
    }
    
    // 4. Circuit Breaker
    if !e.breaker.Allow(node.CredentialID) {
        env.Resources.CircuitState = "open"
        env.Emit(HookCircuitBreakerCheck, "open")
        // 根据 binding level 决定 wait 或 fallback
    }
    
    env.Emit(HookUpstreamPrepare, node)
    
    // Execute PreUpstream hooks
    e.hooks.Execute(ctx, hooks.PhasePreUpstream, env)
    
    // 5. Upstream Call
    resp, err := e.callUpstream(ctx, node, env)
    
    if err != nil {
        env.Emit(HookUpstreamFailure, err)
        e.stateRecorder.Record(ctx, RecordRequest{
            CredentialID: node.CredentialID,
            Success: false,
            ErrorKind: classifyError(err),
        })
        env.Emit(HookStateRecorded, "failure")
    } else {
        env.Emit(HookUpstreamSuccess, resp)
        e.stateRecorder.Record(ctx, RecordRequest{
            CredentialID: node.CredentialID,
            Success: true,
        })
        env.Emit(HookStateRecorded, "success")
        
        // 创建/更新绑定
        if binding == nil {
            binding = e.createBinding(node, resp, env)
            env.Emit(HookBindingCreated, binding)
        } else {
            e.updateBinding(binding, resp, env)
            env.Emit(HookBindingUpdated, binding)
        }
    }
    
    // Execute PostUpstream hooks
    e.hooks.Execute(ctx, hooks.PhasePostUpstream, env)
    
    return resp, err
}
```

## 3. 插件化扩展机制

### 3.1 Plugin 接口

```go
// Plugin 定义了可动态加载的扩展插件
type Plugin interface {
    // Name 返回插件名称
    Name() string
    
    // Version 返回插件版本
    Version() string
    
    // Init 初始化插件 (传入 URSM 核心依赖)
    Init(deps *PluginDependencies) error
    
    // Start 启动插件
    Start(ctx context.Context) error
    
    // Stop 停止插件
    Stop(ctx context.Context) error
    
    // Hooks 返回插件注册的 Hook 列表
    Hooks() []hooks.Hook
    
    // HealthCheck 健康检查
    HealthCheck(ctx context.Context) error
}

type PluginDependencies struct {
    Redis       redis.UniversalClient
    DB          *pgxpool.Pool
    HookRegistry *hooks.HookRegistry
    Metrics     prometheus.Registerer
    Logger      *slog.Logger
    Config      map[string]interface{}
}

// PluginManager 管理插件生命周期
type PluginManager struct {
    plugins map[string]Plugin
    mu      sync.RWMutex
}

func (pm *PluginManager) Register(plugin Plugin) error
func (pm *PluginManager) Start(ctx context.Context, name string) error
func (pm *PluginManager) Stop(ctx context.Context, name string) error
func (pm *PluginManager) Reload(ctx context.Context, name string) error
func (pm *PluginManager) List() []PluginInfo
```

### 3.2 内置插件示例

```go
// 1. Session Governor Plugin (会话治理)
type SessionGovernorPlugin struct {
    // 监控会话路由稳定性、成本趋势、异常切换
    // Hook: binding_hit, binding_invalidated, cost_scored
}

// 2. Cost Optimizer Plugin (成本优化)
type CostOptimizerPlugin struct {
    // 动态调整 CostScorer 权重、触发软绑定重选
    // Hook: cost_scored, binding_reevaluate
}

// 3. Resource Predictor Plugin (资源预测)
type ResourcePredictorPlugin struct {
    // 预测 FP/并发槽压力，提前触发降级
    // Hook: fp_slot_acquire, conc_slot_acquire, resource_degraded
}

// 4. Route Auditor Plugin (路由审计)
type RouteAuditorPlugin struct {
    // 记录每次路由决策的完整上下文，用于回放和分析
    // Hook: route_selected, upstream_failure, binding_created
}

// 5. Adaptive Throttle Plugin (自适应限流)
type AdaptiveThrottlePlugin struct {
    // 根据上游响应时间动态调整 API Key RPM/Concurrent
    // Hook: upstream_success, rate_limit_checked
}
```

### 3.3 插件配置示例

```yaml
# plugins.yaml
plugins:
  - name: session_governor
    enabled: true
    version: "1.0.0"
    config:
      alert_on_frequent_switch: true
      switch_threshold: 3  # 5分钟内切换 >3 次告警
      
  - name: cost_optimizer
    enabled: true
    config:
      optimization_window: 300  # 5分钟
      price_weight_adaptive: true
      
  - name: resource_predictor
    enabled: false
    config:
      prediction_horizon: 60  # 预测未来 60s
      
  - name: route_auditor
    enabled: true
    config:
      retention_days: 7
      sample_rate: 0.1  # 采样 10%
```

## 4. 全场景业务模拟

见下一个文档: `10-scenario-simulation.md`

## 5. 核心接口汇总

### 5.1 StateRecorder (状态管理核心)

```go
type StateRecorder interface {
    Record(ctx context.Context, req RecordRequest) error
    IsAvailable(ctx context.Context, credID int, stdModel string) (bool, string)
    GetNodeState(ctx context.Context, credID int, stdModel string) (*NodeReadState, error)
}
```

### 5.2 BindingManager (绑定管理核心)

```go
type BindingManager interface {
    Get(sessionID, stdModel string) (*SessionRouteBinding, error)
    Create(sessionID, stdModel string, node *RouteNode, resp *Response) (*SessionRouteBinding, error)
    Update(binding *SessionRouteBinding, resp *Response) error
    Invalidate(sessionID, stdModel string, reason string) error
    ShouldReEvaluate(binding *SessionRouteBinding) (bool, string)
}
```

### 5.3 CostScorer (成本评分核心)

```go
type CostScorer interface {
    Score(nodes []*NodeReadState, load *ResourceLoad) map[int]float64
    SetWeights(price, speed, stability, pressure float64)
}
```

### 5.4 ResourceGate (资源门禁核心)

```go
type ResourceGate interface {
    // FP Slot
    AcquireFPSlot(ctx context.Context, credID int, binding *SessionRouteBinding, deadline time.Time) (*FPSlotLease, error)
    ReleaseFPSlot(ctx context.Context, lease *FPSlotLease) error
    
    // Concurrency Slot
    AcquireConcSlot(ctx context.Context, credID int, binding *SessionRouteBinding, deadline time.Time) (bool, error)
    ReleaseConcSlot(ctx context.Context, credID int, sessionID, requestID string) error
}
```

## 6. 部署与监控

### 6.1 关键指标

```
# 路由指标
llmgw_binding_hit_total{level="strong|soft|none"}
llmgw_binding_created_total{level="strong|soft"}
llmgw_binding_invalidated_total{reason="upstream_error|manual|reevaluate"}
llmgw_route_selection_duration_seconds
llmgw_cost_score_distribution

# 资源指标
llmgw_fp_slot_acquire_duration_seconds{result="success|degraded|timeout"}
llmgw_conc_slot_pressure_ratio{credential_id}
llmgw_resource_wait_total{reason="fp_saturated|conc_saturated"}

# 成本指标
llmgw_request_cost_usd{credential_id, provider_id}
llmgw_cache_hit_ratio{provider}
llmgw_binding_cost_savings_usd

# Hook 指标
llmgw_hook_execution_duration_seconds{hook_name, phase}
llmgw_hook_failure_total{hook_name, error_type}
```

### 6.2 配置热加载

```go
// 监听配置变更
configWatcher.Watch(func(changes ConfigChanges) {
    if changes.Has("cost_scorer.weights") {
        costScorer.SetWeights(changes.Get("cost_scorer.weights"))
    }
    if changes.Has("binding.thresholds") {
        bindingMgr.UpdateThresholds(changes.Get("binding.thresholds"))
    }
    if changes.Has("plugins") {
        pluginMgr.Reload(ctx, changes.GetPluginNames())
    }
})
```

## 7. 下一步

1. ✅ 完成核心架构设计 (本文档)
2. ⏭ 全场景业务模拟 (`10-scenario-simulation.md`)
3. ⏭ 详细实现计划 (`11-implementation-roadmap.md`)
4. ⏭ 测试与验收标准 (`12-testing-acceptance.md`)
