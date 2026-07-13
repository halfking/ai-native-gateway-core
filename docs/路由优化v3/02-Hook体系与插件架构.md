# Hook 体系与插件架构

> 基于现有 `domains/hooks/` 体系扩展 URSM 专用 Hook 点与插件化架构。

## 1. 现有 Hook 体系

### 1.1 基础接口

```go
type Hook interface {
    Name() string
    Priority() int      // 0-1000, 越小越先
    Enabled() bool
    Phase() Phase       // pre_routing / routing / pre_upstream / post_upstream / post_response
    Execute(ctx context.Context, env *Environment) error
}

type Environment struct {
    RequestID, TenantID, SessionKey, TaskID string
    Request, Response, UpstreamRequest, UpstreamResponse interface{}
    Session *session.Session
    Metadata map[string]interface{}
    StartTime time.Time
    Skip bool      // 跳过后续 Hook
    Abort bool     // 中止请求
}
```

### 1.2 HookRegistry

```go
type HookRegistry struct {
    hooks       map[Phase][]Hook
    hooksByName map[string]Hook
}
// Register(hook Hook) error
// Execute(ctx, phase, env) error
// GetHooks(phase) []Hook
```

## 2. URSM 细粒度 Hook 点

### 2.1 Hook 点定义

```go
const (
    // PhasePreRouting 认证与限流
    HookAuthCompleted        = "auth_completed"
    HookRateLimitChecked     = "rate_limit_checked"
    HookKeyConcurrentAcquired = "key_concurrent_acquired"
    HookRequestRejected      = "request_rejected"

    // PhaseRouting 路由选择
    HookBindingLookup        = "binding_lookup"
    HookBindingHit           = "binding_hit"
    HookBindingMiss          = "binding_miss"
    HookBindingReEvaluate    = "binding_reevaluate"
    HookCandidateLoaded      = "candidate_loaded"
    HookCandidateFiltered    = "candidate_filtered"
    HookCostScored           = "cost_scored"
    HookRouteSelected        = "route_selected"

    // PhasePreUpstream 资源门禁
    HookProviderGateCheck    = "provider_gate_check"
    HookFPSlotAcquire        = "fp_slot_acquire"
    HookConcSlotAcquire      = "conc_slot_acquire"
    HookCircuitBreakerCheck  = "circuit_breaker_check"
    HookResourceWaited       = "resource_waited"
    HookResourceDegraded     = "resource_degraded"

    // PhasePostUpstream 上游执行后
    HookUpstreamSuccess      = "upstream_success"
    HookUpstreamFailure      = "upstream_failure"
    HookStateRecorded        = "state_recorded"
    HookResponseCached       = "response_cached"
    HookBindingCreated       = "binding_created"
    HookBindingUpdated       = "binding_updated"
    HookBindingInvalidated   = "binding_invalidated"
    HookRouteChanged         = "route_changed"

    // PhasePostResponse 响应后
    HookResourceReleased     = "resource_released"
    HookTelemetryRecorded    = "telemetry_recorded"
)
```

### 2.2 Hook 调用时序

```
handler.go:
  PhasePreRouting Execute
    → Emit auth_completed, rate_limit_checked, key_concurrent_acquired

router.go:
  PhaseRouting Execute
    → Emit binding_lookup, binding_hit/miss
    → Emit candidate_loaded, candidate_filtered, cost_scored
    → Emit route_selected, binding_reevaluate

executor.go:
  PhasePreUpstream Execute
    → Emit provider_gate_check, fp_slot_acquire, conc_slot_acquire
    → Emit circuit_breaker_check, resource_waited/degraded

  PhasePostUpstream Execute
    → Emit upstream_success/failure, state_recorded
    → Emit response_cached, binding_created/updated/invalidated
    → Emit route_changed

handler.go:
  PhasePostResponse Execute
    → Emit resource_released, telemetry_recorded
```

## 3. 插件化扩展

### 3.1 Plugin 接口

```go
type Plugin interface {
    Name() string
    Version() string
    Init(deps *PluginDependencies) error
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
    Hooks() []hooks.Hook
    HealthCheck(ctx context.Context) error
}

type PluginDependencies struct {
    Redis       redis.UniversalClient
    DB          *pgxpool.Pool
    HookRegistry *hooks.HookRegistry
    Registry    prometheus.Registerer
    Logger      *slog.Logger
}
```

### 3.2 PluginManager

```go
type PluginManager struct {
    plugins map[string]Plugin
    mu      sync.RWMutex
}

func (pm *PluginManager) Register(p Plugin) error
func (pm *PluginManager) Start(ctx context.Context, name string) error
func (pm *PluginManager) Stop(ctx context.Context, name string) error
func (pm *PluginManager) List() []PluginInfo
// 配置热加载: 监听 plugins/*.yaml, 变更时 Reload
```

### 3.3 内置插件

```go
// 1. SessionGovernorPlugin (会话治理)
//    Hook: binding_hit, binding_invalidated, cost_scored
//    监控路由稳定性, 频繁切换告警

// 2. CostOptimizerPlugin (成本优化)
//    Hook: cost_scored, binding_reevaluate
//    动态调整 CostScorer 权重

// 3. ResourcePredictorPlugin (资源预测)
//    Hook: fp_slot_acquire, conc_slot_acquire, resource_degraded
//    预测 FP/并发压力, 提前触发降级

// 4. RouteAuditorPlugin (路由审计)
//    Hook: route_selected, upstream_failure, binding_created
//    记录每次路由决策完整上下文

// 5. AdaptiveThrottlePlugin (自适应限流)
//    Hook: upstream_success, rate_limit_checked
//    根据上游响应时间动态调整客户端 RPM
```

### 3.4 插件配置

```yaml
# plugins/session_governor.yaml
name: session_governor
enabled: true
config:
  alert_on_frequent_switch: true
  switch_threshold: 3
  window_minutes: 5

# plugins/route_auditor.yaml
name: route_auditor
enabled: true
config:
  retention_days: 7
  sample_rate: 0.1
```

## 4. Hook 约束

| Hook | 必须记录 | 可做 | 不可做 |
|------|----------|------|--------|
| `route_selected` | 候选、评分、绑定级别 | 观测/建议 | 静默改选节点 |
| `provider_gate_check` | 开关、限额、压力 | 调整等待预算 | 绕过硬限制 |
| `upstream_failure` | 错误分类、是否已输出 | 触发 failover 建议 | 客户端错误标为供应商错误 |
| `binding_invalidated` | 原节点、原因、时间 | 审计/通知 | 无错误静默解除 |
| `cost_scored` | 每候选成本和权重 | 调整策略参数 | 覆盖稳定性硬门槛 |

**插件失败模式**: 治理/审计插件 fail-open (不阻塞数据面)
                  安全/限流插件 fail-closed (阻塞请求)
