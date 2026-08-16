# 会话优化 v4 P0-Z2 + P0-Z4 Handoff

日期：2026-08-16

## 结论

本会话完成 `session_turns` 六维存储与 hot + 月分区模式：

- Migration 526：`525_session_turns_six_dim.sql`
- Migration 526：`526_session_turns_hot.sql`
- SQL 验收：`sql/migrations/test/test_525_526.test.sql`

原计划使用 522/523，但仓库已有两个 522，且 `origin/main` 已正式占用 523/524（context window override NOTIFY 的连续修复）。为避免 production ledger/checksum 冲突，本任务最终使用 525/526。

## 已实现

### P0-Z4 六维

现有维度：`session_id`、`turn_no`。

新增 nullable TEXT 列：

- `project_id`
- `namespace`
- `parent_request_id`
- `task_type`

Migration 526 在父分区表执行 `ALTER TABLE`，并验证所有 leaf partition 均自动传播列；新增 tenant-scoped partial indexes。主 schema、deploy baseline 与 table object 镜像已同步为完整 50 列合同；baseline 的 2026_07/08 子表也已同步并通过 PostgreSQL 15 `ATTACH PARTITION` 验证。

写链路已贯通：

- `X-Gw-Project-Id` -> request meta -> telemetry entry -> V2 request -> turn
- 已加载 `Session.Namespace` -> request log context -> telemetry entry -> turn
- `RequestLogEntry.ParentRequestID` -> turn
- `RequestLogEntry.TaskType` -> turn
- `sessions.task_type` 异步生成后，`SetSessionMetadata` 单调回填 hot 与历史 turns

业务 auto-route 不再被 `IsAutoRequest` 粗粒度跳过；内部 title/summary loopback 仍跳过。

`TurnWriter` 同时补齐 migration 513 已建但此前实时 INSERT 漏写的 T0-T9。

### P0-Z2 hot + 分区

Migration 526 新增：

- `public.session_turns_hot`
- `public.session_turns_with_current_month`
- `public.promote_session_turns_hot_to_partition(interval, integer)`

约束：

- 526 要求 PostgreSQL 15+，因为统一 view 使用 `security_invoker=true` 保持底表 RLS。
- hot owner policy 同时读取 `request_logs_hot` 与历史 `request_logs`。
- promote 使用单条 `DELETE ... RETURNING -> INSERT` data-modifying CTE；正常批次保持 statement 原子性。
- writer、aggregator、metadata、repair 与 promote 共享数据库 canonical session lock；writer/promote 另共享 tenant/request lock。
- 新 turn 只写 hot；MAX turn_no、幂等回读和所有只读查询走统一 view。request_id 在 tenant 内跨日期只允许一条，迟到重放不会生成第二轮。
- 已在 parent 出现的异常 hot 重复行从统一 view 和 promote batch 隔离：保留原数据供人工对账、发 warning，但不再阻塞后续正常冷行搬迁。
- conflict enrichment 同时兼容 hot/历史 parent；aggregate claim 以 parent 为权威，parent 已存在时不会 claim 被 view 隐藏的 hot duplicate。
- repair 的 turn/body/session snapshot 使用一致的历史 `partition_date`，详情 JOIN 不会因执行日默认值丢正文。
- partition manager 注册 Migration 526 promote；现有 `ensure_sessions_v2_partitions(date)` 每 24 小时预建当前月和下月。
- `/api/admin/turns` 支持 `project_id`、`namespace`、`parent_request_id`、`task_type` 直接过滤，不 JOIN `sessions`。

## 验收结果

在一次性 `postgres:15` Docker 数据库中真实执行，不是伪 `psql --dry-run`：

1. 顺序 apply 525 -> 526：通过。
2. `test_525_526.test.sql`：通过。
3. promote 正常搬迁：通过。
4. 人工制造 hot/parent 重复键：重复 hot 行保留且统一 view 只返回一行，后续正常冷行继续 promote，通过。
5. 使用 `NOBYPASSRLS` 非 owner 角色验证 security-invoker view：owner 可见 hot+parent、stranger 不可见、super_admin 可见，通过。
6. hot 非空执行 526 down：按预期拒绝。
7. 清空 hot 后按 526 down -> 525 down：通过；526 down 恢复旧 parent owner policy，并保持 Migration 474 的五值 submit_mode（parent 已有 attachment_only 行时回滚成功）。
8. 重新 apply 两次并重跑 SQL 测试：通过。
9. fresh schema mirror 的 50 列 parent/children create + attach、attachment_only 与非负约束：通过。

Go 验收：

- `go test ./domains/session/v2/...`：通过。
- session mirror、session、streaming、telemetry、admin、bg、sessionsummary、gateway、validator 受影响包：全部通过。
- `git diff --check`：通过。
- 静态检查：统一 view 无 INSERT/UPDATE/DELETE；跨轮次维度查询无 `JOIN public.sessions`。
- `go test ./...`：当前工作树全仓通过。

## 严格回滚清单

1. 先回滚/停止写 `session_turns_hot` 的应用版本。
2. 暂停 partition manager 的 session turns promote 调度。
3. 获取同一 promote advisory lock，确认无搬迁事务在途。
4. 反复调用 promote；对 warning 标记的 hot/parent 重复键逐条比较并人工合并/导出，直至 `public.session_turns_hot` 为 0 行。
5. 执行 `526_session_turns_hot.down.sql`。hot 非空时脚本会 fail closed。
6. 部署回只读/写 `public.session_turns` 的旧应用版本。
7. 备份四维列；确认允许丢失维度值后，执行 `525_session_turns_six_dim.down.sql`。
8. 验证 view/function/hot 表已删除，四维列与相关索引已删除。

不要先回滚 525；526 view、hot 表和函数依赖四维列。

## 待对账项

- Migration ledger：历史两个 522 仍是部署阻断风险；生产 checksum ledger 必须在下一次 deploy 前单独修复/核对。
- 历史 backfill：按 request ID 从 `request_context_attrs`/`request_logs_hot`/`request_logs` 回填 project、parent、task，并统计 NULL 比例。
- namespace 历史数据：早期失败和仅有 provisional session ID 的请求无法可靠恢复，保持 NULL，不从 ID 猜测。
- installer schema：installer embed 的 `01-schema.sql` 仍整体缺 Sessions V2；本任务已同步主 schema/deploy baseline/table object 的完整 50 列合同，但未伪造 installer 的不完整结构。
- 索引部署窗口：525 在 partition parent 建 4 个索引，生产大分区需低峰执行并监控 lock wait；必要时另行采用逐子分区 concurrent build + attach。
- promote 对账：上线后监控 hot 行龄、每批 moved rows、hot/parent 重复 request key、失败计数及下月 partition 是否存在。
- PostgreSQL 版本：525 可在 PG14+；526 因 RLS-safe security-invoker view 要求 PG15+。生产已核实为 PG17，部署前仍应显式检查 `server_version_num`。
