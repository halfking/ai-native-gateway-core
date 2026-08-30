# 2026-08-31 数据库结构一致性审计报告（252 ↔ local llm-gateway-pg）

**审计人**: ZCode agent（用户指令：查文档→理需求→审计→修脚本→修问题→合并推送）
**范围**: 252 测试库（172.16.2.210:5432，经 SSH 隧道）↔ 本地 docker `llm-gateway-pg`
**结论**: 修复后两端**结构完全一致**（`verify-db-consistency.sh` 全部 6 个维度通过，唯一剩余差异为预期内的运行时月度分区）。

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

## 六、变更记录

| 日期 | 说明 |
|------|------|
| 2026-08-31 | 初版：审计发现 4 类真实差异 + P0 脚本缺陷；修复 local 5 项 / 252 2 项 / 脚本 2 个；复验全绿 |
