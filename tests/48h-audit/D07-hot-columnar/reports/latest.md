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

---

# R79 续四 · P1：分页查询的 LIMIT 其实没有约束工作量（本轮未修，已登记）

R79 在存储函数侧抓到过一个形状——`ORDER BY <cols> LIMIT <n>` 的批游标若列无索引，
成本 O(rows²)。本轮在**查询面**抓到同型的另一个误解：

```sql
SELECT ... FROM <view> WHERE <ts 范围> ORDER BY ts DESC LIMIT <page_size>
```

读起来像「只要一页」。实测**页大小一点也没约束工作量**。

## 1. 受控对照（同一天窗口、同一条 ORDER BY ts DESC LIMIT 10，只改 FROM 来源）

| FROM 来源 | 执行时间 | 计划形状 |
|---|---|---|
| 直查 `request_logs` | 0.288 ms | Index Scan，**LIMIT 下推** |
| 内层嵌套视图（含 LATERAL，**不含**两个反连接） | 0.120 ms | `Merge Append` + `Limit loops=10`，**下推** |
| 完整视图 `request_logs_with_current_month` | **12,390 ms** | `Append (actual rows=404794)`，**下推失效** |

第二行与第三行之间**只差两个相关反连接**（视图体末尾）：

```sql
WHERE NOT (EXISTS (SELECT 1 FROM session_turns_hot th WHERE th.request_id = rl.request_id))
  AND NOT (EXISTS (SELECT 1 FROM session_turns      tp WHERE tp.request_id = rl.request_id))
```

这不是猜测：`EXPLAIN (ANALYZE)` 显示无反连接时规划器对 UNION ALL 的每个分支做 top-N
再归并（`Limit (actual rows=1 loops=10)`）；加了两个反连接后，
它必须先知道每行能否通过反连接才能决定去留，于是无法预截断，
只能把 404,794 行全部物化、两轮索引探测、再排序。

## 2. 「页大小不约束工作量」的直接证据

```
LIMIT 10  → Append (actual rows=404794)
LIMIT 1000→ Append (actual rows=404794)     ← 完全相同
```

缓冲区读 **9,216,949 block**（hit 8,991,503 + read 225,446 ≈ 70 GB 逻辑读）换回 **10 行**。

**所以这不是深翻页问题**：第 1 页和第 500 页一样贵，因为贵的部分在 LIMIT 之前就付完了。

## 3. 命中面

- 接口 `admin/logs.go` 的 ctx 预算是 30s（`:470`）；默认窗口是 `now-24h → now`（`:474-475`），
  即**一天窗口就已经 12.4s**
- `pageSize` 上限 500（`:486-488`），但 `page` **无上限**（`:478-481` 只夹下界）
- 窗口由 R37 的 366 天上限兜着（`maxLogQueryWindow`），所以不会无限放大

## 4. 定级 P1，但不由本轮修

爆炸半径：视图被 `admin/logs.go`、`bg/stats_minute_rollup.go`、
`domains/routeincident/store.go`、`db/probe_views_unified.go`、`maas/usage.go` 共用，
且有自愈重建链（`db/request_logs_view_schema.go`）+ 迁移 575/577/680/696/700/717 +
一整套视图列数冻结契约测试（113/115 列）。
**改视图是迁移 + schema 契约决策，审计轮只定位与登记。**

## 5. 门的设计，以及它自己红过的三次

新增 `TestData_PaginatedViewSource_CorrelatedAntiJoinIsRegistered`：
目录侧找视图体含相关子查询的视图，仓内侧找 `ORDER BY+LIMIT` 的分页引用，两侧求交。
命中必须有书面理由。**不判红「视图里有相关子查询」本身**——那不是缺陷。

写门过程中它自己红了三次，**每次都是门有缺陷**，且都是同一个家族：

1. **按字符串字面量判分页** → `admin/logs.go` 用 `fmt.Sprintf` 拼装，
   FROM 源（`logsSourceFromSQL()` 返回的 `"request_logs_with_current_month rl"`）
   与 `ORDER BY ... LIMIT` 分处不同字面量，组装后才相遇。
   于是门对**全树最被分页的那个查询**报「仓内已无分页引用」。改文件级粒度。
2. **只认纯标识符** → SQL 源都带别名（`"... AS r"`、`"... rl"`），
   `[a-z0-9_]+` 匹配不到，真实用法几乎全漏。改为抽字面量内的标识符 token，
   再与目录里的视图名求交——**用目录当词表**。
3. **门把自己的源码当成了消费方** → 门文件里每个 allowlist 视图名都是字符串字面量，
   注释里还有 `ORDER BY ... LIMIT`。于是删掉一条登记会让交集归零，
   触发真空守卫而不是真正的断言。
   **这是最坏的门的失效形态：它因为一个与被审代码毫无关系的理由保持绿色。**
   修法：门必须排除自己的目录（`tests/48h-audit`）。

三条修正之后，基线 PASS（4 条登记），撤掉 P1 那条登记会**指名红**
（`request_logs_with_current_month：视图体含相关子查询…且仓内有 ORDER BY+LIMIT 的分页引用`），
而不是再误触发真空守卫。

## 6. 三条新登记的分诊结果（逐个核实，不是「先登记再说」）

| 视图 | 分诊 | 依据 |
|---|---|---|
| `v_routable_credential_models` | **假阳性** | 全仓无生产查询读它；命中的三处是测试断言、`fmt.Errorf` 文案、测试清单 |
| `v_task_model_ranking` | **真阳性候选（未测）** | `admin/auto_route.go:1084` 确认 `FROM v_task_model_ranking … ORDER BY affinity DESC, sample_count DESC LIMIT $4` |
| `session_turns_with_current_month` | **混合** | `dual_read_validator.go:167`、`loader.go:258` 两处读它但无 LIMIT；`message_source_v2.go:82` 是 CTE 里的 LEFT JOIN，外层 `fetchTurns` 施加 `LIMIT 20`，构成间接分页读 |

**假阳性也写进登记并注明理由**，而不是让它红着或悄悄删掉——
仓内扫描是文件级粒度，天然有假阳性，处理办法是让分诊结果留在代码里可查。

## 7. 可迁移的教训

- **「LIMIT」出现在代码里不等于它约束了工作量。** 只有 EXPLAIN 显示下推（或 top-N）
  才算；`LIMIT 10` 与 `LIMIT 1000` 的 Append 行数相同，就是它没约束的证据。
- **受控对照要只差一个变量。** 本轮能定位到「那两个反连接」，
  靠的是让第二次与第三次查询**只差反连接**这一项；
  否则「视图很慢」只能停在抱怨层。
- **门必须排除自己的源码**（见 5.3）。

---

# R79 续五 · P1：一段 SQL 常驻代码里、有测试断言它的文本，却从未被执行过一次

上一轮留下的两条「未测」候选，本轮去测，测出一段**执行即报错**的查询。

## 1. 起点：一句读不通的 EXPLAIN

去量 `session_turns_with_current_month` 的下推行为，按 `fetchTurns` 的形状手写查询，
PG 直接回：

```
ERROR:  column t.origin_actor does not exist
LINE 10:  AND COALESCE(t.origin_actor, '') NOT LIKE 'goal-%'
```

## 2. 查清它为什么不存在

| 位置 | `origin_actor` |
|---|---|
| 基表 `session_turns` | **有**（attnum 99） |
| `db/db.go:2990` 的自愈 | 只保证 `request_logs_hot` / `request_logs` |
| `session_turns_hot_bootstrap.sql`（视图定义） | **0 次出现** |
| 迁移 713 重建段 | **0 次出现** |
| 全仓所有同时提到该视图与 origin_actor 的 .sql | **一个都没有** |

**即：视图的 65 列定值投影漏了这一列，而基表有。**

## 3. 唯一相关的测试是绿的，因为它只比字符串

```go
// domains/sessionsummary/message_source_v2_test.go:155
if !strings.Contains(v2SessionBodiesBaseQuery, "LEFT JOIN public.session_turns_with_current_month t") { … }
```

断言的是**文本包含某个 JOIN**。查询本身能不能执行，没有任何一层门在看。
这是 R79 主段那条结论的又一次同型复现：**「形状核对」这一类门禁在结构上抓不到执行期缺陷。**

## 4. 影响面：两条设置路径都中招 —— 这是定 P1 的理由

```
main_pipeline.go:1416   if settings.GetPlatformBool("sessions_v2_compression_read", true)   // 默认 true
                          summaryService.SetMessageSource(sessionsummary.NewPerTurnDigestSource(pool))

NewPerTurnDigestSource = gatedPerTurnDigestSource{
    digest:   &perTurnDigestSource{pool},        // → sessionTurnDigestQuery    ✗ t.origin_actor
    fallback: &v2SessionBodiesSource{pool},      // → v2SessionBodiesBaseQuery ✗ t.origin_actor
}
```

- 开关 `sessions_summary_per_turn_digest` **开** → 走 digest → 同样缺 `t.origin_actor`
- 开关关（**默认**）→ 逐字节委托 fallback → 缺 `t.origin_actor`

**两条路都撞同一个列。** 唯一可用配置是把 `sessions_v2_compression_read` 置 false 退回 V1。
消费面是会话摘要的输入读取（`GenerateSummary` / `GenerateRollingSummary`），
而 `message_source_digest_test.go` 自己写明「source errors must reach the caller regardless of gate state」。

**定 P1。** 源码常量原文实跑（非手抄）确认：`ERROR: column t.origin_actor does not exist`；
去掉该谓词后同形查询正常返回 53,851 行。

## 5. 第二例：诊断跑批查询一张不存在的列

普查顺带抓到 `domains/routeincident` 共 **8 处** `42703`：

| 常量 | 缺失 |
|---|---|
| `action_infra.go:390 / :479 / :507 / :529`、`evidence.go:149` | `route_key` |
| `action_infra.go:608 / :666` | `routing_audit_log.reason` |

核对：**基线 `01-schema.sql:7955` 的 `diagnostic_runs` 没有 `route_key`**，真库也没有，
**全仓没有任何 SQL 给它添加**。`route_key` 只存在于迁移 390 的 `routing_audit_log`——
**是另一张表**，疑为串表。定 **P1 候选**（本轮未逐个核对全部 8 处的消费面）。

## 6. 新门：把仓内 SQL 常量对真库 PREPARE 一遍

`TestData_GoSQLConstants_PrepareAgainstRealDB`：
抽取仓内**无 fmt 占位符的完整 DML 语句常量**（160 条），逐条 `PREPARE`。
PREPARE 只解析+规划**不执行**，所以对本域「主库全程只读」的硬约束无冲突。

**错误必须按 SQLSTATE 分层，不能一律判红**：

| 码 | 含义 | 处置 |
|---|---|---|
| `42703` undefined_column | 列真的不存在（已翻遍仓内迁移确认） | **判红** |
| `42P01` / `42704` / `3F000` | 本机库比代码旧（`outbox_events` 等） | 只记录 |
| `42501` | 只读角色权限受限 | 只记录 |
| 其他 | 未预料的形状 | **判红**（fail-closed） |

结果：133 条规划成功 / 9 条已定性登记 / 9 条未分诊 backlog / 9 条本机无法验证。

## 7. 这道门自己又红了四次，全是同一个家族

| # | 门的缺陷 | 现象 |
|---|---|---|
| 1 | 抽取器只判「含 SELECT」 | 48 条 `42601` 语法错——其实大部分是 **SQL 片段**（`requestLogsJoins` 是 JOIN 块、`sessionSummarySelectCols` 是列清单）与 DDL 常量，PREPARE 本来就不收 |
| 2 | 没排除 SQLite | `storage/sqlite/*` 的常量被拿去 PREPARE 到 PostgreSQL，**换了个方言问错服务器** |
| 3 | 登记键用 `file::name` | `action_infra.go` 有 **6 个同名 `sql` 常量**，自收缩检查命中其中一个后 `delete()` 了登记，**把另外 5 个失败项的理由一起抹掉**，它们随即红在一个与自身 SQL 无关的原因上 |
| 4 | 同上 | 修法：键加行号 + 内容哈希；且**只报告、绝不遍历中改注册表** |

**第 3 条最值得记**：一道门为了维护自己的白名单，把白名单本身改坏了。
判据是「同一条目的所有实例必须共用一个键」，而事实是**键不唯一**。

## 8. backlog 用棘轮管，不用永久红

9 条未分诊项**登记而不判红**，理由是已经验证过的失效形态：**长期红的门会让人习惯性忽略它**。
但登记不等于放过，配三条断言：

- 新增一条无法规划的 SQL（未登记）→ **红**
- 任一登记项开始能正常 PREPARE → **红**（问题已修，白名单该收缩）
- `len(backlog) != expectedUntriagedBacklog` → **红**（有人动了 backlog 却不改常量）

**换句话说：这道门今天绿，是因为已知的 9 条被记账了；它不会因为记账而变瞎。**

## 9. 结论与待办

- **P1 ×2**：`origin_actor` 视图漏投影（会话摘要输入读取，两条设置路径都失败）、
  `diagnostic_runs.route_key` / `routing_audit_log.reason` 缺列（8 处查询，P1 候选）
- **未分诊 backlog 9 条**（带棘轮），需逐条查基线定性
- **本机无法验证 9 条**：`internal/outbox/*` 依赖 `outbox_events` 表，本机库比代码旧
- **未修理由**：视图投影属迁移 + schema 契约决策（`session_turns_with_current_month`
  有 526/636/640/713 多条重建路径 + 列数契约测试）；补列属迁移决策。审计轮只定位与登记。

---

# R79 续六 · 把 9 条 backlog 逐条定性完毕（清空 backlog，又挖出 2 个 P1）

上一轮把 9 条无法规划的 SQL 记进了 backlog。本轮逐条查证，backlog 清空，
其中 **4 条是真缺陷**，其中 2 条是全新的 P1。

## 1. 9 条的最终定性

| 条目 | 原错误 | 定性 | 依据 |
|---|---|---|---|
| `admin/credential_models_dto.go::offerListSQLColumns` | `column "__mo_modality__" does not exist` | **假阳性（设计内）** | `__mo_modality__` 是**模板占位符**：启动时探测 `information_schema.columns` 里有没有 `model_offers.provider_modality`，据结果替换成 `moModalityWithColumn`（真列）或兼容常量（该文件 `:76`/`:120`/`:138`）。它本来就不是会被原样执行的语句 |
| `domains/reportrollup/rollup.go::internalPersonDaySQL` | `unterminated quoted string` | **抽取器判错对象** | 该常量用 Go 字符串拼接（`' + personUnknown + '`），正则只抓到第一段反引号内容 |
| `domains/toolexecution/postgres_store.go::selectExecutionCols` | `tool_name` | **抽取器判错对象** | 无 `FROM` 的列清单片段，FROM 由调用方拼 |
| `pending/pg_source.go::pgSourceColumns` | `id` | **抽取器判错对象** | 同上 |
| `modelcatalog/upsert.go::insertManualCredentialModelSQL` | `could not determine data type of parameter $8` | **PREPARE 的推断限制** | `$8` 只在 CTE 内被引用；PREPARE 不带参数类型故推断不出，运行时驱动会传类型。非语句缺陷 |
| `internal/reasoncap/pgsource.go::q` | `column ma.alias does not exist` | **真缺陷 → P1-4** | 见下 |
| `domains/toolexecution/postgres_store.go::q / qHot / q` | `tool_name` | **真缺陷 → P1-3** | 见下 |

## 2. P1-3｜`tool_usage_stats` 列名漂移：Go 用 `tool_name`/`date`，schema 是 `tool_id`/`usage_date`

**两个独立 SSOT 一致**，这不是本机漂移：

| 来源 | `tool_usage_stats_hot` 的列 |
|---|---|
| 基线 `installer/cmd/llm-gw-installer/embeddata/01-schema.sql` | id, **tool_id**, tenant_id, **usage_date**, call_count, success_count, error_count, avg_latency_ms, last_called_at, created_at, updated_at |
| 真库实测（`pg_attribute`） | **逐列完全相同** |

全仓**没有任何迁移**改名。而 `domains/toolexecution/postgres_store.go` 直接：

```sql
INSERT INTO tool_usage_stats_hot (… tool_name, date, …)
ON CONFLICT (tool_name, date) DO UPDATE SET …
```

活接线确认：`cmd/gateway/tool_execution_integration.go:40` `te.NewPostgresStore(db, logger)`。
→ **工具调用的用量统计写入路径不可执行。**

## 3. P1-4｜`model_aliases.alias` —— 应为 `raw_name`

基线 `model_aliases` 列：id, canonical_id, **raw_name**, quantization, surface, status,
notes, created_at, updated_at, client_profiles —— **没有 `alias`**。
`admin/logs.go:1249` 另有注释佐证「model_aliases.raw_name is persisted lowercase」。
而 `internal/reasoncap/pgsource.go:59` 写的是 `WHERE ma.canonical_id = mc.id AND ma.alias = $1`
→ **reasoncap 覆盖查询不可执行**。

**我此前把这条误读成 modelcatalog 的问题**——用 `paste` 把「键」行与下一行错误消息配对时错位了一行。
正确做法是让门自己逐条打印 `键：…` 与消息，我后来用一次性探针重取才拿到准确配对。

## 4. 分类器新增一档：`42P08`

`could not determine data type of parameter $N` 是 **PREPARE 没有参数类型**导致的推断限制，
运行时驱动会传类型。归入「本机无法验证」，不判红。

## 5. 自收缩检查补上了缺失的那一侧

原有检查只在「登记的常量现在能正常 PREPARE」时报——覆盖了「bug 被修好」，
但**不覆盖「抽取器不再采集它」**。补 FROM/拼接过滤之后，4 条 backlog 登记项
悄无声息地离开了语料，而门一直绿着：一份能持有不可达键的登记表，
等于把自己的 backlog 藏起来了。

补的断言：**每条登记键都必须对应本轮语料里的一个候选**，否则红。

## 6. 结论

- **backlog 清空**（`expectedUntriagedBacklog = 0`），机制保留
- **新增 P1-3**（`tool_usage_stats` 列名漂移，有活接线）、**P1-4**（`model_aliases.alias`）
- 语料 155 条：**131 规划成功 / 14 已定性登记 / 10 本机无法验证**（9 条 `outbox_events` 本机无表 + 1 条 42P08）
- 未修理由不变：补列、改视图、迁移列名都属迁移 + schema 契约决策

---

# R79 续七 · 把「P1 候选」量成 P1：8 处缺列的可达性核实

上一轮把 `domains/routeincident` 的 8 处缺列记为「P1 候选」——缺列是事实，
但**会不会真断**当时没量。这一轮量可达性，结论是 **P1**。

## 1. 缺列事实：三方一致

| 来源 | `diagnostic_runs` 是否含 `route_key` |
|---|---|
| 基线 `01-schema.sql:7955` | 否 |
| 真库 `pg_attribute` 实测 | 否 |
| 全仓仅有的两处 `ALTER TABLE diagnostic_runs`（445 / 391） | 都只 `ADD COLUMN created_at/updated_at` |

`route_key` 在全仓只出现于 **390 的 `routing_audit_log`**——另一张表，疑串表。

## 2. 可达性：它在**每条请求日志**的路径上

```
cmd/gateway/main.go:3539   incidentStore := routeincident.NewStore(dbConn.Pool())
cmd/gateway/main.go:3545   incidentObserver := routeincident.NewObserver(...)
                           telemetryClient.AddOnRequestLogPersisted(incidentObserver.AsHook())
                                                            ↑ 每一条落库的请求日志
        ↓
Observer.Transition(ctx, in)          observer.go:297
        ↓
Store.writeAudit → Store.persistRunInTx   action_infra.go:375 / :458
        ↓
INSERT … diagnostic_runs (…, route_key, …)   ← 42703
```

8 处缺列各自所在的方法：

| 常量 | 所在方法 | 路径 |
|---|---|---|
| `action_infra.go:390` | `writeAudit` | **observer 热路径** |
| `:479` | `persistRunInTx` | **observer 热路径** |
| `:507` / `:529` | `loadRunByIDInTx` / `loadRunByID` | 读路径 |
| `:608` / `:666` | `AuditLogListByRun` / `DiagnosticRunsList` | **admin HTTP**（`NewRouteIncidentsHandler` 无条件接线） |
| `evidence.go:149` | `RecordEvidenceExportAudit` | 证据导出 |

## 3. 一条加重因素：重试机制在这里零收益

`observer.go` 的重试循环注释写着：

> Transient: lock conflict, transient deadlock. Backoff and retry. We never abort a
> transition; the next persisted row will catch up…

但 `42703 undefined_column` 是**永久性**错误——每次重试、每条后续请求都必然同样失败。
`maxRetries: 4` 意味着**每条请求日志触发 5 次注定失败的查询**，退避后
`o.failed++` 并只记一条 warning。这套重试是为瞬时冲突设计的，
对一个永久性 SQL 错误，它只是把热路径上的失败成本放大 5 倍。

## 4. 定级：P1（不是 P1 候选）

理由与 R79 续五给 `origin_actor` 定 P1 同源：**不是「缺一列」，而是「缺一列且默认开启、
落在每条请求的热路径上、失败被重试放大」**。两处都属迁移 + schema 契约决策
（补列、改列名、串表修正），审计轮不代做。

登记表已从「P1 候选」升为「P1」并写入完整可达性链；变异检验（撤登记→指名红）仍通过。

---

# R79 续八 · 非 hot 分区族普查（第一族：session_bodies）—— 一条差点写成 P2 的 P3

前面几轮都在查 `request_logs` 一族。本轮换族，查全库**最大**的表 `session_bodies`
（8.68 GB / 5 个分区），查询面走 `session_bodies_unified` 视图。

## 1. 缺 `(session_id, ts)` 索引，53,851 行换 20 行

会话摘要输入查询（`fetchTurns` 去掉 P1-1 那条坏谓词后的形态）：

```sql
SELECT … FROM session_bodies_unified b
  LEFT JOIN session_turns_with_current_month t ON t.tenant_id=b.tenant_id AND t.request_id=b.request_id
WHERE b.session_id = $1 ORDER BY b.ts ASC LIMIT 20
```

该族 `session_id` 打头的索引只有：

```
idx_session_bodies_hot_lookup        (session_id, turn_no, tenant_id)
idx_session_bodies_session           (session_id, turn_no DESC)   -- ON ONLY
session_bodies_2026_09_session_id_turn_no_idx (session_id, turn_no DESC)
```

**没有 `(session_id, ts)`**。`turn_no` 与 `ts` 的相关性 PG 无法证明，于是
`WHERE session_id=$1 ORDER BY ts LIMIT 20` 只能：

```
Bitmap Index Scan on session_bodies_2026_09_session_id_turn_no_idx (actual rows=53851)
  → Bitmap Heap Scan on session_bodies_2026_09 (actual rows=53851)
     → Sort（temp read=347 written=760，**落盘外排**）
        → Limit 20
```

实测（`sys:probe:cred126:20260924`，53,851 body）：
**Execution Time 835.853 ms，Buffers hit=653,557 read=13,616（≈5.2 GB 逻辑读）+ 临时文件溢出。**

## 2. 分布一量，定级从 P2 掉到 P3

```
SELECT percentile_cont(…) FROM (SELECT session_id, count(*) FROM session_bodies GROUP BY 1)
→ 817,986 个会话：avg 2.1 / p50 1 / p90 1 / p99 2 / max 53,851
```

按来源拆开：

| 会话类别 | 会话数 | avg body | max body | >100 body 的会话数 |
|---|---|---|---|---|
| 真实会话 | 817,579 | **1.22** | 1,607 | 222 |
| `sys:probe:*`（探针生成） | 445 | **1,693** | 53,851 | 350 |

**若我停在「53,851 行 / 836 ms / 5.2 GB」就会写出一条 P2——而它对 99.98% 的会话完全不成立。**
真实会话 p99 是 **2 个 body**，读 2 行拿 20 条毫无压力；成本集中在**探针子系统**
（445 个会话、均值 1,693 行），而探针会话是否真的走摘要路径本轮没查。

**定 P3（潜在）**：这是一条**潜伏**成本，不是当下在付的成本。触发条件是
(a) 探针会话真的进入摘要/close-hook 路径，或 (b) 真实会话长到几百个 body
（已有 222 个 >100）。修法便宜：补 `(session_id, ts)` 索引即可。

## 3. 顺带记录一个与 P1-1 耦合的事实

`SessionMetadataCloseHook.OnSessionClosed` 每次会话关闭都调
`loader.GetSessionMessages` → 这条查询。**它现在因为 `origin_actor` 而直接报错**。
也就是说：**只修 P1-1（给视图补投影）会把这条查询面立刻激活**，
届时探针类会话每次 close 都要付那 836 ms。
**修 P1-1 时应一并评估是否补 `(session_id, ts)` 索引**，否则等于把一条休眠的
昂贵路径唤醒。

## 4. 本轮的可迁移教训

**定级之前先量分布，不是量峰值。** 我在这一族上已经准备了「P2：8.68 GB 的表读
53,851 行只返回 20 条」这段结论，是分布数据把它拦下来的。
一条结论的说服力往往来自它的**中位数**，而触发审计的是**最大值**——
两者不一致时，只有中位数能告诉你这是不是常态。

---

# R79 续九 · 四个非 hot 族普查：1 个 P2（无界增长 + 死配置）、1 个 P3、1 条被证伪

| 族 | 分区键 | 分区数 | 体积 | 行数 | ts 打头索引 |
|---|---|---|---|---|---|
| `usage_ledger` | RANGE(ts) | 5 | 1001 MB | 2,038,536 | ✅ `idx_usage_ledger_part_ts` |
| `sessions` | RANGE(partition_date) | 5 | 476 MB | 760,808 | ❌ |
| `request_wal` | RANGE(created_at) | 6 | 375 MB | 975,156 | ❌ |
| `routing_decision_log` | RANGE(ts) | 5 | 275 MB | 921,463 | ✅ `idx_routing_decision_log_part_ts` |

四族分区结构都健康：DEFAULT 分区皆空，数据都在当月分区，边界正确。
**与 `usage_facts`（R68 已改日分区）不同，这四族仍是月分区**——不是缺陷，
但意味着月内任何 ts 范围查询都不能裁剪。

## 1. P2｜`request_wal` 完全没有保留期机制

三条独立证据指向同一结论：

**(a) 唯一该做清理的 SQL 函数是死的。** `archive_request_wal` 仍在真库（R79 续二 已登记），
但**全仓零 Go 调用方**；它是迁移 331 声明要删的对象，而 331 不在 installer startup 通道、
本机 `schema_migrations` 只到 V359。

**(b) 另两条可能的路径都不覆盖它。** `drop_old_state_partitions` 的函数体里
**根本没提 request_wal**；Go 侧全仓**没有任何 DELETE / DROP / TRUNCATE request_wal**。

**(c) 声明的 TTL 是个死配置。**

```go
// settings/spec_lifecycle.go:83 —— 全仓仅此一处出现
{Key: "lifecycle.request_wal_ttl_days", Type: TypeInt, Scope: ScopePlatform,
 Category: TypeLifecycle, Min: 1, Max: 30, Default: 1,
 DangerLevel: Warning, HotReload: true, Description: "request_wal 保留天数",
 DescriptionLong: "request_wal 月度分区保留天数。默认 1 天。"}
```

`grep -rn "request_wal_ttl_days"` 在全仓**只命中它自己的声明**——零消费方。
这不是「没接完」，是**一个可热更新、界面上可见、描述明确的平台设置，运维改它没有任何效果
且不报错**。这是本轮最该单独记的一条。

**代价**（实测）：`request_wal_2026_09` = **975,156 行 / 375 MB / 2026-09-03 → 09-28（26 天）**。
按日分布：09-24 151,875、09-25 131,584、09-26 142,387（尖峰），近期 8,000–12,000。
**若 1 天 TTL 生效，应只留约 1 万行 / 4 MB；实际是声明意图的约 80–100 倍，且每天还在涨。**
按 ~30K 行/天外推，一年 ≈ 11M 行 / ~4 GB；而 `drop_old_state_partitions` 不覆盖它，
**旧的月分区也不会被 drop**（`_2026_06/07/08` 至今仍在），分区数本身也在增长。

## 2. 修法被 columnar 挡住（这条最实用）

直觉修法是「把 DELETE 补回去」。**但它现在连计划都做不出来**：

```
EXPLAIN DELETE FROM request_wal WHERE created_at < …;
ERROR:  UPDATE and CTID scans not supported for ColumnarScan
```

原因是 `request_wal_2026_08` 的 access method 是 **359239 = citus columnar**，
而 `request_wal_2026_09` 等是 heap（2）。逐分区验证：

```
EXPLAIN DELETE FROM request_wal_2026_09 …  →  Seq Scan（可执行）
EXPLAIN DELETE FROM request_wal_2026_08 …  →  ERROR: CTID scans not supported
```

**所以修复必须先处理 columnar 分区**（转回 heap，或改走 `DETACH + DROP TABLE`）。
这正是 `routing_decision_log` 那族采用的模式——见下。

## 3. P3｜`sessions` 保留期删除全分区 Seq Scan

```sql
-- bg/lite_retention_worker.go:131
DELETE FROM sessions WHERE updated_at < ?
```

```
Delete on sessions → Append
  → Seq Scan on sessions_2026_09  Filter: (updated_at < …)   (est. 865,385 rows)
  → 其余 4 个分区同样 Seq Scan
```

`idx_sessions_status (status, updated_at DESC)` **用不上**——首列 `status` 未被谓词约束。
476 MB 的表每次保留期跑一遍全扫。定 P3（有无实际影响取决于该 worker 的调度周期与表增长）。

## 4. 一条被证伪的怀疑

我怀疑**每月 1 日调度的** `archive_routing_decision_log`（`archiveSpecs()` day:1）
会因为 `routing_decision_log_2026_09` 是 columnar 而失败——毕竟同族的 DELETE 已经报错。

**证伪**：它的函数体末尾是

```sql
EXECUTE format('ALTER TABLE routing_decision_log DETACH PARTITION %I', src_part);
EXECUTE format('DROP TABLE %I', src_part);
```

**走 `DETACH + DROP TABLE` 而不是 DELETE**，因此 columnar 安全。
**「同样是 columnar 表」不等于「同样受同一限制」**——限制作用于操作类型，不是表。
这条同时说明 `routing_decision_log` 族是被正常清理的，本轮无发现。

## R79 续十 · 保留期删除的索引普查（本轮：1 个 P1 + 2 个 P2 + 1 个 P3 + 1 个 P3 文档 + 1 条证伪）

上一轮普查的是**非 hot 分区族**，本轮把面扩到**保留期 worker 的删除路径**——
一个此前完全没被机械门覆盖的类别。

### 判据：只认首列

`candidate_failure_logs` 的每个分区有 6 条索引，`ts` 是其中 4 条的**第二列**：

```
credential_id, ts DESC  |  provider_id, ts DESC
raw_model_name, ts DESC  |  session_id, ts DESC
```

裸 `ts < cutoff` 谓词用不上任何一条。这是「索引首列匹配 ≠ 索引可用」那条纪律的
**镜像**：`armor_judgments` 的 `created_at` 同样只作 `(tenant_id, created_at DESC)`
的第二列。换句话说，上一轮记的规则只覆盖了「索引有这列但不是首列」的一半，
另一半——「这列在索引里，但不是首列」——本轮才补上。

### P1 · armor_judgments：删除吞吐追不上写入吞吐（不只是缺一列）

`security/armor/logger.go` 为每条流式请求写一条 observe-only 安全审计。
`bg/audit_trimmer.go:126-135` 负责清理：90 天保留、24h tick、每轮硬编码 `LIMIT 5000`。

| 实测（2026-09-29） | 值 |
|---|---|
| 行数 / 体积 | **804,253** / 327 MB |
| 时间跨度 | **86.94 天**（最老 2026-07-04 03:07） |
| 写入速率（last 24h） | **6,318 行/天** |
| 删除速率（硬上限） | **5,000 行/天** |
| 净增速率 | **+1,318 行/天** |
| EXPLAIN cost | **94,538**（`Sort rows=378,463`） |

两处叠加：

1. **删除能力 < 写入能力**（5,000 < 6,318）。90 天后开始有过期行时，表永远追不平。
2. **当前一行都删不掉**：跨度 86.94 天 < 90 天保留期 ⇒ 每轮走一遍全表才确认无可删。
   按 24h 周期这是每天一次的无谓扫描（EXPLAIN cost 94,538；cost 是**估算**，
   实测执行时间见续十一：763ms）。

定 P1 的理由不是「缺一列」，而是**默认开启 + 每 24h + 表已 327MB + 吞吐倒挂**。
删除上限 5000 硬编码、不可配置；保留期 90 天也是常量，没有提速手段。

### P2 · journal_snapshot_receipts：无索引，且全仓少见的无分批删除

三条索引首列分别是 `claim_until`(partial)、`tenant_id`、identity 复合，
**没有一条以 `updated_at` 开头** ⇒ Seq Scan cost 5,020 / 94,657 行 / 57 MB。

加剧项：`domains/requestjourney/retention.go:143` 这条 DELETE
**没有 LIMIT、没有分批**，是本轮 37 条保留期 DELETE 里少见的全量删除形态。

### P2 · candidate_failure_logs：13.6 倍扫描放大

活跃分区 `candidate_failure_logs_2026_09` 是 heap 105 MB / 83,289 行，EXPLAIN cost 10,646，
规划器估**扫 67,826 行**只为删 5,000 行。表里 **67,580 行（81%）早已过期**，积压需 13.5 天自行消化。

> ⚠️ 这个「13.6x 扫描放大」是 **cost 估算，未实测执行时间**。续十一在同规模的
> `armor_judgments` 上实测发现：LIMIT 会让 Seq Scan 提前终止，cost 差 3.8 倍而
> 实际执行时间差 ≈ 0。**同一机制很可能让本条的扫描成本被高估**，
> 所以本条的 P2 定级依据是**速率**（318 行/24h ≪ 5,000/天，积压会收敛），
> 不依赖这条未实测的扫描成本。

**只定 P2 不定 P1**，因为写入只有 **318 行/24h**，远小于删除能力 5,000/天——
积压会收敛，不是无界增长。这与 armor_judgments 的 6,318/天是同一量纲下的
相反结论：定级看的从来是速率对比，不是峰值行数。

### P3 · model_iq_runs：登记而非豁免

0 行 / 40 kB，`tested_at` 是 3 条索引的第二列，365 天保留期尚未到达触发规模。
登记而不是豁免，是为了等它长起来时这条信息还在，不必重新普查。

### P3 · 一条注释已与现实不符（已修）

`internal/trace/stage_events_retention.go` 文件头原写着：

> 该表没有 created_at 单列索引（现有索引均以 tenant/stage 为前导列），子查询按
> created_at 过滤会走顺序扫描；**若生产实测扫描成本高，再补 created_at 索引迁移**。

实测 `idx_stage_events_created_at` 存在且被规划器采用（Index Scan，3,286,679 行 / 2035 MB）。
**这是全仓唯一写着「再补 created_at 索引」的地方**，owner 照做会做一次已完成的迁移。
已改写该注记并指向本轮的门。

### 证伪：request_state_transitions 的「无 LIMIT 全量 DELETE」不是缺陷

`domains/requestjourney/retention.go:134` 是全仓唯一没有 LIMIT、没有分批的保留期
DELETE，形如 `DELETE FROM request_state_transitions WHERE created_at < ...`。
它形状上正是本该红的那一类。实测否掉了：

```
时间跨度   min=2026-09-22 01:20  max=2026-09-29 01:34  = 7.01 天
过期行(>7d) 2,648        每小时新增 1,103 行
索引       idx_state_transitions_created (created_at) → Index Scan
```

保留窗口**正好等于** 7 天保留期，说明保留期在生效；每 tick 删 1.1k 行，
30s ctx 预算绰绰有余。写在这里是为了记下「看起来该红但实测不该红」的位置，
免得下一轮重新怀疑一次。

### 门：TestData_RetentionTrim_PredicateColumnHasLeadingIndex

`tests/48h-audit/D07-hot-columnar/data/retention_trim_index_test.go`

覆盖 37 条保留期 DELETE（16 张表：6 张有首列索引、9 张小表豁免、4 张已知缺陷）。
分三类，不让它们混在一个桶里：

- `retentionSmallTables`（9 张）—— 小到 Seq Scan 是**正确**选择。每条带实测行数与
  EXPLAIN 成本。不登记就判红，所以新增小表必须显式说明。
- `knownRetentionDefects`（4 张）—— 已确认未修。**不让它们一直红**：长期红的门会被人
  习惯性忽略，久了等于没有门。改为登记 + 棘轮，并在每次运行时逐条打印当前状态。
- 其余 —— 新判红。

三条 fail-closed 设计：

1. **扫描器失效要红**。只扫到 <10 条 DELETE 时 `t.Fatalf`，而不是当成「没缺陷」。
   变异检验证实：把目录列表缩到 `{"security"}` 后门红并打印「只扫到 0 条」。
2. **过期的登记要红**。已补上首列索引的表若仍留在登记表里，门报「已补上索引但仍留在
   knownRetentionDefects」——过期的登记比没有登记更糟。变异检验证实。
3. **门的报错必须指名对东西**。第一版正则只抓 `(\w+)`，把 `DELETE FROM public.request_attachments`
   的表名报成 `public`，是门自己的 bug，由首跑结果暴露；已修 schema 限定名。

变异检验 3 处，全部**指名对了东西**：

| 变异 | 门的反应 |
|---|---|
| 撤掉 `armor_judgments` 登记 | 红「保留期删除走全表扫描: armor_judgments（bg/audit_trimmer.go:128）」+ 棘轮 3≠4 |
| 把 `request_attachments` 塞进登记表 | 红「已补上以 created_at 为首列的索引（domains/attachments/repository.go:294），但仍留在登记表」+ 棘轮 5≠4 |
| 扫描器目录退化 | 红「只扫到 0 条保留期 DELETE，扫描器很可能已失效」 |

### 一条被证伪的变异

第一次变异我塞的是 `DELETE FROM request_stage_events WHERE created_at < ...`，
门没红。复核后确认这是**正确行为**——该表确实有 `idx_stage_events_created_at` 首列索引。
是变异选错了对象，不是门失效。

### 累计待 owner 拍板

本轮新增 4 项（armor_judgments P1 / journal_snapshot_receipts P2 /
candidate_failure_logs P2 / model_iq_runs P3），全部是索引迁移或 worker 吞吐调整，
改动面比前几轮的列名/视图问题小。


## R79 续十一 · 修法实测：补索引这条建议被自己的对照实验否掉

续十给 `armor_judgments` 提的修法是**补 `(created_at)` 索引**。这一节把它拿回
TEMP TABLE 上做受控对照——**结论是这个修法无效，且是净损失**。

### 做法

生产表全程只读，用 `CREATE TEMP TABLE aj AS SELECT * FROM armor_judgments`
在会话内复刻**生产规模（804,253 行 / 327 MB）**，真删真计时
（`EXPLAIN (ANALYZE, TIMING OFF, COSTS OFF)`）。TEMP TABLE 会话结束即销毁，
不触碰任何生产表。

### 结果（毫秒，重复运行取中位数）

| 场景 | 方案 | 实测 |
|---|---|---|
| 0% 过期（**生产现状**） | 无索引 LIMIT 5000 | **763**（自身波动 756 / 763 / 796） |
| 0% 过期 | **补索引** LIMIT 5000 | **732** |
| 0% 过期 | 无索引 LIMIT 500000 | **871** |
| 97% 过期（90 天后稳态） | 无索引 LIMIT 5000 | **966** |
| 97% 过期 | **补索引** LIMIT 5000 | **915** |
| 97% 过期 | 无索引 LIMIT **100000** | **1,728** |

**补索引收益 ≈ 0。** 732ms 落在无索引自身波动区间（756–796ms）之内，
不是改进。而 EXPLAIN 估算的 cost 差是 **3.8 倍**（27,644 → 7,294）——
**cost 差 3.8 倍不等于执行时间差 3.8 倍**。

### 为什么 cost 和实测差这么远

cost 假设 Seq Scan 扫完**全部匹配行**；实测里 `LIMIT` 会让扫描**提前终止**——
过期行占比 97% 时，扫到第 5,000 个满足条件的行就停，根本没往后走。
这解释了 20 万行样本上的曲线：

| LIMIT（97% 过期、无索引） | 执行时间 |
|---|---|
| 5,000 | 237 ms |
| 40,000 | 355 ms |
| 200,000 | 1,070 ms |

随 LIMIT **线性**增长 ⇒ 确实提前终止，而不是扫全表。

> 这是「索引首列匹配 ≠ 索引可用」的同源教训的**第四种形态**：
> 之前是「索引有这列 ≠ 索引可用」，这次是「**cost 估算 ≠ 执行时间**」。
> 只看 EXPLAIN 的 cost 就写修法，等于没测过。

### 正确的修法是提高批量，不是补索引

`armor_judgments` 每条流式请求写一条审计记录（6,318 行/天），**补索引是持续的
写入维护成本**；换来的删除侧收益是 0。净损失。

而提高批量代价极小：批量 ×20（5,000 → 100,000），耗时 966ms → 1,728ms（×1.8），
**吞吐能力提升 20 倍**。这直接解除 5,000/天 < 6,318/天 的倒挂。

| 方案 | 吞吐 | 每轮代价 |
|---|---|---|
| 现状 LIMIT 5000 | 5,000 行/天 | 966 ms |
| **LIMIT 100000** | **100,000 行/天** | 1,728 ms |

因此 `armor_judgments` 仍是 **P1**——但**理由是吞吐倒挂，不是缺索引**，
修法是提高批量上限（`bg/audit_trimmer.go` 里的常量），**不是加迁移**。

### 对续十条目的连带修正

- 续十写「每轮扫 67,826 行只为删 5,000 行（13.6x 放大）」并当成已证实的成本。
  **未实测，且同规模实测显示 LIMIT 会提前终止**，已在续十原文加注更正。
- 续十给 `armor_judgments` 的修法「补 `(created_at)` 索引」已作废。
- `journal_snapshot_receipts` 的修法**不受影响**：它那条 DELETE **没有 LIMIT**，
  没有提前终止可剪枝，Seq Scan 是真实成本。补索引 + 改分批仍然成立。

## R79 续十二 · DELETE 形态才是瓶颈：ctid 形态 124 倍，且零迁移

续十一证明了「补索引」这条修法在 `armor_judgments` 上收益 ≈ 0。既然索引不是瓶颈，
那瓶颈是什么？这一节把它找出来。

### 实测：改 DELETE 形态，870.7ms → 7.0ms

生产规模（804,253 行）TEMP TABLE 对照，同一批数据：

| 方案 | 实测 |
|---|---|
| A **现状** `id IN (SELECT id … ORDER BY created_at LIMIT 5000)` | **870.7ms** |
| B `id IN`，去掉 `ORDER BY` | 695.6ms |
| C **`ctid IN (SELECT ctid … LIMIT 5000)`** | **7.0ms** ← **124 倍** |
| D C + 补 `(created_at)` 索引 | 6.8ms（与 C 无差别，索引仍然无用） |

机制在 EXPLAIN 里看得一清二楚：

```
A/C 对照 —— Delete on armor_judgments
  ->  Hash Join  (actual rows=5000)          ← A：外层被拖进全表扫
       ->  Seq Scan on e3 (actual rows=804306)   ← 804,306 行全表扫
  ->  Nested Loop
       ->  Tid Scan on armor_judgments        ← C：直接按物理地址定位
          TID Cond: ("ANY_subquery".ctid = ctid)
```

`DELETE … WHERE id IN (SELECT id FROM t WHERE …)` 会让 DELETE 主体去**全表扫 t**
做 Hash Join；而 `ctid IN (…)` 让规划器用 **Tid Scan** 按子查询输出的物理地址直接定位行，
外层根本不用扫。

**这不需要任何迁移**——本仓已有两个生产用例就是这个写法：
`bg/session_summaries_trimmer.go:122`、`domains/attachments/repository.go:294`。

### candidate_failure_logs：瓶颈是 ORDER BY 逼出的全量 Sort

同一轮把 P2 那张也测了（83,289 行，81% 过期）：

| 方案 | 实测 |
|---|---|
| 补 `(ts)` 索引 + **保留** `ORDER BY ts` | 411.1ms |
| 补 `(ts)` 索引 + **去掉** `ORDER BY` | 19.5ms（22×） |
| **无索引** + **去掉** `ORDER BY` | **14.6ms**（25×，比补索引还快） |

```
Sort Method: top-N heapsort  Memory: 581kB
  ->  Seq Scan (actual rows=67608)     ← Sort 吃掉了全部 67,608 个过期行
```

`ORDER BY ts` + `LIMIT` 会强制**全量 Sort**（top-N heapsort），Limit 的提前终止
被 Sort 吃掉。去掉 `ORDER BY` 后 Limit 直接压在 Seq Scan 上，扫 5,000 行就停。
**补索引在两种形态下都没用**——瓶颈是 Sort，不是 Scan。

### 一条反例：ctid 形态不能整类推广

同一轮测了第三张表 `session_aggregate_outbox`（1,157,822 行 / 1.5GB）：

| 方案 | 实测 |
|---|---|
| `id IN` + `ORDER BY` | 6,216ms |
| **`ctid IN` + 去掉 `ORDER BY`** | **6,278ms** ← **无差别** |
| `ctid IN` + 补索引 | 5,935ms |

计划形状解释了为什么这张表改形态没用：

```
->  Index Scan using idx_session_aggregate_outbox_done_completed_at
      Index Cond: (completed_at < (now() - '7 days'::interval))
->  Tid Scan on session_aggregate_outbox
```

它的 `completed_at` **本来就是索引首列**（所以续十的门判它绿），内层已经在走索引；
6.2 秒的成本是 **5,000 次随机回表**到 1.5GB 宽表的 I/O，与 DELETE 形态无关。
`armor_judgments` 赢在它内层本来是 Seq Scan，改形态一次消掉了外层全表扫。

> **判据**：ctid 形态的收益来自**消除外层全表 Hash Join**。外层本来就不贵
> （内层已走索引）的表，改形态等于没改。**逐表实测，不能整类推广。**
> 顺带确认：`session_aggregate_outbox` 无缺陷，它在续十里判绿是对的。

### 缓存污染过一次测量，记下来

中途出现过一次自相矛盾的结果：804,306 行的 `armor_judgments` 纯 SELECT 只要 4.4ms，
而 83,289 行的 `candidate_failure_logs` 要 30.3ms——**大表比小表快 7 倍**。
原因是前一个表的数据已被前几轮实验反复扫描、全在 shared_buffers 里。

**因此单看耗时不可靠，扫描行数（`actual rows`）才可靠**：
有无 `ORDER BY` 的差别在扫描行数上是 67,608 vs 5,000（13.5 倍），
在冷热缓存不同的两次测量里则可能完全看不出来。

### 门与登记的连带更新

`TestData_RetentionTrim_PredicateColumnHasLeadingIndex` 的文件头新增一节
**「这道门不覆盖的东西：DELETE 形态」**，写明 sao 这个反例，并明确：
**本门判绿 ≠ 这条 DELETE 写得对**——形态与吞吐属于 worker 侧改造，
两者不可互相替代。登记里 armor 与 candidate_failure_logs 的修法字段
也已从「补索引」改写为实测结论。

### 修法方案（现在每一条都有实测背书）

| 表 | 定级 | 修法 | 实测收益 | 迁移 |
|---|---|---|---|---|
| `armor_judgments` | P1 | 改 `ctid` 形态 + 去 `ORDER BY`，再提高批量 | **124 倍**（870.7→7.0ms） | **不需要** |
| `candidate_failure_logs` | P2 | 同上 | **22 倍**（411→19.5ms） | **不需要** |
| `journal_snapshot_receipts` | **P3（实测降级）** | 暂不改 | 98ms vs 30ms，**绝对成本 2.4 秒/天** | 需要 |
| `session_aggregate_outbox` | 无缺陷 | — | 形态改动实测无收益 | — |
| `model_iq_runs` | P3 | 待触发后处理 | 0 行 | 需要 |


## R79 续十三 · 最后一条待实测：journal_snapshot_receipts 的 P2 被降级

续十二之后只剩这一条修法没实测。它形态上最可疑——**无 LIMIT、无分批**，
按理说没有提前终止可剪枝，Seq Scan 应该是真实成本。

### 测的时候先撞上一个新事实

建对照索引时报 `ERROR: column "id" does not exist`：

```
tenant_id | request_id | snapshot_version | payload_hash | status
claim_owner | claim_until | created_at | updated_at | projection_base_seq
```

`journal_snapshot_receipts` **没有 `id` 列，也没有主键**，唯一键是
`(tenant_id, request_id, snapshot_version)`。所以续十二的 `id IN` 形态
**对它根本不适用**，必须 ctid 形态 + `updated_at` 索引配合；
真写成 `id IN` 会直接 42703。留此注记是防止将来有人「顺手优化」成 id 形态。

### 实测（92,862 行，匹配仅 ~556 行，重复 4 次取中位）

| 方案 | 实测 |
|---|---|
| **现状**（无 LIMIT、无索引） | **98ms**（94.1 / 94.5 / 101.6 / 127.2） |
| 补 `(updated_at)` 索引，仍无 LIMIT | **285ms** ← **无收益** |
| `ctid IN` + `LIMIT 500` | **30ms**（3.2 倍） |

`LIMIT 500` 小于匹配数 556，所以子查询确实提前终止了（`actual rows=500`）——
这是续十二里唯一一次 ctid 形态的 Limit 真正起作用。

### P2 → P3：降级理由是绝对成本，不是相对倍数

3.2 倍看着不错，但换算成时间：

```
本表       98ms × 24 次/天 = 2.4 秒/天   （tick = 1h）
armor      870ms × 1 次/天 = 0.87 秒/天  （tick = 24h）
```

**单位时间成本几乎相同**（0.040 vs 0.036 ms/s），但本表的绝对值小两个量级，
改动的收益是「每天省 1.6 秒」。按「定级看实测、不看倍数」的标准，**降 P3**。

这是本轮第三次纠正定级（前两次：session_bodies P2→P3、armor 补索引作废）。
三次的共性都是**同一个错误：把相对倍数或估算数字当成了实际代价**。

### 形态风险仍然存在，但不是现实成本

无 LIMIT 无分批在**积压**时是真风险：一次事务里可能删掉远超预期的行数，
撞上 `CleanupExpired` 的 30s ctx 超时后整笔回滚，而失败只 `slog.Warn`
——保留期会静默失效。

但实测跨度 **7.0 天 = 7 天保留期**，说明写入与删除是平衡的、**积压不存在**。
所以这是潜在风险不是现实成本，登记为 P3 留待观察。

### 一个被顺手确认的事实

`domains/requestjourney/retention_test.go:172` 的断言只检查
`strings.Contains(stmt, "DELETE FROM journal_snapshot_receipts")` 与
`strings.Contains(stmt, "updated_at")` ——**纯文本断言，不校验列存在性**。
本轮 `go_sql_constant_prepare_test.go`（PREPARE 真库）覆盖了这条 SQL 且未报 42703，
说明该语句当前合法。**文本断言守不住列漂移，PREPARE 才守得住**——这是
续五那道门存在的理由，本轮又一次得到印证。

## R79 续十四 · tool_usage_stats：代码里写着 ≠ 会执行（P1 降 P2）

前 6 个 P1 里剩下的是列名/缺列类。`tool_usage_stats` 这条是改动面最小的
（纯列名问题），本打算直接出方案。动手前先核一遍可达性，结果定级翻了。

### 我之前记的「两个列名漂移」是错的

`domains/toolexecution/postgres_store.go` 的 INSERT 用 15 个列名，真库只有 11 列，
**12/15 个列名不存在**：

| 代码里的列 | 真库实际 |
|---|---|
| `tool_name` / `date` | `tool_id` / `usage_date` |
| `total_calls` / `success_calls` / `failed_calls` | `call_count` / `success_count` / `error_count` |
| `avg_duration_ms` | `avg_latency_ms` |
| `p50_duration_ms` / `p95_duration_ms` / `p99_duration_ms` | **不存在** |
| `unique_users` / `unique_sessions` / `top_users` | **不存在** |

`ToolUsageStats` 结构体（`types.go:107-131`）有 17 个字段，**依赖 6 个不存在的统计维度**。
所以修法不是改名，是路线选择：**加 8 列迁移** vs **改代码降级到现有 11 列**——
后者会丢掉 3 个分位数和 3 个去重维度。属 owner 决策。

### 真正的发现：整条统计写入路径零调用方

| 方法 | 生产调用点 |
|---|---|
| `StatsAggregator.AggregateDaily` | **0** |
| `Store.SaveStats`（含全部错列的 INSERT） | **0**（仅被 `AggregateDaily` 调） |
| `Store.ListToolNamesWithActivity` | **0**（仅被 `AggregateDaily` 调） |

`grep` 出的 `AggregateDailyProfiles` 是 providerprofile 的**另一个同名方法**，
不是 `toolexecution.StatsAggregator.AggregateDaily`——名字相似极易误判成有调用方。

**所以那 12 个错列从来没有被 PostgreSQL 执行过。** 它不是「线上故障」，
是一段**从未跑过的代码**。P1 是虚报，降 **P2**。

### 但缺陷是真的，而且更阴险

`AggregateDaily` 里每个工具的失败只 `slog.Error` 计数，函数**返回 nil**
（`stats_aggregator.go:60-84`）。也就是说：**一旦有人补一个每日 02:00 的定时任务**，
每条 INSERT 都会 42703，而调用方看到的是 `err == nil`，**以为聚合成功了**。

Hook 本身是活的（`main.go:3265` 注册，`Tracker` 写 `public.session_tools_hot`，
R30 审计修过）。所以形态是：**执行明细在写，聚合统计不写**。

### 两条证据必须分开（本轮的方法论要点）

| 证据 | 能不能用 |
|---|---|
| `AggregateDaily` 零调用方 | ✅ **代码事实，与流量无关**，是定级依据 |
| `tool_usage_stats` / `session_tools` 都是 0 行 | ❌ **本机是无流量的开发库**，空表属正常，不能当缺陷证据 |

我差点用第二条去论证第一条。**「表是空的」在开发库上不是发现。**

### 本轮第四次定级纠正，四次共性

| # | 纠正 | 我犯的错 |
|---|---|---|
| 1 | session_bodies P2→P3 | 把**峰值**当分布 |
| 2 | armor「补索引」作废 | 把 **cost 估算**当执行时间 |
| 3 | journal_snapshot_receipts P2→P3 | 把**相对倍数**当绝对成本 |
| 4 | **tool_usage_stats P1→P2** | 把**形态相似**当可达 |

第 4 条是最容易犯的：**代码里写着某段 SQL，只说明它被写下来了，不说明它会执行。**
判「某缺陷是否已造成影响」时，**可达性（谁调它）优先于形态（它看起来像在跑）**。

## R79 续十五 · 可达性登记门：潜伏缺陷被接线的那一刻

续十四证明了「形态相似 ≠ 可达」，但那是一张**静态贴纸**——今天核实过了，明天有人
把 `reasoncap.NewPGSource` 接上线，审计文档不会响。这道门就是那个响声。

`TestData_SchemaMismatch_ReachabilityIsStillAccurate`（不需要连库，只读源码）：
登记 4 条 schema 不匹配发现，每条带一个**可机械复核的锚点**（AST 层面的
`pkg.Symbol`），断言其实测调用点数与登记一致。三种红法：

| 情形 | 门说什么 |
|---|---|
| 登记 ORPHAN，今天有调用 | 「那段与真库不匹配的 SQL 现在会执行了，**这是本门存在的理由**」 |
| 登记 LIVE，今天零调用 | 「别让它悄悄退化成潜伏陷阱，请改判为无调用方并重估定级」 |
| 条数与棘轮不符 | 指名差几条 |

当前 4 条：**活接线 2、潜伏 2**。
- 潜伏：`toolexecution.tool_usage_stats`（12/15 错列）、`reasoncap.model_aliases.alias`
- 活接线：routeincident 两处缺列（`main.go:3554`）、`session_turns` 视图漏投影
  `origin_actor`（`main_pipeline.go:1416` + `:1185`）

### 门自己暴露的三个问题（都由首跑与变异检验抓出，不是 review）

**1. 假绿：map 取错了 key。**
```go
for _, sym := range needles { ... }   // 拿到的是 value（登记名字），不是 key（symbol）
```
于是 `res` 的 key 全是登记名，而后面查的是 `out[f.symbol]`（真 symbol），
**所有匹配恒为 0**——两条 ORPHAN 登记「碰巧对了」。**门在自己身上制造了假绿**，
而它绿着的原因恰好是最危险的那一类：潜伏项被判成潜伏。

**2. 假阳性：文本匹配把注释当调用点。**
`credentialquota/postgres.go` 的注释 `// NewPGSource wraps a *pgxpool.Pool...`
会让 `reasoncap.NewPGSource` 之外的同形文本命中。

**3. 假阴性：用正则剥注释反而更糟。**
`//` 与 `/*` 会在字符串字面量里出现（`"https://..."`、SQL 注释），正则会从那里
一路吞到行尾或下一个 `*/`，**把真实代码一起删掉**。剥离注释后
`routeincident.NewObserver` 从 1 变 0 就是这么来的。

**修法：判「有没有调用」就在 AST 层面判**，用 `go/parser` 找 `SelectorExpr`。
注释和字符串根本不进 AST，两个问题一起消失。

### 变异检验：锚点必须指向「接线」而不是「构造」

第二次变异**没红**，又抓到一个真问题。我用 `routeincident.NewObserver`（构造函数）
当锚点，于是摘掉 `AddOnRequestLogPersisted(incidentObserver.AsHook())` 那一行后，
`main.go:3545` 的构造调用仍在，门全绿。

**构造了不等于挂上了。** 可达性指的是「缺陷路径真的接进了请求链路」，
不是「有人 new 了这个对象」。锚点改成 `incidentObserver.AsHook`（注册点）后：

| 变异 | 门的反应 |
|---|---|
| 给 `reasoncap.NewPGSource` 加一处调用 | 红「登记为无生产调用方，但今天找到 1 处调用：internal/paramguard/guard.go」+「这是本门存在的理由」 |
| 摘掉 `incidentObserver.AsHook` 注册 | 红「它可能已被摘掉或改名——别让它悄悄退化成潜伏陷阱」 |

### 顺带：fail-closed 抓到了并发会话的冲突中间态

AST 解析在健康树之外立刻抓到 7 个 `<<<<<<<` 冲突文件（并发会话遗留）。
门把「工作区冲突态」与「代码解析失败」分成两条红因——前者是瞬时状态不是缺陷，
但覆盖率缺口必须可见（会报出具体文件，冲突解决后自动覆盖）。

> 这也是本轮第二次工具性陷阱的教训：**`grep | head -N` 的截断会制造假零命中**。
> 我一度用 `head -6` grep `AsHook()`，结论「零调用方」——`main.go:3554` 正好被截掉。
> **判「零命中」之前先确认没有截断。**
