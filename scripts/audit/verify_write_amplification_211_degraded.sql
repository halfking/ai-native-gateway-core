-- ═══════════════════════════════════════════════════════════════════════════
-- verify_write_amplification_211_degraded.sql
--
-- 用途（§143.49，2026-10-21）：**211 实例的「降级版」写放大读数**。
--
-- ★★★ 为什么需要「降级版」：
--   211 的 `pg_stat_statements` **扩展装了（1.11）但未进 `shared_preload_libraries`**
--   （§143.20.4 的 C4）⇒ ⇒ **语句级 WAL 排名在 211 上完全不可用**
--   （实测报错：`pg_stat_statements must be loaded via "shared_preload_libraries"`）。
--   ⇒ ⇒ 但 **`pg_stat_wal` 是 PG 14+ 的【内置视图】，不依赖 preload** ⇒ **总量与速率仍可测**。
--
-- ★★ 本脚本的取舍（诚实边界）：
--   ✅ 能测：全库 WAL 总量与**速率**（`pg_stat_wal`，两次采样求差）、
--            表级写入行数（`pg_stat_user_tables`）、对象体积（`pg_class`）。
--   ❌ 不能测：**按语句的 WAL 排名**（252 侧 W2/W3/W4/W5/W7/W8 全部依赖 `pg_stat_statements`）。
--   ❌ 也不能测：**按 backend 归因** ⇒ ★ **PG17 的 `pg_stat_activity` 已没有 WAL 列**
--      （实测 252 与 211 上 `attname LIKE 'wal%'` 均 0 行 ⇒ 两实例一致 ⇒ 不是配置差异）。
--   ⇒ ⇒ **211 侧拿不到「哪条语句最贵」** ⇒ ⇒ **§143.49 的结论主体仍以 252 为准**，
--         本脚本只提供「211 的 WAL 总量/速率与表级形态」这一层旁证。
--
-- ★★ 本脚本**只读**：全部语句为 SELECT，无 DDL/DML。
-- 用法（211，docker）：
--   ssh 211 'docker exec -i acc-pg17 psql -U acc_super -d llm_gateway \
--            -v ON_ERROR_STOP=1 -P pager=off -f -' < 本文件
-- ═══════════════════════════════════════════════════════════════════════════

SET statement_timeout = '120s';
SET lock_timeout = '5s';
SET work_mem = '32MB';
SET jit = off;

\echo '### D0. ★★★ 量具自检（★ PG 版本 / preload / 统计是否已重置）'
SELECT version() AS pg_version,
       (SELECT count(*) FROM pg_extension WHERE extname = 'pg_stat_statements') AS pss_ext,
       (SELECT current_setting('shared_preload_libraries', true)) AS preload_libraries,
       (SELECT stats_reset FROM pg_stat_database WHERE datname = current_database()) AS db_stats_reset,
       (SELECT stats_reset FROM pg_stat_wal) AS wal_stats_reset;

\echo '### D0b. ★★ pss 视图是否存在 + 是否可查（★ 安全探测：报错不中止脚本）'
-- ★ 注意：扩展装在 `public` schema（实测 211 上 `to_regclass('pg_stat_statements')` 有值、
--   `to_regclass('pg_catalog.pg_stat_statements')` 为 NULL）⇒ ⇒ **不要写死 pg_catalog 前缀**
--   （写死会直接 42P01 中止脚本，量具就白跑了）。
SELECT to_regclass('pg_stat_statements') AS pss_view_exists,
       (SELECT n.nspname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
         WHERE c.relname = 'pg_stat_statements') AS pss_schema,
       (SELECT count(*) FROM pg_attribute
         WHERE attrelid = to_regclass('pg_stat_statements')
           AND attname = 'wal_bytes') AS pss_has_wal_bytes_col;
-- ★ 用 DO/EXCEPTION 捕获实际可查性（★ 不抛异常 ⇒ 不中止脚本）
-- ★ 两个视图一起探：`pg_stat_statements`（语句明细）与 `pg_stat_statements_info`（统计元信息）
--   ⇒ ★ 二者都依赖 preload ⇒ 任一不可用都证明「降级成立」。
DO $$
DECLARE
  n bigint;
  m bigint;
BEGIN
  SELECT count(*) INTO n FROM pg_stat_statements;
  RAISE NOTICE 'pg_stat_statements: ★ 可查，rows=%', n;
EXCEPTION WHEN OTHERS THEN
  RAISE NOTICE 'pg_stat_statements: ★ 不可用（降级成立），err=%', SQLERRM;
END $$;
DO $$
DECLARE
  d bigint;
BEGIN
  SELECT dealloc INTO d FROM pg_stat_statements_info;
  RAISE NOTICE 'pg_stat_statements_info: ★ 可查，dealloc=%', d;
EXCEPTION WHEN OTHERS THEN
  RAISE NOTICE 'pg_stat_statements_info: ★ 不可用，err=%', SQLERRM;
END $$;

\echo '### D1. ★★★ 全库 WAL 总量（★ 内置视图 ⇒ 不依赖 preload）'
SELECT (SELECT wal_bytes FROM pg_stat_wal) AS wal_bytes_total,
       pg_size_pretty((SELECT wal_bytes FROM pg_stat_wal)) AS wal_total,
       (SELECT wal_records FROM pg_stat_wal) AS wal_records,
       (SELECT wal_fpi FROM pg_stat_wal) AS wal_fpi,
       (SELECT wal_buffers_full FROM pg_stat_wal) AS wal_buffers_full,
       (SELECT wal_write FROM pg_stat_wal) AS wal_write,
       (SELECT wal_sync FROM pg_stat_wal) AS wal_sync,
       (SELECT round(wal_write_time::numeric, 1) FROM pg_stat_wal) AS wal_write_time_ms,
       (SELECT round(wal_sync_time::numeric, 1) FROM pg_stat_wal) AS wal_sync_time_ms;

\echo '### D2. ★★★ WAL 生成速率（★ 10 秒两次采样求差 ⇒ 内置可测）'
-- ★ 为什么测速率而不是总量：总量是「自 stats_reset 起的累计值」，
--   两个实例的统计起点不同 ⇒ **速率才是可跨实例对拍的口径**。
WITH a AS (SELECT wal_bytes AS b0, wal_records AS r0, clock_timestamp() AS t0 FROM pg_stat_wal),
     s AS (SELECT pg_sleep(10)),
     b AS (SELECT wal_bytes AS b1, wal_records AS r1, clock_timestamp() AS t1 FROM pg_stat_wal)
SELECT round((b.b1 - a.b0) / 1024.0 / 1024.0, 3) AS wal_mb_per_10s,
       round((b.b1 - a.b0) / 1024.0 / 1024.0 / 10.0 * 3600, 1) AS wal_mb_per_hour,
       (b.r1 - a.r0) AS wal_records_per_10s,
       round(extract(epoch FROM (b.t1 - a.t0))::numeric, 2) AS elapsed_s
  FROM a, s, b;

\echo '### D3. ★★★ 表级：写入行数最多的表（★ 211 侧与 252 对拍）'
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

\echo '### D4. ★★★ 全库对象体积 TOP 25（★ 211 侧与 252 对拍）'
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

\echo '### D5. ★★ 211 的 ursm_node_snapshot_min 形态（★ 对上 §143.18 / §143.32：211 上是普通表非分区表）'
SELECT c.relname AS object,
       c.relkind,
       c.reltuples::bigint AS approx_rows,
       pg_size_pretty(pg_total_relation_size(c.oid)) AS total_size,
       pg_size_pretty(pg_relation_size(c.oid)) AS heap,
       pg_size_pretty(pg_indexes_size(c.oid)) AS indexes
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = 'public'
   AND c.relname LIKE 'ursm_node_snapshot_min%'
 ORDER BY pg_total_relation_size(c.oid) DESC
 LIMIT 20;

\echo '### D6. ★★ 211 的会话族体积（★ 与 252 对拍：§143.46/§143.48 的口径）'
SELECT c.relname AS object,
       c.relkind,
       c.reltuples::bigint AS approx_rows,
       pg_size_pretty(pg_total_relation_size(c.oid)) AS total_size,
       pg_size_pretty(pg_relation_size(c.oid)) AS heap,
       pg_size_pretty(pg_indexes_size(c.oid)) AS indexes
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = 'public'
   AND (c.relname LIKE 'session\_%' OR c.relname LIKE 'usage\_ledger%')
 ORDER BY pg_total_relation_size(c.oid) DESC
 LIMIT 20;
