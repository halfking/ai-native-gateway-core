# Session V2 后续只读盘点与设计审计（2026-08-29）

## 结论

**No-Go 保持不变。** 已完成本地 schema/代码盘点、迁移设计草案和 154 staging 只读盘点；未执行 staging/生产 `UPDATE`、`DELETE`、`DROP`、detach、索引创建或数据回填。100 会话 parity 未通过，原因是 staging 已部署的 `validate_sessions_v2_v3` 与当前 staging schema 不兼容（`query session IDs: column "session_id" does not exist`），因此不能把候选数量或 V2 行数当作 parity 证据。

设计文档：`docs/implementation/session-v2-followup-design-20260829.md`

## 只读执行边界

- 执行前通过 `env-injector inject aliyun-gateway-154` 和 envs loader 注入 SSOT；未输出任何 credential value。
- SSH 只读取 154 已运行网关进程 `/proc/<pid>/environ` 中的数据库连接配置。
- SQL 使用 `BEGIN READ ONLY`，并验证 `transaction_read_only=on`；结束执行 `ROLLBACK`。
- 远端只执行 `SELECT`/`EXPLAIN`/`d` 元命令和读取进程信息。

## staging schema 证据

数据库：`llm_gateway`，PostgreSQL `17.10`，用户 `llm_gateway`，连接事务 `read_only=on`。

已存在：

- `public.session_bodies`：RANGE parent，RLS enabled。
- `public.session_turns`：RANGE parent，RLS enabled。
- `public.session_turns_hot`：独立 heap、RLS enabled，约 14.7k rows。
- `public.session_turns_with_current_month`：`security_invoker=true`。
- `public.provider_error_details`：存在但 0 rows。
- `public.candidate_failure_logs_hot`：存在。
- `public.request_state_transitions`：存在。

不存在：

- `public.session_bodies_hot`
- `public.session_bodies_with_current_month`
- `public.promote_session_bodies_hot_to_partition(interval,integer)`

正文 parent 当前有 2026-07、08、09、10 和 `DEFAULT` 分区；只读检查显示这些分区的 access method 均为 heap。该结果与 baseline 中曾出现的 columnar 默认声明存在来源漂移风险，必须在 additive migration 前冻结对象来源，不能盲目切换存储格式。

父表约束已是 tenant-scoped：

- `PRIMARY KEY (id, partition_date)`
- `UNIQUE (tenant_id, session_id, turn_no, partition_date)`
- `UNIQUE (tenant_id, request_id, partition_date)`

## staging 数据与性能证据

低成本只读统计：

- `session_turns_hot`: 14,685 rows；default tenant 14,646，chenb 48。
- `session_bodies`: 168,543 rows；default tenant 168,395，chenb 157。
- `provider_error_details`: 0 rows。
- `session_turns_hot` 时间范围约 `2026-08-29 06:31:40+08` 至 `15:04:16+08`。
- `session_bodies` 时间范围约 `2026-08-25 15:37:24+08` 至 `15:04:16+08`。
- V2 rows 在 staging 主要位于 hot turns + heap monthly bodies，不能据此证明 body hot/promote 闭环。

对 30 天 settled candidate 的 `GROUP BY tenant_id, gw_session_id HAVING max(ts) < now()-30m` 做 `EXPLAIN`：2026-08 分区预计扫描约 220k rows，使用现有 `(tenant_id, gw_session_id, ts)` 索引；完整实际聚合在默认 statement timeout 下取消。既有历史报告也记录过 request/body 查询超时。因此本次没有通过放宽 timeout、建索引或改数据来规避问题。

## 100 会话 parity 结果

尝试使用 staging 已有 `/tmp/validate_sessions_v2_v3`，参数为：

- tenant `default`
- range `[2026-08-01, 2026-08-29)`
- `-max-sessions 100`
- `-settle-window 30m`
- `-format json -verbose`

结果：

```text
Failed to load session IDs: query session IDs: ERROR: column "session_id" does not exist (SQLSTATE 42703)
```

进程无 stdout JSON；该运行不产生写入。失败说明 staging binary/schema contract 仍未冻结，且当前工具不能满足“所有 100 个 loader 成功、skipped=0”的门禁。后续必须先修正候选查询/部署匹配版本，再重新运行；禁止使用 `-repair` 或 `-apply` 作为验证替代。

## 设计审计摘要

1. `session_bodies_hot` 应复制 526 的独立 heap + 三层 RLS + 显式列 view + advisory lock + 原子 CTE promote 模式，默认 retention 8h、batch 500；旧版本兼容通过 additive schema 和 feature flag，回滚只切 flag/排空 hot，不直接删数据。
2. 已 promote turn 的 late metadata 不再直接更新 columnar；使用 append-only enrichment event + aggregate claim ledger，hot 仅允许白名单字段 enrichment。
3. `provider_error_details` 仅作为趋势聚合；candidate failure logs 仍是明细权威源。聚合输入通过有界事件队列、脱敏 fingerprint、有限 payload 和失败重试/drop 指标进入，不能阻塞请求。
4. preflight rejection 统一归类为 `failure_stage=preflight` + 固定 `failure_detail_code`，复用统一 failure recorder；保留 request/session/tenant/provider/credential/model 关联，但不写 secret/raw upstream body。
5. SurvivalCoordinator 在 gate discard 后、wait 开始前通过 nil-safe retry notice seam 发一次受控 `: thinking:` comment；keepalive 仍走 transport heartbeat，semantic commit 后不透明重试。

## 后续门禁

- additive migration 必须先在本地 fixture/testcontainers 验证列合同、RLS、view 去重、promote 错误回滚和 down 前置条件。
- staging apply 需要另开受审会话，先备份并演练 rollback；本文件不授权 apply。
- parity 工具必须读取 `*_with_current_month` view，并以 V1 `request_logs` + `request_logs_bodies` 为 source；至少 100 个 settled sessions 全部成功，任何 loader error/skipped 均失败。
- 在 parity、24h canary、前端抽样、outbox/错误指标和 rollback rehearsal 完成前，继续 No-Go。
