# v6-W1.5 · 调度执行器闭环（Dispatch Executor Loop）

**日期**：2026-08-27
**分支**：main（工作树）
**证据等级**：`LOCAL_VERIFIED`（`go build` + `go test -race` 相关包全绿；staging 负载对比未做）
**Commit / Migration / Flag**：
- commit: 本轮工作树提交
- migration: N/A
- flag: `LLM_GATEWAY_ENABLE_SCHEDULED_DISPATCH`（默认 on，关闭则忽略 `X-Gw-Due-At`）；hotconfig `llmgw_dispatch_dispatcher_workers` / `llmgw_dispatch_failover_workers` / `llmgw_dispatch_dimension_ttl_seconds` / `llmgw_dispatch_dimension_capacity`

## 场景与需求范围

用户需求（2026-08-27）：大量请求进入网关时按供应商并发/限流稳定平衡输出；待处理队列 + 执行器取件（即时直接执行、定时未到期回队、到期执行）；失败回队打标（错误类型/上轮模型节点/下一步动作）；每次回队向客户端发 think 类通知且不影响会话；按凭据/模型/供应商的分维队列（完成后不移除，靠 TTL/挤出）。设计定稿见 [`docs/架构优化v6/08-dispatch-executor-loop.md`](../../架构优化v6/08-dispatch-executor-loop.md)。

## 实现（G-Ⅰ ~ G-Ⅵ）

| 缺口 | 变更 |
|---|---|
| G-Ⅰ 执行器固定 8 | `dispatch/config.go`：`AdaptiveWorkerCount()` = NumCPU-1 钳制 [2,32]；Dispatcher/Failover 池默认取该值，hotconfig 可钉死 |
| G-Ⅱ 无定时请求 | `QueuedRequest.DueAt` + pipeline 自持 `dueScheduler`（Start 建、Stop 关并完成 park 请求）；Tier-0 drainer 未到期（提前量 >100ms）回投到期堆，到期重新 admissions；`X-Gw-Due-At`（RFC3339/unix s/unix ms）→ `ExecParams.DispatchDueAt`；超 24h 拒绝 `ErrScheduleTooFar`（executor 映射 KindClientBug） |
| G-Ⅲ 回队不通知客户端 | 新 `DispatchNotice`（retry/node_switch/model_switch/queued/scheduled 五类）经 `OnDispatchNotice` → executor 桥接 `params.OnNodeJump` → `: thinking:` SSE 注释（三协议 handler 均已接 OnNodeJump；非流式 no-op）；`OnNodeSwitchSummary` 死回调保留兼容 |
| G-Ⅳ 无分维队列 | 新 `DimensionIndex`：model/credential/provider 三维成员环形索引，完成仅改 State 不移除，TTL（默认 900s）+ 每环容量（默认 128）淘汰；`GET /api/admin/dispatch/dimensions?kind=&id=&limit=` 查询 |
| G-Ⅴ 回队打标不显式 | `QueuedRequest.LastFailover`（ErrorKind/HTTPStatus/Model/CredentialID/Vendor/NextAction/Attempt），在 retry/switch/model_change/capacity_wait/scheduled_wait 全部回队站点写入，随通知与分维索引携带 |
| G-Ⅵ 换模型/容量等待无信号 | 并入 G-Ⅲ（model_switch / queued 通知） |

新增指标：`dispatch_notice_total{kind}`、`dispatch_scheduled_parked_total`、`dispatch_scheduled_due_total`、`dispatch_dimension_tracked_total`、`dispatch_dimension_evicted_total`、`dispatch_overflow_total{reason="total_queue_full_on_due"}`。

## 关键正确性决策

1. **通知时序 vs 单所有者**：Tier-2 handoff（`cf.queue <- qr`）后 mover 不得再读写 qr 可变字段 —— switch 通知在 handoff 前 `prepareNotice`（序号盖章），handoff 成功后仅 `deliverNotice`（只读回调字段）。定时 park 的全部元数据写入先于 `Schedule()`（Schedule 即向 picker 移交）；DueAt 剩余 <100ms 直接执行不 park（`minScheduleLead`）。
2. **Tier-0 槽位语义不变**：park 即释放槽位，到期重新 `tryEnqueue`（满则 Overflow 终态），总队列仍是有界等待室。
3. **不破坏冻结契约**：定时 park 复用 `retry_scheduled(retry_at)` pending 语义；restart 语义 `ordinary_dispatch → dropped` 不变；`ErrScheduleTooFar` 不进 11 frozen error_kinds。
4. **上游并发不经 CPU 执行器**：转发仍 goroutine-per-attempt + per-cred Governor（concurrency/RPM/TPM），CPU 数只决定调度决策池大小 —— 供应商并发额度与 CPU 解耦。

## 回归证据

- `go test -race -count=2 ./domains/dispatch/`（含新增 `dispatch_loop_test.go`：AdaptiveWorkerCount 边界、定时 park→due→执行、far-future 拒绝、停机完成 park、5 类通知、LastFailover 打标、DimensionIndex 生命周期/TTL/容量/禁用/端到端）全绿。
- `go test ./domains/streaming/`（`dispatch_schedule_test.go`：X-Gw-Due-At 8 case 表测试）、`./domains/streaming/executors/`、全仓 `go build ./...`。
- v4 冻结 fixture（lifecycle_states_v1 / restart_semantics_v1 / error_classification_v1）不受影响（未新增 dispatch Observation 类型，未改状态机）。

## 负向测试

- DueAt 超 24h → Submit 直接 `ErrScheduleTooFar`（不进队列）。
- 关闭 `LLM_GATEWAY_ENABLE_SCHEDULED_DISPATCH=0` → 头被忽略，行为与现状完全一致。
- `llmgw_dispatch_dimension_capacity=-1` → 分维索引整体 no-op。
- 停机时 park 中请求 → `ErrShutdown` 完成，Submit 不悬挂。

## 回滚动作

- 通知：executor 侧一行不设 `qr.OnDispatchNotice` 即回到现状（等价死回调）。
- 定时请求：不传 `X-Gw-Due-At` 或 env 置 0，零行为变化。
- 执行器数：hotconfig `llmgw_dispatch_dispatcher_workers=8` 钉回。
- 整体：revert 本轮 commit（无 schema 变更、无新依赖）。

## 遗留（后续波次，见 08 号文档 §3.2）

- F1 统一 RetryBudget；F2 Candidate 联合 lease；F4 Bandit 接线；F5 分维索引 Redis 跨实例投影；F6 定时请求 body 字段/管理 API；F7 队列位置通知。
