# 01 — 架构与状态模型

## 1. 目标边界

在 `domains/ursm` 内演进为 **URSM v2**，成为“号池状态 + 资源池 + 候选索引 + 恢复/灰度”的统一模块。

| 做 | 不做（第一阶段） |
|---|---|
| Redis 作为运行时状态权威 | 不直接全量切换生产路由 |
| 五层状态统一管理 | 不重写 Executor 候选循环 |
| 原子更新 + 来源优先 CAS | 不引入 Redis Cluster/Sentinel |
| 影子对比 → 灰度 → 权威 | 不把 DB 重新做成热路径状态源 |
| 每分钟快照落库复盘 | 不把健康状态与资源租约混成一个 key |

## 2. 六组件职责

```text
                    ┌──────────────────────────────┐
  request/probe/    │         ursm.Manager         │
  admin/config ───► │   (唯一对外 facade/闭包装配)   │
                    └──────────────┬───────────────┘
           ┌───────────┬───────────┼───────────┬───────────┬───────────┐
           ▼           ▼           ▼           ▼           ▼           ▼
      StateStore  StateReducer CandidateIndex ResourcePools RecoveryMgr RolloutCtrl
      Redis权威    裁决纯函数    模型候选索引    FP/RPM/并发   预热/阻塞     off/shadow
      Lua/CAS      版本/来源     快速检索API     租约适配      快照恢复      /canary/auth
```

### 2.1 StateStore

- Redis 读写、Lua 原子更新、TTL、epoch、key 协议
- **不**理解路由策略
- 区分三种读结果：`ok` / `missing` / `redis_error`（禁止把 error 当 missing）

### 2.2 StateReducer

- 纯函数：`Evidence + ExistingState → Decision`
- 优先级：`manual > probe > request`
- 人工禁用只允许显式 enable
- 免费凭据 transient：不硬剔，只软降权
- 可单测、无 I/O

### 2.3 CandidateIndex

- 键：`tenant|canonical|profile|modality`
- member：`credential_id:raw_model`
- score：composite 或 last_activity
- 提供按模型拉候选、按维度过滤的快速检索
- 由配置同步、状态变更、恢复预热维护

### 2.4 ResourcePools

- 适配现有 `credentialfpslot` 与 `credential.Limiter` / Redis RPM
- 统一 `Acquire / Release / Stats`
- 资源租约与健康状态 **分 key**，生命周期独立
- Plan 阶段只读 stats，**不** acquire（避免历史 URSM 泄漏）

### 2.5 RecoveryManager

- Redis 重启/空库：`ready=0`，阻塞权威/canary 路由
- 从 DB 配置 + 最近分钟快照预热
- 校验通过后 `ready=1` 并 bump/记录 `recovery_epoch`

### 2.6 RolloutController

- 模式：`off | shadow | canary | authoritative`
- 影子：新旧双算，生产用旧结果，记 diff
- canary：`tenant+model` 稳定哈希按百分比切流
- **不得**复用 `Manager.Enabled() == (config != nil)` 作为开关

## 3. 五层状态模型

| 层 | 主键 | 权威字段 | 谁可写 |
|---|---|---|---|
| Provider | `provider_id` | enabled / manual_disabled / base_url_latency / trust | admin, config sync, baseurl probe |
| Credential | `credential_id` | lifecycle / auth / quota / plan_type / billing | admin, auth/quota probe, request(有限) |
| Binding | `credential_id + raw_model` | model available / probe consensus / price / cost | admin, model probe, price sync |
| Node | `credential_id + raw_model` | 1m/5m/30m 成功率、延迟、连续失败、冷却、评分 | request, probe, reducer |
| Resource | `credential_id (+ holder/slot)` | 当前并发、最大并发、FP used/free、RPM 窗口 | acquire/release 路径 |

### 3.1 关键语义

- **配置硬门**仍来自 DB：provider/credential/binding 是否存在、是否 active、租户策略
- **运行时状态**来自 Redis：冷却、连续失败、滑动成功率、资源压力、人工禁用镜像
- **懒初始化**：仅当候选已通过 DB 硬门时，首次请求用 Redis `NX` 创建 `unknown+eligible`
- **Redis 读失败**：保护性拒绝该候选，不 fail-open
- **Redis 丢失/重启**：全量阻塞，直到 RecoveryManager 预热完成
- **初始态 `unknown`**：无运行时负面证据，允许进入候选；首次成功后转 healthy 观测

### 3.2 来源优先级与 CAS

```text
source_priority:
  0 = seed/lazy
  10 = request
  20 = probe (active/node/model/credential/passive)
  30 = recovery_snapshot
  40 = admin
```

Lua CAS 规则：

1. `incoming.generation < current.generation` → ignore（或仅 append 窗口样本）
2. `incoming.source_priority < current.source_priority` 且目标字段为裁决字段 → ignore
3. admin 的 `manual_disabled=true` 只能被 admin enable 清除
4. 窗口样本可 append-only；`available/cool_until/manual_*` 必须 CAS

## 4. 对外 API

```go
// 写
RecordRequest(ctx, RequestOutcome) error
ApplyAdmin(ctx, AdminAction) error
ApplyProbe(ctx, ProbeOutcome) error
SyncConfig(ctx, ConfigSnapshot) error
LazyInitNode(ctx, NodeSeed) (created bool, err error)

// 读
IsAvailable(ctx, cid, rawModel) (ok bool, reason string, err error)
GetNodeView(ctx, cid, rawModel) (*NodeView, error)
ListCandidates(ctx, CandidateQuery) ([]NodeView, error)
FilterAndScore(ctx, seeds []CandidateSeed) ([]NodeView, error)
ResourceStats(ctx, cid) (*ResourceView, error)

// 生命周期
Warmup(ctx) error
Ready(ctx) (bool, progress Progress)
EnterRecovery(reason string)
Mode() RolloutMode
```

### 4.1 重要 DTO 字段（NodeView）

```text
ProviderID, CredentialID, RawModel, CanonicalName, TenantID
Available, Reason, HealthStatus
FailStreak, CoolUntil
SR1m, SR5m, SR30m, Samples1m/5m/30m
LatP50Ms, LatP95Ms, LatEWMA
Score
PriceIn/OutPer1M, BillingMode, PlanType
Trust, BaseURLLatencyMs
OriginType (origin/relay)
ConcUsed/Limit, FPUsed/Limit, RPMUsed/Limit
Generation, SourcePriority, UpdatedAt
```

## 5. 与现有代码对接

| 现有 | URSM v2 |
|---|---|
| `credentialstate.Manager` | 读/写逐步被 Store+Reducer 替代；过渡期 adapter |
| `routingstate.ShadowObserver` | 升级为候选集合/顺序/原因 diff，不只 evidence 去重 |
| `credentialfpslot` | ResourcePools.FP 后端，不重写 Lua 租约语义 |
| `credential.Limiter` + Redis RPM | Conc/RPM 接入 ResourcePools；跨实例 conc 后置 |
| `provider.Client` 候选缓存 | 配置候选仍 DB+30s cache；运行时可用性问 URSM |
| `executors.Router` | off 旧路径；shadow 双算；canary/auth 用 URSM |
| `executors.Executor` | 成功/失败旁路/替换 `RecordRequest`；资源仍循环内 acquire |
| 半成品 `ursm.LayerCache` | 废弃 mem→redis→db fail-open 路径，改为 Redis 权威读 |

## 6. 建议包结构

```text
domains/ursm/
  manager.go              # facade 闭包装配
  api.go                  # 对外 DTO / 接口
  store/                  # StateStore + lua scripts
  reducer/                # 纯裁决
  index/                  # CandidateIndex
  resource/               # FP/RPM/Conc adapters
  recovery/               # warmup + ready
  rollout/                # mode + canary hash
  persist/                # minute snapshot writer
  shadow/                 # plan diff
  metrics.go
```

## 7. 单一原则与闭包

- **一个写入裁决入口**：所有 request/probe/admin 证据经 Reducer + Store，禁止业务路径直接 `SET` 状态 key
- **一个读取入口**：Router 只调 `FilterAndScore` / `IsAvailable`，不直接拼多层 Redis key
- **Manager 闭包装配**：脚本、TTL、阈值、适配器在 `NewManager` 内注入；测试可替换 Store/Reducer
- **资源与健康分离**：ResourcePools 失败不等于 node unhealthy
- **配置与运行时分离**：价格/限额上限由 SyncConfig；成功率/冷却由 RecordRequest
