-- ═══════════════════════════════════════════════════════════════════════════
-- verify_write_amplification_252.sql
--
-- 用途（§143.49，2026-10-21）：从 **252 的 PG 日志侧**（`pg_stat_statements` 的
--       **WAL 字节** + `pg_stat_user_tables` 的行级计数）给「写放大」排名，
--       找出最贵的写入语句与最热的表，为「数据与字段拆分 / 更新与查询重构」提供目标。
--
-- ★★★ 为什么用 WAL 字节而不是耗时：
--   「写放大」的直接度量是**写进 WAL 的字节**（要复制、要压缩、要落盘）。
--   `pg_stat_statements` 自 PG 15 起带 `wal_bytes` 列 ⇒ ⇒ **每个语句的 WAL 字节可排名**。
--   ⚠ **耗时 ≠ 写放大**：一个执行 5 秒但只写 20 行的 UPDATE 不是写放大；
--     一个执行 20 ms 但写了 50 MB 的 UPDATE 才是。
--   ⚠ 反之亦然：`ursm_node_snapshot_min` 每次调用 **0.32 ms**，但 **1,011 万次** ⇒ 4,017 MB。
--
-- ★★★★★★★ 两条【口径铁律】（本脚本的全部 pss 查询都必须遵守，否则读数作废）：
--
--   ★ 铁律一：**`pg_stat_statements` 视图是【跨数据库】的**。
--     实测（252，2026-10-21）：该实例有 **30 个库**，`pg_stat_statements` 里
--     `llm_gateway` 只占 3,863 条 / 23 GB，而 `smm` 有 447 条 / 3,800 MB，
--     另有 281 条指向**已被 DROP 的库**（dbid 在 `pg_database` 里查无此库）⇒ ⇒
--     **不加 `dbid` 过滤 ⇒ 排名会把别的产品混进来**（§143.19 已就慢查询写下过同一条纪律，
--     本轮脚本首版漏了 ⇒ 已在 §143.49 里就地订正）。
--     ⇒ ⇒ **所有 pss 查询一律加 `dbid = (SELECT oid FROM pg_database WHERE datname = current_database())`**。
--
--   ★ 铁律二：**pss 的统计窗口与 `pg_stat_wal` 的统计窗口【不是同一个】**。
--     实测（252，2026-10-21）：
--       `pg_stat_statements_info.stats_reset` = **2026-10-18 22:03:37**（窗口 **2.42 天**）
--       `pg_stat_wal.stats_reset`            = **2026-10-10 16:12:27**（窗口 **10.66 天**）
--     ⇒ ⇒ **语句级 WAL 数字只覆盖 2.42 天，总量覆盖 10.66 天 ⇒ 两者不可直接相除**。
--
-- ★★★ 铁律三（覆盖率）：**pss 记账的 WAL 远小于实际 WAL**。
--   实测（252，2026-10-21）：实际 WAL 累计 149.7 GB，而 pss 按 dbid 记账的只有 23 GB
--   ⇒ ⇒ **覆盖率 ≈ 15%** ⇒ ⇒ **下面的「排名」是【相对排名】，不是「绝对占比」**。
--   ⇒ ⇒ W1b 段专门测这个覆盖率，**读排名前必须先看 W1b**。
--
-- ★★ 本脚本**只读**：全部语句为 SELECT，无 DDL/DML。
-- 用法：bash ~/.mavis-tools/lgwp-prod.sh <本文件> 900
-- ═══════════════════════════════════════════════════════════════════════════

SET statement_timeout = '240s';
SET lock_timeout = '5s';
SET work_mem = '32MB';
SET jit = off;

-- ★★★ 统一口径：当前库 oid（本脚本所有 pss 查询都引用它）
-- ⚠ **不要用临时表**：生产只读护栏会拦 DDL（CREATE TEMP TABLE）⇒ 这里一律用子查询。

\echo '### W0. 量具自检（★ PG 版本 / 扩展 / 统计是否已重置 / ★ 两个统计窗口）'
SELECT version() AS pg_version,
       (SELECT count(*) FROM pg_extension WHERE extname = 'pg_stat_statements') AS pss_ext,
       current_setting('pg_stat_statements.track', true) AS pss_track,
       (SELECT stats_reset FROM pg_stat_database WHERE datname = current_database()) AS db_stats_reset,
       -- ★ 铁律二：两个窗口
       (SELECT stats_reset FROM pg_stat_statements_info) AS pss_stats_reset,
       (SELECT stats_reset FROM pg_stat_wal) AS wal_stats_reset,
       now() AS read_at;

\echo '### W0b. ★★ 本库 oid + pss 条目上限（★ 为铁律一/三的过滤与覆盖率做准备）'
SELECT (SELECT oid FROM pg_database WHERE datname = current_database()) AS current_db_oid,
       (SELECT current_setting('pg_stat_statements.max', true)) AS pss_max,
       (SELECT dealloc FROM pg_stat_statements_info) AS pss_dealloc,
       (SELECT count(*) FROM pg_stat_statements) AS pss_all_db_entries,
       (SELECT count(*) FROM pg_stat_statements WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())) AS pss_cur_db_entries;

\echo '### W1. ★★ 全库写放大总量（★ pg_stat_wal：与 dbid 无关，是实例级）'
SELECT (SELECT wal_bytes FROM pg_stat_wal) AS wal_bytes_total,
       pg_size_pretty((SELECT wal_bytes FROM pg_stat_wal)) AS wal_total,
       (SELECT wal_records FROM pg_stat_wal) AS wal_records,
       (SELECT wal_fpi FROM pg_stat_wal) AS wal_fpi,
       (SELECT count(*) FROM pg_stat_statements WHERE wal_bytes > 0) AS stmts_with_wal,
       (SELECT count(*) FROM pg_stat_statements) AS stmts_total;

\echo '### W1b. ★★★★★ pss 按 dbid 分解（★ 铁律一）+ ★ 记账覆盖率（★ 铁律三）'
SELECT d.datname AS db,
       (s.dbid = (SELECT oid FROM pg_database WHERE datname = current_database())) AS is_current_db,
       count(*) AS stmts,
       sum(s.calls) AS calls,
       pg_size_pretty(sum(s.wal_bytes)) AS wal
  FROM pg_stat_statements s
  LEFT JOIN pg_database d ON d.oid = s.dbid
 GROUP BY 1, 2
 ORDER BY sum(s.wal_bytes) DESC;

\echo '### W1c. ★★★★★ 覆盖率：pss 记账 vs 实际 WAL（★ ★ 读任何排名前先看这里）'
SELECT pg_size_pretty((SELECT sum(wal_bytes) FROM pg_stat_statements WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database()))) AS pss_wal_cur_db,
       pg_size_pretty((SELECT sum(wal_bytes) FROM pg_stat_statements)) AS pss_wal_all_db,
       pg_size_pretty((SELECT wal_bytes FROM pg_stat_wal)) AS actual_wal_total,
       round(100.0 * (SELECT sum(wal_bytes) FROM pg_stat_statements WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database()))
                   / NULLIF((SELECT wal_bytes FROM pg_stat_wal), 0), 2) AS coverage_cur_db_pct,
       round(100.0 * (SELECT sum(wal_bytes) FROM pg_stat_statements)
                   / NULLIF((SELECT wal_bytes FROM pg_stat_wal), 0), 2) AS coverage_all_db_pct;

\echo '### W2. ★★★ ★★ 本库（dbid 已过滤）按 WAL 字节排名的语句 TOP 25（★ 写放大的主战场）'
SELECT calls,
       rows,
       pg_size_pretty(wal_bytes) AS wal,
       pg_size_pretty(shared_blks_written * 8192::bigint) AS blks_written,
       round(total_exec_time::numeric, 1) AS total_ms,
       round(mean_exec_time::numeric, 2) AS mean_ms,
       left(regexp_replace(query, '\s+', ' ', 'g'), 150) AS q
  FROM pg_stat_statements
 WHERE wal_bytes > 0
   AND dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
 ORDER BY wal_bytes DESC
 LIMIT 25;

\echo '### W3. ★★★ 本库按「每次调用的 WAL 字节」排名的语句 TOP 20'
SELECT calls,
       rows,
       pg_size_pretty(wal_bytes) AS wal_total,
       pg_size_pretty((wal_bytes / calls)::bigint) AS wal_per_call,
       round((rows::numeric / calls)::numeric, 2) AS rows_per_call,
       left(regexp_replace(query, '\s+', ' ', 'g'), 150) AS q
  FROM pg_stat_statements
 WHERE wal_bytes > 0 AND calls > 0
   AND dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
 ORDER BY (wal_bytes / calls) DESC
 LIMIT 20;

\echo '### W4. ★★ 本库按语句类别聚合（★ 各占多少 WAL）'
SELECT CASE
         WHEN query ~* '^\s*insert' THEN '1_insert'
         WHEN query ~* '^\s*update' THEN '2_update'
         WHEN query ~* '^\s*delete' THEN '3_delete'
         WHEN query ~* '^\s*refresh' THEN '4_refresh_matview'
         WHEN query ~* '^\s*(select|with)' THEN '5_select_with'
         ELSE '6_other' END AS kind,
       count(*) AS distinct_stmts,
       sum(calls) AS calls,
       pg_size_pretty(sum(wal_bytes)) AS wal,
       round(sum(total_exec_time)::numeric, 1) AS total_ms
  FROM pg_stat_statements
 WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
 GROUP BY 1 ORDER BY 1;

\echo '### W5. ★★★ promote 家族 vs hot 写入：WAL 成本倍率（★ 「搬一遍」贵多少）'
-- ★ 判别式：会话族每行先写 hot、再 promote 进分区 ⇒ 若 promote 的 WAL 远大于 hot 写入，
--   说明**搬动本身**是写放大的主因（而不是行数）。
-- ★★ ★ 注意口径：`promote_*` 是**函数调用**（`SELECT promote_xxx(...)`），
--   `rows` 列记的是**函数返回的行数（= 调用次数）**，**不是被搬走的行数**！
--   ⇒ ⇒ `wal_per_row` 这一列在 promote 家族里**实际是「每次调用的 WAL」**，
--   **不能**与 hot 家族的「每行 WAL」直接比大小 ⇒ ★ 必须先分清「行」还是「次」。
WITH fam AS (
  SELECT CASE
           WHEN query ILIKE '%promote_session_bodies%' THEN 'promote_session_bodies'
           WHEN query ILIKE '%promote_session_turns%' THEN 'promote_session_turns'
           WHEN query ILIKE '%promote_session_turn_details%' THEN 'promote_session_turn_details'
           WHEN query ILIKE '%promote_session_memora%' THEN 'promote_session_memora'
           WHEN query ILIKE '%promote_usage_ledger%' THEN 'promote_usage_ledger'
           WHEN query ILIKE '%promote_request_wal%' THEN 'promote_request_wal'
           WHEN query ILIKE '%_hot %' OR query ILIKE '%_hot(' THEN 'insert_into_hot'
           ELSE 'other' END AS kind,
         calls, wal_bytes, rows
    FROM pg_stat_statements
   WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
     AND (query ILIKE '%promote_%' OR query ILIKE '%_hot %' OR query ILIKE '%_hot(')
)
SELECT kind,
       count(*) AS stmts,
       sum(calls) AS calls,
       sum(rows) AS rows,
       pg_size_pretty(sum(wal_bytes)) AS wal,
       -- ★ 统一按「每次调用」算 ⇒ 两族可比
       pg_size_pretty((sum(wal_bytes) / NULLIF(sum(calls), 0))::bigint) AS wal_per_call,
       -- ★ 行/次比：hot 家族有意义；promote 家族「行=调用数」⇒ 这一列仅供 hot 家族解读
       pg_size_pretty((sum(wal_bytes) / NULLIF(sum(rows), 0))::bigint) AS wal_per_row_or_call
  FROM fam
 GROUP BY 1 ORDER BY 5 DESC;

\echo '### W6. ★★★ ursm_node_snapshot_min 族在 252 上的形态（★ 对上 §143.18 的 8.4 GB / 7 天）'
SELECT c.relname AS part,
       c.relkind,
       c.reltuples::bigint AS approx_rows,
       pg_size_pretty(pg_relation_size(c.oid)) AS heap,
       pg_size_pretty(pg_indexes_size(c.oid)) AS indexes,
       pg_size_pretty(pg_total_relation_size(c.oid)) AS total
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = 'public'
   AND c.relname LIKE 'ursm_node_snapshot_min%'
 ORDER BY pg_total_relation_size(c.oid) DESC
 LIMIT 20;

\echo '### W7. ★★ ursm_node_snapshot_min 的写入来源（★ 谁在高频写它）'
SELECT calls,
       rows,
       pg_size_pretty(wal_bytes) AS wal,
       round(mean_exec_time::numeric, 3) AS mean_ms,
       left(regexp_replace(query, '\s+', ' ', 'g'), 220) AS q
  FROM pg_stat_statements
 WHERE query ILIKE '%ursm_node_snapshot_min%'
   AND dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
 ORDER BY wal_bytes DESC
 LIMIT 12;

\echo '### W8. ★★ 本库内「同名语句多形态」的排查（★ 判「同源双写」：★ 用 dbid 过滤后重做）'
-- ★ 本段替代首版的「daily_kline 两形态」：首版那条 daily_kline 的 INSERT
--   **属于同实例的另一个库（smm）**，不在 llm_gateway ⇒ 首版结论作废。
--   ⇒ 这里改成【通用形态】：列出本库里「同一张表被 INSERT 了多种参数形态」的情况。
SELECT CASE
         WHEN query ILIKE 'insert into %' THEN regexp_replace(query, '\s*\(.*', '', 'g')
         WHEN query ILIKE 'update %' THEN regexp_replace(query, '\s+where.*', '', 'g')
         ELSE left(regexp_replace(query, '\s+', ' ', 'g'), 60) END AS stmt_shape,
       count(*) AS n_forms,
       sum(calls) AS calls,
       pg_size_pretty(sum(wal_bytes)) AS wal
  FROM pg_stat_statements
 WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
   AND wal_bytes > 0
 GROUP BY 1
 HAVING count(*) > 1
 ORDER BY sum(wal_bytes) DESC
 LIMIT 15;

\echo '### W9. ★★ 表级：写入行数最多的表（★ 行级计数侧；★ 本来就是本库的）'
SELECT relname AS table_name,
       n_tup_ins, n_tup_upd, n_tup_del, n_tup_hot_upd,
       n_live_tup, n_dead_tup,
       round(100.0 * n_dead_tup / NULLIF(n_live_tup + n_dead_tup, 0), 2) AS dead_pct,
       pg_size_pretty(pg_total_relation_size(relid)) AS total_size,
       pg_size_pretty(pg_relation_size(relid)) AS heap_size,
       pg_size_pretty(pg_indexes_size(relid)) AS index_size
  FROM pg_stat_user_tables
 WHERE n_tup_ins + n_tup_upd + n_tup_del > 0
 ORDER BY (n_tup_ins + n_tup_upd + n_tup_del) DESC
 LIMIT 25;

\echo '### W10. ★★ 全库对象体积 TOP 25（★ 存储现状总貌；★ 本来就是本库的）'
SELECT n.nspname AS schema, c.relname AS object,
       c.relkind,
       c.reltuples::bigint AS approx_rows,
       pg_size_pretty(pg_total_relation_size(c.oid)) AS total_size,
       pg_size_pretty(pg_relation_size(c.oid)) AS heap,
       pg_size_pretty(pg_indexes_size(c.oid)) AS indexes
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE c.relkind IN ('r','m','p')
   AND n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
   AND pg_total_relation_size(c.oid) > 0
 ORDER BY pg_total_relation_size(c.oid) DESC
 LIMIT 25;

\echo '### W11. ★★ checkpoint 与 WAL FPI（★ 判「FPI 是否是 WAL 的大头」）'
-- ★ PG17：checkpoint 统计已移到 `pg_stat_checkpointer`（`pg_stat_bgwriter` 里没有
--   `checkpoints_timed` 列了 ⇒ 直接查 pg_stat_bgwriter 会 42703）。
SELECT num_timed, num_requested, write_time, sync_time, buffers_written, stats_reset
  FROM pg_stat_checkpointer;

-- ═══════════════════════════════════════════════════════════════════════════
-- §143.50 追加段（W12~W16）：★ 把 §143.49 的三个「疑似口径错」逐一钉死
--   W12/W13：promote 家族的【确定口径】与「每行 WAL」的真实值
--   W14    ：hot 表 vs 分区表的【索引数】（解释搬运成本）
--   W15    ：行宽的【全表/分层】口径（★ 推翻「无序 LIMIT 抽样」的读数）
--   W16    ：hot 表的 kind 分布（★ 判 final_full 开关当前是否真的关着）
-- ═══════════════════════════════════════════════════════════════════════════

\echo '### W12. ★★★★★ 按【INSERT 目标表】聚合 WAL（★ 比 LIKE 模式匹配更确定）'
-- ★ 动机：§143.49 的 W5 用 `ILIKE '%_hot %'` 归「hot 家族」⇒ 漏不漏无法证伪。
--   ★ 本段从 query 文本里【提取目标表名】⇒ 确定口径。
-- ★ ★ 注意：★ 「hot 家族」的判定正则**不能**用 `_hot\b` ——
--   `_hot` 后面紧跟的是词字符（表名继续），`\b` 不成立 ⇒ **会把 hot 表全漏掉**（实测）。
--   ⇒ 用 `_hot(\s|\(|$)` 判定。
WITH t AS (
  SELECT calls, rows, wal_bytes, query
    FROM pg_stat_statements
   WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
     AND query ~* '^\s*insert'
)
SELECT CASE
         WHEN regexp_replace(query, '^\s*INSERT\s+INTO\s+', '', 'i') ~* '^(public\.)?[a-z0-9_]+_hot(\s|\(|$)'
              THEN '★_hot 表族（直接 INSERT）'
         ELSE regexp_replace(
                regexp_replace(query, '^\s*INSERT\s+INTO\s+', '', 'i'),
                '\s*\(.*', '', 'g')
       END AS target_table,
       count(*) AS stmts,
       sum(calls) AS calls,
       sum(rows) AS rows,
       pg_size_pretty(sum(wal_bytes)) AS wal,
       pg_size_pretty((sum(wal_bytes) / NULLIF(sum(rows), 0))::bigint) AS wal_per_row
  FROM t
 GROUP BY 1
 ORDER BY sum(wal_bytes) DESC
 LIMIT 30;

\echo '### W13. ★★★★★ promote 家族 vs hot 表族：确定口径的两桶对比'
-- ★ ★ 口径要点：`promote_*` 是 `SELECT promote_xxx(...)` ⇒ 它内部的 INSERT 在
--   `track = top` 下【不单独记账】⇒ ⇒ promote 的 WAL **全部记在函数调用那条语句上**
--   ⇒ ⇒ 「hot 表的直接 INSERT」与「promote 调用」必须**分成两个桶**比。
--   ★ ★★ 且 `pg_stat_statements.rows` 对函数调用记的是【返回行数 = 调用次数】，
--   **不是被搬走的行数** ⇒ ⇒ 「每行 WAL」在 promote 桶里【没有意义】，
--   只有「每次调用的 WAL」可比；被搬走的行数要用**表级 `n_tup_del`** 去反推。
WITH hot AS (
  SELECT sum(calls) AS calls, sum(rows) AS rows, sum(wal_bytes) AS wal
    FROM pg_stat_statements
   WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
     AND query ~* '^\s*insert'
     AND regexp_replace(query, '^\s*INSERT\s+INTO\s+', '', 'i') ~* '^(public\.)?[a-z0-9_]+_hot(\s|\(|$)'
), pro AS (
  SELECT sum(calls) AS calls, sum(rows) AS rows, sum(wal_bytes) AS wal
    FROM pg_stat_statements
   WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
     AND query ~* '^\s*select\s+promote_'
)
SELECT 'hot 表族：直接 INSERT' AS bucket, calls, rows, pg_size_pretty(wal) AS wal FROM hot
UNION ALL
SELECT 'promote 家族：搬进分区', calls, rows, pg_size_pretty(wal) FROM pro;

\echo '### W13b. ★★ 每个 promote 函数的明细（★ 每次调用的 WAL 与 mean_ms）'
SELECT calls, rows,
       pg_size_pretty(wal_bytes) AS wal,
       pg_size_pretty((wal_bytes / calls)::bigint) AS wal_per_call,
       round(mean_exec_time::numeric, 1) AS mean_ms,
       regexp_replace(query, '\s+', ' ', 'g') AS q
  FROM pg_stat_statements
 WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
   AND query ~* '^\s*select\s+promote_'
 ORDER BY wal_bytes DESC;

\echo '### W14. ★★ hot 表 vs 分区表的索引数（★ 解释「搬进分区要维护几套索引」）'
SELECT c.relname AS tbl,
       (SELECT count(*) FROM pg_index i WHERE i.indrelid = c.oid) AS n_indexes,
       pg_size_pretty(pg_indexes_size(c.oid)) AS idx_size,
       pg_size_pretty(pg_relation_size(c.oid)) AS heap_size
  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = 'public'
   AND c.relname IN ('session_bodies_hot','session_bodies',
                     'session_turns_hot','session_turns',
                     'session_turn_details_hot','session_turn_details')
 ORDER BY c.relname;

\echo '### W15. ★★★★ 行宽对比：hot 表【全表】vs 分区表【代表日窗口】（★ IO 可控）'
-- ★ ★★ 口径纪律：★ 【不要用无序 LIMIT 抽样】——
--   实测「LIMIT 2000 无序抽样」给出 hot 平均 3,075 B，而**全表是 13 kB**（差 4.2 倍）
--   ⇒ ⇒ hot 表里约 14.6% 的行极度巨大（平均约 69 kB/行）⇒ ⇒ 无序抽样会完全错过尾部。
-- ★ ★ 分区表【全表扫】实测 60 s 超时（259,916 行 × 大 TOAST）⇒ ⇒ 改「按 ts 索引取代表日」。
SELECT 'session_bodies_hot (全表)' AS tbl,
       count(*) AS rows,
       pg_size_pretty(sum(pg_column_size(t))::bigint) AS total_row_bytes,
       pg_size_pretty(avg(pg_column_size(t))::bigint) AS avg_row_bytes,
       pg_size_pretty(max(pg_column_size(t))::bigint) AS max_row_bytes
  FROM public.session_bodies_hot t
UNION ALL
SELECT 'session_bodies_2026_10 (代表日)',
       count(*),
       pg_size_pretty(sum(pg_column_size(t))::bigint),
       pg_size_pretty(avg(pg_column_size(t))::bigint),
       pg_size_pretty(max(pg_column_size(t))::bigint)
  FROM public.session_bodies_2026_10 t
 WHERE ts >= '2026-10-15 00:00:00+00' AND ts < '2026-10-16 00:00:00+00';

\echo '### W15b. ★★★★ 胖在哪一列：有/无 outbound_body 的每列字节（★ 分区表代表日）'
SELECT (outbound_body IS NOT NULL AND outbound_body <> 'null'::jsonb) AS has_outbound,
       count(*) AS rows,
       pg_size_pretty(avg(pg_column_size(t))::bigint) AS avg_row_bytes,
       pg_size_pretty(avg(pg_column_size(t.outbound_body))::bigint) AS avg_outbound_bytes,
       pg_size_pretty(avg(pg_column_size(t.request_delta))::bigint) AS avg_request_delta_bytes,
       pg_size_pretty(avg(pg_column_size(t.response_delta))::bigint) AS avg_response_delta_bytes
  FROM public.session_bodies_2026_10 t
 WHERE ts >= '2026-10-15 00:00:00+00' AND ts < '2026-10-16 00:00:00+00'
 GROUP BY 1 ORDER BY 1;

\echo '### W15c. ★★★ hot 表同样口径（★ 与 W15b 对照）'
SELECT (outbound_body IS NOT NULL AND outbound_body <> 'null'::jsonb) AS has_outbound,
       count(*) AS rows,
       pg_size_pretty(avg(pg_column_size(t))::bigint) AS avg_row_bytes,
       pg_size_pretty(avg(pg_column_size(t.outbound_body))::bigint) AS avg_outbound_bytes,
       pg_size_pretty(avg(pg_column_size(t.request_delta))::bigint) AS avg_request_delta_bytes,
       pg_size_pretty(avg(pg_column_size(t.response_delta))::bigint) AS avg_response_delta_bytes
  FROM public.session_bodies_hot t
 GROUP BY 1 ORDER BY 1;

\echo '### W16. ★★ hot 表的 kind 分布（★ 判 final_full 开关当前是否真的关着）'
-- ★ 仓内事实源：`domains/session/v2/bodies_writer.go` 的
--   `WriteFinalFullInTx` 也写 `session_bodies_hot`（kind='final_full'），
--   而 `WriteTurnDelta` 写 kind='turn_delta'
--   ⇒ ⇒ ★ hot 表里若【没有 final_full】⇒ ⇒ 说明该开关当前确实是关的
--   （§143.48 已读 `storage.session_final_full_enabled = false` ⇒ 两边可交叉验证）。
SELECT kind,
       count(*) AS rows,
       pg_size_pretty(avg(pg_column_size(t))::bigint) AS avg_row_bytes,
       pg_size_pretty(sum(pg_column_size(t))::bigint) AS total_bytes
  FROM public.session_bodies_hot t
 GROUP BY 1 ORDER BY 3 DESC;

\echo '### W17. ★★ hot 表的行级计数（★ ★ 用 n_tup_del 反推「每次 promote 搬多少行」）'
-- ★ ★★ 这是本节最关键的一个交叉核对：
--   `pg_stat_statements.rows` 对 promote 记的是【调用次数】，不是被搬走的行数
--   ⇒ ⇒ 「每次 promote 搬 N 行」必须由【表级 n_tup_del ÷ promote 调用次数】反推
--   ⇒ ⇒ 再与仓内 `promoteBatchSize`（默认 5000）对照 ⇒ 看「批量」是否真的在起作用。
SELECT relname, n_tup_ins, n_tup_upd, n_tup_del, n_live_tup, n_dead_tup,
       pg_size_pretty(pg_total_relation_size(relid)) AS total
  FROM pg_stat_user_tables
 WHERE relname ~* '_hot$'
 ORDER BY (n_tup_ins + n_tup_upd + n_tup_del) DESC
 LIMIT 15;

