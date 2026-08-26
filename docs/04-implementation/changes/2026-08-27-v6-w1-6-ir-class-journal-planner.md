# v6-W1.6 · IR 请求类型 + 执行轨迹队列 + 调度解耦（IR Class / AttemptJournal / Planner）

**日期**：2026-08-27
**分支**：main
**证据等级**：`LOCAL_VERIFIED`（`go build ./...` + `go test -race ./domains/dispatch/ ./domains/transformation/ ./internal/ir/` 全绿 + v4 冻结契约 fixture 回归通过；staging 负载对比未做）
**Commit / Migration / Flag**：
- commit: `c386aa427`（T1~T3）、`1350dbdbc`（T4）、`def2e9373`（T5+T6）
- migration: N/A
- flag: N/A（纯内部行为等价重构 + 加法字段；无新开关）

## 场景与需求范围

用户需求（2026-08-27）：① 在 IR 上增加请求类型（即时/定时）；② 增加按请求的执行轨迹队列（尝试过的模型+节点、下一步操作类型、执行次数分类计数、完成/失败终态）；③ 复用既有分维队列作可查询存储，不新建存储子系统；④ 队列管理与执行操作解耦（决策纯函数化）；⑤ 对齐单请求 100 次重试/切换限额。设计定稿与逐点代码锚点见 [`docs/架构优化v6/09-ir-class-journal-decoupling.md`](../../架构优化v6/09-ir-class-journal-decoupling.md)（R8~R12 + §3 异常矩阵 E1~E16 + §3.3 时序不变量 1~5）。

## 实现（T1 ~ T7）

| 任务 | 变更 |
|---|---|
| T1 IR 请求类型 | `internal/ir/class.go`：`RequestClass`（`immediate`/`scheduled`）+ `ClassOf(dueAt)`（零/过去 → immediate）；`InternalRequest` 加 `Class`/`DueAt` 内部字段，serializer 显式构造输出零泄漏（golden 测试 `TestSerializersDoNotLeakRequestClass` 钉住） |
| T2 TransportContext→IR 盖章 | `domain.TransportContext` 加 `RequestClass`/`DueAt`；executor 4 个 SetContext 站点从 `params.DispatchDueAt` 填入；`ir_converter.go` 3 个 Parse 方法成功后盖章 |
| T3 AttemptJournal | `domains/dispatch/journal.go`（新）：`JournalEntry`（Seq/At/Model/CredentialID/Action/ErrorKind/HTTPStatus/Attempt/Counts）+ `ActionCounts`（Retries/NodeSwitches/ModelSwitches/CapacityWaits/ScheduledWaits）+ `recordDecision` 统一入口（Seq 单调、Counts 折叠、容量 128 环形、LastFailover 作为延续动作的投影视图）；6 个决策站点（move×2/换模型/容量等待/定时停靠/complete 终态）全部经 recordDecision；complete 的终条写在完成 CAS 内（恰好一次，invariant 3）；`NextActionKind` 扩充 3 个终态词（completed/failed/canceled） |
| T4 分维队列复用扩展 | **（2026-08-27 范围修正）执行轨迹附属请求自身，禁止复制进分维索引**：`DimensionEntry` 仅加 `Class`（Track/MarkNode 从 `qr.requestClass()` 盖章），条目不带 Journal（`TestDimensionEntriesDoNotCarryJournal` 钉死）；`UpdateWait` 刷新 `ExpiresAt`（requeue 后未完成的条目也能 TTL 淘汰）；`Complete` 在请求 journal 上兜底补终条（幂等）；轨迹读面 = `QueuedRequest.JournalSnapshot()`（detached 副本，终条后不可变）；新增 `EntriesByRequest()` + `GET /api/admin/dispatch/request-dimensions/{request_id}`（仅查三维度归属，不返回轨迹；窗口外 404）；`/dimensions` 条目自动携带 Class；dispatch↔ir 常量镜像由 `TestRequestClassMirrorsIRConstants` 钉死（E14） |
| T5 planner 纯决策层 | `domains/dispatch/planner.go`（新）：`Decision` + `PlanAfterFailure`/`PlanSwitchCred`/`PlanNoRoute`/`PlanModelChange`/`PlanCapacityWait`/`AttemptBudgetLeft` 纯函数（无 I/O/锁/goroutine/状态变更）；`move()` 阶梯、`tryModelChangeOutcome`、`scheduleCapacityRetry` 改为消费 Decision；`providerSwitchAllowed`/`exhaustedTerminal`/`retryBudget` 提为自由纯函数；候选获取（routeFunc/alt-fetch）留执行侧 |
| T6 100 限额收敛 | 三个延续步骤的 cap 检查统一 `AttemptBudgetLeft`（AttemptCount=100 唯一权威；Counts 为分类视图允许 ≤1 偏差）；`attemptCapOutcome` 盖 `ErrorKind=attempt_cap` → journal 终条 `failed(attempt_cap)`；组合穷尽（ExhaustedError）优先级不变（R2.4） |
| T7 文档 | 本 changes 文档 |

## 关键正确性决策

1. **journal 权威在 QueuedRequest（单所有者）且附属请求生灭（范围修正后口径）**：轨迹不复制进任何进程级存储，读面 = `JournalSnapshot()`；`DimensionIndex.Complete` 的终条兜底写在**请求的** journal 上（幂等，pipeline 的 complete CAS 内已先写则跳过）—— 轨迹恒以终态收尾（invariant 4 修正口径，见文末「范围修正」）。
2. **dispatch 不 import internal/ir**：Class 用字符串镜像常量（`RequestClassImmediate/Scheduled`），一致性单测钉死（E14，编译期不可达用测试补）。
3. **LastFailover 保留为 journal 尾部投影视面**（recordDecision 内同步刷新，终态动作不覆盖）—— 通知路径与轨迹不漂移，08 号测试零改动。
4. **等价重构硬门禁**：T5 改造后 `go test -race ./domains/dispatch/` 全量（含 08 号 13 测试 + W1.6 新增）**零测试修改通过**；错误码/事件类型/通知文案一字未动。
5. **不新增 Redis 同步写**（E15/E16）：Journal/Class 仅本地观测投影；Redis 侧仍只有 `retry_at:*`/`scheduled_at:*` 镜像键族（08 号 §2.1 存储拓扑）。

## 回归证据

- `go build ./...`；`go test -race ./domains/dispatch/ ./domains/transformation/ ./internal/ir/` 全绿（dispatch 含新测试 24 个：ir class 边界/序列化 golden、journal Seq/Counts/容量/投影、链路恒等式 C 链/完成链/定时首条、镜像常量、分维 Class/Journal/404/TTL、planner 表驱动 + 纯度 + attempt_cap 端到端）。
- §3.3 五条不变量单测映射（范围修正后口径）：①②③ → `TestJournalChainFailoverLadder` + `TestAttemptCapJournalTerminal`；④ → `TestDimensionEntriesDoNotCarryJournal`（请求轨迹终条收尾 + 条目永不携带轨迹）；⑤ → `TestPlannerPurity`。
- v4 冻结 fixture（`./test/events/contract/`：lifecycle_states_v1 / restart_semantics_v1 / error_classification_v1）回归通过（未加事件类型、未改状态机）。
- 预存失败与本轮无关（HEAD 已复现）：executors 两个 router 测试构建坏、streaming strip_minimax_leak、requestjourney 需真 PG、cmd/gateway main_livestream_test。

## 负向测试

- serializer 不泄漏 Class/DueAt（golden 对比四个协议输出）。
- journal 容量 128 截断（丢最旧保最新，正常 100 限额路径不截断）。
- `EntriesByRequest` 未知请求 → 404；TTL 过期后 → 404（端点只查归属，不返回轨迹）。
- 分维条目在任何站点（Track/MarkNode/UpdateWait/Complete）都不携带轨迹（范围不变量）。
- attempt cap：99 次后允许延续、100 次后拒绝（表驱动 + 端到端）。
- credential-fatal 错误不消耗同凭据重试预算（直接进换节点阶梯）。

## 回滚动作

- T1~T4 纯加法：revert 对应 commit 即回 08 号形态（无 schema/契约变更）。
- T5/T6 与 T3/T4 分 commit：revert `def2e9373` 单独回到"有 journal 无 planner"形态，dispatch 测试仍全绿。

## 遗留（后续波次）

- F5 分维索引/Journal Redis 跨实例投影（本轮明确不做，E15/E16 边界）。
- F6 定时请求 body 字段与管理 API；F8 due 堆容量上界（E13）。
- E12 外层 goal-retry 与 dispatch attempts 双层叠加（v6-W1-5 统一，本轮仅文档标注）。
- W1.7 双后端队列（[`10-dual-backend-queue.md`](../../架构优化v6/10-dual-backend-queue.md)）随本轮串行实施。

---

## 范围修正（2026-08-27 用户纠偏，同日实施）

**用户指正**：N-2 的执行轨迹记录"一个请求所经历的整个路径"，**附属在具体请求中，不是整个项目**。

**修正内容**（commit 见本轮 `fix(dispatch): journal scope — trace attached to the request, not the process index`）：

1. **移除项目级轨迹副本**：`DimensionEntry.Journal` 字段、`journalTail`/`dimensionJournalTail`（UpdateWait 尾部 16 条 / Complete 全量复制）、`JournalByRequest` 全局查询、`GET /api/admin/dispatch/journal/{request_id}` 端点——全部删除。
2. **分维索引回归纯成员归属**：条目仅保留 Class（新增）+ 既有成员元数据；Complete 的终条回填写在**请求对象** `qr.AttemptJournal` 上（请求附属），不进条目。
3. **轨迹的请求附属出口**：新增 `QueuedRequest.JournalSnapshot()`（detached 全量副本，终条后 ring 不可变）；事后路径查询走**该请求自己的** requestjourney 投影（既有、按请求 ID 存取），实时路径客户端已由 think 通知同步看到。
4. **范围不变量测试**：`TestDimensionEntriesDoNotCarryJournal` 钉死"分维条目永不携带 Journal"；`/api/admin/dispatch/request-dimensions/{id}`（`EntriesByRequest`）只查归属不返回轨迹。
5. 09 号设计文档 N-2/N-3/R9/R10/E9/不变量④/T4/§6 同步修订。

**回归**：`go test -race ./domains/dispatch/ -run 'TestDimension|TestJournal|TestPlanner|TestAttemptCap'` 全绿；不变量④以新口径（范围不变量）覆盖。
