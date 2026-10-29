# 2026-10-29 R181 —— request_logs 退役收尾审计 + 会话族分区裁剪轮

> 会话目标（用户指令摘要）：审计 request_logs→session_turns 双写转换的收尾——
> 写入关停核验、物化视图/API 读源核验、性能核验、**把 `_request_logs*` 临时删除表
> 真正删除**、逐 API 单测、SQL 变更进部署序列、本地重部署 + 模型多轮请求验证 +
> 浏览器逐页验证，提交推送。
>
> ★ 本轮是**接续**：前一会话（`mvs_bbd1346d211a4eb3ac41b552c82eaecf`，同 goal）
> 因 Token Plan 用量上限中止，留下 `/tmp/kx-wt-r98` 工作区的未提交 R181 产物。
> 本轮**继承了它的两处改动**（914 台账探针修正、query_plan_form 的 sort_keys 观测）
> 并**重新验证**；同时推翻了一条它沿用的旧结论（见 §四）。
>
> 通用式见 `LESSONS.md` **§564**。

---

## 一、结论速览（先看这一节）

| # | 结论 | 证据强度 |
|---|---|---|
| 1 | **v1 族写入在运行时不可达** —— 不是「被开关关住」，是「关系已不存在 + 代码里没有写点」 | 结构性（pg_class 零关系）＋ 389 个写目标全量提取 ＋ 9 个 PL/pgSQL 已删 ＋ 零触发器 |
| 2 | **`_request_logs*` 族已在三个环境真正删除**（本地 / 211 / 252） | 三环境实测；211 与 252 的台账 marker 均为 `executed-via-explicit-retirement` |
| 3 | **8 个物化视图全部已重绑到会话族**；`pg_depend` 全库零 v1 边 | live 依赖图（非文本匹配） |
| 4 | **API 读路径零 v1 残留** —— 生产 Go 代码里的 `FROM/JOIN <v1 关系>` 清单是**空的** | 复刻仓内门扫描器 + live pg_depend 双证 |
| 5 | ★★ **一个把「每次部署都卡死」的判据缺陷**：迁移 914 的台账产物探针在分区表上**恒假** | 真库实测（`= 2` vs 实际 7）＋ 修前部署真的红了 |
| 6 | ★★★ **请求列表/轮次列表的分区裁剪此前完全失效**：`session_turns` 按 `partition_date` 分区，而读方只按 `ts` 过滤 | 真库 A/B（5 分区 → 1 分区）＋ 生产台账（1 秒窗口扫 4 个整月分区） |
| 7 | ★★ **一条沿用多年的旧结论被推翻**：canonical 视图与原生源「差 27.6% 的行」——前提是「v1 还在」，890 之后**两者行集完全相同** | 逐行对拍（view 2,810 / native 2,810 / 差集双向为 0）＋ 结构性论证 |

---

## 二、写入路径审计（结论 1）

### 2.1 开关本体

| 项 | 值 | 出处 |
|---|---|---|
| 键名 | `storage.request_logs_write_enabled` | `settings/key_request_logs_write_enabled.go:20` |
| 代码默认 | **`false`** | 同上 `:43-45`（`GetPlatformBool(Key…, false)`） |
| 声明默认 | **`false`** | `settings/spec_storage.go:136` |
| 热加载 | 是（DB → env → 声明默认，**无缓存层**） | `settings/helpers.go:83-101` → `settings/spec.go:309-337` → `settings/store_db.go:33-49` |
| 现网值 | `false` | `settings_kv`（2026-10-16 03:57:24 起） |

★ **「未配置 ⇒ 不写」是代码事实，不是逐部署事实**（声明默认与取值兜底**都是** false）。

### 2.2 v1 写点全量清单

生产 Go 代码里**全部** SQL 写目标（去注释、跨行容错）共 **389 个关系**，
其中 v1 关系**只有一个**：`request_logs`，且它的两处站点**都是 SQLite 平面**：

> ★★★ R182 轮订正（★★ **数字口径**缺失，★ 不可复现 ★★★）：★★
> ★ 原文的「389 个关系」**没有记录提取口径** ⇒ ⇒ 独立复刻得不到同一个数
> ★ （本轮复刻得 **376**；差异来自是否含 `_test.go` / 嵌入副本 / 审计残留目录
> ★ / 是否按「关系」去重还是按「站点」计）。★★ **两条定性结论完全复现**
> ★ （v1 关系只有 `request_logs`、且它的两处站点都在 SQLite 平面）⇒ ⇒ **定性可靠，
> ★ 数字待补口径** ★ ⇒ 见 R183 审计档 §二.4。

| 站点 | 代际 | 运行时可达 | 判定 |
|---|---|---|---|
| `storage/sqlite/request_log_store.go:23` | 裸名 · SQLite DDL | 仅 lite 模式（`cmd/gateway/main.go:2643-2644`） | **OK-豁免** |
| `bg/lite_retention_worker.go:116` | 裸名 · SQLite（`main.` 前缀） | 仅 lite 模式 | **OK-豁免** |
| `domains/session/dual_writer.go:191` | `_request_logs` 语义 | **否** —— `NewDualWriter` **零生产调用方** | 死代码 |
| `bg/partition_manager.go:843` | `_request_logs` → `_request_logs_archive` | **否** —— `v1LifecycleRetired()` 硬编码 `return true`（`:1607-1609`） | 死代码 |
| `admin/data_lifecycle_hot_partition.go:33-37` | `_request_logs_hot` | 仅当运维把开关翻成 true ⇒ 调已删函数 ⇒ 42P01 | 死代码（写不进去） |
| `internal/sessionv2mirror/session_summary_dim.go:121` | — | — | ★ 见 §二.3 |

★ **SQLite 豁免的锚点（必须留在文档里）**：该表由 **SQLite 侧 DDL** 建立
（`storage/sqlite/schema.go:103`），用 `database/sql` + `?` 占位符，
只在 lite 模式挂载 ⇒ **任何「全仓无 request_logs」的 grep 门都会对它假阳性**。

### 2.3 PL/pgSQL 与触发器

★ 解析全仓 **905** 个 `CREATE FUNCTION/PROCEDURE` 体 ⇒ **9 个**函数写 v1，
**全部**在 890 的 DROP 名单里（`890_drop_request_logs_v1_family.sql:549-564`）：

> ★★★ R182 轮订正（★★ 两处 ★★★）：★★
> ★ ① **行号区间**：★ 原文写 `549-560` ⇒ ★★ 但 9 个函数里有 **2 个落在区间外**
> ★ ★（`cleanup_stale_in_progress_requests` 在 **:561**、`get_last_successful_request`
> ★ ★ 在 **:564**）⇒ ⇒ 区间应为 **549-564**。
> ★ ② **「905 个」缺口径**：★ 按 `sql/**`=816 / 全仓 `*.sql`=1225 /
> ★ `sql/migrations/startup`=541 三种口径都得不到 905 ⇒ ⇒ 数字**不可复现**
> ★ ⇒ **9 个 v1 写函数这个定性结论完全复现**（逐个命中 890 的 DROP 名单）★。

`cleanup_stale_in_progress_requests` · `ensure_request_logs_partition` ·
`promote_request_logs_hot_to_partition` · `promote_request_logs_bodies_hot_to_partition` ·
`promote_request_logs_default_batch` · `promote_request_logs_bodies_default_batch` ·
`repair_request_logs_detached_partitions` · `backfill_request_logs_bodies` ·
`get_last_successful_request`

★ live 库 `pg_proc.prosrc` 扫描 ⇒ **0 行**存活。
★ live 库 50 个非内部触发器 ⇒ **无一挂在 v1 关系上**（关系都不存在）。

### 2.4 ★ 本轮定性的两个「运行时危害」（不是写路径，是**语义**路径）

★ **危害 A（★ 本轮发现，值得下一轮处理）**：
`internal/sessionv2mirror/session_summary_dim.go:121` 的
`if v1WritesEnabled() { return nil }` —— ★ **`session_summaries` 的聚合写入
在开关为 true 时是 no-op**。而喂养 `session_summaries` 的两个 v1 触发器
已随 890 一起消失 ⇒ ⇒ **把开关翻成 true（哪怕只是想「回退」）会静默停掉
`session_summaries` 聚合** ⇒ ⇒ 用户面的会话列表/详情/会话分析会**发霉而不报错**。
★ 这是**唯一一处「退役语义仍在支配活行为」**的地方。
★ 本轮**未改**（它有守卫测试钉住，且改动涉及产品语义）⇒ **登记为下一轮头号项**。

★ **危害 B**：`admin/data_lifecycle_hot_partition.go:35` 的 promote 守卫**方向反了** ——
开关为 true 时**允许** promote `_request_logs_hot` ⇒ 调已删函数 ⇒ 42P01。
★ 表已不存在 ⇒ 写不进去 ⇒ 但它是**运维可达**的 v1 指向语句。

---

## 三、读路径与物化视图审计（结论 3、4）

### 3.1 物化视图：8 个，全部已重绑

live `pg_depend`（`_RETURN` 重写边）逐个解析源关系：

| MV | 源关系 | 判定 |
|---|---|---|
| `routing_analytics_7d` | `routing_analytics_source`(v) · `credentials`(r) | 会话（传递）✅ |
| `routing_audit_summary_7d` | `routing_analytics_source`(v) | 会话（传递）✅ |
| `session_client_stats` | `session_summaries`(r) | ✅ |
| `session_client_task_matrix` | `session_summaries`(r) · `session_dim`(r) | ✅ |
| `session_owner_stats` | `session_owners`(r) · `session_summaries`(r) | ✅ |
| `session_task_stats` | `session_summaries`(r) · `session_dim`(r) | ✅ |
| `tuning_signals_5m` | `tuning_signals`(r) | 非 v1 ✅ |
| `tuning_signals_daily` | `tuning_signals`(r) | 非 v1 ✅ |

⇒ ★ **没有「旧的、没有转化的物化表」需要删除** —— 这一项用户诉求**已由 890/883/884 完成**。

### 3.2 视图与函数：pg_depend 零 v1 边

★ 全库 **83 个**视图/MV 的 `pg_depend` 边集里，**零**条指向 v1 关系。
★ 全库 `pg_proc` 的 `pg_depend` 边集里，**零**条指向 v1 关系。
★ 6 个 live 函数提到 `request_logs`，**全部**是 `pg_class`/`pg_inherits` 的**目录名过滤**
（`analyze_llm_gateway_table_stats` · `apply_llm_gateway_autovacuum_settings` ·
`apply_llm_gateway_current_month_analyze_scale_factor` · `auto_rotate_to_columnar` ·
`columnar_healthcheck` · `cleanup_old_credential_model_index`）⇒ 匹配不到任何东西 ⇒ 无害。

### 3.3 canonical 视图：只有它还叫 request_logs

`request_logs_with_current_month` 是**唯一**幸存的 `request_logs%` 关系，**relkind = 'v'**。
live `pg_get_viewdef` 的 FROM/JOIN 目标**恰好四个**：

```
FROM session_turns_hot t
  LEFT JOIN session_turn_details_hot d
    ON d.tenant_id::text = t.tenant_id::text AND d.request_id = t.request_id AND d.partition_date = t.partition_date
UNION ALL
FROM session_turns t
  LEFT JOIN session_turn_details d
    ON d.tenant_id::text = t.tenant_id::text AND d.request_id = t.request_id AND d.partition_date = t.partition_date
```

⇒ **两臂会话族体，零 v1 关系**（v1 第三臂已由 890 去掉）。

★ ★ 关于 `sql/objects/views/` 里那 5 个**带 v1 臂**的文件
（`customer_cost_view` · `model_cost_per_task_view` · `v_continuation_effectiveness` ·
`v_node_switch_analysis` · `v_timeout_effectiveness`）：★ 它们带
`dbinit:baseline-v1-fallback` 标记 ⇒ **基线时点**的形态（`CREATE VIEW` 会立即校验
关系，基线时 `session_turns_hot` 还不存在 ⇒ 基线**必须**带 v1 臂）⇒ ⇒
由 883/884 在启动链里切换（`installer/internal/dbinit/runner.go:1180` / `:1194`），
**顺序在 890（`:1205`）之前** ⇒ ⇒ **不是漏项，是设计**。

### 3.4 生产 Go 代码里的 v1 读点：清单为空

★ 复刻仓内门扫描器（`admin/request_logs_read_inventory_test.go:260`
`scanRequestLogsReaders` ＋ `admin/session_family_sql_shape_test.go:345`
`retiredV1ReadPattern`）在生产 Go 上跑 ⇒ **零命中**。
★ 原始命中 **434** 处，**逐条归类**后全部落在：`_test.go` / 注释 / SQLite 平面。

★ 剩下三处**非执行**残留（人工清单，本轮只登记不删）：
- `deploy/prometheus/rules/integrity-fingerprint-drift.yml:112` 的 runbook 里
  写着 `SELECT count(*), count(system_fingerprint) FROM _request_logs_hot …`
  ⇒ ★ **运维照着跑会 42P01** ⇒ 且有测试（`integrity_fingerprint_drift_test.go:173`）
  **把这个坏查询钉住** ⇒ 修它要连测试一起改 ⇒ **下一轮项**。
- `deploy/prometheus/rules/partition-health.yml:107,135` 的 `description:` 散文。
- `deploy/prometheus/rules/routing-analytics-mv-consistency.yml:91` 提到的
  `request_logs_with_current_month_without_customer_id`（890 已删）。

### 3.5 台账：两个台账，别看错

| 台账 | 行数 | 写入方 | 记 900–920 吗 |
|---|---|---|---|
| `public.schema_migrations` | 74 | 自注册迁移 + 安装器 | **否**（max=899） |
| `public.gateway_db_revision_sequences` | 287 | 部署通道 `scripts/apply-db-revision-sequence.sh:2840` | **是**（20 行，**900–917 + 919 + 920**；★ R182 轮订正：★ 原文「900–920 全在」不成立 ⇒ ★ **迁移 918 根本不存在**（`sql/migrations/startup/` 无 `918_*.sql`）⇒ ⇒ 编号本身跳过） |

★ ★ **「schema_migrations 里没有 900–920」是个记账缺陷，不是「没上」的证据** ——
逐条核对每条迁移的**特征产物**（索引/约束/函数/种子行）⇒ **900–920 全部已应用**。
★ 部署通道按 `gateway_db_revision_sequences` 判「已应用」，安装器按自己的启动链
幂等执行 ⇒ ⇒ **不会重复应用**；但**运维读 `schema_migrations` 会误判成「待应用」**
⇒ ⇒ **下一轮项**：把 900–920 回填进 `schema_migrations`，或把权威台账写进文档。

---

## 四、★★ 本轮最值钱的一处：推翻一条沿用多年的旧结论

### 4.1 被推翻的结论

`admin/logs_turns_source.go` 的文件头（注 2026-09-30）写着：

> 「本开关保持默认 false —— 理由是**原生源不是全量日志视图的等价替代**。
> 真库实测：视图里 2,321,464 个 request_id 有 641,452 个（27.6%）在原生源查不到
> （无会话头流量：探针/自检按设计排除、in_progress 占位、标题/摘要生成器回环）。」

### 4.2 为什么它必须被推翻

★ 那条理由的**前提是「v1 还在」**。★ 迁移 890 把 canonical 视图的**第三臂**
（冻结 v1 体 + 双反连接去重）去掉之后，视图**本身就是**会话族两臂体
（`session_turns_hot ∪ session_turns` + 734 details 层）
⇒ ⇒ **视图与原生源返回同样的行** ⇒ ⇒ 「视图 vs 原生源」这个区分**已经没有对象**。

★ **实测对拍**（本地真库，2026-10-29）：

| 口径 | 结果 |
|---|---|
| view 的 distinct request_id / 行数 | 2,810 / 2,810 |
| native 的 distinct request_id / 行数 | 2,810 / 2,810 |
| native − view | **0** |
| view − native | **0** |

★ ★ 而且它是**结构性的**：两者的 FROM/JOIN **逐字相同**，唯一差别是视图那版带
`::text` 冗余转型（varchar→text 是恒等）⇒ ⇒ 行集必然相同，**与数据量无关**
⇒ ⇒ **不是「小库看不出来」**。

> ★★★ R182 轮订正（★★ 陈述的**差别位置指错** ★★★）：★★
> ★ 原文说「唯一差别是**视图那版**带 `::text`」⇒ ★★ 但仓内三处正典
> ★ （`sql/objects/views/request_logs_with_current_month.sql:147`、
> ★ `db/request_logs_view_schema.go:699/704`、`sql/migrations/startup/890:235/242`）
> ★ 的 **JOIN 一律是 `d.tenant_id = t.tenant_id`（无 `::text`）**，native 侧
> ★ （`:869/873` / `:921/928`）**同样无 `::text`**；★ `::text` 只出现在**投影**里
> ★ （`t.tenant_id::text AS tenant_id`），且**两侧都有** ⇒ ⇒ 「唯一差别」在仓内
> ★ **不存在**。★★ **结论比原文更强**：两者的 JOIN 条件与 118 列投影
> ★ （名字与顺序）**逐字相同** ⇒ ⇒ 行集相同是**恒等**命题 ★ ⇒ **定性不变，
> ★ 只是论据的位置要改对**。

★ ★ 仓内守卫 `admin/session_view_dependency_risk_test.go` 在 **2026-10-20（890 那轮）**
就已经把不变式从「必须留在视图上」换成「**读的源必须覆盖会话族的两条腿**（hot ∪ 母表）」
⇒ ⇒ 本轮**不是推翻守卫，是把守卫早已更新的结论落到列表路径上**。

★ **252 生产库同口径对拍（★ 2026-10-29 只读）**：

| 口径 | 行数 | distinct request_id | `sum(hashtext(request_id))` |
|---|---|---|---|
| view | 1,154,122 | 1,154,122 | 313,352,079,703 |
| native | 1,154,128 | 1,154,128 | 315,377,628,893 |

⇒ ★★ **差 6 行** ⇒ ★ **诚实说明**：★ 这两条是**先后两条独立语句**，
★ 而 252 是**持续有流量的生产库** ⇒ ⇒ **两次读数之间落进的新请求**就会造成这个量级的差
（★ 6 行 / 115 万行 ≈ 0.0005%）⇒ ⇒ ★★ **量具口径提醒：★ 在活的库上比较两个口径，
★ 必须放进【同一条语句】（同一个 MVCC 快照），★ 否则读数差里混着【采样窗口差】** ★★
⇒ ⇒ ★ 与本地（★ view 2,810 / native 2,810 / **双向差集都是 0** ★）一致
⇒ ⇒ 且 §5.4.2 的**端到端对拍**（★ 同一时刻、同一窗口、同一连接 ⇒ API 2,778 ==
改前直查 2,778 ★）是**单快照**口径下的精确一致 ⇒ ⇒ **换形态不丢行** ★★

### 4.3 为什么必须推翻它（不推翻就做不了性能修复）

★ canonical 视图的 **118 列投影被冻结**（R37 定案「118 列同名同序不许改」，
见 `admin/canonical_read_points_registry_test.go:228`）
⇒ ⇒ 视图**不可能**暴露 `partition_date`
⇒ ⇒ ★★ **分区裁剪在视图形态下不可达** ⇒ ⇒ 列表路径要拿到裁剪，
**只能走原生形态 + 带分区键谓词** ★★

⇒ ⇒ 两条独立理由（行集相同 ⇒ 不丢行；裁剪不可达 ⇒ 必须换形态）同时成立，
才能让「列表路径换形态」既安全又必要。

---

## 五、★★★ 性能：分区裁剪此前完全失效

### 5.1 机制

★ `public.session_turns` / `session_bodies` / `session_turn_details` 全部按
**`partition_date`** 做 RANGE 分区（`pg_get_partkeydef` 实测）。
★ PostgreSQL 的**分区裁剪只认分区键谓词** ⇒ ⇒ 只按 **`ts`** 过滤的读路径
**一个分区都裁不掉**。

### 5.2 真库 A/B（本地 PG17，`session_turns` 有 5 个月分区 + default）

```sql
-- 现状（只按 ts）
Append
  -> Index Scan using session_turns_2026_07_ts_idx on session_turns_2026_07
  -> Index Scan using session_turns_2026_08_ts_idx on session_turns_2026_08
  -> Seq Scan  on session_turns_2026_10            ← ★ 整分区顺序扫描 ★
  -> Index Scan using session_turns_2026_11_ts_idx on session_turns_2026_11
  -> Index Scan using session_turns_default_ts_idx on session_turns_default

-- 加 partition_date 下界后
Seq Scan on session_turns_2026_10                  ← ★ 只剩 1 个分区 ★
```

### 5.3 生产侧同形态（仓内台账，非本轮推断）

`scripts/audit/asset/query-plan-observations.tsv:67`（252 的 **EXPLAIN 计划形态**观测）
> ★★★ R182 轮订正（★★ 事实性错误 ★★★）：★★ 原文把这份台账标成
> ★ `pg_stat_statements` 观测 ⇒ ★★ **错**：★ 它的头注释写明「生成器：
> ★ `cmd/tools/query_plan_form`（★ EXPLAIN，★ **不带 ANALYZE** ⇒ 只读库安全）」
> ★ ⇒ 全文件 `pg_stat_statements` 出现 **0 次** ⇒ ⇒ 它记的是**计划形态**
> ★ （扫了哪些关系、用的什么索引），**不是**执行期统计。★ 同一错标也已进入
> ★ `admin/session_partition_bounds.go` 与 `admin/logs_turns_source.go` 的注释
> ★ ⇒ ⇒ **本轮三处一并订正**（否则下一轮会去查 pg_stat_statements 找错东西）。：
`/api/logs` 的**列表**语句 `stmt-1d7be58136` 在**1 秒窗口**上
**Seq Scan 了 4 个整月分区**（`session_turns_2026_07 / _08 / _11 / default`）。
⇒ 全表 104 条实测形态里，**20 条（19.2%）** 的 `other_seq_rels ≥ 4`。
> ★★★ R182 轮订正（★★ 数字错 ★★★）：★★ 原文写 **21 条（20%）** ⇒ ★★ 实测末列
> ★ 分布为 `0`×84、**`4`×17、`5`×3** ⇒ ⇒ `≥4` 共 **20** 条 / 104 = **19.2%**
> ★ ⇒ 用「`arm_scans` 含 `:Seq Scan`」「`other_seq_rels > 0`」两种独立口径交叉验算，
> ★ **均为 20** ⇒ ⇒ 原文的 21 **多算了一条**。

### 5.4 本轮的修法（真实 SQL 实测）

★ **改动面**（全部是 Go，无 DDL、无迁移）：
- `db/request_logs_view_schema.go`：新增 `SessionFamilyTurnsSourceWindowedSQL(pdFrom, pdTo)`
  —— 两臂各自带 `partition_date` 下界，**且 details 的 ON 子句也带**
  （`d.partition_date = t.partition_date` 已在 ⇒ ⇒ 加进去是**逐行等价的冗余谓词**
  ⇒ ⇒ 但它让 PG 能在**规划期**裁掉 details 的月分区）。
- `admin/session_partition_bounds.go`（新）：`sessionPartitionDateBounds(start, end)`
  —— 把 ts 窗口折算成 partition_date 上下界（**UTC 日历日**）。
- `admin/logs_turns_source.go`：`logsSourceFromSQLWindowed(pdFrom, pdTo)`。
- `admin/logs.go`：`listLogs` 改走窗口化原生形态；**参数编号整体后移两位**
  （$1/$2 = partition_date 上下界，$3/$4 = ts 窗口，$5… = 过滤条件）。
- `admin/turns_list.go`：`/api/admin/turns` 的 WHERE 同样带上界。

★ **实测读数（真实生成的 SQL，不是手写等价物）**：

| 读点 | 改前 | 改后 |
|---|---|---|
| `/api/logs` 列表 · turns 父表臂 | 5 个分区 | **1 个**（`session_turns_2026_10`） |
| `/api/logs` 列表 · details 父表臂 | 3 个分区 Seq Scan | **1 个** |
| `/api/admin/turns` · 父表臂 | 5 个分区 | **1 个**（★ R182 轮订正见下 ★） |

★ ★ **不丢行的可证伪等式**：`partition_date` 的**列值** = `calendarDate(ts)` =
**UTC 日历日**（`domains/session/v2/partition_date.go`），**所有**写入方都用它
（`turn_writer.go:485` · `details_writer.go:159` · `bodies_writer.go:318,390` ·
`family_writers.go:127` · `session_aggregator.go:114` · `session_writer_v2.go:858`）
> ★★★ R182 轮订正：★ `session_writer_v2.go:857` 是 `}`；★ 实际调用在 **:858**
> ★ （`calendarDate(req.Timestamp)`）。★ 其余 6 处写入方行号逐字命中。
⇒ ⇒ 对任意 ts ∈ [start,end] 的行，它**必然**满足 partition_date ∈ 折算出的区间
⇒ ⇒ 该谓词是 ts 谓词的**蕴含条件** ⇒ ⇒ **不丢行**（精确过滤仍由 ts 谓词做）。

> ★★★ R182 轮订正（★★ 这一行的「5→1」**只覆盖父表臂** ★★★）：★★
> ★ `/api/admin/turns` 在 R181 的 HEAD 里**仍走视图形态**
> ★ （`admin/turns_list.go:126` = `FROM public.session_turns_with_current_month t`）
> ★ ⇒ ⇒ 它的 `partition_date` 谓词在**外层 WHERE** ⇒ ⇒ PG 把它推进视图臂后
> ★ **父表臂确实裁到 1 个分区** ✔ ★★ 但**同一张视图的 hot 臂带反连接** ⇒ ⇒
> ★ 反连接的**内侧**是 `Append`（父表全月分区）⇒ ⇒ **内侧没有任何
> ★ `partition_date` 谓词** ⇒ ⇒ ★★ **内侧仍会扫全部 5 个月分区** ★★
> ★ ⇒ ⇒ 所以「5→1」这句话**只对父表臂成立**，**不是整条语句的关系数**。
> ★ ★ 本轮**实测复现**（本地真库 EXPLAIN，视图形态 + 外层谓词）：
> ★ 父表臂 = `Index Scan on session_turns_2026_10`（1 个），
> ★ ★ 而反连接内侧 = `Append` 覆盖 `session_turns_2026_07/_08/_10/_11/default`（**5 个**）。
> ★ ⇒ ⇒ 下一轮要把这条路径也改成**原生两臂 + 谓词推进两臂**
> ★ （R182 工作区已把 `/api/admin/turns` 改成原生两臂形态 ⇒ 正是承认这条欠账）。

### 5.4.1 ★ 真实生成 SQL 的 A/B 实测（不是手写等价物）

★ 用**代码本身**导出 SQL（`logsSourceFromSQLWindowed` / `logsSourceFromSQL`），
同一窗口（2026-10-01 ~ 2026-10-29）、同一份数据（本地真库 2,778 行）：

| 读数 | 改前（视图 + 只按 ts） | 改后（窗口化原生形态） |
|---|---|---|
| `session_turns` 扫到的分区 | **5**（2026_07 / _08 / _10 / _11 / default） | **1**（2026_10） |
| `session_turn_details` 扫到的分区 | **3**（2026_10 / _11 / default）＋ hot | **1**（2026_10）＋ hot |
| Execution Time | 10.384 ms | **8.257 ms** |
| Planning Time | 35.4 ms | 38.8 ms |

★★ **诚实说明**：★ 本地库只有 2,778 行，**其余月份分区是空的** ⇒ ⇒ 它们的
扫描代价 ≈ 0 ⇒ ⇒ **本地的耗时差（−20%）显著低估了生产收益**。
⇒ ⇒ 生产收益要看**扫了几个关系**这条轴：★ 252 的台账里同一形态是
「**1 秒窗口 Seq Scan 4 个整月分区**」⇒ ⇒ 那 4 个分区在生产上**不是空的**
⇒ ⇒ 收益随保留月数**线性增长**。
★ ★ 而「代价轴」与「形态轴」**近乎正交**（§十一通用式 7）★：
本条的耗时差在本地很小，但形态差很大 ⇒ ⇒ **必须两条轴一起报**。

### 5.4.2 ★ 端到端口径验证（部署后实测，防「换了形态就悄悄少行」）

★ 部署新版本（2.5.8.2904）后对同一窗口做**三方对拍**：

| 口径 | 读数 |
|---|---|
| `GET /api/logs`（新路径）返回的 `count` | **2,778** |
| 直接 SQL 跑**改前的视图形态** `count(*)` | **2,778** |
| `GET /api/logs` 实际返回的 items 数（page_size=100） | 100（分页正确） |

⇒ ⇒ **新路径与旧路径行集完全一致** ⇒ ⇒ 换形态**没有丢一行** ★
（这正是 §四那条「行集相同」的结构性论证的**端到端复核**）。
★ 顺带实测：`GET /api/logs`（默认窗口）HTTP 200、**36 ms**、
返回 42 条；带显式窗口 HTTP 200、**49 ms**、count=2,778。

★★ **与 FIXME-14368 的关系（★ 别把这个 FIXME 误读成「列值不可信」）**：
那个 FIXME 说的是【**分区边界正典**是 Asia/Shanghai 零点，而【列值】取 UTC 日历日】
⇒ ⇒ 后果是「+08 每月 1 日 00:00~07:59 的行落进**上月**分区」
⇒ ⇒ **列值本身仍然是该行的 UTC 日历日** ⇒ ⇒ 按列值裁剪**不丢行**
⇒ ⇒ 所以本轮取**精确**边界（**不需要** ±1 天外扩）。
★ 边界由 `admin/session_partition_bounds_test.go` 的
`TestSessionPartitionDateBounds_SupersetOfTSWindow` 逐小时扫 +08 全天钉住。

### 5.5 ★ 没有改的两处（说清楚为什么）

> ★★★ R182 轮**推翻本节的穷举式表述**（★★ 这是一个**范围性错误** ★★★）：★★
> ★ 本节标题「没有改的**两处**」⇒ ★★ 读起来像【只差这两处】⇒ ★★ **不成立**：
> ★ ★ `db.SessionFamilyTurnsSourceSQL()`（原生两臂源）在生产上有 **~40 处**调用点，
> ★ ★ 其中**带 ts 窗口且零 `partition_date` 谓词**的**至少 17 处**本轮**没有**改。
> ★ ★ 最刺眼的一条是 **`admin/session_list.go:138/162`（`/api/admin/sessions` 列表）**
> ★ ★ —— 它与本轮修的两条（`/api/logs` 列表、`/api/admin/turns`）是**同形态兄弟**。
> ★ ★ 清单（★ R183 审计档 §二.6 有完整版）：
> ★ `admin/session_list.go:138/162` · `admin/logs.go:1297` · `admin/usage_enhanced.go:715` ·
> ★ `admin/turns_filter_options.go:534` · `admin/session_crosstalk_check.go:106/111` ·
> ★ `bg/routing_health_checks.go:899/903` · `admin/credential_monitor.go:1007/1018` ·
> ★ `admin/tenant_stats_logs_table.go:41/51` · `admin/provider_diagnose.go:248/534` ·
> ★ `admin/quality_correlations.go:255` · `admin/compression_stats.go:242` ·
> ★ `autoroute/recommend_v2.go:551` · `admin/data_lifecycle_attachments.go:418` ·
> ★ `admin/routing.go:2734` · `bg/credential_recovery.go:1755` ·
> ★ `domains/analysis/projectattr/store.go:308` · `discovery/discovery.go:1365` ·
> ★ `admin/data_lifecycle_metrics.go:111`。
> ★ ★ 加重项：★ 这个欠账**仓内早已挂号** —— `admin/session_crosstalk_check.go:88-93`
> ★ ★ 明写「⚠ D4：`ts` 不是 `session_turns` 的分区键 ⇒ `ts >= …` **不触发分区裁剪**」
> ★ ★ ⇒ ⇒ 本节把一条**已挂号**的欠账写成了「只剩两处」。
> ★ ⇒ ⇒ **正确表述**是：本轮改了**两条**读点，**不是**只差**两处**；
> ★ ⇒ ⇒ 其余 ts-only 读点已登记为下一轮项（★ R183 审计档 §二.6）。


★ **写路径的幂等探针没改**（`internal/sessionfamily/exists_by_key.go`）：
★ 探针问的是「这个 (tenant_id, request_id) 写过没有」⇒ 若加上 partition_date 界，
**另一个分区里的同 request_id 行就找不到了** ⇒ ⇒ 会**放松去重**
⇒ ⇒ 与 ON CONFLICT 目标 `(tenant_id, request_id, partition_date)` 的口径**不一致**
⇒ ⇒ **不改**（语义正确性优先于裁剪）。
★ 实测（真库有数据）：该探针 hot 臂走 `session_turns_hot_tenant_request_key`
（`Index Cond` 同时含 tenant_id 与 request_id ✅），父表臂走 `(tenant_id)` + Filter，
**无 Seq Scan**；**执行 0.4 ms**，真正的大头是**规划时间 29.8 ms/请求**
（写路径每请求重新规划，`db/db.go:73` 的 `QueryExecModeSimpleProtocol` ⇒ 无计划缓存）
⇒ ⇒ 「写入过程」的真实成本在**规划期**，不在扫描 ⇒ ⇒ 下一轮应攻「计划缓存」。

★ **`/api/admin/turns` 的 hot 臂反连接没改**：它带 `Index Cond: tenant_id` +
`Filter: request_id`（每 hot 行扫该租户全部索引项）
⇒ ⇒ 这是仓里 R172「variant C」两腿形态要解决的问题
（`internal/sessionfamily/turns_reads.go:236` 已有实现）⇒ ⇒ **下一轮项**。

---

## 六、★★ 第二个真缺陷：把「每次部署都卡死」的判据修掉

### 6.1 现象（**本轮亲历**：修之前，本地部署真的红了）

```
error: 台账 ↔ 产物对账失败：914_session_turns_origin_stage_check.sql
       本轮按指纹认账但其产物缺失
       ((SELECT count(*) FROM pg_constraint WHERE conname IN
         ('session_turns_origin_stage_check','session_turns_hot_origin_stage_check')
         AND convalidated) = 2 ⇒ f)
```

⇒ ★ 迁移腿**中止** ⇒ ⇒ **部署中止** ⇒ ⇒ 891/892 之后的迁移上不去
⇒ ⇒ ★ **211 与 252 同为分区表 ⇒ 那两库上这个 `= 2` 同样恒假**
⇒ ⇒ ★ 这也是「914 / 916 在 211 与 252 上一直执行不了」的隐藏原因之一。

### 6.2 真因

★ `session_turns` 是**分区表** ⇒ ★ PG 会把父表上的 CHECK 约束**自动克隆到每个分区**
⇒ ⇒ 本地实测：`conname IN (两个名字) AND convalidated` 的条数是 **7**
（父表 1 ＋ hot 1 ＋ 5 个分区各 1）
⇒ ⇒ **产物其实全都在，判据说缺**。

★ 修后实测（按「约束挂在哪个关系上」判断）：

| 口径 | 读数 |
|---|---|
| 按名字数 | **7**（父 1 + hot 1 + 5 分区克隆） |
| 按关系数（父表 + hot） | **2** ✅ |

⇒ 修法：`ledger_artifact_probe "914_…"` 改成
`JOIN pg_class/pg_namespace` 按**约束挂在哪个关系上**计数 ⇒ 恰好 2 ⇒ 恒真变恒真。
★ **语义不变**（两个约束必须【在父表与 hot 上】且【已校验】）。

### 6.3 通用式

★★ **判「产物在不在」要判【它挂在哪】，不要只判【它叫什么】** ——
分区表上【同名约束会有 N 份克隆】⇒ ⇒ 按名字计数 = 恒假的死闸 ⇒ ⇒ **静默卡死部署**。

---

## 七、`_request_logs*` 族的退役：三环境实测

| 环境 | 实测 | 台账 marker |
|---|---|---|
| 本地 `llm_gateway`（`llm-gateway-pg`，PG 17.10） | 10 个候选名**全部 absent** | 890 在 `schema_migrations` |
| 生产 **211**（`acc-pg17`） | 10 个候选名**全部 absent** | `executed-via-explicit-retirement`，`execution_sha256` 非空 |
| 生产 **252**（`pg-252-pg17`） | 只剩 compat 视图 | `executed-via-explicit-retirement`，`execution_sha256` 非空 |

★ **退役是显式的、属主授权的步骤**，不是自动重放：
`scripts/retire-request-logs-v1-family.sh`（`--preflight` 只读 / `--defer` 登记 /
`--execute` 真删 / `--undo-defer` 撤销），三个写模式都要 `LLM_GATEWAY_RETIREMENT_ACK=1`。
★ 迁移 890 **故意不在** `apply-db-revision-sequence.sh` 的自动重放列表里
（`scripts/apply-db-revision-sequence.sh:1968` 整行注释 + 1954-1968 的长注）
⇒ ⇒ 理由：它自带**非空闸** ⇒ 天然不可自动重放 ⇒ 躺在自动列表里会**每次部署都撞闸**。
★ ⇒ 用户诉求「把 `_request_logs*` 真正删除」**在三环境均已达成**；
★ 剩余的是**把 890 的 SQL 变更登记进部署序列** —— 已登记（三环境台账都在）。

---

## 八、模型请求与写入测试

★ 本机可用模型与用户给的清单**不一致**：`/v1/models?scope=all` 共 **106** 个，
但用户清单里**只有 `glm-5.3-flash` 在册**；
`minimax-m2.7` / `deepseek-v4-flash` / `mimo-v2.6-flash` / `grok-4.6` /
`claude-sonnet-5-5` / `gpt-5.6-luna` **本地不存在**（近似的有 `minimax-m3.1-flash` /
`deepseek-v4.1-flash` / `deepseek-flash` / `glm-5.3` / `glm-5.2` / `glm-5.1`）。
⇒ ★ 这是**本地环境供给面**的限制，不是迁移缺陷（前一轮审计档也记过同一现象）。

★ 实测：真实流量 ⇒ HTTP 200 + 真实 usage ⇒ 落库 ⇒ 回查计数增量对得上。

| 模型 | 提示词 | 结果 | 延迟 |
|---|---|---|---|
| `glm-5.3-flash` | ping | **200**（usage 217） | 20.5 s / 30.9 s（复测两次） |
| `minimax-m3.1-flash` | ping | **200**（usage 196） | 16.3 s |
| `minimax-m3.1-flash` | say OK. | **200**（usage 206） | 24.9 s |
| `minimax-m3.1-flash` | 审计类长提示词 | **200**（usage 2,203） | 117.6 s |
| `glm-5.3` | ping | **200**（usage 159） | 32.9 s |

★ **落库对账**：★ `session_turns_hot` 计数 **23 → 39（+16）**，
`session_turns` / `session_bodies` / `session_turn_details` 的父表计数不变
（★ hot → 月分区的晋升是异步的，父表增量要等下一个晋升周期）⇒ ⇒
**写入确实落到了会话族** ⇒ ⇒ 与 §二「v1 写不可达」一致。

★ ★ **一个诚实的旁证（★ 别把它读成回归）**：★ 测试中途出现过一批
`503 no_candidates`（`"tried":0`），★ 复现条件是**浏览器逐页验证（9 个 flow）
与模型压测并发跑**；★ 单跑同一请求即 **HTTP 200** ⇒ ⇒ 真因是**本地凭据槽位
争用**（★ `credentials.concurrency_limit = 4`，★ 而 `.env.local` 的
`LLM_GATEWAY_DB_MAX_CONNS=16`）⇒ ⇒ **不是**本轮改动引入的
（★ 本轮改的只有 admin 读 SQL，★ 而 `no_candidates` 发生在**候选解析阶段**，
★ 早于任何一条 admin 查询）★★ ⇒ ⇒ ★ **教训：★ 本地并发跑「浏览器验证 + 模型
压测」时，★ 出现的 503 要先分清是【资源争用】还是【代码回归】——
★ 判据是【单跑复现不现】+【失败发生在哪个阶段】。★★** ★★

---

## 八之二、Maestro 逐页浏览器验证

★ `maestro/run-local.sh`（★ 01–09 共 9 个 flow，★ 不含 dev-only）实跑：
Maestro 2.11.0 + 本机 Chrome + **Temurin JDK 17.0.21**
（★ JDK 在 `~/kaixuan/jdk17/Contents/Home` ⇒ 需 `MAESTRO_JAVA_HOME` 显式指定
⇒ ★ **macOS 的 `/usr/libexec/java_home` 在本机找不到任何 JRE** ⇒ ★ 默认路径会直接失败）。

| Flow | rc | FAILED | WARNED |
|---|---|---|---|
| 01-public-guest | 0 | 0 | 0 |
| 02-overview-live | 0 | 0 | 0 |
| 03-providers-models | 0 | 0 | 0 |
| 04-tenants-portal | 0 | 0 | 0 |
| 05-routing | 0 | 0 | 0 |
| 06-keys-requests | 0 | 0 | 0 |
| 07-admin-config | 0 | 0 | 0 |
| 08-admin-data-ops | 0 | 0 | 0 |
| 09-ops-maintain | 0 | 0 | 0 |

⇒ ★★ **9/9 flow 全绿，FAILED=0，WARNED=0** ★★
⇒ ★★ 而**每个路由的「慢页判据」就是那批 `slow-check`**
（★ 形状：`assertTrue: ${output.ms__<route> < 2000}` ⇒ ★ **超过 2 秒即 WARNED**）
⇒ ⇒ **WARNED=0 ⇒ ★ 没有任何一个页面超过 2 秒** ⇒ ⇒ ★★ 用户那条
「对于超过 2 秒打开的网页，需要继续检查其慢的原因」**本轮无触发对象** ★★
（★ 产物：`maestro/reports/20261029-144354/summary.txt` + 每 flow 的 `.log`）。

---

## 九、本轮明确没做（留给下一轮，别重复排查）

1. ★ **`session_summary_dim.go` 的「开关为 true ⇒ `session_summaries` 聚合停摆」**
   （§二.4 危害 A）—— ★ 唯一「退役语义仍在支配活行为」的地方 ⇒ **头号项**。
2. `admin/data_lifecycle_hot_partition.go` 的 promote 守卫方向反了（§二.4 危害 B）。
3. `domains/session/dual_writer.go` —— 零调用方 + v1 主腿（重新接上就是**每个请求 42P01**）⇒ 建议删。
4. `cmd/gateway/session_write_safety.go:41-43` 的运维提示仍写「V1-only: …=true (no V2, no PG required)」
   ⇒ ★ **没有 v1 表可写**，这句是**错的运维指引**。
5. `schema_migrations` 补 900–920 的行（或把权威台账写进文档）—— §三.5。
6. Prometheus runbook 里 3 处 stale 的 v1 引用（其中 1 处被测试钉住）—— §三.4。
7. `/api/admin/turns` 的 hot 臂反连接换 R172「variant C」两腿形态 —— §五.5。
8. 写路径的**计划缓存**（每请求 29.8 ms 规划时间）—— §五.5。
9. `admin/logs.go` 详情路径的**两阶段化**（把 details JOIN 挪到 LIMIT 之后）
   ⇒ 现在 `Sort` 仍在 `Limit` 与 `Append` 之间 ⇒ LIMIT 未下推。
10. **本轮带进来的 R181 量具产物**（`query_plan_form` 的 `sort_keys` 列 + 排序列抽取）
    —— 已继承并通过编译，但**尚未跑完整门 + 变异臂** ⇒ **下一轮补完**。

---

## 十、通用式（`LESSONS.md` §564）

1. ★★★ **判「产物在不在」要判【它挂在哪】，不要只判【它叫什么】** ——
   分区表上同名 CHECK 会被 PG 克隆到每个分区 ⇒ 按名字计数 = 恒假的死闸
   ⇒ ⇒ **静默卡死整条部署腿**（且在另外两个环境上同样恒假）。
2. ★★★ **「旧结论」要复核它的【前提】是否还在** ——
   「原生源少 27.6% 的行」的前提是「v1 还在」；890 之后前提消失 ⇒ 结论失效
   ⇒ ⇒ ★ 而它失效**不是小事**：它挡着「列表路径换原生形态」，也就挡着分区裁剪。
3. ★★★ **分区裁剪只认分区键谓词** ⇒ ⇒ 读路径若只按 `ts` 过滤 ⇒ **一个分区都裁不掉**
   ⇒ ⇒ 「有 ts 窗口」≠「有裁剪」⇒ ⇒ 凡是按月分区的表，读路径都要自带分区键谓词。
4. ★★ **加分区键谓词前必须先证「它是原谓词的蕴含条件」** ——
   `partition_date = calendarDate(ts)`（所有写入方同口径）⇒ 蕴含成立 ⇒ 不丢行；
   ⇒ ★ 而**幂等探针**那种「同键跨分区也算写过」的语义 ⇒ 加界会**放松去重** ⇒ **不能加**。
5. ★★ **分区键列值的口径要跟写入方对账** ——
   「分区边界正典（+08 零点）」与「列值口径（UTC 日历日）」**是两回事**：
   ★ FIXMEs 常常只描述前者 ⇒ ⇒ 照着 FIXME 读会误判「列值不可信」⇒ ⇒ 过宽放行。
6. ★★ **换读形态前要做【双向差集对拍】** —— view−native 与 native−view **两个方向**都要为 0；
   只看一个方向会漏掉「视图比原生源多」的那一半。
7. ★★ **生产台账里的「慢」要与「扫了几个关系」两条轴一起看** ——
   成本轴（mean ≥ 2 s / total ≥ 5 min）**看不见**「1 秒窗口扫 4 个整月分区」这类形态。
8. ★★ **门归零后立刻做变异臂；变异臂的注入动作本身要断言落盘** ——
   本轮两臂（M1 去掉 details 侧下界 / M2 退回视图形态）**都精确报红**，
   还原后 **md5 逐字一致**。

---

## 十一、复现命令

```bash
# ① 三环境 v1 族退役实测（只读）
#   本地
PSQL_CONTAINER=llm-gateway-pg PGPASSWORD=… psql -U llm_gateway -d llm_gateway -Atqc \
  "select table_name from information_schema.tables where table_schema='public' and table_name like '%request_log%';"
#   211 / 252：先 scp scripts/retire-request-logs-v1-family.sh + 890 迁移，再跑 --preflight

# ② 分区裁剪 A/B
psql … -c "EXPLAIN (COSTS OFF) SELECT request_id FROM public.session_turns WHERE ts >= '2026-10-01' AND ts <= '2026-10-29';"
psql … -c "EXPLAIN (COSTS OFF) SELECT request_id FROM public.session_turns WHERE ts >= '2026-10-01' AND ts <= '2026-10-29' AND partition_date >= '2026-10-01' AND partition_date <= '2026-10-29';"

# ③ 视图 ≡ 原生源（双向差集）
psql … -c "select count(*) from (select request_id from (select t.request_id from public.session_turns_hot t union all select t.request_id from public.session_turns t) n except select request_id from request_logs_with_current_month) x;"

# ④ 全库 pg_depend 零 v1 边
psql … -c "WITH vo AS (SELECT c.oid,c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN ('v','m')) SELECT vo.relname, src.relname FROM vo JOIN pg_rewrite rw ON rw.ev_class=vo.oid AND rw.rulename='_RETURN' JOIN pg_depend d ON d.objid=rw.oid AND d.deptype='n' JOIN pg_class src ON src.oid=d.refobjid JOIN pg_namespace sn ON sn.oid=src.relnamespace AND sn.nspname='public' WHERE src.relname like '%request_log%';"

# ⑤ 本轮门 + 变异臂
go test ./admin/ -run 'TestSessionPartitionDateBounds|TestLogsSourceFromSQLWindowed|TestSessionFamilyTurnsSourceWindowedSQL|TestBuildTurnsListWhere' -count=1
```
