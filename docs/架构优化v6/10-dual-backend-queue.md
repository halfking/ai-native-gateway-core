# 10 · 双后端队列（内存 | Redis）与分布式调度 — 设计定稿（V6-W1.7）

> **版本**：v6.3（2026-08-27 起草；修订 08 号 §2.1 "执行队列本体在内存"的定稿——用户决策升级为**双后端**）
> **需求原文**（用户 2026-08-27）："没有 redis 时，才会回退到本机内存中，有 redis 就需要用 redis，因此整个队列的实现是要有两套：内存与 redis，我们有可能是分布式的，多个服务器来接收请求，需要使用 redis 来操作。"
> **证据等级**：`DESIGN` + `LOCAL_REVIEWED`（锚点已核对；实现须达 `LOCAL_VERIFIED`）

---

## 0. TL;DR + 一个必须先说清的架构约束

**连接亲和（Connection Affinity）约束**：`QueuedRequest` 携带 goroutine、HTTP 连接（`ExecParams.W`）、`ResultCh`、闭包——**不可序列化**。因此"请求由哪台服务器执行"永远等于"客户端连接在哪台服务器"（LB 决定）。Redis 队列后端管的是**跨实例的准入、容量与定时可见性**（多台服务器共同遵守供应商并限/队列上限、互相看到待处理集合），**不是**跨实例 work-stealing（A 台取走 B 台客户端的请求去执行）。

跨实例接管执行（实例宕机后另一台续跑）在既有架构中由 **durable lane**（PG，加密快照 + lease + fencing，默认关）+ **pending 响应缓存**（客户端重连取回）承担；若未来要 durable-on-Redis，是独立 ADR，不在本轮。

按此约束，"两套实现"落地为：**队列原语（准入/容量/停靠）抽象接口 + 内存实现 + Redis 实现，有 Redis 用 Redis、无 Redis 回退内存**——与既有 `GovernorBackend`（`local | redis_shadow | redis_enforce`，`LLM_GATEWAY_DISPATCH_GOVERNOR_BACKEND`）完全同构。

## 1. 现状盘点：哪些已经是双模、哪些还是纯内存

| 队列原语 | 现状 | 证据 |
|---|---|---|
| 供应商并限/限流（Tier-2 Governor：concurrency/RPM/TPM） | ✅ **已双模**：LocalBackend / RedisShadowBackend / RedisEnforceBackend，env 选择，无 Redis 自动回退 local | `governor_backend.go`、`redis_backend.go`、`cmd/gateway/main_dispatch_backend.go:85-136`（`wireDispatchGovernorBackend`，main.go 在 SetQueueMirror 后调用） |
| 队列观测镜像（深度/在途/retry_at/scheduled_at） | ✅ Redis（observation-only） | `queue_mirror.go` |
| Tier-0 等待室准入（cap 1000） | ❌ 纯内存 CAS（每实例各自 1000） | `total_queue.go:21-41` |
| Tier-1/Tier-2 lane 容量上限（cap 300） | ❌ 纯内存 channel 容量 | `pipeline.go:1144`、`getOrCreateForwarder` |
| 错误重试堆 / 定时到期堆 | ❌ 纯内存（对象本地，不可跨实例） | `retry_schedule.go` |
| 定时停靠跨实例可见 | ✅ Redis 键（scheduled_at/retry_at，TTL 10min） | W1.5 修正轮 |
| 请求生命周期投影（registry/journey/dimension） | 本地 + 派生持久（PG/Redis 投影） | `registry.go`、requestjourney |

**结论**：分布式最关键的"按供应商并限稳定输出"已经是 Redis 权威；剩余缺口是 **Tier-0/Tier-1/Tier-2 的集群容量口径**与**定时/重试停靠的集群视图**。

## 2. 目标设计

### 2.1 抽象接口（新文件 `domains/dispatch/queue_backend.go`）

```go
// QueueBackend 是跨实例队列原语的双模抽象（内存 | Redis）。
// 复用 GovernorBackend 的组合根模式（env 选择 + 无 Redis 回退 local）。
type QueueBackend interface {
    Kind() QueueBackendKind // "local" | "redis"
    Open(ctx) error; Close() error
    // TryAdmitTotal: 集群 Tier-0 等待室准入（cap = cfg.TotalQueueCapacity 的集群口径）。
    // 返回 admission token；false = 集群等待室满。
    TryAdmitTotal(ctx, qr *QueuedRequest) (Admission, bool)
    // TryReserveLane: 集群 lane 容量预留（kind=model|credential, id）。
    TryReserveLane(ctx, kind LaneKind, id string) (Admission, bool)
    Release(a Admission)          // 幂等
    Heartbeat(ctx) error          // Redis 实现：刷新实例存活键（崩溃自愈）
    Snapshot(ctx) (QueueBackendStats, error) // 观测：集群/本实例占用
}
type Admission struct{ Key string; Kind LaneKind; ID string }
```

- **内存实现 `localQueueBackend`**：包装现有 `totalExecutionQueue` 的 CAS 计数与 lane 深度——行为与现状**逐位等价**（默认后端，无 Redis 时的回退）。
- **Redis 实现 `redisQueueBackend`**：
  - 实例计数键：`llmgw:dispatch:queue:v1:total:{instance}`（INCR，TTL=30s，Heartbeat 每 10s 续期）——**实例崩溃后计数 30s 自愈归零**，不会永久泄漏集群容量；
  - lane 键：`llmgw:dispatch:queue:v1:lane:{kind}:{id}:{instance}`（同 TTL 模式）；
  - 集群占用 = `SUM(total:*)` / `SUM(lane:{kind}:{id}:*)`（Lua 原子判定 `sum < cap ? INCR own : reject`，避免竞态超发）；
  - due 停靠 ZSET（可选 T3）：`llmgw:dispatch:queue:v1:due`，score=due_ms，member=`{instance}|{request_id}`——集群范围"下一个到期的定时/重试请求"视图，供 admin 与告警；**拾取仍由本实例 promoter**（对象在本实例）。
- **组合根**（`cmd/gateway/main_dispatch_backend.go` 扩展）：`LLM_GATEWAY_DISPATCH_QUEUE_BACKEND` = `auto`(默认：有 redisClient → redis，无 → local+ WARN) | `local` | `redis`（redis 但无 client → 回退 local + WARN）。与用户"没有 redis 才回退本机内存"一致。

### 2.2 降级语义（关键决策，与 Governor 相反方向，必须文档钉死）

| 场景 | Governor（限流） | QueueBackend（准入/容量） | 理由 |
|---|---|---|---|
| 配置 redis 但 Redis 故障 | **fail-closed**（redis_enforce 不回退，防超并发打爆供应商） | **fail-open 回退 local 口径**（本实例独立 cap），`dispatch_queue_backend_degraded` 告警 | 准入丢口径的代价是各实例各自排队（可接受）；限流丢口径的代价是击穿供应商并限（不可接受）。保可用 vs 保供应商，方向相反 |
| Redis 恢复 | 自动回 Redis（发布重放） | 心跳恢复后续期，计数重建（TTL 期间已自愈清零，从零重计） | 无需人工 |
| 实例崩溃 | lease TTL 释放 | 计数键 TTL 30s 归零 | 自愈 |

### 2.3 管线接入点（改动面）

1. `Pipeline.Submit`：本地 `totalQueue.tryEnqueue` 之前先 `TryAdmitTotal`（local 后端时退化为现有 CAS，零开销）；失败 → 既有 `OverflowError{total_queue_full}`（客户端 503+Retry-After 不变）。
2. `tryEnqueueCred` / `enqueueModel`：本地 channel 预留前 `TryReserveLane`；失败走既有"lane 满"路径（换凭据/容量等待）。
3. `complete()` + Submit 取消路径：`Release` 对称释放（幂等，token CAS 防双放）。
4. `parkScheduledRequest`：写 due ZSET（Redis 后端时）+ 既有 `scheduled_at` 键；到期/终态 `ZREM`。
5. 生命周期：`wireDispatchPipeline` 后调用 `wireDispatchQueueBackend`（与 governor 同位），Start 时启动 Heartbeat goroutine，Stop 时 Close。

### 2.4 不变量（实现后必须有单测）

1. local 后端行为与现状**逐位等价**（全量 dispatch 测试零修改通过）。
2. `Admit/Release` 严格对称（对账指标 `dispatch_queue_backend_held` gauge ≈ 集群占用；偏差告警）。
3. 实例"死亡"（停心跳）30s 后其容量自动归还（miniredis 快进 TTL 测试）。
4. Redis 故障时：准入回退 local、零 panic、请求不丢（提交继续，口径降级）；恢复后自动回切。
5. due ZSET 与本地堆一致（park 写 / due+terminal 删；TTL 10min 兜底残留）。

## 3. 异常矩阵

| # | 异常 | 行为 |
|---|---|---|
| D1 | Redis 抖动（偶发超时） | TryAdmit 短重试（≤50ms）→ 仍败按 §2.2 fail-open local 口径；计数可能短暂低估/高估一个窗口，TTL 自愈 |
| D2 | 实例崩溃持有准入 | 键 TTL 30s 归零；期间集群容量被该实例"占用"——cap 需按 (实例数 × 单机 inflow) 留 30s 余量 |
| D3 | Lua 判定与 INCR 之间集群 sum 竞态 | Lua 内原子完成 sum+INCR，无竞态窗口 |
| D4 | 时钟偏差（due ZSET score） | score 用 Redis `TIME`（Lua 内取），不用实例本地钟 |
| D5 | 降级→恢复回切瞬间双口径 | 回切以心跳成功为界；计数从零重建（TTL 已清旧值），短暂欠准入（安全方向） |
| D6 | Redis 持久化丢失/主从切换 | 集群计数清零 → 短暂超发（各实例各自 cap）——与 D2 同级可接受；供应商侧由 Governor（独立键）继续兜底 |
| D7 | 心跳 goroutine 泄漏 | 随 Pipeline.Stop Close，wg 归队 |
| D8 | due ZSET 残留（实例宕机未 ZREM） | member TTL 由定期 ZREMRANGEBYSCORE（score < now-1h）清扫 + 本地堆权威不受影响 |

## 4. 与既有设计的关系

- **08 号 §2.1 存储拓扑**：修订为"双后端"小节——执行对象仍在本实例内存（连接亲和不变）；准入/容量/定时元数据在有 Redis 时以 Redis 为权威、无 Redis 回退本机。观测镜像继续存在（深度键与 QueueBackend 计数可合并，T6 清理去重）。
- **09 号（W1.6）**：journal/dimension 仍为本地（观测投影，不参与准入）；W1.7 不改 planner 决策逻辑。**执行顺序：W1.6 先行（行为等价重构，测试护栏已就位）→ W1.7 后行**（都动 dispatch 核心，串行降冲突）。
- **Governor**：不动（已双模且 fail-closed 语义正确）；T6 仅统一组合根日志与指标前缀。
- **冻结契约**：`restart_semantics_v1` 的 `ordinary_dispatch → dropped` 不变（对象仍不可迁移）；W1.7 只改"准入与容量口径"，不改"执行归属"。

## 5. 任务分解

| # | 任务 | 文件 | 验收 |
|---|---|---|---|
| U1 | `QueueBackend` 接口 + local 实现（收口现状） | `domains/dispatch/queue_backend.go` | 全量 dispatch 测试零修改通过（等价门禁） |
| U2 | redis 实现（Lua 准入 + 心跳 TTL + due ZSET） | `queue_backend_redis.go` | miniredis：准入/释放/对称性/崩溃自愈（快进 TTL）/D1-D8 单测 |
| U3 | 管线接入（Submit/lane/complete/park 五点） | `pipeline.go` | local 等价 + redis 模式集成测试（两实例模拟：共享 miniredis，各自 cap 竞争） |
| U4 | 组合根 + env | `cmd/gateway/main_dispatch_backend.go`、`main.go` | auto/local/redis 三态 + 回退 WARN 日志单测 |
| U5 | 指标与告警 | `metrics.go`、`deploy/prometheus/rules/`（新增文件） | `dispatch_queue_backend_{held,admit_total,release_total,rejected_total,degraded}` |
| U6 | 观测去重（mirror 深度键 vs backend 计数）与 08 §2.1 修订 | `queue_mirror.go`（标注）、08 号文档 | 文档一致性评审 |
| U7 | 文档 | 本文 §3 走查复核 + changes 文档 + README | 落盘 |

顺序：U1→U2→U3→U4→U5；U6/U7 收尾。整体排在 W1.6（09 号）之后执行。

## 6. 完成标准

- [ ] `go build ./...`；`go test -race ./domains/dispatch/`（local 等价 + redis miniredis 双模）全绿
- [ ] 两实例共享 Redis 的集成测试：集群 cap 生效、单实例崩溃 30s 容量归还、Redis 断连 fail-open
- [ ] §2.4 五条不变量各有单测；changes 文档落盘（`LOCAL_VERIFIED`）
