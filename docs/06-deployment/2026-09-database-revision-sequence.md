# Database Revision Sequence

**适用范围**：llm-gateway-go 本地/共享 PostgreSQL 部署

**序列入口**：`scripts/apply-db-revision-sequence.sh`

**执行入口**：`scripts/deploy-local.sh deploy`

## 目的

本序列用于一次性修复 2026-09 发现的数据库结构与写入链路问题。它不是历史迁移重放器，不会按数字扫描并重放全部 `sql/migrations`。

执行顺序固定为：

```text
655 -> 560 -> 572 -> 606 -> 563 -> 564 -> 644 -> 645
```

序列脚本使用 `gateway_db_revision_sequences` 记录完成标记；重复部署在已完成时直接退出。每个 SQL 文件本身也必须保持幂等。

## 修订说明

| 顺序 | 文件 | 作用 | 安全边界 |
|---|---|---|---|
| 655 | `655_session_summaries_schema_reconcile.sql` | 保留 Memora 的 `session_id/summary_json`，增量补齐 Gateway canonical session 字段和索引 | 不删除、不重命名、不重建 `session_summaries`；不存在该表时拒绝执行 |
| 563 | `563_session_summary_trigger_on_hot.sql` | 将 session 聚合触发器绑定到 `request_logs_hot`，使用 `gw_session_id/ts/cost_usd` | 依赖 655 已提供 canonical 列 |
| 564 | `564_session_summary_backfill_safe.sql` | 安全回填 session 聚合数据，避免覆盖较大的累计值 | 失败即停止，不自动删除重复业务数据 |
| 572 | `572_session_summary_large_token_ratio.sql` | 用无界 `numeric` 计算 token 比例，避免长上下文溢出 | 只替换函数定义 |
| 606 | `606_session_summaries_agent_expert_tags.sql` | 增加 summary 的 agent/expert/tags 字段 | `ADD COLUMN IF NOT EXISTS`，带安全默认值 |
| 644 | `644_tuning_views_selfcheck_and_candidate_failure_cache.sql` | 修复 routing/tuning 物化视图和 candidate failure unified view；更新 self-check taxonomy | 仅在对象存在且 schema 满足前提时操作；不创建猜测性的外部表；JSON `context` 由 Go writer 校验，迁移不使用 PostgreSQL 不支持的正则 lookahead |
| 645 | `645_session_bodies_hot_request_unique_repair.sql` | 补齐 `session_bodies_hot` 的 request 唯一仲裁器 | 发现重复 key 时失败并保留数据，不自动选删记录 |

`649_routing_analytics_probe_filter.sql` 与 `632_routing_analytics_materialized_view.sql` 的身份不能只按数字判断。632 analytics 文件位于 `sql/migrations/startup/up/`，当前 Gateway 由 `db.go` 的 `ensureRoutingAnalyticsMaterializedViews` 负责；部署前应按环境 ledger 核对，不得把两个 632 文件当成同一迁移。

## 执行前检查

1. 确认 DSN 指向目标数据库：

```bash
printf '%s\n' "$LLM_GATEWAY_DATABASE_URL"
```

2. 先做数据库备份/快照。禁止在未备份的共享数据库上执行未知 schema 转换。

3. 只读确认基础对象：

```sql
SELECT to_regclass('public.session_summaries');
SELECT column_name, data_type
FROM information_schema.columns
WHERE table_schema = 'public'
  AND table_name = 'session_summaries'
ORDER BY ordinal_position;
```

4. 如果发现 `session_summaries` 缺失，或连接到了不属于 Gateway 的数据库，序列必须停止。不要创建 `employees`、`employee_agent_configs`、`audit_hash_chain` 等 ACC/外部服务对象。

## 执行方式

`deploy-local.sh` 的数据库阶段按以下顺序执行：

```text
schema-if-empty -> revision sequence -> gateway migrate -> build/stage -> candidate health/readiness/version -> cutover
```

手工执行序列时必须显式提供 DSN：

```bash
DATABASE_URL='postgresql://user:password@host:5432/llm_gateway?sslmode=disable' \
  bash scripts/apply-db-revision-sequence.sh
```

若宿主机没有 `psql`，设置 Docker 参数：

```bash
DATABASE_URL='postgresql://unused' \
LLM_GATEWAY_PG_CONTAINER=llm-gateway-pg \
LLM_GATEWAY_PG_USER=llm_gateway \
LLM_GATEWAY_PG_PASSWORD='...' \
LLM_GATEWAY_PG_DATABASE=llm_gateway \
  bash scripts/apply-db-revision-sequence.sh
```

迁移任一步失败时，`deploy-local.sh` 不会继续候选发布或切换 active。普通 deploy 会先 bump 本地版本并写运行态文件，因此正式部署应使用专用干净 checkout；`--dry-run` 不执行版本 bump、数据库迁移或服务切换。

## JSONB 写入约定

`model_integrity_events.context` 的 canonical 写入路径必须：

- Go 侧先 `json.Marshal` 和 `json.Valid`；
- JSON 参数使用 `string`，不直接使用 `[]byte`；
- SQL 使用 `$N::text::jsonb`；
- 空值使用 `null`，不使用空字符串；
- 不依赖 BEFORE INSERT trigger 修复 cast 前的非法 JSON。

## ACC 外部错误边界

数据库日志中的以下查询不属于当前 Gateway 仓库：

- `employees`
- `employee_agent_configs`
- `task_assignments`
- `audit_hash_chain`
- `daily_kline`

当前仓库未找到这些表的 DDL、迁移或 Go 查询。应在 ACC 项目中排查，不应在 Gateway 数据库中猜测性补表。

建议交给 ACC 的提示词：

> 请在 ACC 项目中排查 PostgreSQL 日志中的 schema mismatch。重点定位 `employees`、`employee_agent_configs`、`task_assignments`、`audit_hash_chain`、`daily_kline` 的 SQL 来源和实际表结构。核对 `employee_id`、`agent_type`、`idle_ttl_sec`、`lifecycle_tier`、`status`、`task_id`、`last_heartbeat_at`、`event_data` 等字段。请先输出只读 catalog 检查、代码来源、迁移依赖和数据风险；确认表结构后再提供幂等迁移和 Go 修复方案。禁止删除/重建现有表、自动清理重复业务数据或将 ACC 表写入 Gateway schema。

## 部署后验证

```bash
bash scripts/deploy-local.sh verify
```

只读校验：

```sql
SELECT column_name
FROM information_schema.columns
WHERE table_schema='public'
  AND table_name='session_summaries'
  AND column_name IN ('session_key','request_count','total_tokens','gw_task_id','agent_type','expert_type','tags')
ORDER BY column_name;

SELECT to_regclass('public.candidate_failure_logs_unified');
SELECT column_name
FROM information_schema.columns
WHERE table_schema='public'
  AND table_name='candidate_failure_logs_unified'
  AND column_name='source';

SELECT sequence_name, applied_at
FROM public.gateway_db_revision_sequences
WHERE sequence_name='session-summary-and-integrity-2026-09';
```

观察 PostgreSQL 日志时，重点确认本项目可归因的 `session_key`、`total_tokens`、`request_count`、JSON `22P02` 和 `source` cache 错误不再增长。`employees` 等 ACC 错误必须由 ACC 服务单独验证。

## 回滚边界

- 二进制回滚不能回滚数据库变更。
- `655` down 脚本在 canonical 数据库上可能删除原有列，默认禁止执行。
- 如果序列部分完成，先读取 `gateway_db_revision_sequences`、schema migration ledger 和备份，再由维护者决定补齐或恢复。
- 不使用 `DROP TABLE`、`DROP COLUMN`、未知 JSON 批量转换或自动删除重复记录作为回滚手段。
