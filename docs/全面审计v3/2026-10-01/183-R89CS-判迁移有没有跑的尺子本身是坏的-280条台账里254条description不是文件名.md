# 183 号 · R89-CS —— 判「迁移有没有跑」的尺子本身是坏的：280 条台账里 **254 条（91%）的 description 不是文件名**

> **日期**：2026-10-01
> **轮次**：R89-CS（第 83 轮，审计第 183 号）
> **类型**：**方法论纠错 + 类化**（改写 182 号的一条判据；坐实 182 号的主结论）
> **改动生产代码**：无　**改动数据库**：无（只读 SELECT）
> **上一轮**：182 号（41 个迁移编号撞车 + 改写 181 号根因）

> ## ⚠️ 本报告 §五 的「118 条无内容校验」已被 184 号升级为部署级阻断
>
> 184 号（[R89-CT](2026-10-01/184-R89CT-73条大于412的迁移全部会被部署脚本硬拒-checksum对账在真库上0通过.md)）查明：
> `db-changelog.sh` 对 **≥412**（`DB_LEDGER_RECONCILE_FROM:-412`）的迁移做 checksum 对账，
> **缺记录时是 `_db_err … 拒绝部署` + `return 1`（硬拒，不是警告）**；
> **118 条里有 73 条落在 ≥412 区间，真库复刻判定结果：0 条能通过**。
>
> **⇒ 本报告把它记为「可观测性缺口」是量级不足，正确量级是 P1 部署阻断（待裁决 75）。**

---

## 〇、起手

181 号说「`domain/346` 没跑，是因为编号被 `startup/346` 占用」。
182 号推翻了它，并给出 playbook **§73 第 3 条**：

> 「判『某迁移是否执行过』必须三方对齐：文件名 + `schema_migrations.version` + `schema_migrations.description`。」

**本轮本来是去查其余 5 个 migrations 子目录的投递状态（`local` / `manual` / `operations` / `test` / `timeout-optimization`）。
结果在核对「description 能不能当文件名用」时，发现 §73 第 3 条这条判据本身不成立。**

---

## 一、F1：类化 182 号 —— **6 个子目录，5 个无投递通道**

| 目录 | `.sql` 数 | 被 `.go` 引用 | 实际投递通道 |
|---|---|---|---|
| `startup/` | 789 | — | ✅ **Shell 脚本**（见 F2） |
| `domain/` | 93 | 1 处**注释** | ❌ 无 |
| `test/` | 5 | 0 | ❌ 无（测试用，合理） |
| `manual/` | 2 | 0 | ❌ 无（人工，合理） |
| `timeout-optimization/` | 3 | 1 处**注释** | ❌ 无 |
| `local/` | 1 | 0 | ❌ 无 |
| `operations/` | 1 | 0 | ❌ 无 |

**⇒ `domain/` 那 93 个文件（占全仓迁移文件的 10.4%）确实没有任何执行器。**
**⚠️ 但那 2 处 `.go` 引用必须点名**：
`domains/modelquality/discovery.go:90` 与 `admin/session_online.go:12`
**两处都是注释里的路径提及，不是 `ReadDir`/`Walk`** ——
**「代码里出现了这个目录名」与「代码会读这个目录」是两回事**（§45、§66 同族）。

---

## 二、F2：🔴 找真正的投递通道 —— **不是 Go，是 Shell**

182 号查 `grep -rn "migrations/domain" --include=*.go` 零命中，**那个检索本身没错，
但它只能证明「Go 不扫这个目录」，不能证明「没有执行器」**（§72 第 3 条：检索形态的边界）。

本轮改查全仓，找到三个真正干这事的脚本：

```bash
scripts/apply-missing-migrations.sh   → 只引用 migrations/startup
scripts/local-r112-migrate.sh        → 只引用 migrations/startup
scripts/migrate-db-kaixuan1.sh       → 只引用 migrations/startup
```

**⇒ 三个投递脚本都只扫 `startup/`，`domain/` 出现 0 次。182 号的主结论成立。**

**⚠️ 另有一条 Go 侧的完整通道**：`scripts/deploy-lib/db-changelog.sh:515`
带 `pg_advisory_xact_lock` + `llm_gateway_migration_checksums` 校验和 ——
**这是全仓唯一做迁移幂等与内容校验的通道**，而 182 号与本轮一开始都没注意到它。

---

## 三、🔴 F3：本轮的核心纠错 —— **description 91% 不是文件名，§73 第 3 条的判据不成立**

182 号的 §73 第 3 条教人「三方对齐（文件名 + version + **description**）」。
**本轮量了真库，这条判据不能用：**

```sql
SELECT count(*)                                            AS total,       -- 280
       count(*) FILTER (WHERE description LIKE '%.sql')    AS looks_like_filename,  -- 18
       count(*) FILTER (WHERE description NOT LIKE '%.sql')AS free_text    -- 254
FROM public.schema_migrations;
```

**⇒ 280 条里 254 条（90.7%）的 `description` 是自由文本或标识符，不是文件名。**

| version | description（自由文本样例） |
|---|---|
| 025 | `Tool registry enhancements: version mgmt, usage stats, permi…` |
| 044 | `health_source_probe_now` |
| 100 | `credential_state_machine` |
| 351 | `credential_most_used_model (BUG #5 fix, 2026-07-22 by fix/au…` |
| 2026-07-13-multimodal-token-fields-hot | `P0 fix: missing multimodal columns on request_logs_hot block…` |

**⇒ 连 `version` 都不是纯数字**（`2026-07-13-multimodal-token-fields-hot` 这种日期串也在里面）
⇒ **「按编号对齐两个目录」这个前提本身在台账上就不成立。**

**⇒ 唯一那 18 条 `%.sql` 的（330–347 连号）恰恰是最特殊的一批**：
它们**全部是 `startup/` 目录的文件名**（`330_usage_ledger_partition.sql` …
`347_credential_model_index_hot_independence.sql`），
**与三个投递脚本「只扫 startup」完全吻合** ⇒ **那 18 条是三条脚本写进去的**。

**⚠️ 但这三者互不自洽，本轮抓到一处硬矛盾：**

| 源 | 写入的 description |
|---|---|
| `apply-missing-migrations.sh:93` 硬编码 | `'346', 'routing_decision_log_hot_independence'` ← **无 `.sql`** |
| **真库 346 实际值** | `346_routing_decision_log_hot_independence.sql` ← **有 `.sql`** |

**⇒ 346 那条台账行不是当前版本的 `apply-missing-migrations.sh` 写的**（值对不上）。

**⇒ 本轮继续追查「那 18 条到底谁写的」，三条候选逐一排除：**

| 候选 | 排除依据 |
|---|---|
| `apply-missing-migrations.sh` | ① 硬编码值是 `'routing_decision_log_hot_independence'`（无 `.sql`），**与真库值对不上**；② 该脚本只处理 4 个迁移，**写不出 18 条** |
| `local-r112-migrate.sh` | 清单里确有 `346_routing_decision_log_hot_independence.sql`（`:179`），**但全文 `grep -c schema_migrations` = 0** ⇒ **只应用 SQL、不写台账** |
| `deploy-lib/db-changelog.sh` | 是唯一写 `llm_gateway_migration_checksums` 的通道，**必带 checksum**；而真库 checksum 表 162 行里**没有 330–347 任何一条** ⇒ 被排除 |

**⇒ 结论：那 18 条台账行由「仓库里已不存在的执行路径」写入。**
**⚠️ 诚实边界：具体是哪条路径，本轮无法确定**（可能是早期版本的脚本、或是人工 `psql` 执行后手工补录）。
**⇒ 登记为「写入者未定位」，不猜。**

**⚠️ 顺带一个可观测性缺口**：`llm_gateway_migration_checksums` **162 行 vs `schema_migrations` 280 行**
⇒ **118 条迁移（42%）没有任何内容校验** ⇒ **改这些迁移文件的内容不会被任何机制发现**。

---

## 四、F4：顺带澄清 182 号一处可能被误读的地方（结论不变）

182 号报「`domain/` 全目录 0 条进台账」时，用的检索是
`description LIKE '%system_probe_run%' OR '%credential_probe_queue%' …`，**命中 4 条**。

**本轮逐条核对，这 4 条全部是 `startup/` 侧的滚动迁移**：

| version | `migration_name`（checksum 表） | `description`（台账） |
|---|---|---|
| 488 | `488_credential_probe_queue_startup_backfill.sql` | `credential_probe_queue_startup_backfill` |
| 489 | `489_credential_probe_queue_runtime_columns.sql` | `credential_probe_queue_runtime_columns` |
| 490 | `490_credential_probe_queue_runtime_columns.sql` | `credential_probe_queue_runtime_columns` |
| 644 | （checksum 表无） | `rollup: … self_check_runs_selection_strategy_check …` |

**⇒ 182 号的结论「`domain/` 0 条」成立。**
**⚠️ 但注意 488–490 这一行同时展示了 §73 判据会怎么骗人**：
**同一件事在两张表里 description 带不带 `.sql` 都不一样**
⇒ **「拿 description 去匹配文件名」在 91% 的行上根本不成立**。

---

## 五、结论与定级

| 项 | 定级 | 依据 |
|---|---|---|
| **§73 第 3 条判据**（三方对齐） | 🔴 **撤回并改写** | 91% 的 description 不是文件名；version 甚至不是数字 |
| **182 号主结论**（`domain/` 0 条、无执行器） | ✅ **维持 P3** | 三个投递脚本实测只扫 `startup/` |
| **6 个子目录中 5 个无投递通道** | ✅ **新增事实** | `domain` 93 / `test` 5 / `manual` 2 / `timeout-optimization` 3 / `local` 1 / `operations` 1 |
| **那 18 条 `.sql` 描述的写入者** | ⚠️ **未定位** | 已排除 `apply-missing-migrations.sh`（值对不上）与 `db-changelog.sh`（无 checksum 行） |
| **`llm_gateway_migration_checksums` 仅 162 行 vs 台账 280 行** | ⚠️ **登记** | 118 条迁移**无内容校验** ⇒ 改文件不会被发现 |

**⇒ 零新增待裁决**（74 号的修法建议随之调整：不是「给 `domain` 加执行器」，
而是「先决定 `domain/` 是历史归档还是待执行集」—— 93 个文件里哪些该跑，**需要人确认**）。

---

## 六、playbook §75 新增

> **§75 拿一个字段当匹配键之前，先量它「真的是那个东西」的比例 —— 90% 的行不符，就不能当键用**

**由来**（R89-CS / 183 号）：§73 第 3 条教人用 `schema_migrations.description` 去对齐文件名。
**实测 280 条里只有 18 条（6.4%）是文件名，254 条是自由文本。**
⇒ **这条判据在 93.6% 的行上会给出「不存在」的假答案。**

**⇒ 落地三条：**
1. **把某字段当匹配键之前，先量它的「像不像那个东西」的比例** ——
   一条 `SELECT count(*) FILTER (WHERE description LIKE '%.sql')` 就能决定它能不能用。
   ⚠️ **本仓 `version` 也不干净**（存在 `2026-07-13-multimodal-token-fields-hot` 这种日期串）
   ⇒ **连 `version` 都不能直接当整数用**；
2. **⚠️ 「Go 里零命中」只能证明「Go 不扫」，不能证明「没有执行器」** ——
   本轮改查全仓才发现三个 Shell 投递脚本 + 一个带 checksum 的 `db-changelog.sh`。
   ⇒ **找执行器要跨语言找**（§72 第 3 条「四种形态」的补遗：
   **Go / 非 Go 文本 / SQL 侧自动调用 / 外部脚本 —— 前一轮我把第 4 类当成了 pg_cron，漏了脚本**）；
3. **⚠️ 同名字段在两张表里格式可以不同** ——
   本轮 488–490 在 `llm_gateway_migration_checksums.migration_name` 带 `.sql`、
   在 `schema_migrations.description` 不带 ⇒ **「两张表同名字段」不代表「同一套取值」**。

**⇒ 与 §74 的关系**：
§74 说「对齐之后还要问『撞车解释不解释症状』」；
**本条说「在对齐之前，先确认你要用的那个键真的是键」——
若不是，对齐得越认真，结论越像真的、越错。**

**同族**：§16（否定结论分轮登记）/ §41（注释三桶）/ §45（命中 ≠ 引用）/
§66（守卫存在 ≠ 可达）/ §72（死代码看服务对象 + 四种检索形态）/ §73（三种失效形态）/ §74（撞车归因）。
