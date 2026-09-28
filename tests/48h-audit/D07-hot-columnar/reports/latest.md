# R79 · D07 hot+分区 · S-01 真库 EXPLAIN / 大分区实测

> 时间：2026-09-29 · 环境：本机 PostgreSQL 17.10（`llm-gateway-pg` 容器，arm64）
> · 计划项原标注「环境未提供」，实测时该库已健康运行 34 小时，标注属过期，改为实做

## 0. 一句话结论

`archive_request_logs_default()`（迁移 754）**自落地起从未成功执行过一次**。
真跑首访拿到 SQLSTATE 42703；把列名修好之后再跑，30 分钟被 statement_timeout
击杀并整笔回滚——因为它的「主键游标」在一张**没有主键**的表上。两处都已修，
端到端从「永远失败」变成 25.96 秒跑完 212 万行。

## 1. 环境与方法

| 项 | 值 |
|---|---|
| PostgreSQL | 17.10 (Debian 17.10-1.pgdg13+1)，`kx-citus-pg17:offline-arm64` 基础镜像 |
| 主机 | 32 GB 内存，多核，`/var/lib/postgresql/data` 剩 443 GB |
| 隔离 | 一次性数据库 `d07_s01_probe`；源库 `llm_gateway` **只读**（仅 SELECT 盘点） |
| 表结构 | `pg_dump -t request_logs --schema-only` 从真库导出后原样载入 probe（47 索引 + FORCE RLS + billing trigger + 两条 tenant policy），非手写复刻 |
| 数据 | 2,125,857 行 / **6070 MB** 单月分区，字段含 outbound_body / trace_events / tool_calls 等真实 JSONB（真库同规模分区为 2,125,857 行 / 4885 MB，本 probe 略重，基准偏保守） |
| 污染 | 测量期间 `pg_stat_activity` 仅本查询一条活动会话，load average 主要来自本查询本身；后端持续 ~88-90% CPU 且 `wait_event` 为空 ⇒ **纯 CPU-bound**，不是 I/O 瓶颈 |

## 2. P1-a：首跑即死（SQLSTATE 42703）

首跑 `SELECT * FROM archive_request_logs_default(7)`：

```
ERROR:  column "session_id" does not exist
HINT:  Perhaps you meant to reference the column "request_logs_2026_08.gw_session_id".
QUERY:  WITH candidates AS (
           SELECT id, request_id, ts, tenant_id, session_id, ...
```

candidates CTE 投影的 11 个摘要列里 **2 个在 request_logs 上不存在**：

| 迁移里写的 | request_logs 真实列名 | 基线 01-schema.sql | 真库 4 个分区 |
|---|---|---|---|
| `session_id` | `gw_session_id` (text) | 无 `session_id` | 全部 `session_id=false` |
| `status_code` | `upstream_status_code` (integer) | 无 `status_code` | 全部 `status_code=false` |

真库逐列探针（`d07_colprobe.sql`）在 `request_logs_2026_07/08/09/10` 上输出一致结果：
`session_id=f status_code=f gw_session_id=t upstream_status_code=t request_status=t`。
仓库基线 `01-schema.sql`（137 列）独立复核同一结论。

名字来源可追：754 头注引自 `docs/audit/2026-09-25-session-storage-audit-handoff.md §9`，
列清单照的是 **session_\* 族表**——`session_turns` 确实有 `session_id`。

### 2.1 为什么六轮审计 + 十几道门全部放行

1. **动态 SQL**：`format('%I')` + `EXECUTE`，列名运行时才解析。`CREATE FUNCTION`
   不校验 → 安装成功、台账自登记成功、升级通道全绿。
2. **D-01 是形状核对**：查文件在不在、函数名对不对、调用点接没接。形状全对，内容全错。
3. **整条链路从未被任何测试驱动过一次真执行**，线上也没有。于是 R73 的
   「每日 03:00 闸门」和 R75 的「statement_timeout 已钉在事务内」都属实，
   **但接线的那根线从第一天起就插在空插座上**。

### 2.2 修法

只改**源投影的 2 个列名**（`gw_session_id` / `upstream_status_code`）。
归档表自身的列名 `session_id` / `status_code` **保持不变**——那是有下游读方契约的
归档表自己的词汇。canonical 与 delivery 两份文件字节同步。

## 3. P1-b：批游标在一张没有主键的表上（N²）

修完列名再跑，30 分钟没跑完。EXPLAIN 出真相：

```
 Limit (actual rows=1000 loops=1)
   Buffers: shared hit=256 read=531262
   ->  Gather Merge
         ->  Sort  (Sort Key: id, Sort Method: top-N heapsort)
               ->  Parallel Seq Scan on request_logs_2026_08
                     (actual rows=708619 loops=3)
                   Filter: (id > 0)
```

**取 1000 行读了 531,262 个缓冲块（≈4.2 GB）。**

754 的分批是：

```sql
candidates AS (SELECT ... FROM <月分区> WHERE id > :last_id ORDER BY id LIMIT 1000)
```

头注称之为「1000 行/批 **主键游标**（partition 局部 id 唯一）」。但：

* `request_logs` **无主键**（基线 CREATE TABLE 内无 PRIMARY KEY；真库分区上唯一索引
  只有 `UNIQUE (request_id, ts)` 与一个 partial unique on `gw_session_id`）；
* **没有任何以 `id` 为首列的索引**（真库分区共 48 个索引，已逐个核对）。

于是每批次一次全分区扫描 + top-N 排序，成本 **O(批次数 × 分区行数) = O(行数²/1000)**：
2.1M 行 = 2126 批 × 4.2 GB ≈ **8.9 TB 缓冲读**。行数翻倍，成本四倍。

### 3.1 端到端（这就是 R73 修复没兜住的地方）

| 场景 | 结果 |
|---|---|
| 冷归档，无 id 索引 | `Time: 1800028.093 ms (30:00.028)` → **statement_timeout 击杀 → ROLLBACK** |
| 回滚后归档表 | **不存在**（DDL 与数据同事务，一起没了） |
| 回滚后源分区 | 2,125,857 行，不变（函数无 DELETE，符合契约） |

即：调用方 `SET LOCAL statement_timeout='30min'` + 30min Go ctx **不够用**。
被击杀 → 整笔回滚 → 次日 03:00 重跑同一分区 → **永久活锁**，与 R72 对 753 首扫的
诊断同型，只是在生产规模上复发。R73 把预算从 30s 抬到 30min，只是把墙推远了。

### 3.2 修法：迁移 756

`sql/migrations/startup/756_request_logs_id_index.sql`：

```sql
CREATE INDEX IF NOT EXISTS idx_request_logs_id ON public.request_logs (id);
```

在**分区父表**上建，PG 会下发到全部既有分区，此后 CREATE/ATTACH 的新分区自动继承。

**前后对比（同一分区、同一份数据）**：

| | 计划 | Buffers | 批次耗时 |
|---|---|---|---|
| 756 之前 | Parallel Seq Scan + top-N Sort | hit=256 **read=531,262** | ≈565ms（由 30min/2126 批反推） |
| 756 之后 | **Index Scan using request_logs_2026_08_id_idx** | hit=6 **read=230** | **1.294 ms** |

缓冲块 **2312×** 下降。建索引本身耗时 **2.33s**（2.1M 行分区）。

### 3.3 端到端修复后

| 场景 | 结果 |
|---|---|
| 冷归档（2,125,857 行） | **25.963 s**，rows_archived = 2,125,857，与源行数**完全一致** |
| 热重跑（幂等） | **8.179 s**，rows_archived = **0**，归档表仍 2,125,857 行（**无重复**） |
| 归档表体积 | **497 MB** / 源 6070 MB ≈ **8.2%**（头注称「≈5% 级」，实测略高；结论方向不变） |

## 4. 两条必须一起上线的耦合

只修 P1-a 不修 P1-b，链路会从「毫秒级失败（便宜且吵闹）」退化成
「每晚烧 30 CPU 分钟 → 到点被杀 → 整笔回滚 → 次日重来」的**永久活锁**，
且从此每天留下一条"归档失败"日志与一次全分区级 CPU 消耗。
**42703 的修复不是可独立部署的变更。**

## 5. 新增门禁（全部经变异检验）

| 门 | 位置 | 抓什么 | 变异检验 |
|---|---|---|---|
| D-02 跨源列名 | `data/archive_source_columns_test.go` | 基线 DDL 的 request_logs 列集合 × 754 投影列交叉校验（不需要数据库） | 改回 `session_id` → 红 ✅ |
| D-03 投影错位 | 同上 | INSERT 目标列序 × 源投影列序一一对应 | 打乱 `success`/`status_code` 顺序 → 红 ✅ |
| D-04 双副本一致 | 同上 | canonical vs delivery 字节一致 | 只改 canonical → 红 ✅ |
| S-01a 真跑 | `stress/archive_request_logs_exec_test.go` | scratch 库 + 真基线 DDL + 真 754，首跑必须成功且行数对得上 | 改回坏列名 → 42703 红 ✅ |
| S-01b 计划形状 | 同上 | EXPLAIN 批次查询，**自带对照**：建 756 前必须 Seq Scan、建 756 后必须 Index Scan | 把 756 改成空迁移 → 红 ✅ |
| S-01c 幂等 | 同上 | 冷/热两轮 + 归档表行数不重复 | — |
| S-01d 留存联锁 | 同上 | 0/6/-1/366/1000 必须 RAISE，7/30/365 必须放行 | — |

S-01b 的**对照步骤**是关键设计：它先断言「建索引前确实是 Seq Scan」，再断言
「建索引后确实是 Index Scan」。没有对照的话，「断言计划里有 Index」这类门
在解析器换版本时可能恒真或恒假——这正是本会话反复吃亏的那类空洞门。

## 6. 方法论教训

**L1｜静态形状门禁对动态 SQL 结构性失明。** 看到 `format(` + `EXECUTE`，就默认它
从未被执行验证过，并要求至少一条「真库真跑」的门。门禁应分三层：静态形状 +
跨源列名交叉校验 + 真库首跑；缺中间那层是本案的直接成因。

**L2｜小样本压测证明不了 N² 游标。** 本会话自建的 5000 行压测门在无索引时
**全绿**（34.5ms），因为对 5000 行做全表扫描很便宜。游标型缺陷的代价是
O(n²)，只有到生产量级才暴露。可推广：**凡「按某列排序 + LIMIT 分页」的批处理，
必须断言计划形状（EXPLAIN），而不是断言小样本耗时**。

**L3｜一份 SQL 修复落在双副本迁移的其中一份，等于没落。** 本轮 42703 修复先只改了
installer 的 delivery 副本，canonical（契约测试读的那份）仍是坏的——是 D-04 那道门
在下一轮把它抓出来的。双副本迁移必须有字节一致门。

**L4｜以「没跑过所以先不改」为理由保留的缺陷，都在欠一次实跑。** 754 头注原写
「本 SQL 从未在真实 PostgreSQL 上执行过，不宜在此盲改」——S-01 实测后证明：
不是「不宜盲改」，是**它根本没跑过**，而「不宜盲改」的判断本身就出自没跑过的人。

## 7. 登记未修（owner 裁决）

1. **归档月表未挂父表**：754 建的是独立 heap 表 `request_logs_archive_YYYY_MM`，
   未 ATTACH 到 `PARTITION BY RANGE (ts)` 的父表 `request_logs_archive`。该父表
   **分区数为 0**——唯一会 ATTACH 的旧函数 `archive_request_logs(date)` 已被迁移
   331 移除，而 **331 本身未进 installer startup 通道**
   （`installer/.../embeddata/startup/` 下无 331），所以基线又把父表建了回来。
   后果：未来任何读方写 `SELECT ... FROM request_logs_archive` 会**静默得到 0 行**
   而不是报错。现状「无任何读方」属实，但这是静默陷阱。
2. **归档函数无「已归档」标记**：每晚重扫全部超窗行。热重跑实测 8.18s /
   2.1M 行，随保留窗单调增长不自收敛。加 ledger 表是已登记的后续项，
   本轮**不做**——链路上限刚被 S-01 测出来，ledger 属于第二阶段优化。

## 8. 未纳入本轮的范围

- 245 / 252 两台真机未连。754/756 是否已应用、request_logs 真机索引构成需在部署前复核。
- 其他分区族（candidate_failure_logs / usage_facts / mock_probe_history 等）的
  同类「游标列是否有索引」问题未逐族检查——**本轮只查了 request_logs 一族**，
  但方法可直接套用，见 L2。

---

# R79 续 · promote_* 全族普查：把 S-01 的方法推到底

S-01 只修了 `request_logs` 一族。同一缺陷类（**批游标列没有首列索引 ⇒ 每批次全表
排序 ⇒ O(rows²/batch)**）在 hot→partition 的 `promote_*` 家族里是否复发，本节逐族核。

## 1. 方法

不解析迁移文件（死代码太多），直接对**真库**取 `pg_proc` 里全部 `promote_*` 函数体，
抽出批游标的首列与源表，再核该表是否真有以该列**开头**的索引（首列，不是「包含该列」；
且**排除部分索引**——见 §4）。

真库实测 **29 个 `promote_*` 函数 / 27 个批游标 / 18 个注册在 `promoteSpecs()`**。

## 2. Gate A：活函数的批游标首列索引

**18 个活函数里 16 个有首列索引，2 个没有**：

| 表 | 游标 | 现状 | 定级 |
|---|---|---|---|
| `candidate_failure_logs_hot` | `ts` | **无任何以 ts 开头的索引**（6 个索引全是 credential_id/provider_id/raw_model_name/request_id/session_id/aggregation_id 开头）。EXPLAIN：`Seq Scan + Sort`，取 28 行读 113 块 | **P2 潜在** |
| `auto_route_selections_hot` | `ts` | 仅有**部分**索引 `idx_ars_hot_unsettled(ts) WHERE settled_at IS NULL` 与 `idx_ars_hot_session(session_id,ts) WHERE session_id IS NOT NULL`；promote 谓词 `ts < statement_timestamp() - p_retention` 推不出这两个谓词，故用不上。EXPLAIN 仍带 `Sort` | **P2 潜在** |

### 为什么是 P2 而不是 P1

按当前摄入速率，这两张表永远走不到「多批次」：

| 表 | 现存量 | 时间跨度 | 摄入速率 | 8h 窗口预估 | 批次大小 |
|---|---|---|---|---|---|
| `candidate_failure_logs_hot` | 211 行 / 5.2 MB | 8h47m | ≈24 行/h | ≈190 行 | 5000 |
| `auto_route_selections_hot` | 862 行 / 872 kB | 2h24m | ≈359 行/h | ≈2,900 行 | 5000 |

两者都**不到一个批次**，所以 O(rows²/5000) 的那部分代价今天根本没被支付。
成本是**潜在的**、不是**已计费的**。写「P1」会是虚报。

### 一条被证伪的假设（记录下来，因为差点就写进报告）

初看 `candidate_failure_logs_hot` 里有 8h47m 的行，超过 8h 保留窗还没被 promote 走，
一度判「8h hot 不变式已破」。追下去发现：
`lifecycle.hot_retention_hours = 8`，promote 上一轮执行于 23:04:50，截止线 15:04:50，
而当时最老行是 15:05:32 —— **尚未到期**。promote 日志证实该表 23:04:50 正常排了 36 行。
按小时周期运行，8h 窗口的实际滞留上界是 8h + 1h，观测到的 8h47m 完全在界内。
**不变式没破，是我在验证之前就开始归因。**

## 3. Gate B：死函数引用不存在的表（普查副产物）

真库 29 个 `promote_*` 函数中，**11 个短名 `promote_*_batch` 在 Go 侧零调用方**，
其中 3 个引用的表**根本不存在**：

| 死函数 | 缺失表 | 父表 DEFAULT 分区状态 |
|---|---|---|
| `promote_credit_ledger_default_batch` | `credit_ledger_default` | `credit_ledger` 有 4 个月分区，**无 DEFAULT** |
| `promote_request_logs_bodies_default_batch` | `request_logs_bodies_default` | 有 2 个月分区，**无 DEFAULT** |
| `promote_tool_usage_stats_default_batch` | `tool_usage_stats_default` | **无 DEFAULT** |

**定级 P3 文档债**：无调用方 ⇒ 不会执行；父表无 DEFAULT 分区 ⇒ 根本没有可排空的数据。
风险是「上了膛的枪」——它看起来像个能用的排空函数，谁顺手接进 spec，第一次执行就 42P01。
**登记而不删**：删 schema 对象是迁移决策，本轮不替 owner 做。

## 4. 这道门自己踩的三个坑（比结论更重要）

写门的过程里，它先红过三次，每次都是**门自己有缺陷**而不是被测对象有问题：

1. **静默覆盖率 7/26**：正则里用了字面空格，而 `prosrc` 保留原始换行与缩进，
   只有 7 个单行函数体能匹配上。**如果当时写成「解析不出来就跳过」，这道门会立刻变成
   覆盖 27% 的真空门**——正是本会话反复吃亏的形态。改成 **fail-closed**
   （活函数解析不出游标即红）后，它当场把这个坑顶了出来。
2. **误报源表三连**：
   - 取首个 `FROM` → 命中 `NOT EXISTS` 里的 `session_turns archived`；
   - 改取最后一个 `FROM` → 命中更早语句里的 `pg_partitioned_table`（父表校验）；
   - 按首个 `ORDER BY` 定位 → 命中列清单推导里的 `ORDER BY attnum`。
   正确规则是**三条一起**：按「带 batch LIMIT 的那次 ORDER BY」定位 →
   切到该 plpgsql 语句内（最后一个 `;` 之后）→ 取语句内**首个** FROM。
3. **把部分索引算成「有索引」**：`idx_ars_hot_unsettled(ts) WHERE settled_at IS NULL`
   的首列确实是 `ts`，于是门报告 `auto_route_selections_hot` 已覆盖——
   而 EXPLAIN 明明显示规划器没用它、Sort 仍在。**首列匹配 ≠ 能用**。
   部分索引只在查询谓词蕴含其谓词时可用，这必须由 EXPLAIN 背书，不能由首列位置推断。

`livePromoteFunctions` 也踩过一次：先按整个文件扫 `fnName:`，把 `ensure_*`/`archive_*`/
`drop_*`（来自另外三个 spec 列表）也算成活函数，凭空要求 19 个不存在的函数。改为按花括号
匹配只取 `promoteSpecs()` 函数体，并剔除注释行（`promote_model_probe_runs_hot_to_partition`
在上游是被注释掉的）。

## 5. 变异检验（三处，全部由红转绿）

| 变异 | 期望 | 实测 |
|---|---|---|
| Gate A 白名单塞一个不存在的表 | 红（自收缩检查） | ✅ 红并指名 `totally_made_up_table` |
| 让 Gate A 看不见索引（`WHERE false`） | 红且报出全部活函数 | ✅ 红，报 **16 个**活函数缺索引 |
| Gate B 白名单塞一个不存在的函数 | 红（自收缩检查） | ✅ 红并指名 |

第二条特别重要：它证明这道门真在逐个检查 16-18 个游标，而不是象征性点几个。

## 6. 结论与后续

- **没有新增 P1。** S-01 修完的 `request_logs` 仍然是全族里唯一真正危险的那个，
  且已修复。其余 16 个活函数首列索引齐全。
- 2 项 P2 潜在债 + 3 项 P3 文档债，均已带测量数据登记在门禁白名单里，白名单自收缩。
- **未做**：其余分区族（`session_*`、`usage_*` 等非 hot 表）里是否存在同型游标，
  本轮只普查了 `promote_*`。方法可直接套用。

---

# R79 续二 · 全库批游标普查（不限 promote_*）

上一节把「批游标必须有首列索引」推到了 hot→partition 的 `promote_*` 全族。
本节再推一步：**public schema 里任何带批游标的函数**。

## 1. 范围

真库 `pg_proc` 全量扫描（`prokind='f'`），形态为 `ORDER BY <列> LIMIT <字面量|%L|批次参数>`，
排除 `ORDER BY 1 LIMIT 12` 那类「近 12 个月」枚举（不是游标）：

| 族 | 数量 | 处置 |
|---|---|---|
| `promote_*` | 27 | 上一节已普查，专项门管理 |
| **非 promote** | **4** | 本节处理 |

4 个非 promote 逐个结论：

| 函数 | 游标 | 源表 | 结论 |
|---|---|---|---|
| `archive_request_logs_default` | `id` | `%I`（动态） | **R79 上半已修**（754 补列名 + 756 补 id 索引） |
| `archive_request_wal` | `created_at` | `%I`（动态） | **死函数**，见 §2 |
| `ensure_request_logs_partition` | `ctid` | `request_logs_default` | 系统列游标，非缺陷，见 §3 |
| `repair_request_logs_detached_partitions` | `ctid` | `request_logs_default` | 同上，且仅测试引用 |

**无新增 P1。**

## 2. 新登记：archive_request_wal 至今仍存在于真库（331 通道缺席）

迁移 331（2026-07-04）在正文里明写：

```
-- Step 2: Drop archive functions
--   Drop archive_request_logs(); Drop archive_request_wal();
```

`bg/partition_manager.go:1269` 也照此注释「Migration 331 removed request_logs_archive
and request_wal_archive」。但实测：

- **331 不在** `installer/cmd/llm-gw-installer/embeddata/startup/`（R71 已核实，五点同步从未补）；
- 本机真库 `schema_migrations` 只到 **V359**，331 从未在此库应用；
- `archive_request_wal` 因此**仍然存在于真库**，全仓 .go 侧零调用方。

这把上一节登记的 P3（父表 `request_logs_archive` 未被 331 删除、分区数 0）
**从「表」扩到了「函数」：331 整条通道缺席，它声明要删的对象一个都没删。**

顺带核了它的游标支撑：`request_wal` 分区父表**无任何 created_at 首列索引**
（只有 `gw_session_id`/`request_id`/`status`/`tenant_id` 开头的复合索引）。
即**若复活 `archive_request_wal`，就会复现 S-01 的 N² 形态**。但正确修法是
**执行 331 的意图把它删掉**，而不是给一张没人查的表补索引。

## 3. ctid 游标为什么不是缺陷（规则用错了对象就是误报）

`ensure_request_logs_partition` 的排空循环：

```sql
LOOP
  WITH batch AS (
    SELECT ctid FROM public.request_logs_default
    WHERE ts >= month_start AND ts < month_end
    ORDER BY ctid LIMIT 50000 FOR UPDATE SKIP LOCKED
  ), moved AS (DELETE FROM public.request_logs_default d WHERE d.ctid IN (SELECT ctid FROM batch) RETURNING d.*)
  INSERT INTO public.request_logs SELECT * FROM moved;
  GET DIAGNOSTICS drained = ROW_COUNT;
  EXIT WHEN drained = 0;
END LOOP;
```

ctid 是**系统列，无法建索引**。所以「缺首列索引」这条规则对它不适用，
判红是规则用错了对象。物理序 + 显式 `EXIT WHEN drained = 0` 的收敛保证本来就是合法策略。

门据此把系统列归入第三类「不可索引」，**只登记不判红，并在输出里写明理由**——
而不是悄悄把它算进「已覆盖」（那和把部分索引算成有索引是同一类自欺）。

量级：它排空的是 DEFAULT 分区里落在「缺口月份」的行，本机 `request_logs_default`
**0 行 / 440 kB**，即今天这个循环跑一轮就退出。

## 4. 泛化门与专项门的范围必须互斥

这道泛化门第一版**没有排除 `promote_*`**，结果：它用更粗的可达性判据
（仓库全量 `.go` 字符串 grep）把专项门已经用 `promoteSpecs()` 精确管理的 **6 项债**
原样重报一遍——同样的 6 项、不同措辞。后果是这道门**永久红**，红在别人管理的债上。

**规则：泛化门必须显式把专项门覆盖的范围排除掉，并在注释里写明「谁负责什么」。**
一套门里两道各自扫描而范围重叠，不会增加覆盖，只会制造噪声和永久红。

同理，专项门与这道门共用解析辅助函数时只保留一份实现（`unqualify` /
`sourceTableForBatch` / `resolveCursor`）——两份各自演化下去必然分叉。

## 5. fail-closed 第四次生效

这道门的第一版对 `archive_request_logs_default` 报「无法解析源表」——
因为它的批次是 `FROM %I`（`format()` 占位符），**源表在运行时才由 pg_inherits 决定**，
函数体里根本没有具体表名。

处理：新增「动态源表」类，**要求为它写明 justification**（这张表实际是什么、
为什么它的游标有索引支撑），否则判红。加了 `archive_request_wal` 的第二条后转绿。

这条正是 fail-closed 的价值：它是**唯一那道真正出过事的函数**（S-01 的 42703 + 30 分钟回滚）。
若当时按惯例「解析不出就跳过」，这道门会在最该查的地方留白。

## 6. 变异检验（两处，红转绿）

| 变异 | 期望 | 实测 |
|---|---|---|
| 撤掉 `archive_request_logs_default` 的动态源表 justification | 红 | ✅ 红并指名 |
| 撤掉 `ctid` 的系统列豁免 | 红，且区分引用中/未引用 | ✅ `ensure_request_logs_partition`（引用中）判缺陷；`repair_request_logs_detached_partitions`（未引用）仅记录 |

第二条顺带证明了可达性判据在起作用：同一个形态，两个函数，一个判红一个只记。

## 7. 结论

- **全库无新增 P1。** 唯一真正危险的那个（`archive_request_logs_default` 的 id 游标）
  已在 R79 上半修复并由静态+真跑两道门守住。
- 新登记 1 条 P3 深化：`archive_request_wal` 因 331 通道缺席而未死。
- 债台账：2 项 P2 潜在（promote 族，带测量）、3 项 P3（promote 族死函数）、2 条动态源表
  justification、2 个 ctid 系统列登记。全部自收缩。
- **仍未做**：`request_wal` / `credit_ledger` / `tool_usage_stats` 等分区的**非游标**查询面
  （比如按 ts 范围做对账/报表的接口）未做计划形状普查；本轮只覆盖「批游标」这一形态。

---

# R79 续三 · 非游标查询面与 DEFAULT 分区普查（一个 P2 + 三条被证伪的假设）

前三批的靶子都是「批游标」这一种形态。本轮换靶子：查**查询面**上另外两种风险——
分区裁剪是否真的在生效，以及 DEFAULT 分区是否在静默吞掉数据。

## 1. 三条被证伪的假设（这轮的一半价值在「不是」上）

### 1.1 `ON ONLY` 让分区漏索引 —— 证伪

`pg_indexes` 里 `session_turns` 的索引全部带 `ON ONLY`：
`CREATE INDEX idx_session_turns_request ON ONLY public.session_turns USING btree (request_id)`。
而 PG 的 `ON ONLY` **不递归到已存在的分区**——若属实，则 6.2GB 的
`session_turns_2026_09` 上没有 `request_id` 索引，视图里逐行跑的
`NOT EXISTS (... FROM session_turns tp WHERE tp.request_id = rl.request_id)`
就会退化成每行一次全分区顺序扫。

实测：5 个分区逐个查 `pg_index`，**每个都有** `*_request_id_idx ON (request_id)`。
分区侧另有自己的索引命名体系。假设不成立。

### 1.2 计划里的 `Seq Scan on request_logs_2026_07/08` 是生产缺陷 —— 证伪

`EXPLAIN` 显示 LATERAL 对 07/08 分区走 Seq Scan、对 09 走 Index Scan。第一反应是
「07/08 缺索引」。实测：`request_logs_2026_07` = 576 kB / 0 行，
`request_logs_2026_08` = 440 kB / 0 行。**本机这两个分区本来就是空的**，
规划器选顺序扫是对的。这是本地空表造成的假象，生产有数据时规划器会用上索引。

**两次都是同一个错误模式：看到计划里不理想的形状，先去查被扫描对象的真实体量，
再去查索引的真实分布——而不是直接归因。**

### 1.3 「DEFAULT 分区装了 1168 MB，所以分区裁剪全废了」—— 证伪

这是本轮最初写下的结论，而且写进了文件的注释里。EXPLAIN 三次复跑把它否掉：

| 查询窗口 | 计划 |
|---|---|
| `09-28 → 09-29` | `Index Only Scan using usage_facts_20260928_occurred_at_idx`，**无 Append，DEFAULT 不在计划里** |
| 同上但 `SET enable_partition_pruning=off` | `Append` 下游 4 个日分区全部出现 |
| `2026-07-01 → 07-02`（无兄弟分区覆盖） | 扫 `usage_facts_default` |
| `2027-01-01 → 01-02`（无兄弟分区覆盖） | 扫 `usage_facts_default` |
| `09-20 → 09-29`（部分覆盖） | `Append`，DEFAULT 返回 448,186 行 |

即 PG 17.10 在窗口被显式兄弟分区**完整覆盖**时会裁掉 DEFAULT。
**我没有去读规划器源码确认它的判定路径，因此这里只登记实测行为，不登记机制解释。**

剩下的真实结论比初判窄得多：覆盖那 36 天的查询确实要扫 DEFAULT
（单日 2026-09-25 实测 12.4 ms，Index Only Scan，可接受）；
2026-09-26 之后的窗口不受影响。**P3，不是性能缺陷。**

## 2. 真正的新发现：P2 —— `stats_event_inbox` 名义分区、实际零分区

`stats_event_inbox` 声明 `PARTITION BY RANGE (occurred_at)`，却**只有 DEFAULT 一个子分区**：

- 全表 **1,419,612 行 / 1298 MB** 全在 `stats_event_inbox_default`
- 行区间 2026-08-19 01:02 → 2026-09-29 00:21，**跨 41 个自然日**
- `bg/partition_manager.go` 的 `ensureSpecs()` 里**没有任何条目**引用它，`bg/` 整包对它零引用
- 全仓**零处** `DELETE FROM stats_event_inbox` / `TRUNCATE`：消费者只 `markProcessed`
  （`inbox_consumer.go:378`），`replaySQL` 还刻意把已处理行回退成 retryable
  —— 即这是一本**只增不减的重放账本**

**定级 P2 而非 P1**：声明查询本身有部分索引兜底
（`..._occurred_at_created_at_idx ON (occurred_at, created_at) WHERE processed_at IS NULL`），
今天不慢。真实代价是**无界增长**：实测 ~10–26K 行/天（09-23~09-26 尖峰 110K–150K），
一年量级 4M–10M 行 / 4–9 GB，全部堆在一张无分区表里。

待 owner 决策：(a) 接入 `ensureSpecs()` 按日分区 + 保留期清理；
(b) 若确实不需要分区，则去掉 `PARTITION BY` 声明，别让下一个人以为有裁剪。

## 3. 这道门抓到了我自己手工普查漏掉的那一张

写门之前我手工跑过一遍 census，结论是「只有 `usage_facts` 一张」，
因为那条 SQL 带了个过滤：

```sql
AND (SELECT count(*) FROM pg_inherits i4 WHERE i4.inhparent = p.oid) > 1   -- 只保留子分区多于一个的父表
```

而 `stats_event_inbox` **恰好只有一个子分区**（DEFAULT 自己），于是被整条抹掉——
**恰恰因为它退化，它才不会被那个条件选中**，全表最严重的一张正好被过滤掉了。

写成门之后（同一段查询、只是没有那个过滤条件），立刻报出 2 张。

**教训：普查脚本里的过滤条件会同时充当「筛选」和「掩盖」。
「我想要的那些对象」和「我筛选之后还剩的对象」不是一回事。**

## 4. 门的设计：为什么不判红「DEFAULT 必须为空」

那是最容易写的一句断言，也是错的。DEFAULT 分区的设计目的就是接住兜不住的行，
让写入永不失败；`bg/partition_manager.go:1256` 对 usage_facts 的
「DEFAULT 保留作历史 catch-all」是**有文档的设计决策**，拿它判红是在惩罚一个正确的设计。

真正要抓的是**静默堆积**：分区创建滞后、或某张表从没接过分区，
于是写入一路落进 DEFAULT 而没人察觉——写入不报错、裁剪悄悄失效、某天才发现。

所以判据不是「有没有数据」，而是**「有数据但没人登记过」**：
- 实质堆积且未登记 → 红，指名并要求写明「这是有意设计还是分区创建滞后」
- 已登记 → 绿，但在输出里复述登记理由，保持可见
- 已登记但实际不再堆积 → **红**（白名单自收缩）
- 登记了本库不存在的表 → **红**（幽灵条目不能躲在「本机没这张表」后面）

## 5. 变异检验（3 处，红转绿）

| 变异 | 期望 | 实测 |
|---|---|---|
| 撤掉 `stats_event_inbox` 登记 | 红并指名 | ✅ `stats_event_inbox: DEFAULT ... 约 1381881 行 / 1298 MB，压过 0 个显式兄弟分区` |
| 白名单塞 `sessions`（DEFAULT 为空） | 红「条目已失效」 | ✅ 红 |
| 白名单塞 `totally_absent_table` | 红「没有对应的分区父表」 | ✅ 红 |

## 6. 结论

- **1 个 P2**（`stats_event_inbox` 名义分区 + 无界增长）、**1 个 P3**（usage_facts 文档注释
  `partition_manager.go:1258` 那句「partition pruning 对 WHERE 范围查询仅扫命中分区」
  对 2026-09-26 之前的数据不成立）。
- **三条假设被证伪**，其中「DEFAULT 破坏裁剪」那条已经写进注释里又被自己推翻——
  留在文件里作为「先实测再归因」的样本。
- **仍未做**：`admin/logs.go` 这类 UNION ALL 视图上的 `ORDER BY + LIMIT` 无法下推
  （计划恒为 `Limit → Sort → Append`，页大小不能约束工作量），目前只靠 R37 的 366 天
  窗口上限兜底；非 hot 分区族的逐族查询面普查。
