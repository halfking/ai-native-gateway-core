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
