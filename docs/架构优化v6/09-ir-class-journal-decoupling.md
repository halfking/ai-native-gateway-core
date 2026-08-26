# 09 · IR 请求类型 + 执行轨迹队列 + 调度解耦 — 设计定稿（V6-W1.6）

> **版本**：v6.2（2026-08-27 起草；基于 [`08-dispatch-executor-loop.md`](08-dispatch-executor-loop.md) 已实施基线（G-Ⅰ~G-Ⅵ，LOCAL_VERIFIED））
> **读者**：架构组 / 后端 Owner / 执行本方案的实现者
> **范围**：① IR 上增加请求类型（即时/定时）；② 按请求的执行轨迹队列（尝试过的模型+节点、下一步操作类型、各类执行计数、终态）；③ 复用既有分维队列作可查询存储，**不新建存储子系统**；④ 队列管理与执行操作解耦（决策纯函数化）；⑤ 对齐单请求 100 次重试/切换限额。
> **证据等级**：`LOCAL_VERIFIED`（2026-08-27 实现完成：commit c386aa427 / 1350dbdbc / def2e9373；`go build ./...` + dispatch/transformation/ir `-race` 全绿 + 冻结契约 fixture 回归通过；实现记录见 [`docs/04-implementation/changes/2026-08-27-v6-w1-6-ir-class-journal-planner.md`](../04-implementation/changes/2026-08-27-v6-w1-6-ir-class-journal-planner.md)）。
> **⚠ 范围修正（2026-08-27 用户纠偏，已实施）**：N-2 的执行轨迹**附属具体请求**（随请求生灭，经 `JournalSnapshot()`/该请求自己的 journey 投影流出），**不是项目级数据**——初版把 Journal 快照复制进分维索引并暴露全局 `/dispatch/journal/{id}` 端点属越界，已移除；分维条目回归纯成员归属（仅增 `Class`），全局端点改为 `/dispatch/request-dimensions/{id}`（只查归属）。详见 §R9/R10 与 changes 修正记录。

---

## 0. TL;DR

本轮在 08 号基线上做四件事，全部是**加法或等价重构**，不改既有执行语义：

| # | 需求（用户原话要点） | 方案一句话 | 新建子系统？ |
|---|---|---|---|
| N-1 | "在 ir 上加上请求的类型：即时，定时" | `ir.InternalRequest` 增加 `Class`/`DueAt` 网关内部字段；经 `domain.TransportContext`（executor 每 attempt 设置）→ `TransportIRConverter` Parse 后盖章；序列化器显式输出字段，**不上游泄漏** | 否 |
| N-2 | "加上一个队列，存放所有的节点、尝试过的模型+节点、下一步操作类型、执行次数（重试/切节点/切模型）、完成/失败" | `QueuedRequest.AttemptJournal`（有界环形，容量 128 = 100 限额 + 余量）+ `ActionCounts` 累计计数；**轨迹附属请求自身**（单所有者写入，经 `JournalSnapshot()` 随请求结果流出，持久走该请求自己的 journey/日志记录）；6 个回队/终态站点统一经 `recordDecision` 写入 | 否——且**禁止**进任何项目级存储（范围修正） |
| N-3 | "复用原来的请求分维队列，不要全部新建" | 分维条目（`DimensionEntry`）**仅增加 `Class` 字段**（成员归属属性）；**不携带 Journal**——分维索引保持纯成员视图（哪个请求在/曾在哪个模型/凭据/供应商上，State/Outcome/LastAction） | **否，扩展 08 号实现** |
| N-4 | "队列管理与执行操作解耦，逻辑变简单" | 抽 `dispatch/planner.go` 纯决策层（读标记 → 出 `Decision`），`Pipeline` 只做队列管道（enqueue/park/complete）；行为等价由既有 dispatch 全量测试守护 | 否（等价重构） |
| N-5 | "同一请求重试/切换次数限额 100" | 既有 `maxAttempts=100`（AttemptCount）确认为唯一限额口径；planner 每个延续步骤检查，触顶走终态并写 `failed(attempt_cap)` 轨迹 | 既有，仅对齐口径 |

---

## 1. 需求补全（Refined Requirements）

### R8 · IR 请求类型（即时/定时）

- **类型**：`ir.RequestClass`：`immediate` | `scheduled`；`InternalRequest` 新增：
  ```go
  Class RequestClass // 网关内部元数据；序列化器不输出（已核对 4 个 serializer 均为显式字段构造，无整体 json.Marshal）
  DueAt time.Time    // scheduled 时的到期时刻；immediate 为零值
  func ClassOf(dueAt time.Time) RequestClass // 零/过去 → immediate
  ```
- **注入链**（唯一缝）：`domain.TransportContext`（`domain/transport.go:9`）新增 `RequestClass string` 与 `DueAt time.Time` → executor 各协议 `SetContext` 站点（`executor_chat.go:1491`、`executor_anthropic.go:432`、gemini/responses 对应站点）从 `params.DispatchDueAt` 填入 → `TransportIRConverter.ParseOpenAI/ParseAnthropic/ParseResponses`（`ir_converter.go:357/372/390`）在 Parse 成功后把 contextSnapshot 的 Class/DueAt 盖到返回的 `*ir.InternalRequest`。
- **为什么走 TransportContext**：IR 由 converter 每 attempt 从 body 解析生成，header 层信息不在 body；TransportContext 是 executor → converter 的既有每请求元数据通道，加两个字段零新链路。
- **消费方**：① 分维条目 `Class` 字段（admin 可按类型过滤"当前哪些定时请求在等"）；② 轨迹队列首条记录（`admitted` 类可选，见 R9）；③ 审计（requestjourney 不动——冻结契约不加事件类型）。
- **显式不做**：不给 body schema 加 `schedule.at` 字段（F6 后续）；不在上游请求体中携带 Class/DueAt。

### R9 · 执行轨迹队列（AttemptJournal）——附属请求（范围修正后定稿）

- **范围原则（用户 2026-08-27 纠偏）**：轨迹回答的是"**这个请求**经历了什么路径"，它是**请求自身的附属状态**：权威副本在 `QueuedRequest`（单所有者写入，随请求生灭），**不复制进任何进程级/项目级结构**（分维索引、全局 map、Redis 皆不存轨迹）。轨迹离开请求只有两条路：
  1. `JournalSnapshot()`——请求结果的持有者（Submit 返回后的 executor adapter、测试）按需取 detached 副本；终条之后 ring 不可变，快照恒有效；
  2. 该请求**自己的**持久投影——requestjourney 既有 17 事件按请求存取（冻结契约，不加事件类型）与请求日志；不建全局轨迹存储。

- **权威位置**：`QueuedRequest`（单所有者不变量内写入），**不是**共享存储——轨迹是请求执行史的一部分。
- **结构**：
  ```go
  type JournalEntry struct {
      Seq          int           // 全请求单调递增
      At           time.Time
      Model        string        // 本轮执行的模型
      CredentialID int           // 本轮执行的节点（凭据）
      ProviderID   int
      Vendor       string
      Action       NextActionKind // 决策出的下一步（含终态）
      ErrorKind    string
      HTTPStatus   int
      Attempt      int           // 事件后的累计 AttemptCount
      Counts       ActionCounts  // 事件后的累计分类计数
  }
  type ActionCounts struct {
      Retries, NodeSwitches, ModelSwitches,
      CapacityWaits, ScheduledWaits int
  }
  ```
- **NextActionKind 词汇表扩充**（在 08 号 5 类基础上加 3 类终态）：
  `retry_same_cred | switch_cred | switch_model | capacity_wait | scheduled_wait | completed | failed | canceled`
- **写入点 = 既有 6 个决策站点**（与 08 号 LastFailover/通知完全同点，无新增路径）：
  1. `move()` 同凭据重试 → `retry_same_cred`（Counts.Retries++）
  2. `move()` 换凭据 → `switch_cred`（NodeSwitches++）
  3. `tryModelChangeOutcome()` 换模型 → `switch_model`（ModelSwitches++）
  4. `scheduleCapacityRetry()` 容量等待 → `capacity_wait`（CapacityWaits++）
  5. `parkScheduledRequest()` 定时停靠 → `scheduled_wait`（ScheduledWaits++）
  6. `complete()` 终态 → `completed`/`failed`/`canceled`（按 out.Err 分类）
- **统一入口** `recordDecision(qr, entry)`：递增 Seq → 更新 Counts → append（超容量 128 丢最旧）→ 同步更新 `qr.LastFailover`（LastFailover 保留为"最后一条轨迹的投影视图"，通知继续用它，避免两处记账漂移）。
- **边界（与 100 限额对齐）**：`AttemptCount ≤ maxAttempts=100`；journal 容量 128 ≥ 100+首条+终条，正常运行**不截断**；容量仅防御病态路径（例如旧版本放大的请求）。

### R10 · 复用分维队列（成员归属视图，不含轨迹）

- `DimensionEntry`（`dimension_index.go`）**仅增加 `Class string`** 字段——Track() 时从 `qr.requestClass()` 填入（QueuedRequest 的 `RequestClass` 冗余字段由 executor 预盖或从 DueAt 推导；**dispatch 不 import internal/ir**，用字符串常量镜像 + 一致性单测钉死，E14）。
- **分维条目不携带 Journal（范围不变量，有专项测试 `TestDimensionEntriesDoNotCarryJournal` 钉住）**：UpdateWait/Complete 只刷新成员元数据（State/Outcome/ErrorKind/RetryAt/LastAction/Attempts/时间戳），轨迹始终只在请求对象上（Complete 内的终条回填写在 `qr.AttemptJournal`，不进条目）。
- **Redis 侧边界（2026-08-27 修正，见 08 号 §2.1 存储拓扑）**：执行队列本体（Tier-0/1/2、重试堆、到期堆）在进程内存，**不在 Redis**（v4 冻结契约：`ordinary_dispatch → dropped`；镜像禁止恢复执行）。Redis 侧待处理集合 = `llmgw:dispatch:mirror:v1:retry_at:*`（错误/容量重试）∪ `scheduled_at:*`（定时停靠，独立键族）∪ 深度/在途镜像。DimensionIndex 本轮**不投影 Redis**（F5 后续）。
- **查询面**：
  - 既有 `GET /api/admin/dispatch/dimensions?kind=&id=&limit=` 条目自动携带 Class；
  - `GET /api/admin/dispatch/request-dimensions/{request_id}`（`EntriesByRequest`）：查某请求的分维**归属**（三个维度的条目）；**不返回轨迹**；
  - 事后路径查询（该请求经历了什么）→ **该请求自己的 requestjourney 投影**（既有，按请求 ID 存取，Redis 24h + PG 持久）；执行中的实时路径 → 客户端已通过 think 通知同步看到。

### R11 · 解耦：队列管理 vs 执行操作

- **新文件 `domains/dispatch/planner.go`（纯函数层）**：
  ```go
  type Decision struct {
      Action    NextActionKind
      NextCred  *CredentialRef // switch_cred 时非空
      NextModel string         // switch_model 时非空
      RetryAt   time.Time      // 等待类动作非空
      Reason    string         // 触发原因（error_kind / no_route / capacity…）
      Terminal  *ForwardOutcome // 终态决策时非空
  }
  // 读侧输入只有 (qr 只读视图, outcome, cfg, route 候选)；禁止 I/O、禁止改队列。
  func PlanAfterFailure(qr, out ForwardOutcome, cfg Config, candidates []CredentialRef) Decision
  func PlanNoRoute(qr, cause error, cfg Config) Decision
  func PlanCapacityWait(qr, cfg Config) Decision
  func AttemptBudgetLeft(qr) int
  ```
- **改造点**：`move()` 与 `dispatcher.go` 的决策分支（同凭据重试？换哪个凭据？换模型？终止？）全部收敛到 planner；`Pipeline` 侧只执行 Decision（enqueue/park/complete/通知）。**行为等价是硬约束**：`go test -race ./domains/dispatch/` 全量（含 08 号 13 个新测试 + 既有测试）零修改通过即视为等价；不允许改任何错误码/事件类型/通知文案。
- **解耦边界图**（目标态）：
  ```text
  ┌─ 状态层（谁）── QueuedRequest: DueAt/Class/Tried*/Counts/Journal/LastFailover
  ├─ 决策层（下一步做什么）── planner.go 纯函数：读状态+outcome → Decision
  ├─ 队列管理层（怎么放）── Pipeline: Tier-0/1/2、due堆、retry堆、registry、dimensionIndex
  └─ 执行层（怎么发）── forwarder(governor) + executor adapters（不变）
  ```

### R12 · 100 次限额（口径对齐）

- **唯一口径**：`AttemptCount`（每次上游 forward +1）≤ `maxAttempts=100`。即：首试 + 重试 + 切节点 + 切模型的总发送次数 ≤ 100。
- planner 在每个**延续型** Decision 前检查（现状已在 move/tryModelChangeOutcome 各步检查，收敛到 planner 后仍保持）；触顶 → `Decision{Action: failed, Reason: "attempt_cap"}` → `terminateOnAttemptCap` 路径 → journal 终条 `failed`。
- 组合穷尽（ExhaustedError）优先于预算触顶——既有 R2.4 优先级不变。
- Counts 各分类之和 + 1（首试）与 AttemptCount 允许出现≤1 的偏差（pace_timeout 不发送即不计数）；文档明确 **AttemptCount 是限额权威，Counts 是分类视图**。

---

## 2. 方案-代码匹配度核查（逐点，2026-08-27 工作树）

| 设计点 | 代码锚点 | 匹配结论 |
|---|---|---|
| IR 加字段不泄漏上游 | `internal/ir/serialize_{openai,anthropic,gemini,responses}.go` 均显式构造输出，无 `json.Marshal(ir)` 整体序列化（grep 零命中） | ✅ 直接加字段 |
| TransportContext 注入缝 | `domain/transport.go:9`；executor 设置点 `executor_chat.go:1491`、`executor_anthropic.go:432`（gemini/responses 同型）；converter Parse 点 `domains/transformation/ir_converter.go:357/372/390` | ✅ 缝存在，加 2 字段 |
| DispatchDueAt 已在 ExecParams | `executors/executor.go`（08 号新增）、`executeViaDispatch` 透传 `qr.DueAt` | ✅ 上游数据源已就绪 |
| journal 写入点已具备 | 08 号 `LastFailover` 6 站点（failover.go move×2、dispatcher.go 模型切换/容量等待、pipeline.go parkScheduled/complete） | ✅ 同点扩写，无新路径 |
| 分维队列可扩展 | `DimensionEntry`（08 号新建）已有 State/Outcome/ErrorKind/LastAction/Attempts；UpdateWait/Complete 同步点在 pipeline 内 | ✅ 加 Class 字段即可；**Journal 不入条目**（范围修正） |
| dispatch 不依赖 ir | dispatch 包 import 表无 internal/ir；ir 也不依赖 dispatch | ✅ 用字符串镜像常量保持双向解耦 |
| 100 限额已存在 | `domains/dispatch/errors.go:18 maxAttempts=100`；`terminateOnAttemptCap` 各延续步检查 | ✅ 只收敛不新造 |
| 单所有者写入安全 | 08 号已建立 prepare/deliver 通知模式与"handoff 前写元数据"规约；journal 写点全在 owner 侧 | ✅ 沿用同一规约 |
| planner 抽取可行性 | move() 决策与管道动作交织但边界清晰（读 qr/out → 选路 → enqueue/complete） | ✅ 可等价抽取；风险在细节搬运，用全量测试守护 |
| complete() 终态分类 | `emitRequestTerminal` 已按 out.Err 区分 success/failure/canceled | ✅ journal 终条直接复用该分类 |

---

## 3. 流程验证（文档级走查）

### 3.1 主流程

**A. 即时请求**：handler（无 X-Gw-Due-At）→ `ClassOf(zero)=immediate` → Submit（Tier-0）→ drainer 直接下模型道（无 park）→ dispatch → cred 道 → forward → 成功：complete → journal 终条 `completed`，分维三环 State=completed、Journal 拉齐 → TTL/容量自然淘汰。✅

**B. 定时请求**：header → `params.DispatchDueAt` → TransportContext(Class=scheduled, DueAt) → IR 盖章（仅元数据）；Submit → drainer 见 DueAt 未来（>minScheduleLead）→ park（journal 首条 `scheduled_wait`，Counts.ScheduledWaits=1，通知"已受理"）→ 到期 picker → 重新 Tier-0 → 正常执行链。分维条目 Class=scheduled、RetryAt=dueAt。✅

**C. 失败重试链**：forward 失败（pre-firstbyte）→ planner `PlanAfterFailure`：非致命 且 Retries 配额有余 且 AttemptCount<100 → `retry_same_cred`（journal：Retries++，通知 retry+retry_at）→ 退避堆 → 到期再发；重试配额尽 → `switch_cred`（候选扣除 TriedCredentials，NodeSwitches++，通知 node_switch）→ 全尽 → `switch_model`（ModelSwitches++，重置 Tried，通知 model_switch）→ 备选尽/开关关 → 终态 `failed`（ExhaustedError 包裹）。每个决策一条 journal，AttemptCount 单调。✅

**D. 容量等待**：所有 cred 道满 → planner `PlanCapacityWait`（不标记 tried）→ 5s×12 → 仍满 → 升级换模型。journal：`capacity_wait`×N + 计数。✅

### 3.2 异常矩阵

| # | 异常 | 行为 | 验证结论 |
|---|---|---|---|
| E1 | 定时 park 中客户端断开 | Submit ctx.Done → completed CAS 置位 → picker 到期 no-op 返回 | 08 号已实现并有测试；journal 侧：complete 未被调用 → 分维条目停在 pending/scheduled_wait 快照，TTL 淘汰（可接受：客户端主动放弃，终态由 requestjourney 的 canceled 事件承载）|
| E2 | 停机时 park/在途 | dueScheduler.Close 完成 ErrShutdown；Pipeline.Stop 排空 | 08 号已有测试；journal 补 `failed(shutdown)` 终条（complete() 统一路径天然覆盖）|
| E3 | 到期重入 Tier-0 满 | Overflow 终态（reason=total_queue_full_on_due）→ complete → journal `failed` | 08 号已实现；终条复用 |
| E4 | 换模型后 Tier-1 满 | OverflowError(model_queue_full) 终态 → journal `failed` | 既有行为；终条复用 |
| E5 | attempt cap 触顶 | planner 拒绝延续 → terminateOnAttemptCap → journal `failed(attempt_cap)`；ExhaustedError 优先级不变 | R12 口径 |
| E6 | 首字节后失败（BytesSent） | 不切换、带错完成（ADR-Disp-003）→ journal `failed`，Counts 不再加 | 既有不变 |
| E7 | 通知回调 panic / 慢 | recover + SerializedStreamWriter detach | 08 号已实现 |
| E8 | journal 写点越权（handoff 后写） | 规约：recordDecision 仅在 owner 侧调用；cred-switch 站点在 handoff 前完成 append（与 08 号 prepare/deliver 同位）| 走查 6 站点全部满足；新增 -race 测试覆盖 |
| E9 | 轨迹被误加进项目级结构（分维条目/全局端点/Redis） | **范围不变量**：分维条目不携带 Journal（`TestDimensionEntriesDoNotCarryJournal` 钉住）；无全局 journal 端点；轨迹仅经 `JournalSnapshot()`/请求自身 journey 流出 | 范围修正 2026-08-27 |
| E10 | IR Class 串改 body / 泄漏上游 | serializer 显式构造；Class/DueAt 为 Go 字段非 Extensions，不进 JSON | 已核对零整体序列化点 |
| E11 | DimensionIndex 禁用（capacity=-1） | journal 仍在 QueuedRequest（权威不丢）；仅查询面退化 | 设计使然 |
| E12 | 外层 goal-retry 与 dispatch attempts 双层叠加 | 既有债务（UpstreamAttemptBudget vs AttemptCount），v6-W1-5 统一，本轮仅文档标注 | 明示不做 |
| E13 | DueAt 巨量并发 park | Tier-0 槽位 park 即释放（08 号）；due heap 无界——沿用 retry heap 同样假设，文档登记（后续可加 due 容量） | 登记为 F8 |
| E14 | dispatch 与 ir 的 Class 常量漂移 | dispatch 侧镜像常量 + 单测断言字符串相等（编译期不可达，用测试钉死）| 任务 T4 验收含此项 |
| E15 | Redis 不可达 / 镜像通道满 | scheduled_at/retry_at 镜像丢失可接受（mirror 是异步旁路，满即丢并计数 `dispatch_overflow_total{queue_mirror_full}`）；权威在内存到期堆/重试堆，执行不受影响；Redis 恢复后新事件重新镜像，丢的旧键靠 TTL 10min 自然消失 | 08 号 §2.1；既有 mirror 契约 |
| E16 | 误把 Redis 镜像当执行队列（读 scheduled_at 去驱动执行）| 契约禁止：镜像仅观测/元数据重建；执行恢复唯一通道是 durable lane（PG，默认关）| 08 号 §2.1 明示；Code review 门禁 |

### 3.3 时序不变量（实现后须全部成立）

1. journal Seq 严格递增且每请求连续（无空洞）；终条 Seq 最大。
2. `AttemptCount = 1 + Σ发送型动作`（retry/switch_cred/switch_model 各对应一次后续发送；capacity_wait/scheduled_wait 不发送不计数）。
3. 任何 `Decision.Action ∈ 终态` 后不再有新 journal 条目（complete 的 CAS 保证）。
4. **范围不变量：分维条目永不携带 Journal**；请求轨迹仅存在于请求对象（`JournalSnapshot()`）与该请求自己的 journey 投影。
5. planner 为纯函数：同输入同输出，无 goroutine/锁/IO（单测可用反复调用断言）。

---

## 4. 任务分解（执行清单）

| # | 任务 | 文件 | 验收 |
|---|---|---|---|
| T1 | IR 请求类型 | `internal/ir/types.go`（+`class.go` 常量与 ClassOf）、4 个 serializer 不动 | 编译零改 serializer；单测：ClassOf 边界；serialize 输出不含 class（golden 对比）|
| T2 | TransportContext→IR 盖章 | `domain/transport.go`、`domains/transformation/ir_converter.go`（3 个 Parse 方法 + scopedConverter 透传）、executor 4 个 SetContext 站点 | 单测：Parse 后 IR.Class==ctx.Class；未设置时为空/immediate |
| T3 | AttemptJournal + ActionCounts + recordDecision | `domains/dispatch/journal.go`（新）、`queued_request.go`（字段）、6 个决策站点改造、`notice.go`（LastFailover 由 journal 投影） | 单测：C 链全走查的 Seq/Counts/Attempt 恒等式；容量 128 截断；终条唯一 |
| T4 | 分维队列复用扩展（范围修正版） | `domains/dispatch/dimension_index.go`（仅 Class 字段 + `EntriesByRequest`）、`pipeline.go` 同步点、`cmd/gateway/main_dispatch.go`（条目携带 Class + `/api/admin/dispatch/request-dimensions/{id}`）、`cmd/gateway/main.go` 路由 | 单测：Class 透传、成员元数据刷新、404/TTL、**`TestDimensionEntriesDoNotCarryJournal` 范围不变量**；dispatch↔ir 常量一致断言；不新增 Redis 同步写（E15/E16） |
| T5 | planner 抽取（等价重构） | `domains/dispatch/planner.go`（新）、`failover.go`/`dispatcher.go` 改为消费 Decision | **`go test -race ./domains/dispatch/` 全量零修改通过**；planner 纯函数单测（表驱动覆盖 C/D/E5 路径）|
| T6 | 100 限额口径 | planner 内集中检查；`failed(attempt_cap)` 终条 | 单测：99 次后允许延续、100 次后拒绝 |
| T7 | 文档 | 本文 §3 走查结论复核 + `docs/04-implementation/changes/2026-MM-DD-v6-w1-6-*.md` | 落盘 |

顺序依赖：T1→T2；T3→T4；T5 依赖 T3（planner 读 journal/counts）；T6 并入 T5。

> **执行顺序（2026-08-27 增补）**：W1.6（本文档）先行，随后执行 W1.7
> （[`10-dual-backend-queue.md`](10-dual-backend-queue.md)，双后端队列）——两者
> 都动 dispatch 核心，串行降低冲突。W1.7 不改变本文任何设计：journal/dimension
> 仍为本地观测投影，planner 决策不感知后端。

## 5. 风险与回滚

| 风险 | 缓解 | 回滚 |
|---|---|---|
| T5 重构改变行为 | 全量 dispatch 测试零修改通过为硬门禁；分 commit（T3 与 T5 分离） | revert T5 commit 即回 08 号形态 |
| journal 内存放大 | 每条 ~200B × ≤128 × 在途请求（Tier-0 1000 + 各道）≈ 最坏 ~30MB 量级；分维快照仅尾部 16 条 | 常量可调；T3 单测含容量断言 |
| IR 字段被未来 serializer 误输出 | golden 测试钉住 | 删字段无下游依赖 |
| Class 双包常量漂移 | 一致性单测 | — |

## 6. 完成标准

- [x] `go build ./...`；`go test -race ./domains/dispatch/ ./domains/transformation/ ./internal/ir/ ./cmd/gateway/`（可编译部分）全绿（cmd/gateway 测试构建失败为预存 main_livestream_test，与本轮无关）
- [x] §3.3 五条不变量各有对应单测（①②③ TestJournalChainFailoverLadder + TestAttemptCapJournalTerminal；④ TestDimensionEntriesDoNotCarryJournal（范围修正后口径）；⑤ TestPlannerPurity）
- [x] v4 冻结契约 fixture 回归通过（./test/events/contract/；不加事件类型、不改状态机）
- [x] changes 文档落盘（证据等级 `LOCAL_VERIFIED`）
