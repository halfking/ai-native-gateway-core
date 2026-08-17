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

#### 1.1 FpSlots NodeState 迁移映射(显式)

`credentialfpslot/node_state.go` 顶部已自标 **DEPRECATED**(等 Router/Executor 适配 URSM 后迁入 `_to-be-deprecated/`)。其 `NodeState` 字段与 `ur:cred:` Hash 的映射关系**固定如下**,M1 实施时严格按表迁移,禁止字段语义漂移:

| FpSlots `NodeState` 字段 | `ur:cred:` Hash 字段 | 说明 |
|---|---|---|
| `Disabled` (bool) | `disabled` (0/1) | 直接取反映射 |
| `DisabledUntil` (unix sec) | `cool_until_ms` (unix ms) | **单位转换**: FpSlots 用秒,URSM v2 用毫秒。迁移函数 `sec * 1000` |
| `DisabledReason` (string) | `disabled_reason` | URSM v2 有独立 `disabled_reason` 字段(`record_request.lua:55,115` 写),与 `last_err`(错误 kind)分开;FpSlots 的 `DisabledReason` 语义更接近前者,映射到 `disabled_reason` 而非 `last_err` |
| `SuccessCount` / `FailureCount` (int64) | `success_count` / `failure_count` | 直接映射。**注意**:URSM v2 写脚本统一用 `failure_count`(`record_request.lua:69,103`),`fail_count` 仅作为 `clear_state.lua:24` 的 HDEL 清理目标存在,**不是有效字段**,迁移时不要写 `fail_count` |
| `SlideWindow` ([]NodeRecord) | **不迁移** | URSM v2 改用 `fail_streak`(连续失败计数)+ Redis 侧滑动窗口 key(`record_request.lua` 的 `w1/w5/w30`),Go 侧不再持有窗口数组。这是有意的去重,避免大 JSON 写放大 |
| `LastDisabledAt` (unix sec) | **无对应字段(不迁移)** | URSM v2 当前**没有任何 Lua 写脚本**写入"冷却开始时间"。`cool_start_ms` 仅在 `clear_state.lua:21` 作为 HDEL 目标出现,从未被 HSET。若强行映射到一个幽灵字段,迁移会静默丢数据。两个可选:(a) 丢弃 `LastDisabledAt`(冷却起始时间非路由必需,cool_until_ms 已足够);(b) M1 给 `record_request.lua` 的禁用分支(L110)补一行 `"cool_start_ms", tostring(now_ms)`。**本稿选 (a) 丢弃**,保持"不修改现有 Lua 写语义"的边界;若运维需要起始时间观测,另起工单改 Lua |
| `DisableCount` (int) | `disable_count` | 直接映射,用于指数退避 |
| `LastSuccessAt` / `LastFailureAt` (unix sec) | **不迁移** | URSM v2 无 `last_success_at_ms`/`last_failure_at_ms` 字段(核实:4 个写脚本均不 HSET)。其"最近一次成功/失败时间"语义由 `updated_at_ms`(最近更新)+ `fail_streak`(当前连续失败)+ Redis 滑动窗口 ZSET(`w1/w5/w30`)覆盖,迁移后可由这些字段推导,丢弃不损失路由决策能力 |
| (无) | `generation` / `source_priority` | URSM v2 新增,FpSlots 无对应;迁移时初始化 `generation=1, source_priority=10` |

**迁移契约**:
- 迁移在 M1 的 `domains/ursm/v2/cache/lrumirror.go` 初始化时一次性扫描旧 `llmgw:cred_fp_node:{cred}:{model}` key(JSON),按表写入 `ur:cred:{cred}:{model}` Hash,写完 `HINCRBY generation 1`,旧 key **不删**(7d TTL 自然过期),避免回滚期数据丢失。
- `IsUsable()` / `ConsecutiveFailureStreak()` 的 Go 端逻辑由 URSM v2 `FilterAndScore` 接管,FpSlots 的 Lua `recordNodeOutcomeScript` 在 `URSM_V2_MODE=authoritative` 后停止调用(由 Router 侧 `if mode==Authoritative` 守卫)。
- 迁移脚本纳入 M1 验收(单元测试覆盖每个字段映射 + 单位转换)。

### 2. 进程 LRU 镜像 — 读路径加速

新增 `domains/ursm/v2/cache/lrumirror.go` 作为热路径的本地镜像:

- 容量默认 `100_000 entries`。底层数据结构**待定**,在 M1 实施前二选一:
  1. **(推荐)** 自研轻量 LRU(基于 `container/list` + `map`,约 120 行,已锁 `sync.Mutex`),不引入外部依赖;`domains/session/v2/cache_v2.go` 已有同款实现可借鉴。
  2. 引入 `hashicorp/golang-lru/v2`(**核实:截至本稿 `go.mod`/`go.sum` 无此依赖**,引入即为新增直接依赖,需走依赖评审)。
- 选择(1)的代价是不支持 `Peek`/`Keys` 等高级 API,但本场景仅需 `Get/Purge`,足够;选择(2)代价是 go.mod 多一项直接依赖 + 供应链审查。**默认按(1)推进**,M1 实施时若自研实现复杂度失控再回退到(2)并在本节更新说明。
- 写策略: URSM v2 Lua 写 Redis 成功 → 同步更新 LRU; LRU miss → 同步读 Redis → 写入 LRU。
- **generation 单调契约(与 `apply_decision.lua` 对齐)**: `apply_decision.lua` 在 Redis 侧用 `cur_gen > in_gen or (cur_gen==in_gen and cur_pri>in_pri)` 拒绝迟到写入。LRU 镜像**必须**遵守同等约束,否则回填路径会把旧状态盖到新 entry 上:
  - 写入(write-back)路径: 仅当 Lua 返回 `{applied, gen}` 时才更新 LRU;`{ignored_stale, cur_gen}` / `{ignored_manual_hold}` 返回**禁止**触碰 LRU entry。
  - 回填(read-back)路径: 读 Redis 得 `entry_B` 后,与 LRU 中已有 `entry_A` 比较 `(generation, source_priority)`,仅当 `entry_B.generation > entry_A.generation` 或(gen 相等且 `entry_B.priority > entry_A.priority`)时才覆盖;否则保留 `entry_A`。这复刻了 lua 的拒绝条件,避免「读到 Redis 快照 → 但 LRU 已被更晚的 write-back 更新」的回退。
  - 用一个 `applyToLRU(entry)` 内部方法集中该比较,write-back 与 read-back 共用,杜绝两处规则漂移。
- TTL: LRU 中的 entry 默认 30s 软过期,过期后下次访问强制回源 Redis; Redis 失败则 entry 标记 `stale_until` = `now + 5s` (防 Redis 抖动期穿透)。
- 不变量: 任何 entry 写入 LRU 前必须先经 Redis Lua 成功; LRU 永远只是 Redis 的**只读副本**。
- 软过期与 gen 单调**叠加**生效: 软过期仅触发「下次访问回源」,不立即删除 entry;回源后的写入仍走 `applyToLRU` 的 gen 比较,不会因为过期而绕过单调性。

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
- **基数边界(核实)**: 三级 key 结构已固定(见 `domains/routing/sticky.go` `buildStickyKeys`):
  - L1 = `{tenant}:{app}:{key}:{profile}:{session}:{model}` — 基数 = tenant × app × apiKey × profile × session × model,**随会话数线性增长**,是 Redis 内存的主要贡献者。
  - L2 = `{tenant}:{app}:{key}:{profile}:{model}` — 基数 = tenant × app × apiKey × profile × model。
  - L3 = `{tenant}:{app}:{key}:{profile}`(**不含 model**,见 `sticky.go:369`)— 基数 = tenant × app × apiKey × profile,最稳定。
  - 7d TTL + L1 随会话生灭 → 稳态 Redis 内存 ≈ `活跃会话数 × 3 key`。1 万活跃会话 ≈ 3 万 key × ~100B ≈ 3MB,可忽略;10 万级活跃会话需监控 `ur:sticky:*` 的 key count,设 Grafana 告警。本设计**不设硬上限**,靠 TTL 自然收敛。

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
       Limiter (4 层)      ├─ cool_until_ms    ur:intent:{sid}
       RPM                ├─ disabled          (ur:cooldown: / ur:circuit:
       DisguisePool       ├─ fail_streak        已撤销,见 Data Contracts)
       EgressIdentity     └─ generation
       (进程内熔断器 breaker.go 维持独立)
```

数据流:
1. 请求进入 → `Router.PlanCandidates` (单次)
2. `URSM v2.FilterAndScore` 经 LRU 命中 → 0.1ms; miss → Redis 读取 (1-2ms)
3. 防封锁机制在 plan 之后、execute 之前申请, **完全独立**, URSM 状态不影响
4. 成功后 Lua 原子写 Redis → 同步更新 LRU → 异步 batch 落 DB
5. 失败时进入 reducer 裁决 → 更新 `ur:cred:` 的 `disabled/cool_until_ms/fail_streak`(由 `record_request.lua` 原子写),进程内熔断器 `breaker.go` 并行更新(不落 Redis)。

---

## Key Data Contracts

### ur:cred:{cred_id}:{model} (Hash)

> **字段已对齐现有 URSM v2 Lua 写**(`apply_decision.lua` / `record_request.lua`),本稿不做新字段发明。

| Field | Type | 说明 | 现有来源 |
|---|---|---|---|
| available | 0/1 | 节点是否可路由 | `record_request.lua` / `apply_decision.lua` / `apply_probe.lua` |
| disabled | 0/1 | 是否处于禁用(冷却)态 | `record_request.lua` / `apply_probe.lua` |
| cool_until_ms | unix_ms (nullable) | 冷却解除时间,`disabled=1` 时才有意义 | `record_request.lua` / `apply_probe.lua` |
| fail_streak | int | 连续失败数 | `record_request.lua` / `apply_decision.lua` |
| failure_count | int | 累计失败数 | `record_request.lua`(HINCRBY L69,103) |
| success_count | int | 累计成功数 | `record_request.lua`(HINCRBY L57,100) |
| disable_count | int | 累计禁用次数(用于指数退避) | `record_request.lua` |
| disabled_reason | string | 禁用原因(`fail_streak_N` / `recovered_with_success_during_cool`) | `record_request.lua`(L55,115) |
| last_err | string | 最近错误 kind | `record_request.lua` / `apply_decision.lua` / `apply_probe.lua` |
| updated_at_ms | unix_ms | 最近更新时间 | `record_request.lua` / `apply_probe.lua` |
| last_probe_at_ms / last_probe_latency_ms | unix_ms | 探测时间戳 | `apply_probe.lua`(L26-27, L38-39) |
| source_priority | int | 来源优先级(admin=40 / probe=20 / normal=10) | `apply_admin.lua` / `apply_probe.lua` / `record_request.lua` |
| generation | uint64 | 单调递增,**抗迟到回放**(详见 Decision 2) | 大多数写脚本 `HINCRBY ... generation 1`;`apply_decision.lua` 例外,用 `HSET` 写入外部传入的 gen(L34),因为 reducer 已先 CAS 比较 gen |
| manual_hold / manual_actor / manual_reason / manual_at_ms | 混合 | 管理员手动禁用标记(优先级 40,屏蔽所有其他写) | `apply_admin.lua`(L15-18) |

**非有效字段(仅作为清理目标,不写入)**: `cool_start_ms`、`fail_count`、`disabled_at_ms`、`last_err_at_ms` —— 仅在 `clear_state.lua` 的 `HDEL` 中出现,**没有写脚本 populate 它们**。本设计的迁移与 LRU 镜像**禁止**读写这些字段。

TTL: 由 `record_request.lua` 的 `ARGV[6] node_ttl_sec` 管理(每次写都 `EXPIRE node_key node_ttl`,见 lua L59/72/97)。**不是永不过期**——旧稿的"永不过期"说法失实。默认 `node_ttl_sec` 来自 `ursm.Config`,当前配置 1h(见 `domains/ursm/v2/config.go`)。节点长期无流量会自然过期,下次访问由 reducer 重新创建。reducer/admin 主动清时调 `clear_state.lua`(`HDEL` 清字段,不删 key)。

> **注意**: 旧稿写有独立 `reason` / `gen` / `observed_at` / `recover_at` 字段,与代码不符。`reason` 对应 `last_err`,`recover_at` 对应 `cool_until_ms`,`gen` 即 `generation`。本表已改名对齐,避免 M1 新增字段。

### ur:cooldown:{cred_id}:{model} — **不引入**

> **修正**: 旧稿把 cooldown 描述为独立 String key。核实代码后,cooldown **是 `ur:cred:` Hash 的 `cool_until_ms` 字段**,不是独立键。独立 key 会与 `record_request.lua` 原子写分裂,引入双写竞态。本设计**不新增** `ur:cooldown:` 键。

### ur:sticky:{L1|L2|L3_key} (String)

内容: `{cred_id}`
TTL: 1h / 24h / 7d

### ur:intent:{session_id} (String)

内容: JSON `{task, model, cred_id, profile, confidence, hit_count, last_seen}`
TTL: 1h

### ur:circuit:{cred_id}:{model} — **不引入**

> **修正**: 旧稿把熔断器(circuit breaker)声明为新的 Redis 权威 key。核实代码后,**无任何 `ur:circuit:` 键存在**:
> - 熔断器实现在 `domains/credential/breaker.go`(`Breaker` 结构,`providerID+credentialID` 维度),**纯进程内**,无 Redis 持久化,重启即重置为 closed。
> - 冷却逻辑是 `ur:cred:` Hash 的 `cool_until_ms` / `disabled` 字段(见上表),由 `record_request.lua` 原子写,不是独立键。
>
> 本设计**保持现状**: 熔断器继续进程内运行(其语义本就是「本实例近期观测」的短期态,跨实例共享反而失真),`ur:cred:` Hash 承载冷却/禁用态。**不在 Redis 新建 `ur:circuit:` 权威源**。如果未来需要跨实例共享熔断状态,另起 spec,不在本期范围。

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
