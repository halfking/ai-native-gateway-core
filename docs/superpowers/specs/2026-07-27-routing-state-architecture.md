# 路由 & 状态管理长期架构 — 单源 URSM v2 + 进程 LRU 镜像

> 设计稿 — 取代当前 4 套并存的状态后端 (URSM v2 / `credentialstate.Manager` / `routingstate` Shadow / FpSlots NodeState)、3 级 sticky 内存缓存 + 异步 DB 写、会话意图纯内存缓存、7+ 个环境变量 feature flag。

## Context

LLM Gateway 当前路由侧的状态判断存在以下已观察的"长期负债":

1. **多源并存** — `domains/streaming/executors/router.go` 中 `r.URSM`、`r.URSMv2`、`selectStateBackend` 三套入口分支并存；`domains/routingstate/shadow_observer.go` 已在文件顶部自标 "⚠️ LEGACY,等待 v3.0 移除"。
2. **热路径重复 `Ready()` 检查** — Phase 1 (commit 2c8ccc61) 已把 `Ready()` 调用从 4 次降到 1 次,但 URSM v2 + StateManager + Shadow 的三元组合仍是热路径上的"三态机"。
3. **Sticky 三级异步 DB 写** — `domains/routing/sticky.go` 的 `RecordSuccessMultiLevel` 触发 `go dbSetMultiLevel(...)` 多次 fire-and-forget,重启后通过 `RestoreFromDB` 全表回灌,延迟与一致性都不可控 (7 月 27 日 `bf8a1bcc` 修复"5s 压力缓存遮蔽 Release"已说明异步竞态反复出现)。
4. **会话意图缓存易失** — `autoroute/session_intent_cache.go` 纯内存,重启即丢,且无法跨实例共享。
5. **Feature Flag 泛滥** — `autoroute/feature_flags.go` 暴露 `UseSimplifiedScoring / UseHotTop3Pool / UseCacheRevalidation / Use48hFallback / UseChannelQualityRouting / AutoOnMessages / AutoOnResponses / AutoOnEmbeddings / AutoEmbeddingRoute` 等 9 个 env,新代码无法判断哪一组"应当开"。
6. **2026-07-27 credential pressure + LoadFromRedis 修复** 已确认 Redis 是稳态方向 (`7eaf1701`),但 sticky / intent 仍跑在 DB 异步链路上,与稳态方向错位。

## Goals (本期)

- 让 URSM v2 成为**唯一**运行时权威源,本地 LRU 镜像作为热路径加速,Redis 不可用时 `fail-open`。
- 收敛 7+ 个 feature flag 为一个主开关 + 一个 stage。
- Sticky L1/L2/L3 与 Session Intent 缓存改用 Redis,进程 LRU 镜像读路径,DB 仅做归档 (异步、最终一致)。
- 防封锁机制 (FpSlots / Limiter / RPM / DisguisePool / EgressIdentity) 维持完全独立,严禁与状态判断耦合。
- 历史包 (credentialstate / routingstate / autoroute/internal/legacyflags) 全部下沉到 `_to-be-deprecated/`,不删除文件,但 main.go 不再 wire。

## Non-Goals (本期不做)

- 不重写 FpSlots / Limiter / RPM 等防封锁机制。
- 不改变 sticky key 的命名格式 (兼容 245 旧键)。
- 不重写 `routing_benchmark_test` / `stress_test` 基准。
- 不引入新的分布式协调框架 (etcd/zookeeper),只使用既有 Redis。

---

## Decisions

### 1. 唯一权威源 — URSM v2 (Redis)

URSM v2 (`domains/ursm/v2`) 已落地 Lua 原子写、Reducer 裁决、Recovery warmup、Rollout Controller。本次升级到 **authoritative** 单源模式,任何状态变更必须先经 Lua 写入 Redis,再异步 batch 落 DB。

- `ur:` 前缀统一所有新 key (与 `llmgw:credstate:` 旧前缀**物理隔离**,旧 key 不迁移,7d 后清理)。
- 每个候选 `(cred_id, model)` 一个 Hash,字段 `available / reason / gen / observed_at / recover_at / generation`。
- `generation` 单调递增,裁决时只接受 `gen >= last_seen_gen` 的更新 (避免迟到回放)。

### 2. 进程 LRU 镜像 — 读路径加速

新增 `domains/ursm/v2/cache/lrumirror.go` 作为热路径的本地镜像:

- 容量默认 `100_000 entries`,`sync.Map` + LRU 淘汰 (使用 `hashicorp/golang-lru/v2` 已是间接依赖,避免新增)。
- 写策略: URSM v2 Lua 写 Redis 成功 → 同步更新 LRU; LRU miss → 同步读 Redis → 写入 LRU。
- TTL: LRU 中的 entry 默认 30s 软过期,过期后下次访问强制回源 Redis; Redis 失败则 entry 标记 `stale_until` = `now + 5s` (防 Redis 抖动期穿透)。
- 不变量: 任何 entry 写入 LRU 前必须先经 Redis Lua 成功; LRU 永远只是 Redis 的**只读副本**。

### 3. 回退策略 — fail-open 三段式

```
Redis 正常        → URSM v2 权威 + LRU 命中 < 0.1ms
Redis 不可达      → LRU 镜像,标记 state_source=stale
LRU 也没有        → fail-open,返回原 candidates 切片,标记 state_source=fallback
```

`fallback` 路径在请求日志中以 `routing_state_source` 字段暴露,Grafana 告警阈值: 1 分钟内 fallback 占比 > 5% 触发 P3 告警。

### 4. Sticky L1/L2/L3 收敛到 Redis

- 旧 `domains/routing/sticky.go` 的内存 `map + 异步 DB 写` 改造为 Redis 同步写 + 进程 LRU 镜像。
- Key 格式保持不变 `{tenant}:{app}:{key}:{profile}[:{session}:]{model}`,便于 245/154 跨版本兼容。
- 新增 `ur:sticky:` 前缀,旧 `sticky_sessions` 表保留为**冷归档** (≥7d 的 entry 异步迁入, 仅供审计)。
- TTL: L1=1h, L2=24h, L3=7d (与现有策略一致)。

### 5. Session Intent 缓存

- 新增 `ur:intent:{session_id}` Redis String,TTL=1h,内容 JSON: `{task, model, cred_id, profile, confidence, hit_count, last_seen}`。
- 进程 LRU 镜像同上 (容量 50_000, 软 TTL 60s)。
- `hit_count` 用于漂移检测 (`shouldReclassify` 仍走 V2 已有逻辑, 不变)。

### 6. Feature Flag 收敛

**保留 (新)**
- `URSM_V2_MODE` ∈ {`off`, `canary`, `authoritative`} — 主开关
- `URSM_V2_LRU_SIZE` ∈ {1000..500000} (默认 100000)

**下沉到 `autoroute/internal/legacyflags/` (deprecated)**
- `UseSimplifiedScoring`
- `UseHotTop3Pool`
- `UseCacheRevalidation`
- `Use48hFallback`
- `AutoOnMessages` / `AutoOnResponses` / `AutoOnEmbeddings`
- `AutoEmbeddingRoute`

**永久保留 (业务规则, 不收敛)**
- `UseChannelQualityRouting` (CHANNEL_QUALITY_ROUTING_DESIGN.md 明确 2026-06-28 起默认全量开启, 是已审计稳定特性)

新代码一律只读 `GetURSMMode() == Authoritative`; 旧 flag 仍可读, 但仅在 `URSM_V2_MODE=off` 时生效 (回退路径)。

### 7. 防封锁机制保持独立

- FpSlots / Limiter / RPM / DisguisePool / EgressIdentity 维持原接口与原文件位置, 不进入 URSM v2 包。
- `executor.go` 中申请 FpSlots / Limiter 的代码段**不做任何修改**。
- 仅在 `PlanCandidates` 末段增加一行 `r.planPressurePenalty(...)` 读 FpSlots pressure, 用于 P2C 加权 (Phase 2.3 已有, 本期不重写)。

---

## Architecture

```
                      ┌──────────────────────────────────────────────┐
                      │            Router.PlanCandidates            │
                      │                                              │
                      │  1. dedupe candidates                        │
                      │  2. URSM v2 FilterAndScore (Redis + LRU)     │
                      │  3. tier sort + P2C / Bandit                 │
                      │  4. planPressurePenalty (reads FpSlots)      │
                      │  5. sticky L1/L2/L3 lookup (Redis + LRU)     │
                      └────────┬─────────────────────────────────────┘
                               │
              ┌────────────────┼─────────────────────┐
              ▼                ▼                     ▼
       防封锁机制 (独立)   路由状态 (URSM v2)   Sticky/Intent (Redis + LRU)
       FpSlots            ur:cred:{c}:{m}      ur:sticky:{L*}
       Limiter (4 层)     ur:cooldown:*       ur:intent:{sid}
       RPM                ur:circuit:*
       DisguisePool
       EgressIdentity
```

数据流:
1. 请求进入 → `Router.PlanCandidates` (单次)
2. `URSM v2.FilterAndScore` 经 LRU 命中 → 0.1ms; miss → Redis 读取 (1-2ms)
3. 防封锁机制在 plan 之后、execute 之前申请, **完全独立**, URSM 状态不影响
4. 成功后 Lua 原子写 Redis → 同步更新 LRU → 异步 batch 落 DB
5. 失败时进入 reducer 裁决 → 更新 ur:cred:.* 与 ur:cooldown:.*

---

## Key Data Contracts

### ur:cred:{cred_id}:{model} (Hash)

| Field | Type | 说明 |
|---|---|---|
| available | 0/1 | 节点是否可路由 |
| reason | string | unavailable 原因 (cooling / circuit / manual) |
| gen | uint64 | 单调递增, 抗迟到回放 |
| observed_at | unix_ts | 最近一次观察时间 |
| recover_at | unix_ts (nullable) | 冷却解除时间 |
| generation | uint64 | 来自 URSM v2 api.SourcePriority |

TTL: 永不过期 (除非显式 DEL, 由 reducer 写 0 时同步清 key)。

### ur:cooldown:{cred_id}:{model} (String)

内容: `cool_seconds:{expires_at_unix}`
TTL: cool_seconds (默认 60s, 来自熔断器配置)

### ur:sticky:{L1|L2|L3_key} (String)

内容: `{cred_id}`
TTL: 1h / 24h / 7d

### ur:intent:{session_id} (String)

内容: JSON `{task, model, cred_id, profile, confidence, hit_count, last_seen}`
TTL: 1h

### ur:circuit:{cred_id}:{model} (Hash)

熔断器状态: `state` (closed/half_open/open), `fail_count`, `opened_at`, `next_retry_at`
TTL: 30min (熔断器自身重置)

---

## Rollout Plan (4 阶段)

### M1: 命名空间与新存储 (3-5 天)

- [ ] 创建 `ur:*` key 命名空间
- [ ] 新增 `domains/ursm/v2/cache/lrumirror.go`
- [ ] 新增 `domains/ursm/v2/cache/sticky_redis.go` (双写 sticky)
- [ ] 新增 `domains/ursm/v2/cache/intent_redis.go` (双写 intent)
- [ ] Sticky 双写验证: 写 ur:sticky 成功后,异步 fallback 写旧 sticky_sessions 表 (保留兼容)
- [ ] 单元测试 + 集成测试 (miniredis)

### M2: 进程 LRU 镜像层 (3-5 天)

- [ ] lrumirror 接入 URSM v2 FilterAndScore 热路径
- [ ] lrumirror 接入 sticky Get/GetMultiLevel
- [ ] lrumirror 接入 intent cache Get
- [ ] 性能基准: URSM v2 FilterAndScore 路径 P95 目标 ≤ 0.5ms (含 LRU 命中)
- [ ] 故障注入: Redis kill 验证 fail-open 路径

### M3: 入口收敛 (5-7 天)

- [ ] `router.go` 移除 `r.URSM` (旧 Manager 路径) 调用
- [ ] `router.go` 移除 `r.StateManager` 调用, `stateBackend.FilterAvailable` 退化为单实现 (URSMv2Backend)
- [ ] LegacyStateBackend / DBOnlyBackend 移到 `_to-be-deprecated/streaming/executors/`
- [ ] `executor.go` 中 `isURSMv2Authoritative()` 标记 deprecated, 改用 `stateBackend.IsAuthoritative()`
- [ ] `shadow_observer.go` 整体下沉到 `_to-be-deprecated/routingstate/`
- [ ] main.go 移除 `routingstate.NewShadowObserver()` wire
- [ ] autoroute `feature_flags.go` 9 个 env 全部下沉到 `autoroute/internal/legacyflags/`

### M4: Canary 灰度 → Authoritative (7 天+)

- [ ] 部署 245: `URSM_V2_MODE=canary` 跑 7 天
- [ ] 监控: P95 路由耗时、fallback 占比、LRU hit rate
- [ ] 通过后切到 `URSM_V2_MODE=authoritative`
- [ ] 部署 154: 同样流程
- [ ] 7d 稳定后, 物理删除 `_to-be-deprecated/routingstate/`、`_to-be-deprecated/credentialstate/`

---

## Compatibility & Rollback

- `URSM_V2_MODE=off` 时,旧路径 (StateManager + DB-only) 仍可工作, 走 `_to-be-deprecated/` 下的快照
- `URSM_V2_MODE=canary` 时,新旧双跑, 日志 `routing_state_source` 字段标记每条决策走的是哪条路
- sticky key 格式不变, 跨 245/154 兼容
- `ur:` 与 `llmgw:credstate:` 前缀物理隔离, 旧 key 7d 后异步清理, 不影响 URSM v2
- 任何 M 阶段都可一键回滚, 因为旧路径在 `_to-be-deprecated/` 仍编译通过

---

## Acceptance Criteria

1. `URSM_V2_MODE=authoritative` 下, 路由热路径只走 URSM v2 (Redis + LRU), StateManager / Shadow 路径零调用
2. Sticky L1/L2/L3 与 Intent 缓存全部走 Redis, 进程 LRU hit rate ≥ 80%
3. Redis 不可用时, fail-open 路径在 50ms 内返回原候选集, 日志 `routing_state_source=fallback` 比例可观测
4. Feature Flag 数量从 9 个收敛到 1 个 (URSM_V2_MODE), `UseChannelQualityRouting` 保留
5. 防封锁机制 (FpSlots/Limiter/RPM/DisguisePool/EgressIdentity) 单元测试零修改通过
6. Canary 灰度 7 天内 P95 路由耗时不超过 1ms 增量, fallback 占比 < 1%
7. M4 完成后, 245/154 部署切换无 503, sticky 跨实例兼容
8. `_to-be-deprecated/routingstate`、`_to-be-deprecated/credentialstate` 物理删除后, `go build ./...` 仍通过

---

## Test Strategy

- 单元测试: 沿用 Phase 1 `state_backend_test.go` 风格, 覆盖 LRU 命中/miss/失效/Redis 抖动 4 场景
- 集成测试: `domains/ursm/v2/integration/redis_recovery_test.go` 风格, 覆盖 fail-open / sticky 双写 / intent 跨实例
- 故障注入: 启动时 miniredis 注入 kill, 验证 fallback 路径
- 性能基准: `domains/ursm/v2/integration/warmup_test.go` 风格, 目标 P95 ≤ 0.5ms (LRU 命中)
- 灰度监控: Grafana 面板 `routing_state_source` 分布 + `ur_*_hit_rate` 仪表盘

---

## Risks & Mitigations

| 风险 | 缓解 |
|---|---|
| Redis 抖动引发 fallback 飙升 | LRU 镜像 + 5s stale 保护, fallback 告警阈值 1min > 5% 触发 P3 |
| Sticky key 跨实例不一致 | Redis 强一致, 进程 LRU 仅加速读, 不参与决策 |
| 旧 credentialstate 异步 DB 写丢失 | 双写 7d 期间保留旧表, 异步比对 |
| Canary 阶段 P95 升高 | 灰度指标 dashboard, 超过 1ms 增量自动回退 |
| 历史包下沉影响 build | `_to-be-deprecated/` 通过 //nolint:depguard 注释豁免, 不进 lint 红线 |

---

## Files To Be Created / Modified

**新建 (M1-M2)**
- `domains/ursm/v2/cache/lrumirror.go`
- `domains/ursm/v2/cache/lrumirror_test.go`
- `domains/ursm/v2/cache/sticky_redis.go`
- `domains/ursm/v2/cache/intent_redis.go`
- `domains/ursm/v2/cache/keys.go`

**修改 (M3)**
- `domains/streaming/executors/router.go` (移除旧分支)
- `domains/streaming/executors/executor.go` (替换 isURSMv2Authoritative 调用)
- `domains/streaming/executors/state_backend.go` (退化为 URSMv2Backend 单实现)
- `autoroute/feature_flags.go` (env 下沉)
- `cmd/gateway/main.go` (移除 ShadowObserver wire)
- `autoroute/session_intent_cache.go` (Redis 后备)
- `domains/routing/sticky.go` (Redis 优先 + LRU 镜像)

**下沉 (M3)**
- `domains/routingstate/` → `_to-be-deprecated/routingstate/`
- `domains/credentialstate/` → `_to-be-deprecated/credentialstate/`
- `autoroute/internal/legacyflags/` (新建, 收纳 9 个 env)

**保留不动**
- `credentialfpslot/`、`credential/limiter.go`、`rpm/`、`disguise/`、`identity/`

---

## Out of Scope (后续)

- 跨区域多活 (需要 etcd 协调, 单独设计)
- 路由决策机器学习化 (P3 路线)
- 防封锁机制进一步整合 (与本设计正交)

---

## References

- `docs/2026-07-24-routing-state-optimization.md` (Phase 1 实施记录)
- `docs/2026-07-24-phase1-final-status.md` (245 部署状态)
- `docs/superpowers/plans/2026-07-21-ursm-v2.md` (URSM v2 实施计划)
- `domains/streaming/executors/state_backend.go` (StateBackend 接口)
- `domains/ursm/v2/manager.go` (URSM v2 facade)
- `docs/CHANNEL_QUALITY_ROUTING_DESIGN.md` (业务规则, 不收敛)
- `docs/2026-07-24-phase2-completion-summary.md` (压力感知, 不重写)
