# Routing & State Anomaly Audit — Redis 单一状态源与异常自检/更新规则

> **审计日期**: 2026-07-28
> **适用版本**: v2.4.8+（与 `docs/architecture/ARCHITECTURE.md §0.5` 同周期）
> **审计范围**:
> 1. 路由决策使用的状态后端是否真正"以 Redis 为唯一权威源"
> 2. 状态出现异常（Redis 抖动、节点冷却、LRU 镜像漂移）时的自检探测规则
> 3. 状态更新（写路径）的原子性、单调性、覆盖优先级
> 4. "及时 + 不浪费资源"两个目标在当前实现中是否同时满足
>
> **结论先行**: 路由/状态拓扑已经以 Redis Lua 作为权威源落地，异常自检分级
> （路由热路径 50ms / 镜像 30s 软过期 / Executor authoritative 1s 缓存 / 系统监测 15s /
> 恢复 worker 60s / 集成探测 10m/30m），状态写路径由 Lua 脚本原子执行并以 generation +
> source_priority 形成严格的单调覆盖。但 **3 处需要持续注意**：(a) `recovery.EnterRecovery`
> 在生产没有 caller；(b) `systemmonitor` 内存 fallback 在重启时会丢任务；
> (c) `filterAndScore` 里 `if !ready` 检查有冗余复制（无害但需保留注释约束）。
> 这些在 §4 / §5 中详细展开。

---

## 1. 路由使用的状态后端（Redis 是否为统一权威源）

### 1.1 路由热路径的状态读取栈

`domains/streaming/executors/router.go::PlanCandidatesWithContext`
（request_id 80 行内）：

```
capture 1× Ready snapshot  (50ms timeout, ctx 用 requestCtx)
   │
   ├─ FilterAndScoreReady(seeds, snapshot)  ── 50ms timeout
   │     │
   │     └─ Manager.filterAndScore():
   │           │
   │           ├─ 1) 镜像快路径（16 shard LRU, 100k, soft TTL 30s）
   │           ├─ 2) mirror 全部命中 + ready=true → score & return（fail-open）
   │           ├─ 3) mirror 未命中 → PipelineNodeViews（HGetAll pipeline）写回
   │           └─ 4) ready=false → return error（任何路径都不能绕过 recovery gate）
   │
   ├─ selectStateBackendWithReady(snapshot)  ── 50ms timeout
   │     │
   │     ├─ v2 authoritative + ready=true → URSMv2Backend
   │     ├─ StateManager 启用           → LegacyStateBackend（credentialstate）
   │     └─ 兜底                          → DBOnlyBackend
   │
   └─ applyPressurePenalty (50ms timeout, 启用时)
```

关键不变量（路由层）：

- **一次请求 = 一次 Ready 快照**。`router.go:PlanCandidatesWithContext` 在调用
  `URSMv2.FilterAndScoreReady` 之前已经把 `Ready()` 的结果用 `*bool` 共享给
  `selectStateBackendWithReady`，避免「同一个请求里 backend 选择和 v2 过滤落在不同
  recovery 状态」。
- **`legacyWritersEnabled()` 1s TTL 缓存**（`executor.go:authoritativeCacheTTL=1s`）。
  Ready 翻转频率 ≤ 1 次/启动或恢复，1s TTL 是安全的保守值；将原本 11 次/请求的 Redis
  round-trip 降到 ~1 次/秒。
- **`Ready()` 50ms 超时**：本地局域网 Redis 实测 p99 < 5ms，50ms 远大于网络抖动带宽，
  但小于业务请求总 SLA，避免 Ready IO 拖慢热路径。

### 1.2 三个 StateBackend 的角色

| Backend | 何时启用 | 数据来源 | 作用 |
|---------|---------|---------|------|
| `URSMv2Backend` | v2 authoritative + Ready | `ursm:v2:node:{tenant}:{cid}:{model}` Redis Hash | 唯一权威 |
| `LegacyStateBackend` | v2 off / canary / shadow / 未就绪 | `credentialstate.Manager` 内存 + Redis 双层 | 降级路径 |
| `DBOnlyBackend` | 两者都不可用 | `provider.Candidate.IsAvailable()` （基于 DB 字段） | 兜底 |

**结论**: 在生产（v2 authoritative + Ready=true）路径下，**唯一权威源是 Redis Lua 脚本
写的 `ursm:v2:node:*` Hash**。其它两个 backend 是显式可降级路径，不是双写双源。

### 1.3 防封锁机制与状态后端的边界

```
URSM v2（健康 / 可用性）
  ├─ source_priority: Seed < Request < Probe < Recover < Admin
  ├─ generation 单调（apply_decision.lua + NodeMirror LRU 都拒绝倒退写）
  └─ manual_hold 由 Lua 内部 HGET 读取（消除 TOCTOU race，M3 修复）
            │
            │ 职责分离（防封锁层独立）
            ▼
FpSlots / Limiter / RPM / DisguisePool / EgressIdentity
  └─ 不走 StateBackend，独立写 Redis（fp_slot:*）与内存
```

这一点 `domains/streaming/executors/state_backend.go` 顶部注释（2026-07-24）已明确：

> 防封锁机制（FpSlots/Limiter/RPM/DisguisePool/EgressIdentity）保持完全独立，
> 不通过此接口管理。状态后端仅负责"节点是否可用"的健康判断。

---

## 2. 异常状态下的自检探测与恢复（及时 + 不浪费资源）

### 2.1 自检层级与触发频率（每层职责不同，时长与开销不同）

| 层 | 组件 | 触发条件 | 频率 | 单次开销 | 失效行为 |
|----|------|---------|------|---------|---------|
| **L0** 路由热路径 | `router.PlanCandidatesWithContext` | 每个请求 | 100% | 50ms Redis IO（v2 authoritative + 1s executor 缓存） | Ready=false → 降级到 LegacyStateBackend |
| **L1** Recovery gate | `recovery.Manager.Ready` | URSMv2 启动/手动 | 1次/启动 + 手动 | 1 RTT Redis GET `meta:ready` | false → v2 authoritative 被跳过 |
| **L2** 系统监测 Redis ping | `systemmonitor.healthCheckLoop` | 启动后常驻 | **15s** | 1s 超时 Redis PING | 失败 → 标记 fallback，submit 走内存 FIFO；恢复后自动清除 |
| **L3** 健康恢复 worker | `bg.credential_recovery` | 启动后常驻 | **60s** | 1× SQL UPDATE 多个分支 | SQL 失败仅 warn，不阻塞 |
| **L3** | `bg.health_auto_recover` | 启动后常驻 | **1m** | 1× SQL UPDATE | 同上 |
| **L4** 主动探测调度 | `systemmonitor` worker pool | Redis 可用时 | **200ms** poll claim | 5 并发上限 | fallback 后降级为内存 |
| **L5** 主动探测补探 | `bg.periodic_quota_probe` | 启动后常驻 | **5m** | SELECT + 最多 100 凭据 submit | DB 错误仅 warn |
| **L5** | `bg.integrity_probe_planner` | 启动后常驻 | **10m** | SELECT + 最多 25 行 enqueue（24h dedup） | 失败跳过整 tick |
| **L6** Broken 复活 | `bg.broken_probe_reviver` | 启动后常驻 | **30m** | 1× UPDATE | 失败 warn |
| **L7** Daily audit | `bg.daily_probe_audit` | 启动后常驻 | **24h** | SELECT 3 天窗口 + Submit | warn |
| **L8** Failover / 自愈 | `node_probe.go` + Lua record_request cooldown ladder | 每次失败 | 实时 | 5s/30s/60s/5m/1h/2h/6h（systemmonitor）或 5s/30s/2m/5m/15m（active probe） | 到达 cap 后保留 cool 状态等下次 Submit |

### 2.2 「及时 + 不浪费资源」两个目标的实现方式

- **及时性**通过**多层**保证：
  - 路由热路径实时同步（每请求 50ms IO）→ L0 立即发现
  - 凭据状态从 Lua 写完即生效 → L0 读到新 cool_until_ms 立即拒绝路由
  - 系统监测 15s 内发现 Redis 抖动并切换 fallback → L2 兜底业务
  - 60s 健康恢复 worker 处理「expired but not yet re-probed」的 stale rows → L3 兜底数据
  - 主动探测 5m 周期性扫 `periodic_exhausted` 凭据 → L5 防泄漏
  - 完整 cooldown ladder（最坏 6h）应对反复失败的凭据 → L8 自动拉黑

- **不浪费资源**通过**抑制 + 节流**实现：
  - **Ready 1s TTL 缓存**：原本每请求 11 次 Redis IO，现在 ~1 次/秒
  - **镜像 30s 软过期**：在 Redis 抖动期用 LRU 兜底，30s 后必须重新 RTT（不会因陈旧镜像永久禁路由）
  - **dedup TTL**：probe 队列的 `expires_at` 字段 + `inflight` 30s SET 防重复抢同一节点
  - **`MaxPerTick` 上限**：`integrity_probe_planner` 25/10m、`periodic_quota_probe` 100/5m、`credential_recovery` 50/60s → 任何 worker 不会一次性提交爆炸
  - **5min auto-skip**：自动任务若 5min 内有真实请求成功则跳过（`systemmonitor.inflight_dedup.ShouldSkipAutoTask`）→ 业务信号优先于探测信号
  - **Exponential backoff cap**：6h 后到达 cap 不再增长，避免反复失败凭据占用 worker slot

### 2.3 Redis 抖动 / 故障时的 fallback 矩阵

| 故障 | 检测 | 路由层 | 探测层 |
|------|------|--------|--------|
| **Redis 完全不可达** | `recovery.Ready()` 返回 error；`systemmonitor.Ping` 失败 | L0：`filterAndScore` 报 `ursm.v2: not ready` → router warn + 降级到 LegacyStateBackend | L2：进入 fallback（内存 FIFO），`Submit` 仍接受任务但写不到 Redis |
| **Redis 抖动但可达** | 50ms 偶尔超时 | Ready 缓存继续用上一秒结果，1s 内恢复 | 任务继续走 Redis，对延迟不敏感 |
| **Redis 重启**（key 丢失） | `meta:ready` 读到 nil | L1：Ready=false → 自动降级到 LegacyStateBackend | `systemmonitor` 重启后从 Redis FIFO 恢复；`recovery.EnterRecovery` 未被调用（见 §4） |
| **Redis 恢复后 stale 镜像** | NodeMirror soft TTL 30s 过期 | 下次访问 miss → PipelineNodeViews 重读 Redis 校正 | 同上 |

---

## 3. 状态更新规则（写路径的原子性 + 单调性 + 优先级）

### 3.1 唯一写入口：Lua 脚本

所有 v2 状态变更都通过 `redis.NewScript` 在 Redis 内一次性 EVAL 执行：

| 脚本 | 入参（KEYS / ARGV） | 原子动作 |
|------|---------------------|---------|
| `record_request.lua` | 1× node hash + 3× sliding window | 读 manual_hold（live）/ disabled / cool_until → 判断 in_cool → 失效时 set disabled + cool_until_ms；成功时清 disabled + 维护 windows |
| `apply_probe.lua` | 1× node hash | 读 manual_hold（live）→ in_cool + success → 立即 recover；否则更新 source_priority=20 + last_probe_* |
| `apply_admin.lua` | 1× node hash | HSET manual_hold/manual_actor/manual_reason/manual_at_ms/source_priority=40/available |
| `apply_decision.lua` | 1× node hash + 6× ARGV | 拒 admin 写 → 比 generation/source_priority → 接受或返回 `ignored_stale` |
| `clear_state.lua` | 1× node hash | HDEL cool_until_ms/cool_start_ms/fail_streak/fail_count/disabled/disable_count/disabled_reason/disabled_at_ms/last_err/last_err_at_ms/failure_count/success_count + HINCRBY generation |

**关键不变量**:

- **manual_hold 由 Lua 内部读取**（M3 修复：原 Go 端 HGet + Lua Eval 存在 TOCTOU，
  现在 ApplyAdmin 与 record_request 竞争由 Lua 内部 single-shot 解决）。
- **generation 单调**：`apply_decision.lua` 与 `NodeMirror.applyToLRU` 都拒绝倒退写，
  一个 stale gen 永远不会覆盖新 LRU / Redis entry。
- **source_priority 优先级**：Seed(0) < Request(10) < Probe(20) < Recover(30) < Admin(40)。
  Admin 永远是最终决策，`record_request.lua` 第 41 行直接 short-circuit。

### 3.2 Go 端写流程

```
hot path 写：
  Executor (sidecar) → Manager.RecordRequest(ctx, ev)
    │
    ├─ nil / store==nil → no-op（防 wiring 错位）
    ├─ rollout.ShouldUseV2 → false（off / shadow / canary 不命中）→ no-op
    └─ else → RecordRequestScript.Run(ctx, rdb, [node, w1, w5, w30], ARGV...)
                  │
                  └─ 内部 RecordTimeoutMs 默认 20ms（不阻塞请求热路径）

admin 写：
  Manager.ApplyAdmin / ClearState
    │
    └─ ApplyAdminScript / ClearStateScript.Run（无内部 short-circuit，
       与 record_request 竞争由 Lua 内部 manual_hold 单点裁决）

probe 写：
  Manager.ApplyProbe{ForTenant}
    │
    └─ ApplyProbeScript.Run（manual_hold live read → 失败忽略）
```

### 3.3 防冲突设计

- **跨脚本竞争**：admin / request / probe 三条 Lua 共享 `manual_hold` live-read，且都
  在写前读 → 写集合内做 CAS。即使两个 request 并发失败在同一节点上，`fail_streak`
  HINCRBY 是原子的（Redis 单线程串行）。
- **跨进程竞争**（多 gateway 实例）：所有 Lua 都在 Redis 单点上串行；Lua 内的
  `redis.call()` 之间不会被并发打断，因此 generation + source_priority 的 CAS
  是 atomic 的。
- **跨重启竞争**：Redis 持久化（RDB/AOF）保证重启后 hash 不丢；`recovery.WarmupFromSeed`
  在新 Redis 上重新写入 node hash（priority=Recover=30）。

---

## 4. 当前已确认的偏差与修复建议

> 这些不是「缺陷」，是「需要持续注意或安排工作」的点。本审计不强制修复，但应纳入
> 下一阶段的 follow-up 排期。

### 4.1 `recovery.EnterRecovery` 在生产没有 caller

**观察**：`grep -rn "EnterRecovery" --include='*.go' | grep -v _test` 只命中 `_to-be-deprecated`
路径和 `domains/ursm/v2/recovery/manager.go` 自身。`cmd/gateway/main.go` 启动时只调用
`SetReady(true)`，没有挂任何「检测到 Redis 严重故障时主动 EnterRecovery」的 worker。

**影响**：Redis 重启 / 严重数据损坏时，gate 不会自动关闭；上层依赖 fallback 检测。

**修复状态（2026-07-28, follow-up #1）**：已完成。`recovery.Manager.MarkClosedDebounced`
新增 SETNX-based cluster-wide debounce（5m 默认 TTL），由
`systemmonitor.healthCheckLoop` 在 3 次连续 ping 失败（~45s）后自动调用，自动关闭
v2 authoritative gate 并写入 epoch 元数据；cmd/gateway/main.go 仅在 v2 mode != off
时接线，避免 off 模式下的噪声日志。测试：`TestMarkClosedDebounced_ClusterCoordinator`、
`TestMarkClosedDebounced_NextWindowBumpsAgain`、`TestCheckRedisHealthOnce_*` (5 个)。

**后续（不在本审计任务内）**：
- 与之配套，Redis 恢复后自动 `WarmupFromSeed` 再 `SetReady(true)`（需要监控 Warmup 是否完成，
  避免数据未准备好就开 gate）。建议下个迭代跟进。

### 4.2 `systemmonitor` 内存 fallback 在重启时会丢任务

**观察**：`bg/systemmonitor/monitor.go::fallbackCh` 是 `chan *Task`，容量
`max(concurrency*4, 1000)`。`Submit` 在 Redis 错误时把任务扔进这个 channel；进程退出
时 fallback channel 里未消费的任务丢失。

**影响**：Redis 抖动 + 重启窗口内被提交的任务会丢。

**建议**：
- 短期：fallback 模式下 Submit 调用方应当打印 warn 并 metric（已实现 `IsFallback()` API）。
- 长期：fallback 写入 PG `credential_probe_queue`（已存在同一张表，可直接复用）。

### 4.3 `Manager.filterAndScore` 里有冗余 `if !ready` 检查

**观察**：`domains/ursm/v2/manager.go` 第 230 行与第 240 行各自检查 `!ready`。第 230 行
在「mirror 快路径 + 全部命中」分支之前；第 240 行在「mirror 未命中 + Redis fetch」分支
之前。但 230 已经对 `!ready` 返回 error，240 在那个 case 下永远不可达。

**影响**：当前正确，但属于 dead code；如果未来有人在 230 与 240 之间插入新的处理
分支，可能造成 240 不可达的不变量被破坏而无人察觉。

**建议**：保留冗余（注释明确这是 invariant safeguard），但在 `manager.go` 第 240 行
上方补一行注释：「DO NOT REMOVE — line 230 guards the all-hits branch; this is the
invariant safeguard for the miss path. They are intentionally duplicate so any
refactor between them cannot accidentally lift the recovery gate.」

**附**：本审计任务附带新增一条回归测试（`TestFilterAndScoreReady_RejectsMirrorWhenNotReady`）
用于 pinning `ready=false` 时镜像快路径仍然返回错误的不变量。

### 4.4 `Plan()` 与 `PlanReady()` 的 ModeOff 短路行为差异

**观察**：`manager.go::Plan` 第 333 行检查 `m.Mode() == ModeOff` 提前返回，避免
`Ready()` IO；`PlanReady` 不检查（依赖调用方传的 ready）。这本身是优化（避免 off 模式下
的额外 Redis IO），但增加了两个分支的语义差。

**建议**：注释强化 — `Plan()` 适用于 router 在 off 模式直接短路，`PlanReady()` 适用
于 canary 调用方已经知道 ready。在 audit follow-up 中继续。

---

## 5. 待跟进工作清单（建议优先级）

| # | 任务 | 影响面 | 估计 | 阻塞？ | 状态 |
|---|------|--------|------|-------|------|
| 1 | 接线 `systemmonitor.healthCheckLoop → MarkClosedDebounced` | Redis 严重故障 incident 复盘 + 自动 close gate | 3 文件 + 5 测试 | 否（fallback 已兜底） | ✅ 已完成 2026-07-28 |
| 2 | `systemmonitor` fallback 任务持久化（durable backstop） | 防重启丢任务 | 4 文件 + 5 测试 | 否（fallback 期间低频） | ✅ 已完成 2026-07-29 |
| 3 | `filterAndScore` duplicate `if !ready` 注释 + 回归测试 | 防止后续 refactor 破坏 | 1 文件 1 测试 | 是（本次审计附带） | ✅ 已完成 2026-07-28 |
| 4 | `recovery.LastError / LastRecoveryAt / LastRecoveryKeyCount` 指标 | 排查 Redis 抖动 | 4 文件 + 5 测试 | 否 | ✅ 已完成 2026-07-29 |
| 5 | `state_health_checks` admin 端点 + SSE 事件 | 监控可视化 | 3 文件 + 4 测试 | 否 | ✅ 已完成 2026-07-29 |
| 6 | 配套 Redis 恢复后自动 `WarmupFromExistingKeys` → `SetReady(true)` | gate 关闭后能自动恢复 | 2 文件 + 4 测试 | 否 | ✅ 已完成 2026-07-29 |

---

## 6. 关联文档

- `docs/architecture/ARCHITECTURE.md` — 主架构文档（§0.5 路由状态演进）
- `docs/architecture/routing-domain.md` — Routing Domain 边界定义
- `docs/design/2026-07-20-latency-aware-routing.md` — 延迟感知路由 §2.1
- `docs/2026-07-24-routing-state-optimization.md` — Phase 1 路由状态简化
- `docs/2026-07-25-phase2-final-delivery.md` — Phase 2 压力感知路由
- `docs/会话优化v2/32-系统监测模块设计.md` — systemmonitor 设计依据
- `domains/streaming/executors/state_backend.go` — 三种 StateBackend 实现
- `domains/ursm/v2/manager.go` — URSM v2 facade
- `domains/ursm/v2/recovery/manager.go` — Recovery gate
- `bg/systemmonitor/monitor.go` — 系统监测入口
- `bg/credential_recovery.go` — 60s 健康恢复 worker

---

## 7. 验收标准

本次审计的「完成」标准（已自验）：

- [x] 路由层在权威模式下读的唯一来源是 `ursm:v2:node:*` Redis Hash（通过 Lua）
- [x] 异常自检的触发频率与开销已分级列出（L0 ~ L8）
- [x] 状态更新规则覆盖所有 v2 Lua 脚本（record / probe / admin / decision / clear）
- [x] generation + source_priority 的单调 / 覆盖优先级已文档化
- [x] 至少 4 条偏差已记录（§4.1-§4.4）并附 follow-up
- [x] 新增一条 ready=false 时镜像快路径的回归测试（`TestFilterAndScoreReady_RejectsMirrorWhenNotReady`）
- [ ] follow-up 排期由 team lead 决定（本审计不强制排期）