# Routing and State — 路由与状态管理

> **事实快照：** 2026-08-21  
> **目标：** 说明当前路由状态源、候选评分、健康与资源治理的边界，并列出提升为统一策略前的门禁。

## 1. 状态源

```text
Router
  -> StateBackend selection
       -> URSM v2 authoritative (recommended)
       -> LegacyStateBackend (rollback)
       -> DBOnlyBackend (degraded/controlled)
       -> ShadowObserver (observe only)
```

### 模式状态

| 模式 | 作用 | 是否影响选择 |
|---|---|---|
| URSM authoritative | Redis/Lua 为主要实时状态，PG 保存 durable facts | 是 |
| Legacy fallback | 内存 + Redis 的兼容健康状态 | 仅在回退时 |
| DB only | 受控降级或初始化路径 | 取决于 ready gate |
| shadow/canary | 对比策略、状态来源或候选结果 | 否 |
| off | 保留旧路径，通常用于回滚 | 是旧路径 |

URSM 需要 Redis、PostgreSQL、SystemMonitor 和对应 probe/ready 条件；缺少这些依赖时，必须显式记录 degraded 状态，而不是把 fallback 当作 authoritative。

## 2. 健康与资源分配

```text
Health plane
  credential auth/quota/error state
  node probe / circuit / recovery
  URSM availability and lifecycle

Resource plane
  concurrent slots / RPM / TPM
  fingerprint slots / egress identity
  queue pressure / provider pressure
  disguise and connection pool
```

健康回答“是否允许进入候选集”；资源回答“当前是否应该承载这次请求”。两者不应共享一个未经解释的 `available` 布尔值。

当前实现已具备 credential health、probe、P2C、limiter、FP slot 和 identity 资源控制，但 TPM admission 完整接线、FP+concurrency 原子联合 lease 仍需单独验收。

## 3. 候选生命周期

```text
DB/provider catalog query
  -> tenant/model/protocol/modality filter
  -> lifecycle/routable/probe/health gate
  -> URSM state and circuit gate
  -> tier / billing round / sticky / protocol affinity
  -> score/order
  -> resource acquire
  -> forward attempt
  -> outcome reduces health + usage + route observation
```

Provider Candidate 的价格、cache、context、延迟、质量、配额和协议字段可复用，不应为每一种上游 Provider 增加新的执行器。

## 4. 当前评分

线上主路径仍以 P2C/Bandit/tier/sticky/URSM 为主。复合惩罚通常包括：

```text
concurrency pressure
+ identity pressure
+ latency penalty
+ quality penalty
+ headroom penalty
+ capacity penalty
+ optional cost penalty
+ optional IQ penalty
```

OmniRoute 风格的 `cost-optimized`、`cache-optimized`、`context-aware`、`headroom` scorer 已存在，但主要通过 `ShadowStrategy` 观测，不应称为默认 active strategy。

### 策略语义门禁

| 策略 | 当前缺口 | active 前要求 |
|---|---|---|
| cost | 请求 token budget、可靠性和延迟参与不足 | unknown price 显式惩罚；费用和路由解释一致 |
| cache | cache affinity/telemetry 不完整 | cache policy、命中率和 cache price 有请求级输入 |
| context | 主要偏好最大窗口 | 输入 token、output budget、安全余量硬过滤 |
| headroom | 主要复用 limiter pressure | 明确 FP slot、concurrency、queue 的语义和权重 |

active strategy 只允许排序已通过 tenant、health、URSM、tier 和资源安全门禁的候选，不能让策略评分复活不可用节点。

## 5. TPM 与资源租约

### TPM

TPM limiter 和 Redis Lua sliding window 已存在，但主 admission 路径需要确认 `CheckTPM` 是否与 RPM、预估 token、最终 usage reconcile 一致。Redis 故障时进程内 fallback 会破坏多实例严格额度，付费/严格配额应提供 fail-closed 或明确 degraded policy。

### 联合 lease

当前 FP slot 与 concurrency 可能分步 acquire/release。目标接口可以是：

```go
type CandidateLeaseCoordinator interface {
    Acquire(ctx context.Context, req LeaseRequest) (Lease, error)
    Release(ctx context.Context, lease Lease) error
    Renew(ctx context.Context, lease Lease) error
}
```

`Lease` 必须包含 request/attempt、tenant、candidate、lease token、expiry 和已获取资源；release 必须 exactly-once 或可幂等。实现时优先复用 `bg/probe_queue` 的 `FOR UPDATE SKIP LOCKED`、lease token compare、heartbeat 和 zombie rescue 模式。

## 6. 统一 retry budget

建议在 `dispatch.QueuedRequest` 或应用级 request state 中维护：

```go
type RetryBudget struct {
    MaxAttempts       int
    Attempts          int
    Deadline           time.Time
    FirstByteSent     bool
    CredentialRetries int
    ModelSwitches     int
    LastReason        string
    RetryAt           *time.Time
}
```

streamretry、dispatch scheduler、goal policy、request survival 和 failover 只能通过该预算读写，避免各层独立累计造成上游尝试放大、成本重复或超过请求 deadline。

## 7. 迁移顺序

1. 保持现有 P2C/URSM 线上行为，补齐 shadow decision record。
2. 统一 `RouteFeatures`，把请求 token/cache/context/pressure 输入传给 scorer。
3. 增加 tenant/request opt-in active strategy，先单租户灰度。
4. 完成 TPM reconcile 和联合 lease；先在 shadow mode 对比 acquire/release。
5. 建立 route decision、candidate exclusion、attempt、cost、quality 的完整指标。
6. 观察期通过后再扩大 active routing；任何异常可关闭 flag 回到 P2C/URSM。

## 8. 相关代码和测试

- Candidate/provider：`provider/client.go`
- Router：`domains/streaming/executors/router.go`
- 评分：`domains/streaming/executors/router_scoring.go`、`strategy_*.go`
- Dispatch：`domains/dispatch/`
- TPM：`ratelimit/redis_sliding.go`
- Probe lease：`bg/probe_queue.go`、`bg/probe_service.go`
- URSM：`domains/ursm/v2/`、`domains/streaming/executors/state_backend.go`
- 测试：`domains/streaming/executors/*routing*_test.go`、`ratelimit/*_test.go`、`bg/*lease*_test.go`
