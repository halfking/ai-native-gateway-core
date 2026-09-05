# 2026-07-24 — llm-gateway DB 结构同步（252 → 本地）

## 背景

本地开发环境（Docker `llm-gateway-pg`，PG17 / kx-citus-pg17:arm64-vector-fixed）的
`llm_gateway` 数据库结构相对于阿里云 252 服务器（`pg-252-pg17`）的 PG17 出现漂移，
需要以 252 为 SSOT 把差异结构同步到本地，确保开发环境与生产环境一致。

本次是**纯数据库结构同步**（DDL），**不涉及任何代码/配置文件改动**，因此
本批次没有可执行的 `git diff`，仅留此文档作为审计与回溯记录。

## 同步方式

| 项目 | 内容 |
|------|------|
| 同步源 | 252 服务器 `pg-252-pg17` (172.16.2.210:5432) → `llm_gateway` DB |
| 同步目标 | 本地 Docker `llm-gateway-pg` → `llm_gateway` DB |
| 网络通道 | `ssh -L 15432:172.16.2.210:5432 root@115.29.212.252 -i ~/.ssh/56_id_rsa` |
| 同步方式 | 直接执行 DDL（`ALTER TABLE ADD COLUMN` / `CREATE TABLE` / `CREATE INDEX`），非 `pg_dump` 全量还原（避免覆盖本地数据）|
| 凭据 | `PG_PASS_252=***REDACTED***`（来自 `env-252.sh`），本地 `llm_gateway_db_pass_2026_secure` |
| 同步范围 | 仅结构（schema），**不动数据** |

## 同步前差异

| 指标 | 252 | 本地 | 差异 |
|------|-----|------|------|
| 表数量 | 263 | 274 | 本地多 11 个（备份/测试表） |
| 索引数量 | 911 | 904 | 本地缺 7 个 |
| 列数量 | 5192 | 5279 | 本地多 87 列 |

### 缺失表（252 有，本地无）— 已同步

| 表名 | 类型 |
|------|------|
| `schema_migrations_backup_20260722` | 普通表 |
| `session_last_requests` | 普通表（会话最后请求缓存） |
| `system_probe_runs` | 分区表（RANGE by `created_at`） |
| `system_probe_runs_default` | 分区表默认分区（`ATTACH PARTITION ... DEFAULT`） |
| `system_settings` | 普通表（系统配置 KV） |
| `ursm_node_snapshot_min` | 普通表（节点快照分钟级聚合） |

### 缺失列（252 有，本地无）— 已同步

| 表 | 新增列 |
|----|--------|
| `request_logs` | `effective_timeout_seconds`, `context_size_tokens`, `timeout_mode`, `is_continuation`, `continuation_keywords`, `node_switch_count`, `keepalive_sent_count`, `cached_response_id` |
| `model_offers` | （视图，不支持 ALTER，已记录） |
| `provider_models` | `modality text DEFAULT 'text'` |
| `self_check_runs` | `selection_strategy text DEFAULT 'most_used'`, `attempted_models jsonb DEFAULT '[]'` |
| `self_check_settings` | `monitor_concurrency integer NOT NULL DEFAULT 5` |

注：`request_logs_2026_07` / `request_logs_2026_08` 通过父表 `request_logs` 的
`ADD COLUMN` 自动继承；`model_offers` 是视图无法 ALTER，列缺失保留。

### 缺失索引（252 有，本地无）— 已同步

| 索引 | 表 |
|------|----|
| `idx_session_last_requests_expires`, `idx_session_last_requests_status` | `session_last_requests` |
| `system_probe_runs_*` (8 个父表 ONLY 索引 + 7 个 default 分区索引) | `system_probe_runs` / `_default` |
| `idx_system_settings_category`, `idx_system_settings_key`, `idx_system_settings_updated` | `system_settings` |
| `ursm_node_snapshot_min_ts_idx` | `ursm_node_snapshot_min` |
| `idx_request_logs_cached_response`, `idx_request_logs_node_switch`, `idx_request_logs_timeout_analysis`（父表 ONLY） | `request_logs` |
| `request_logs_2026_0{7,8}_cached_response_id_idx`, `_node_switch_count_ts_idx`, `_effective_timeout_seconds_latency_ms_idx` | 分区表 |
| `idx_response_format_anomalies_bridge` | `response_format_anomalies` |

## 关键问题修复（本次最关键的发现）

### ❌ 第一次尝试失败：在 `request_logs_2026_07`（columnar 分区）上直接 CREATE GIN INDEX

```
ERROR: unsupported access method for the index on columnar table
       request_logs_2026_07
```

### ✅ 第二次成功：在父表 `request_logs` 上用 `ONLY` 子句创建

```sql
-- ❌ 失败：分区本身是 columnar，GIN 索引不被支持
CREATE INDEX ... ON public.request_logs_2026_07 USING gin (...);

-- ✅ 成功：父表是普通分区表（PARTITION BY RANGE），GIN 索引在父表元数据上合法
CREATE INDEX ... ON ONLY public.request_logs USING gin (...) WHERE ...;
```

这与 252 上的索引定义（`pg_get_indexdef` 显示为 `ON ONLY public.request_logs`）完全一致，
说明 252 上的 GIN 索引也是挂在父表上，分区上不直接建 GIN。

## 同步后状态

| 指标 | 252 | 本地 | 差异 |
|------|-----|------|------|
| 表数量 | 263 | 274 | +11（本地独有备份表/测试表，合理） |
| 索引数量 | 911 | 926 | +15（本地独有的本地表索引） |
| **共同表列数** | 4460 | 4460 | **0 ✅** |
| **缺失索引** | - | - | **0 ✅** |

### 本地多余内容（合理保留）

- **11 个备份/测试表**：`model_offers_legacy`, `model_probe_runs_old`, `ops_model_offers_backup`,
  `repository_schema_migrations`, `request_wal_2026_07_{archived,col_archived}`,
  `rule48_test_{default,view_src}`, `task_default_routing_backup_20260718`,
  `usage_ledger_2026_07_{archived,col_archived}` —— 均为本地历史数据/测试保留
- **15 个本地多余索引**：`idx_test`, `test_btree_idx`, `idx_mpr_*`,
  `idx_request_logs_hot_request_id_ts_unique`, `model_offers_credential_id_raw_model_name_key`,
  `repository_schema_migrations_pkey`, `request_wal_2026_07_*idx1`, `system_probe_runs_default_id_created_at_idx`,
  `usage_ledger_2026_07_*idx1` —— 均为本地独有表的索引

## 后续 / 风险

- 本次未触碰任何业务数据，`ALTER TABLE ADD COLUMN` 全部用 `IF NOT EXISTS`，
  `CREATE TABLE / CREATE INDEX` 全部用 `IF NOT EXISTS`，**幂等安全**。
- `model_offers.provider_modality` 列未同步（model_offers 是视图，无法 ALTER），
  若业务代码依赖此列会出错；后续如需同步需修改视图定义。
- `columnar` 扩展在 252 与本地版本一致（`13.3-1`），行为差异仅体现在
  GIN 索引的"是否允许在 columnar 分区上直接创建"——本地不允许、252 也不允许
  （即使在 252 上也是通过父表 `ONLY` 创建的）。
- 本地数据库数据完全保留（**仅改结构，未改数据**）。