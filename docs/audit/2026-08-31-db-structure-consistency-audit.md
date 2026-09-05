# 2026-08-31 数据库结构一致性审计报告（252 ↔ local llm-gateway-pg）

> **重要说明**：本文记录 2026-08-31 一次审计运行的结论，**不是当前实时数据库状态**。仓库目标库 `llm-gateway-pg` 已在审计后重建或下线，且没有留下任何形式的 DDL dump。再次确认两端一致需要：
> 1. 重新启动或恢复 `llm-gateway-pg` 容器；
> 2. 跑 `scripts/local-dev/verify-db-consistency.sh --verify` 与 `scripts/local-dev/verify-db-data-consistency.sh`；
> 3. 与本页第七章（LP2/PR3、PR4）引入的迁移、canonical view 变更重新校核。
>
> 把“修复后结构完全一致”当成当前结论是不安全的；后续修改（LP2、PR3、PR4 等）已对该假设做过调整。

**审计人**: ZCode agent（用户指令：查文档→理需求→审计→修脚本→修问题→合并推送）
**范围**: 252 测试库（172.16.2.210:5432，经 SSH 隧道）↔ 本地 docker `llm-gateway-pg`
**结论（历史运行）**: 修复后两端**结构完全一致**（`verify-db-consistency.sh` 全部 7 个维度通过，唯一剩余差异为预期内的运行时月度分区）。

---

## 一、特性需求 → 表映射（来自任务文档）

| 特性 | 需求文档 | 依赖对象 | 审计前状态 |
|------|---------|---------|-----------|
| 代理管理（海外供应商经代理访问） | `docs/proxy-management-design.md` §2.2 + `db/migrations/364_proxy_management.sql` | `proxy_subscriptions` / `proxy_nodes` / `provider_domains` 三表；`providers.egress_profile` + `providers.proxy_subscription_id`（FK，ON DELETE SET NULL）+ `idx_providers_egress` / `idx_providers_proxy_sub`；15 行域名种子数据 | 三表 08-31 上午已回灌 252；**两端都缺 `providers.proxy_subscription_id` 与两个索引（364 §4 从未在任何一端执行）**；252 缺种子数据 |
| request_logs body 外置（存储优化） | `sql/migrations/startup/573_drop_request_logs_body_columns.sql`（2026-08-25 解除 .skip） | 删除 `request_logs_hot`/`request_logs` 上的 `request_body`/`response_body`/`outbound_body`；DROP 视图 `request_logs_bodies_progress`；重建 `request_logs_with_current_month` | **local 的 `request_logs` 父表仍残留 `request_body`/`response_body` 两列与 progress 视图（573 未生效）** |
| request_class/due_at 域约束 | `sql/migrations/startup/610_request_class_due_at.sql` | `request_logs_hot` + `request_logs` 上 `*_request_class_due_at_check` CHECK | **local 的 `request_logs` 缺该约束（610 未生效）** |
| agent 发现 / 编排 / 网关绑定（6 张表） | **无任何特性文档、无 Go struct、无迁移定义**（仅 SKILL.md reconcile 清单提及） | `agent_gateways` `agent_discovery` `agent_migration_log` `orchestration_sessions` `industry_registry` `gateway_run_bindings` | 两端结构一致（08-31 上午回灌）；**文档缺口，见 §五遗留** |

## 二、审计方法

6 维全量对比（均排除 local `_` 前缀清理对象与运行时月度分区 `*_2026_XX`）：

1. 表清单（`pg_tables`）2. 全表列签名（`information_schema.columns`：name|type|null|default，按名列排序）3. 视图（`pg_views` name + `md5(definition)`）4. 索引逻辑形态（pg_index：唯一/主键/键列/谓词存在性，不用 `pg_get_indexdef` 原文）5. 约束（name|type|def，对 `ANY(ARRAY[...])` 两种渲染做归一化）6. 序列（排除 `_` 表属主序列）

## 三、根因（P0：同步脚本静默失败，迁移跟踪表掩盖漂移）

**`scripts/pg-table-copy.sh` PHASE 6 存在两个叠加缺陷，导致 2026-08-26 的 252→local 同步 schema 导入实际失败却报告成功：**

1. **`pg_dump` 导出自带 `SELECT pg_catalog.set_config('search_path', '', false)`**。252 与 local 都装有 columnar 事件触发器 `enforce_columnar_trigger`（调用未限定 schema 的 `columnar_insert_only_parents()`）；`search_path=''` 时该函数解析失败，**每一条 CREATE/DROP TABLE 都报错**（已在两端实测复现）。
2. **导入用 `-v ON_ERROR_STOP=off` 执行，psql 退出码为 0**；旧代码只在退出码非零分支里才检查日志中的 ERROR 行 → 走到 `ok "Schema imported"`，错误被完全吞掉。

后果链：local 保留同步前的旧 schema（573/610 的 DDL 从未落地）→ 而 `schema_migrations` 作为**数据**被照常复制，显示 573/610 "已应用" → 表级对比（表名清单）恰好全匹配 → 漂移被完全掩盖，直到本次列级/视图级审计才暴露。

## 四、修复清单

### 数据库（local，`docker exec llm-gateway-pg`，单事务 + 364 部分）

| # | 动作 | 依据 |
|---|------|------|
| L1 | `DROP VIEW request_logs_bodies_progress`（删列前唯一依赖对象，pg_depend 验证） | 573 |
| L2 | `ALTER TABLE request_logs DROP COLUMN request_body / response_body`（分区父表，local 无分区） | 573 |
| L3 | `ADD CONSTRAINT request_logs_request_class_due_at_check`（预检违反行数=0） | 610 |
| L4 | `ALTER VIEW rule48_test_with_current_month RENAME TO _rule48_test_with_current_month` | `_` 前缀规约（引用 `_rule48_test_view_src` 的测试残留） |
| L5 | `providers` 加 `proxy_subscription_id`（FK→proxy_subscriptions ON DELETE SET NULL）+ `idx_providers_egress` + `idx_providers_proxy_sub` + 注释 | 364 §4 |

### 数据库（252，经隧道，`ON_ERROR_STOP=1`）

| # | 动作 | 依据 |
|---|------|------|
| R1 | 同 L5（providers 加列+索引+注释） | 364 §4 |
| R2 | `provider_domains` 种子 15 行（`ON CONFLICT (domain) DO NOTHING`，幂等；与 local 15 行一致） | 364 §5 |

### 脚本

| 文件 | 修正 |
|------|------|
| `scripts/pg-table-copy.sh` | PHASE 6：① 导入前剔除 `set_config('search_path','',false)` 守卫行（防 columnar 触发器全量失败）；② **无论退出码如何都扫描日志 ERROR/FATAL 行**，有则打错误清单并置失败标记；③ SUMMARY 在 schema 导入失败时 `exit 1`，并警示"跟踪表数据不可信" |
| `scripts/local-dev/verify-db-consistency.sh` | v1.0 只比表清单+列 → v1.1 增加视图（name+md5 定义）、索引逻辑形态、约束（归一化）、序列（`_` 属主过滤）四个维度；列签名改为按名排序（物理列序属无害差异）；约束 `ANY(ARRAY[...])` 两种渲染归一化后比较 |

### 复验（修复后，`verify-db-consistency.sh` exit 0）

```
A. 表清单      : 仅 252 多 request_logs_bodies_2026_10（预期运行时分区）
B. 列(6836)    : identical
C. 视图(68)    : identical
D. 索引(1174)  : identical
E. 约束(723)   : identical（归一化后）
F. 序列(173)   : identical
→ CONSISTENT
```

## 五、遗留与建议

1. **6 张 feature 表（agent_*/orchestration_*/industry_registry/gateway_run_bindings）无需求文档**：本仓库无任何文档/迁移/代码定义它们，结构唯一权威是本地库。建议补一页设计说明，或确认其来源（可能是某分支代码运行时建表）。
2. **252 的 `request_logs.outbound_body` 仍在**：573 意图连 `outbound_body` 一起删，但 252 父表保留了它（252 上 573 也只部分落地于父表）。两端现状一致故本次不动；是否继续删由上游部署流程决定（注意先处理引用它的视图/读方）。
3. **`request_logs` 分区数不同**（252 有 default+月度分区，local 0 个）：属运行态差异（hot 数据本地不拷贝），非 schema 漂移。
4. 表面性差异（无需处理）：`request_logs` 物理列序两端不同；32 个 CHECK 约束与 3 个 partial index 的 `ANY(ARRAY[...])` 文本渲染不同（逻辑等价）；`_` 表遗留孤儿序列 3 个（`model_offers_id_seq` 等，随 rule 19 §11 清理流程处理）。
5. **每次跑完 `pg-table-copy.sh` 后应执行 `verify-db-consistency.sh`**——迁移跟踪表可能随数据被复制而"说谎"，只有结构指纹可信。

## 六、二次复验（2026-08-31，集成 origin/main `45840e2ca` 后）

集成他人提交 `45840e2ca`（audit-data-closure 收尾修复，新增 `628` 的 `.down.sql`）后，按审计建议再次执行结构与数据双审计，并发现**函数维度盲区**。

### 6.1 结构审计升维（v1.1 → v1.2）

原六维审计（表/列/视图/索引/约束/序列）不比较**函数/存储过程**，因此函数迁移（如 628 的 `promote_candidate_failure_logs_hot_to_partition`、382/383/433 的归档与快照函数）的漂移完全不可见。已在 `verify-db-consistency.sh` 新增 **G. 函数维度**：`proname|pg_get_function_identity_arguments(oid)|md5(prosrc)`，覆盖 `prokind IN ('f','p')`。

### 6.2 函数维度暴露的真实漂移（修复前）

升维后结构审计首次报红：6 个函数**仅 local 存在，252 缺失**：

| 函数 | 来源迁移 | 252 状态 |
|------|---------|---------|
| `archive_dashboard_events(retention_days integer)` | `383_dashboard_access_events.sql` | 未应用（表 `dashboard_access_events` 已存在，但 `schema_migrations` 无 383） |
| `archive_session_module_executions(retention_days integer)` | `382_session_module_executions.sql` | 未应用（表已存在，但 `schema_migrations` 无 382） |
| `get_system_snapshot(p_instance_id text, p_hours_ago integer)` | `433_system_metrics_local_ingest.sql` | **部分应用**：`schema_migrations` 记 433 已应用，但函数缺失 |
| `handoff_logs_view_delete()` | **无迁移源**（仓库全仓 grep 仅命中本审计报告）¹ | 已回灌（死函数）² |
| `handoff_logs_view_insert()` | 同上 | 已回灌（死函数）² |
| `promote_handoff_logs_default_batch(p_retention interval, p_batch_size integer)` | 同上 | 已回灌（死函数）² |

> ¹ **勘误（2026-08-31 二次复验后修正）**：上表初版将这三个函数归因于 `534_handoff_logs_hot_columnar.sql`，**错误**。534 实际仅定义 `ensure_handoff_logs_partition(p_month)`、`handoff_logs_with_current_month` 视图、`promote_handoff_logs_hot_to_partition(p_retention, p_batch_size)`。对全仓（含 `.go`/`.ts`/`.disabled`）grep 这三个函数名，结果仅在审计报告本身出现，**仓库内无任何创建源**。它们引用已不存在的 `handoff_logs_parts` 表，且无任何 `INSTEAD OF` 触发器挂接，是 534 之前 heap 设计的**废弃残留（死函数）**。
>
> ² 三个死函数已由本回 gated reconcile 从 local 回灌 252（§6.3），故两端"都有但都死"。函数维度审计把它们当匹配是**假绿**。正确处置：在 252 与 local 两端 `DROP FUNCTION`（无调用方、无触发器，无害），并从一致性契约中排除——**不应**为它们新增迁移或期待 app 二进制重建（那会造出引用不存在表的坏函数）。详见 §6.5。

**根因**：同步契约是 252→local（252 是源）。local 在同步后自行跑 startup 迁移（382/383/433/534）补齐了这些函数；252 测试库要么尚未部署这些迁移（`schema_migrations` 缺 382/383），要么部署部分失败（433 已记录但函数未落地）。两端 `schema_migrations` 均为 211 行，故迁移跟踪表再次"掩盖"了函数级漂移——印证 §三结论。

### 6.3 修复（gated reconcile 思路回灌 252）

这些函数均来自**已提交迁移**，252 作为待部署测试环境应当具备。按 `verify-db-consistency.sh --reconcile` 的"local DDL → 252"回灌思路，从 local 抽取 `pg_get_functiondef`（`CREATE OR REPLACE FUNCTION`，幂等），剔除 `set_config('search_path','',false)` 守卫行，`ON_ERROR_STOP=1` 应用到 252：

```bash
# 抽取 6 个函数的 DDL（每个补末尾 ;），剔除 search_path 守卫，再
PGPASSWORD="$PG_PASS_252" psql -h localhost -p 15432 -U llm_gateway -d llm_gateway \
  -v ON_ERROR_STOP=1 -f /tmp/reconcile-funcs.sql.fixed
# → CREATE FUNCTION × 6，252 现具备全部 6 个函数
```

> 注：此回灌为补偿 252 部署滞后/部分失败的临时措施；**权威修复**是让部署流水线完整应用 382/383/433/534，使其 `schema_migrations` 与函数体一致。回灌不影响 `schema_migrations` 记录（幂等，下次部署重跑同名迁移仍 `CREATE OR REPLACE` 成功）。

### 6.4 复验结果（修复后，全绿）

```
A. 表清单      : 仅 252 多 request_logs_bodies_2026_10（预期运行时分区）
B. 列(6836)    : identical
C. 视图(68)    : identical
D. 索引(1174)  : identical
E. 约束(723)   : identical（归一化后）
F. 序列(173)   : identical
G. 函数(530)   : identical   ← 新增维度，此前为 6 行漂移
→ CONSISTENT（七维）
DATA AUDIT     : 261 张普通表集合 / 行数 / 内容摘要全部 identical
```

> ⚠️ **G 维度"全绿"为假性一致（2026-08-31 勘误）**：上述复验时，三个死函数 `handoff_logs_view_delete` / `handoff_logs_view_insert` / `promote_handoff_logs_default_batch` 在 252 与 local "都存在"，故函数维度 md5 比对 identical。但二者都是引用已删除表 `handoff_logs_parts` 的废弃残留（详见 §6.2 勘误脚注）。正确终态是两端**都不存在**这些函数——需在 252 与 local 执行 `DROP FUNCTION` 后复验仍 CONSISTENT 才是真绿。

### 6.5 二次复验后的勘误（死函数 + 部署根因）

- **死函数根因**：`handoff_logs_view_delete` / `handoff_logs_view_insert` / `promote_handoff_logs_default_batch` 并非来自迁移 534（§6.2 初版误归因）。全仓无任何创建源，引用不存在的 `handoff_logs_parts` 且无触发器挂接，属 534 前 heap 设计残留。本回 gated reconcile 把它们从 local 误回灌 252，制造了"双端都有但都死"的假绿。
- **252 部署真实根因**：app 内嵌迁移集合 `installer/cmd/llm-gw-installer/embeddata/startup/` 为**硬编码精选子集**（50 个文件，范围 511–626），经 `main.go` 逐个 `//go:embed` 声明。**完全不含 382/383/433/534**（531 之后直接跳 536，且 300–500 区间文件本就不在内嵌集合内）。故正常 app 部署永远不应用这四笔——这才是 `repository_schema_migrations`（严格 runner 台账，startup scope 仅记 330/331）缺失它们的根因，而非"request_logs 创建顺序导致 ALTER 失败"（`97d5edb7a` 新增的 `000_base_tables.sql` 与 002/007/009 的 `DO $$` 守卫是良好加固，但非缺失原因，330/331 已落库即证明序列未中断）。
- **权威修复**：对 252 精准重跑 382/383/433/534（`CREATE OR REPLACE` / `IF NOT EXISTS`，幂等）并写入 `repository_schema_migrations`(scope=startup)；对齐内嵌集合需改 `main.go` 的 embed 列表，属部署架构决策，单列待办（见 handoff）。

## 七、变更记录

| 日期 | 说明 |
|------|------|
| 2026-08-31 | 初版：审计发现 4 类真实差异 + P0 脚本缺陷；修复 local 5 项 / 252 2 项 / 脚本 2 个；复验全绿 |
| 2026-08-31 | 二次复验：结构审计升 v1.2（+函数维度）；发现并回灌 252 缺的 6 个函数（来自迁移 382/383/433/534）；七维结构 + 261 表数据双审计全绿；更新 SSOT v1.4 与 db-sync 技能 |
| 2026-08-31 | 勘误：6 个函数中仅 3 个（382/383/433）来自迁移，另 3 个 handoff 函数为无源死函数（§6.2 初版误归因 534）；函数维度"全绿"为假绿；252 部署根因为内嵌迁移集合硬编码精选子集不含 382/383/433/534（非 request_logs 创建顺序）。已规划"删除 request_logs.outbound_body"路径（迁移 629/636 + 移除 db.go self-heal），但与 remote main LP9 schema rollback audit 保留列的设计冲突，已在合并中撤销；后续应作为单独 PR 重新评估。 |
