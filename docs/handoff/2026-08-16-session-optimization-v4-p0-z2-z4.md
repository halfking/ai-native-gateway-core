# 2026-08-16 — 会话优化 v4：Session Turns 与审计裁决交接（单一来源）

> 状态：本文件是本主题唯一的交接与裁决来源，取代原 `2026-08-16-session-optimization-v4-quick-fixes.md`。
> 项目：`llm-gateway-go`
> 最后同步：2026-08-17；migration 525/526 的 PostgreSQL 17 隔离库复验待本会话记录。

## 1. 范围与结论

本主题完成两部分工作：

1. `session_turns` 的四个附加查询维度和独立 hot 写表/按月分区搬迁路径；
2. Goal 审计预设与异步 pending 响应契约的冲突裁决。

生产 migration ledger 已占用 523/524。因此原计划的两个 session turns migration 已重命名为 **525/526**，不得再使用 524/525、522/523 的旧编号。

| Migration | 文件 | 作用 |
| --- | --- | --- |
| 525 | `sql/migrations/startup/525_session_turns_six_dim.sql` | 在 `session_turns` 分区父表及叶分区增加四个 nullable `TEXT` 查询维度，并建立 tenant-scoped partial index。 |
| 526 | `sql/migrations/startup/526_session_turns_hot.sql` | 创建 `session_turns_hot`、统一读取 view、共享 advisory lock 和原子 promote 函数。 |

验收脚本：`sql/migrations/test/test_525_526.test.sql`。

## 2. 已实现：Session Turns 六维与 Hot/分区

### 2.1 四个新增维度

在已有 `session_id`、`turn_no` 基础上，525 添加以下 nullable `TEXT` 列：

- `project_id`
- `namespace`
- `parent_request_id`
- `task_type`

写入链路已覆盖：

- `X-Gw-Project-Id` → request meta → telemetry entry → V2 request → turn；
- 已加载的 `Session.Namespace` → request log context → telemetry entry → turn；
- `RequestLogEntry.ParentRequestID` / `TaskType` → turn；
- `sessions.task_type` 异步生成后，由 `SetSessionMetadata` 单调回填 hot 与历史 turn。

`/api/admin/turns` 可以直接按四个维度过滤，不需要 join `sessions`。`TurnWriter` 也补齐了已由 migration 513 建立、但先前实时 INSERT 漏写的 T0–T9 时序字段。

### 2.2 Hot 表与统一读取

526 建立：

- `public.session_turns_hot`：新 turn 的独立 heap 写表；
- `public.session_turns_with_current_month`：显式列 union，隐藏已进入分区的 hot 重复 request；
- `public.promote_session_turns_hot_to_partition(interval, integer)`：以 `DELETE … RETURNING` → `INSERT` CTE 原子搬迁。

关键合同：

- 526 需要 PostgreSQL 15+；`security_invoker=true` 保留底表 RLS 语义。生产目标是 PG17，部署前仍须显式检查 `server_version_num`。
- hot owner policy 与历史 parent policy 都查询 `request_logs_hot` 和 `request_logs`，并按 tenant 限定 session owner。
- writer、aggregator、metadata、repair 与 promote 共用 canonical session advisory lock；writer/promote 也按 tenant/request 共享锁。
- 新 turn 仅写 hot；MAX turn、幂等回读和只读查询经统一 view。tenant 内 `request_id` 跨日期保持唯一，不产生迟到重放的第二个 turn。
- parent 已存在的异常 hot 重复行保留以便人工对账，view 隐藏该 hot 行，promote 发 warning 但不会阻断同批正常行。
- repair 的 turn/body/session snapshot 使用一致的历史 `partition_date`，避免详情 join 依据执行日默认值丢正文。
- partition manager 已注册 526 promote；`ensure_sessions_v2_partitions(date)` 每 24 小时预建当前月和下月。

## 3. 已裁决：Goal 审计与 Pending HTTP 契约

### 3.1 P1-F：`Balanced.UseAudit`

最终值是 **`false`**。

三档模式的冻结语义为：minimal=false、balanced=false、aggressive=true。`balanced` 不自动驱动审计/修正，自动审计仅属于 aggressive。设计文档内“需改为 true”的相反行动项是遗留错误，已以此裁决为准；不应再改 `ModePresets[CostModeBalanced].UseAudit`。

### 3.2 P0-E：HTTP 202 异步 retry header

保留现有：`202 Accepted`、`X-Gw-Pending`、`X-Gw-Pending-Request` 和 `Retry-After`。

不新增 `X-LLM-Gateway-Retry-Scheduled`：它仅是历史建议示例，未成为冻结契约；幂等 replay 的 pending 响应也不表示本请求新调度了一次 retry。`domains/streaming/handler.go` 因此没有为该提案修改。

若将来实施 Goal retry 的异步协议，必须先完成前端/OpenAI SDK 兼容性验证，并重新冻结状态、body、header 与轮询语义，不能默认复用历史示例。

## 4. 验证记录

已完成：

- 隔离 PostgreSQL 15 数据库顺序 apply 525 → 526、运行 SQL 验收、正常 promote、重复 hot/parent 隔离、RLS security-invoker、hot 非空 down fail-closed、清空后的 526 down → 525 down、两次 reapply 均通过。
- fresh schema mirror 的 50 列 parent/children create + attach、`attachment_only` 和非负约束通过。
- `go test ./domains/session/v2/...`、受影响 session/streaming/telemetry/admin/bg/sessionsummary/gateway/validator 包、`go test ./...`、`go vet ./...`、`go build ./...` 与 `git diff --check` 均在合并前通过。
- strict secrets 全仓扫描命中仓库既有基线（57 BLOCK / 1052 WARN），与本改动无关；针对本主题 8 个文件运行的 strict 扫描为 0 findings。

PG17 隔离复验：

- PostgreSQL 17 隔离临时库复验（2026-08-17）：使用合同完整的 pre-525 fixture（46 列 partitioned parent、当月 leaf、sequence、`ensure_sessions_v2_partitions(date)`、request log 双表和既有 RLS policy）在 Docker `postgres:17` server `170011` 上完成。首次顺序 apply 525 → 526 与 SQL 验收通过；重复 apply 525 → 526 后第二次 SQL 验收同样通过，最终 parent/hot 均为 50 列。验收覆盖 schema/index contract、atomic promotion、hot/parent duplicate 隔离与 RLS security-invoker owner/stranger/cross-tenant/super-admin 路径，均以 `525/526: all checks passed` 结束。526 down 对非空 hot 表按预期 fail-closed；清空后执行 526 down → 525 down 成功，恢复 46 列、移除 hot 表和四个维度。临时 `session_turns_rls_test` 角色已由验收脚本清理。

## 5. 回滚与运维约束

严格回滚顺序：

1. 回滚或停止写 `session_turns_hot` 的应用版本。
2. 暂停 session turns promote 调度，并取得同一 promote advisory lock，确认无搬迁事务在途。
3. 反复调用 promote；对 warning 的 hot/parent 重复 request key 逐条对账并人工合并或导出，直至 `session_turns_hot` 为空。
4. 执行 `526_session_turns_hot.down.sql`；hot 非空时必须 fail closed。
5. 部署回仅读写 `session_turns` 的旧应用版本。
6. 备份四个附加维度，确认允许丢失其值后才执行 `525_session_turns_six_dim.down.sql`。

不得先回滚 525，因为 526 的 view、hot 表和函数依赖这四个维度。

上线后监控 hot 行龄、每批 moved rows、hot/parent 重复 request key、promote 失败计数以及下月 partition 是否存在。525 会在分区 parent 创建四个索引；大生产分区应选择低峰并监控 lock wait，必要时另行采用逐子分区 concurrent build + attach。

## 6. 尚待对账与后续

- 历史 migration ledger 中仍有两个 522；生产 checksum ledger 必须在下次部署前独立修复或核对。
- 历史 backfill 可从 `request_context_attrs`、`request_logs_hot`、`request_logs` 按 request ID 回填 project/parent/task，并统计 NULL 比例。
- 早期失败或仅有 provisional session ID 的请求无法可靠恢复 namespace；保持 NULL，不根据 ID 猜测。
- installer embed 的 `01-schema.sql` 整体仍缺 Sessions V2。主 schema、deploy baseline 与 table object 已具备完整 50 列合同，但不得伪造 installer 的不完整结构。
- rollout runbook 的旧编号需在文档维护时按 production ledger 使用 525/526；此前交接中引用的 `docs/会话优化v4/05-rollout-runbook.md` 在当前 checkout 不存在，不能作为可执行引用。
- Gateway→SM durable outbox 仍为 partial：部署态 HMAC、consumer 幂等和 ownership 对账尚未闭环，完成前不得将 capability 标记为 current。

## 7. 相关文件

- `sql/migrations/startup/525_session_turns_six_dim.sql`
- `sql/migrations/startup/526_session_turns_hot.sql`
- `sql/migrations/test/test_525_526.test.sql`
- `sql/migrations/startup/526_session_turns_hot.down.sql`
- `sql/migrations/startup/525_session_turns_six_dim.down.sql`
- `docs/会话优化v4/05-会话分析与模型选择设计.md`
- `docs/会话优化v4/10-实施计划.md`
- `docs/会话优化v4/11-完成情况核实与并发执行方案.md`
