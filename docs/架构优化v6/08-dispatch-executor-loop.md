# 08 · 调度执行器闭环（Dispatch Executor Loop）— 需求补全与设计定稿

> **版本**：v6.1（2026-08-27 起草，对照代码基线 `domains/dispatch` 当前工作树）
> **读者**：架构组 / 后端 Owner / SRE
> **范围**：在 [`03-roadmap-v6-waves.md`](03-roadmap-v6-waves.md) V6-W1（契约/正确性）基础上，把"请求队列 + 执行器 + 路由切换 + 状态回写 + 客户端保持"收敛为一个可解释的**调度执行器闭环**。本文是对用户 2026-08-27 需求（队列执行器模型）的补全、纠偏与落地方案。
> **证据等级**：`DESIGN` + `LOCAL_REVIEWED`（基于工作树只读勘察；实现随本轮 commit 附带单测）。

---

## 0. TL;DR

用户提出的模型是：**待处理队列（总请求队列）+ 按 CPU 核数-1 的执行器 + 即时/定时两类请求 + 失败回队打标 + 回队时向客户端发 think 通知 + 按凭据/模型/供应商的分维队列（TTL 淘汰）**。

对照代码：这套模型的**骨架已经存在**——`domains/dispatch` 就是"总队列（Tier-0）→ 模型道（Tier-1）→ 凭据道（Tier-2 + Governor 并发/限流）"的三级流水，失败走 failover 阶梯（同凭据重试→换凭据→换模型），生命周期有 requestjourney 17 事件投影。但存在 6 个缺口：

| # | 缺口 | 现状证据 | 本轮动作 |
|---|---|---|---|
| G-Ⅰ | 执行器数量固定 8，不随 CPU 自适应 | `config.go:77-78`（DispatcherWorkers/FailoverWorkers=8） | ✅ 改为 `NumCPU-1`（钳制 [2,32]），hotconfig 可覆盖 |
| G-Ⅱ | 无定时请求：请求一律即时 | 全仓无 `due_at`/`execute_at` 请求字段 | ✅ `QueuedRequest.DueAt` + 到期堆回投，`X-Gw-Due-At` 头 |
| G-Ⅲ | 回队不通知客户端：`OnNodeSwitchSummary` 已实现但**生产无人赋值**（死回调） | `failover.go:64,113` 调用；全仓无赋值点 | ✅ 结构化 `DispatchNotice` 回调 + 5 类事件全接线到 `: thinking:` SSE 注释 |
| G-Ⅳ | 无按凭据/模型/供应商的请求成员索引（完成后保留、TTL 淘汰） | QueueProjection 只有深度，无请求级归属 | ✅ `DimensionIndex`（分维环形 + TTL/容量淘汰）+ admin 查询端点 |
| G-Ⅴ | 回队打标不显式： TriedCredentials/TriedModels/CredRetryCount 散落 | `queued_request.go:127-138` | ✅ `LastFailover` 标记（错误类型/上轮模型节点/下一步动作），进通知与分维索引 |
| G-Ⅵ | 换模型/容量等待时客户端无任何信号 | `tryModelChangeOutcome`/`scheduleCapacityRetry` 无回调 | ✅ 并入 G-Ⅲ 通知体系 |

**明确不做**（与冻结契约/既有设计冲突，见 §5）：把总队列改成"处理完成才移除"的字面语义（Tier-0 是有界等待室，完成后由 LifecycleRegistry 承担 pending→completed 记账）；把上游转发改成固定 CPU 数执行器（上游并发受供应商并发/RPM/TPM Governor 约束，goroutine-per-attempt + Governor 才是按供应商限流"稳定平衡输出"的正确机制）；中央持久化队列（restart 语义 `ordinary_dispatch→dropped` 已冻结，durable lane 是独立 opt-in 通道）。

---

## 1. 用户需求 → 补全后的需求（Requirements, Refined）

以下每条 = 用户原话要点 + 补全后的可执行语义。

### R1 待处理队列（= 现有 Tier-0 总请求队列）
- **收到请求即入待处理队列**：已有。`Pipeline.Submit` → `totalQueue.tryEnqueue`（容量 1000，满则 `OverflowError` + 503 + Retry-After，防重试风暴）。
- **补全**：请求在待处理队列中的**成员身份**持续到终态（`LifecycleRegistry` pending→in_flight→completed 记账），执行流转经 Tier-1/Tier-2 时并不丢失"待处理中"的可观测性。失败回队（retry park / capacity park / 定时 park）都会回到 pending 态（带 `retry_at`），这就是"丢回到待请求队列中"的落地形态：**逻辑上的待处理集合 = Tier-0 等待室 + 三个 park 堆（error-retry / capacity-retry / scheduled-due）**。
- **处理完成从队列移除**：已有。`complete()` 一次性投递 ResultCh 并 MarkCompleted。

### R2 执行器（CPU 核数 - 1）
- **补全与纠偏**：调度决策工作（模型解析、路由、打标、回投）是 CPU 侧工作，按 `NumCPU-1`（钳制 [2,32]，hotconfig `llmgw_dispatch_dispatcher_workers` / `llmgw_dispatch_failover_workers` 可覆盖）配置 ① Dispatcher 与 ③ Failover Mover 两个池。**上游 HTTP 转发不进固定池**：每个 attempt 一个 goroutine，其并发上限由该凭据的 Governor（concurrency/RPM/TPM）精确约束——这正是"按供应商指定的并发及限流稳定、平衡输出"的机制；若把转发塞进 CPU 数执行器，供应商并发额度与 CPU 核数会互相错配（8 核机器无法吃满一个 30 并发的凭据）。
- 执行器取件规则（用户语义的落地）：
  - **即时请求**（DueAt 零值或已到）→ 直接进入模型道执行；
  - **定时请求未到期** → 放回待处理（到期堆），到期由 promoter 重新 admissions 进 Tier-0；
  - **到期** → 正常执行。

### R3 失败回队打标
- 失败后请求**不终态**时（还有预算），回 park 堆等待下一轮，并打上：
  - 错误类型（`errorsx` kind，已有 ForwardOutcome.ErrorKind）；
  - 本轮执行的模型与节点（`LastFailover.Model/CredentialID/Vendor`，本轮新增）；
  - 下一步动作类型（`LastFailover.NextAction`: `retry_same_cred | switch_cred | switch_model | capacity_wait | scheduled_wait`，本轮新增）。
- 下一轮执行时，failover 阶梯**先读标志**（`CredRetryCount` vs `RetryPerCredential`、`TriedCredentials`、`TriedModels`、`FatalCredential`、首字节边界 `BytesSent`）决定路由：同节点重试 / 换节点 / 换模型 / 终止。这一决策链已在 `move()` + `DecideFailover`（errorsx/failover_policy.go）实现，本轮以 `LastFailover` 令其在通知、索引、日志中显式可见。

### R4 无匹配模型/节点的判定（回队等待）
- 已有两条独立信号，均触发**回队等待**而非立即 503：
  1. **真无节点可用**：Router 的状态后端过滤（credentialstate/URSM v2 `IsAvailable`）+ 节点健康过滤（`credentialfpslot.NodeState.IsUsable`，3 连败禁用 5 分钟）→ 候选为空 → 换模型或 `ErrNoRoute`；
  2. **供应商并限/限流到达**：Tier-2 队列满（`HasCapacity` false，不标记 tried，继续找同模型其他凭据）→ 全满 → `scheduleCapacityRetry`（5s × 12 次容量等待，不耗 tried 预算）→ 仍满 → 换模型；Governor pace 超时 → 换凭据。
- **补全**：容量等待现在会向客户端发 think 通知（等待原因 + 预计重试时间）。

### R5 客户端保持与 think 通知
- **连接稳定**：已有 pre-stream keepalive（`: keep-alive` SSE 注释，默认 15s，`LLM_GATEWAY_ENABLE_PRE_STREAM_KEEPALIVE`）+ survival keepalive + `SerializedStreamWriter` 串行写。排队期间连接不会静默超时。
- **think 类型通知（不影响会话）**：复用 `: thinking: <json>` SSE **注释**通道（所有 SSE 解析器忽略注释；opencode Zod 校验不会被打爆——`handler.go:213` 的研究结论）。本轮把 5 类事件接入：
  | 事件 | 触发点 | 消息示例 |
  |---|---|---|
  | `retry` | 同凭据重试排程 | 上游请求暂时失败（timeout），第 2 次重试将于 10s 后执行… |
  | `node_switch` | 切换兄弟凭据 | 上游请求失败（rate_limit），正在切换到备用节点… |
  | `model_switch` | 模型切换 | 模型 gpt-4o 无可用节点，切换到 claude-3-5… |
  | `queued` | 容量等待 | 所有节点并发已满，排队等待中（第 3/12 轮，5s 后重试）… |
  | `scheduled` | 定时请求受理/到期 | 定时请求已受理，将于 12:00:00 执行… |
- **安全边界**：仅首字节前发送（`move()` 天然 pre-firstbyte）；非流式请求无 SSE 通道，通知为 no-op；写失败由 SerializedStreamWriter 永久detach 兜底。

### R6 分维队列（凭据/模型/供应商节点归属，TTL 淘汰）
- **用户语义**：请求在哪个凭据/模型/供应商节点执行，就登记进它们的对应队列；操作完成更新状态；**无论结果如何都不移除**，靠 TTL 或容量挤出去。
- **落地**：`DimensionIndex` —— 请求级成员索引（非执行队列，不参与调度，纯归属/审计/运维面）：
  - 维度：`model:<name>`、`credential:<id>`、`provider:<id>`；
  - 条目：RequestID/TenantID/SessionID/Model/CredentialID/Vendor/State/Outcome/ErrorKind/RetryAt/LastAction/Attempts/时间戳；
  - 淘汰：条目 TTL（默认 15min，`llmgw_dispatch_dimension_ttl_seconds`）+ 每维度环形容量（默认 128，`llmgw_dispatch_dimension_capacity`）；完成只改 State，不删除；
  - 用途：admin 查询"某凭据最近执行/正在排队哪些请求"、故障时定位某节点上的滞留请求、与 requestjourney 投影互为补充（journey 是全局 100 条显示容量，分维索引是 per-维度 128 条）。

### R7 大量请求下的稳定平衡输出（供应商并发/限流对齐）
- 已有：per-cred Governor（concurrency 信号量 / RPM 令牌桶 / TPM 令牌桶，可 Redis 全局化 + 热策略 `ApplyPolicy` 热换）、Tier-2 每凭据队列深度上限、Executor 侧 Limiter（global/pool/identity/key）、P2C + 权重 + sticky 的负载均衡、容量感知软排序（Stage D）。
- 本轮补强：容量等待与 pace 超时的客户端可见性（G-Ⅲ/Ⅳ/Ⅵ），使"限流等待"不再是黑盒。

---

## 2. 架构（Target）

```text
                        ┌────────────────────────────────────────────────────────┐
  HTTP handler          │  Pipeline (domains/dispatch)                           │
  ────────────────►     │                                                        │
  X-Gw-Due-At?          │  Submit ─► Tier-0 总队列(1000) ─► runTotalDrainer      │
  : keep-alive          │      │            ▲              │ DueAt未到?          │
  : thinking: … ◄───────┼──────┼────────────┼──────────────┤ ├─► scheduled堆 ──┐ │
  (DispatchNotice)      │      ▼            │              ▼ 到期重新admission ◄─┘ │
                        │  Tier-1 模型道 ─► Dispatcher×(CPU-1)                    │
                        │      │  模型解析 · RouteFunc(可用性+健康+已试过滤)       │
                        │      ▼                                               │
                        │  Tier-2 凭据道 ─► Governor(并发/RPM/TPM) ─► attempt    │
                        │      ▲                              │ goroutine        │
                        │      │ pace超时/队列满               ▼                  │
                        │  Failover Mover×(CPU-1) ◄── 失败(pre-firstbyte) ───────┤
                        │      │ 打标 LastFailover + DispatchNotice → 客户端     │
                        │      ├─ retry堆(同凭据退避 5s→120s) ─┐                  │
                        │      ├─ capacity堆(5s×12) ──────────┤ 回到待处理集合    │
                        │      └─ model_change → Tier-1       │                  │
                        │                                     ▼                  │
                        │  complete() → ResultCh → handler; LifecycleRegistry     │
                        │             → DimensionIndex(分维, TTL淘汰, 不移除)     │
                        └────────────────────────────────────────────────────────┘
```

关键不变量（沿用 + 新增）：
1. **单所有者**：QueuedRequest 任一时刻仅一个执行器 goroutine 拥有（既有）；`LastFailover`/`DueAt` 写入遵循同一不变量。
2. **Tier-0 是唯一执行准入边界**：registry 只是投影（既有）；定时到期重入也走 `tryEnqueue`，满则 Overflow 终态。
3. **首字节边界**（ADR-Disp-003）：BytesSent 后不再换节点/换模型（既有）；think 通知也因此只在首字节前。
4. **通知不阻塞调度**：OnDispatchNotice 在调度 goroutine 上同步调用，但 handler 侧写入是串行化非阻塞写（失败即 detach）；通知回调必须短平快（文档约束 + recover 兜底）。
5. **分维索引旁路**：DimensionIndex 失败/慢不影响执行路径（锁内只做指针操作，快照按需复制）。

---

## 2.1 存储拓扑：内存 vs Redis（2026-08-27 修正增补）

> 背景：需要明确"请求队列放在哪里"。核查结论（file:line 证据见 §2.1 表）——
> **执行队列本体在进程内存，不在 Redis**；Redis 承担的是观测镜像、响应缓存
> 与跨实例限流。这是 v4 冻结契约（`restart_semantics_v1_valid.json`：
> `ordinary_dispatch → expected_projection: "dropped"`；`queue_mirror.go` 头注释
> "MUST NOT be used to resume execution"）。

| 状态 | 位置 | 角色 | 键/结构 |
|---|---|---|---|
| Tier-0 总队列（等待室，cap 1000） | 进程内存（`total_queue.go` chan） | **执行准入权威** | — |
| Tier-1 模型道 / Tier-2 凭据道 | 进程内存（`pipeline.go`/`forwarder.go` chan） | 执行排队 | — |
| 错误重试堆 / 定时到期堆 | 进程内存（`retry_schedule.go` heap） | 到期再入队 | — |
| 队列深度 / 在途数 / retry_at / scheduled_at | **Redis 镜像**（`queue_mirror.go`，TTL 10min，异步旁路） | 观测 + 重启后元数据重建（**不得恢复执行**） | `llmgw:dispatch:mirror:v1:{t1_depth,t2_depth,inflight,retry_at:*,scheduled_at:*}` |
| 定时请求停靠（G-Ⅱ） | 权威在内存到期堆；**Redis 侧由 `scheduled_at:{request_id}` 独立键族投影**（与错误重试 `retry_at` 可区分），park 写 / 到期与终态清 | 观测 | 同上 |
| 待取回响应（客户端重连） | **Redis**（`pending/pending.go`） | 响应缓存，非执行队列 | `pending_response` + ZSET index |
| 生命周期动作事件 | **Redis** LIST（`internal/liveactions`） | 观测（admin SSE） | `llmgw:live:actions` |
| 供应商并限/限流（concurrency/RPM/TPM） | **Redis 权威**（Governor `redis_backend.go`，可回退本地） | 跨实例执行准入的一部分 | GovernorSpec 键 |
| 执行恢复（重启续跑） | **PG**（`durable/`，默认关） | 唯一合法恢复通道 | `durable_llm_tasks` |

若未来需要"执行队列本体入 Redis"（跨实例排队/重启续跑），那是推翻冻结契约的
架构级变更，必须走新 ADR + durable lane 评审，不属于 v6-W1.5/W1.6 范围。

> **2026-08-27 用户决策修订**：队列原语升级为**双后端**（有 Redis 用 Redis、
> 无 Redis 回退本机内存），支撑多服务器分布式接收——设计定稿见
> [`10-dual-backend-queue.md`](10-dual-backend-queue.md)（V6-W1.7）。连接亲和
> 约束不变：执行对象（goroutine/连接/ResultCh 不可序列化）仍在本实例，Redis
> 后端管跨实例**准入/容量/定时可见**，跨实例接管执行仍属 durable lane。本表
> 中"Tier-0/1/2、重试堆、到期堆 在进程内存"指**执行对象**；其**准入与容量
> 口径**自 W1.7 起按后端选择（local | redis）。


---

## 3. 任务分解（Waves）

### 3.1 V6-W1.5 · 本轮实施（LOCAL_VERIFIED 目标）

| # | 任务 | 文件 | 验收 |
|---|---|---|---|
| T1 | 执行器 CPU 自适应 | `domains/dispatch/config.go` | `AdaptiveWorkerCount()` 单测；DefaultConfig/LoadConfig 默认值切换；hotconfig 覆盖生效 |
| T2 | 定时请求闭环 | `queued_request.go`、`pipeline.go`、`errors.go`、`executors/executor_dispatch.go`、`executors/executor.go`、`handler.go` | DueAt 未来 → park + journey `retry_scheduled` + 通知；到期 → 执行；`X-Gw-Due-At`（RFC3339/unix 秒）解析；超过 24h 拒绝 `ErrScheduleTooFar`；停机完成 park 请求 |
| T3 | DispatchNotice think 通知 | `queued_request.go`(notice.go)、`failover.go`、`dispatcher.go`、`pipeline.go`、`executor_dispatch.go` | 5 类事件单测：retry/node_switch/model_switch/queued/scheduled；executor 侧桥接 `params.OnNodeJump`（handler `: thinking:` 注释）；OnNodeSwitchSummary 保留兼容 |
| T4 | 回队打标 LastFailover | `queued_request.go`、`failover.go`、`dispatcher.go` | 每个回队站点写标；通知与索引携带 |
| T5 | DimensionIndex 分维队列 | `domains/dispatch/dimension_index.go`、`pipeline.go`、`metrics.go`、`cmd/gateway/main_dispatch.go` | 登记/完成/TTL/容量淘汰单测；`GET /api/admin/dispatch/dimensions` 端点 |
| T6 | 文档 | 本文件 + `docs/04-implementation/changes/2026-08-27-v6-dispatch-executor-loop.md` + README 索引 | 落盘 |
| T7 | scheduled_at Redis 镜像键族（2026-08-27 修正） | `queue_mirror.go`、`pipeline.go`（park/due/complete 接线） | 键写入/清理/RebuildMetadata 分类单测；管线级 park→到期→清理闭环（miniredis）；Redis 侧待处理集合 = `retry_at:*`（错误/容量重试）∪ `scheduled_at:*`（定时），可区分 |

### 3.2 后续波次（不在本轮）

| # | 任务 | 归属 | 说明 |
|---|---|---|---|
| F1 | 统一 RetryBudget（五层 attempts 收敛） | V6-W1-5（既有） | stream retry/survival/dispatch/goal 预算统一 |
| F8 | IR 请求类型 + 执行轨迹队列 + 调度解耦（100 限额口径） | **V6-W1.6，设计见 [`09-ir-class-journal-decoupling.md`](09-ir-class-journal-decoupling.md)** | IR Class/DueAt、AttemptJournal+ActionCounts（复用 DimensionIndex 存储）、planner 纯决策层 |
| F2 | Candidate 联合 lease（TPM+FP slot+并发） | V6-W1-8（既有） | `TryAcquire/Release` 镜像挂 `domains/nodestatecache/resources.go`（已实现未接线） |
| F3 | URSM v2 单一 owner + routing 三套收敛 | V6-W1-3/4（既有） | 本轮不动路由内部 |
| F4 | Bandit 生产接线（main.go 注释掉的 wiring） | V6-W1 增补 | 修 NET-012 build break 后恢复 |
| F5 | 分维索引 Redis 投影（跨实例视图） | V6-W2 | 本地索引先行，跨实例聚合走 Redis mirror 模式 |
| F6 | 定时请求的 API 化（body 字段/管理端点） | V6-W2 | 本轮仅 header 触发；若需要 body `schedule.at` 再扩 handler 解析 |
| F7 | 队列位置通知（排队第 N 位） | V6-W2 | 需要 Tier-0 有序位置计算，成本/收益再评 |

---

## 4. 与冻结契约的相容性核查

| 契约 | 影响 |
|---|---|
| `lifecycle_states_v1.json` | ✅ 兼容：定时 park 复用 `retry_scheduled(retry_at)` pending 语义，无新状态 |
| `restart_semantics_v1_valid.json` | ✅ 不改：`ordinary_dispatch → dropped` 不变；定时 park 堆同样是进程内（停机由 Close handler 完成 ErrShutdown） |
| `error_classification_v1.json` 11 frozen kinds | ✅ 不新增 dispatch 子集错误 kind；`ErrScheduleTooFar` 是网关侧参数错误（executor 映射 KindClientBug，HTTP 400 族），不进 frozen 集 |
| v4 五身份/四通道 | ✅ 不动 |
| ADR-Disp-003 首字节边界 | ✅ 通知与切换均 pre-firstbyte |
| ADR-Disp-006 ExhaustedError | ✅ 终态路径不变 |

---

## 5. 风险与回滚

| 风险 | 缓解 | 回滚 |
|---|---|---|
| 通知回调阻塞调度 goroutine | 回调约束为非阻塞写（SerializedStreamWriter）；dispatch 侧 recover 兜底 | executor 不设 `qr.OnDispatchNotice` 即回到现状（死回调等价） |
| 定时请求占用 Tier-0/Tier-1 容量 | park 即释放 Tier-0 槽位；24h 上限拒绝 | 头不传即零行为变化 |
| CPU 自适应在高核机器放大 goroutine 数 | 钳制 [2,32]；hotconfig 可钉死 | `llmgw_dispatch_dispatcher_workers=8` |
| 分维索引内存 | 每维度有界环形 + TTL；上限 3×维度数×128 条目量级 | `llmgw_dispatch_dimension_capacity=0` 关闭登记 |
| worker 数变化影响时序测试 | 默认值单测 + 全包 `go test -race` 回归 | config 单点回滚 |

---

## 6. 完成标准（本轮 = `LOCAL_VERIFIED`）

- [x] `go build ./...` 通过
- [x] `go test -race ./domains/dispatch/... ./domains/streaming/... ./cmd/gateway/...`（相关包）通过
- [x] 新增单测：AdaptiveWorkerCount / 定时请求（park→due→执行）/ 5 类 DispatchNotice / LastFailover 打标 / DimensionIndex 登记·完成·TTL·容量淘汰
- [x] 冻结契约 fixture 回归（`test/events/fixtures/`）不受影响
- [ ] （后续）staging 负载对比：P99 排队时延、通知帧对客户端兼容性抽测 → `REAL_DEPENDENCY_VERIFIED`
