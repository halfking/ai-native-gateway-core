# 方案：4 套 Outbox 收敛到 ≤2 套 —— 经 taskbus 的切表路径

> 日期：2026-11-02 · 状态：PROPOSED（待评审）
> 归属：《会话治理融合优化方案-2026-11》P1-1 · 基线：`internal/taskbus/doc.go` 平迁四步 + ARCHITECTURE.md P-08 债务 + 本文新增的第 1 步落地（`internal/taskbus/asm_event_handler.go`，asm.event.v1 真实消费 handler 已在位，测试全绿）

## 0. 问题

网关并存 4 套 outbox 实现，幂等/重放/签名语义各自为政（ARCHITECTURE §5.1 P-08）。本方案给出收敛终局、去留判定与四步时间线；执行主体是 taskbus（已具备 Store/Registry/Dispatcher/幂等键去重/回收窗口 + asm.event.v1 真实投递 handler）。

## 1. 四套盘点（2026-11-02 源码核实）

| # | 实现 | 位置 | 语义 | 服务的业务 | 幂等键 | 投递执行体 |
|---|---|---|---|---|---|---|
| O1 | ASM 事件 outbox | `internal/outbox/`（outbox_events 表 + Dispatcher + HTTPDeliverer + replay/DLQ/背压熔断） | at-least-once + HMAC 签名 batch + DLQ + 背压熔断（2026-10-28 生产加固） | Gateway→ASM 会话事件（opened/completed/closed…） | event_id（确定性派生） | HTTPDeliverer（**已被 taskbus asm.event.v1 handler 复用** ✓） |
| O2 | V2 镜像回填 outbox | `internal/sessionv2mirror/outbox.go`（session_mirror_outbox，迁移 712） | 同事务登记失败写 + reaper FOR UPDATE SKIP LOCKED 重放 | 会话族影子写失败回填 | request_id | SessionWriterV2（进程内） |
| O3 | 聚合快照 outbox | `domains/session/v2/session_aggregate_outbox*`（session_aggregate_outbox） | 同事务登记 + reaper 指数退避 | sessions 聚合快照更新 | （行级） | SessionAggregator（进程内） |
| O4 | 任务存储自有 outbox | `durable/pending_outbox.go` + `settlement_outbox.go` | 任务执行的两阶段登记 | durable 任务（授权/结算） | 任务键 | durable runner（进程内） |

## 2. 收敛终局：**2 套**

**保留：**
- **T = task_events（taskbus）**：唯一通用任务/事件面——asm.event.v1（承接 O1）、mirror.replay.v1（承接 O2）、aggregate.update.v1（承接 O3）、durable 任务族（承接 O4 的通用部分）。统一：idempotency_key UNIQUE、at-least-once、attempts/backoff/DLQ、CompletedAt 回收窗口（r1041）、背压熔断（从 O1 平移）。
- **D = 同事务登记层（薄）**：各业务事务内的"登记失败→任务"模式保留**同事务 INSERT**（这是 outbox 模式的本质：与业务原子），但登记目标统一为 task_events，登记代码收敛为 taskbus.Enqueuer 一个入口。O2/O3 的 reaper 退役（taskbus Dispatcher 取代）。

**即：4 套实现 + 3 张表 → 1 套实现（taskbus）+ 1 张表（task_events）+ 1 个登记入口；"≤2"以实现套数计 = taskbus 执行核 + 同事务登记薄层。**

## 3. 去留判定依据

- O1：投递执行体（HTTPDeliverer）保留为库；**表与 Dispatcher 退役**（asm.event.v1 handler 已真实化，见 asm_event_handler.go 测试）。
- O2/O3：**执行核都是进程内**，无跨服务投递——本质是"持久化重试队列"，与 taskbus Dispatcher 同构；reaper 双实现是纯债务。
- O4：durable 两阶段与任务语义耦合最深，**最后迁**；其 outbox 仅为"断点续跑"，task_events 的 attempts/next_retry_at 可表达。
- 不收敛的（显式排除）：`outbox_events` 的 **replay 工具链**（`cmd/tools/outbox-dlq-replay`、审计台账）在过渡期保留服务 O1 存量；迁移完成后重定向到 task_events。

## 4. 四步时间线（doc.go 方案展开，含门禁）

对齐方案 §4.4 风险表（事件切表引入丢事件 ⇒ 双发对账一周再下线旧路径）：

| 步 | 内容 | 前置/门禁 | 回滚 |
|---|---|---|---|
| **S1 双写期**（本轮已完成执行体） | a) asm.event.v1 真实 handler 在位（✓）；b) 迁移 928 建 `task_events` 表 + SQL Store（列形态按 doc.go：idempotency_key UNIQUE/status/attempts/next_retry_at/last_error/CompletedAt）；c) O1 Dispatcher 旁挂投影器：dispatch 成功后镜像 Enqueue asm.event.v1（**默认关**，env `TASKBUS_ASM_BRIDGE=1` 灰度） | SQL Store 单测 + realdb 集成（复刻 store_memory 语义：claim/mark/reclaim）；投影器幂等（event_id 即 idempotency_key） | 关 env 即回 O1 单轨 |
| **S2 桥接期** | 新流量（session.closed.v1 等新事件类型）**只写 task_events**；存量类型双写；对账报表：两表按 event_id 比对 7 天 | S1 灰度一周 DLQ 占比无回归；ASM 侧 event_id 幂等已验证（34,183 积压重投演练可作压力证据） | 新类型切回 outbox_events 写入（builder 不变） |
| **S3 切读+迁 O2/O3** | O1 Dispatcher 停新事件消费只消化存量；sessionv2mirror/aggregate 改登记 task_events（mirror.replay.v1 / aggregate.update.v1），reaper 退役 | O2/O3 各自回填语义测试平移到 taskbus Store；每迁移一个域跑一周对账 | reaper 代码保留一版可回切 |
| **S4 停写+归档** | outbox_events 停写归档；O4 durable 任务族迁移评估（独立决策点）；HTTPDeliverer 保留为 asm.event.v1 handler 的执行体（依赖方向消除：handler 直持） | 存量清零 + 归档核对（数量/校验和） | 从归档恢复表（DDL down 保留） |

## 5. 开放决策点

1. task_events 的 DLQ 查询/重投运维面（O1 现有 dlq 工具平移成本 vs 新建 admin 视图）。
2. O4 durable 是否最终并入（建议：若 S3 后 taskbus 无生产事故一季，并入；否则维持 2+1 长期）。
3. 背压熔断（O1 的 bpPausedUntil）平移到 taskbus Dispatcher 的批语义（当前 taskbus 无批级熔断）——S1 SQL Store 时一并设计。
4. 迁移号：928 是否预留（P0-3 ADR 已占 928 为 capture 表族——两 ADR 需协调错峰：本文建议 task_events 用 **930**）。

## 6. 与本仓文档的关系

- ARCHITECTURE §5.1 P-08 行：S4 完成后改"4 套"为"2 套（task_events + 同事务登记薄层）"。
- taskbus/doc.go 的四步注释与本文一致，本文为权威展开。
