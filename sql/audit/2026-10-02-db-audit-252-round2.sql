-- ============================================================================
-- 2026-10-02-252-round2.sql
--
-- 252 生产库第二轮采集:补齐第一轮缺失或报错的关键 section。
-- 全部为 pg_class / pg_stat_* / pg_stat_statements 的常数级聚合,
-- 不扫业务表、不 count(*) 业务表、不取锁、不建表。
-- ============================================================================

\pset pager off

\echo '===SECTION:STORAGE_MIX==='
SELECT 'total = ' || pg_size_pretty(t.tot)
     || ' | heap = ' || pg_size_pretty(t.hp)
     || ' | index = ' || pg_size_pretty(t.ix)
     || ' | toast = ' || pg_size_pretty(t.ts)
     || ' | relations = ' || t.n
FROM (
  SELECT coalesce(sum(pg_relation_size(c.oid)), 0) AS tot,
         coalesce(sum(pg_relation_size(c.oid)) FILTER (WHERE c.relkind IN ('r','m')), 0) AS hp,
         -- R89-DX（211 号）更正：原来第 18 行是
         --   coalesce(sum(pg_relation_size(c.oid)) FILTER (WHERE c.relkind = 'i'), 0) AS ix
         -- 而同一子查询的 WHERE 已经限定 `c.relkind IN ('r','m','p')`（**不含 'i'**）
         -- ⇒ 该 FILTER **结构上永远为假** ⇒ `sum(...)` = NULL。
         -- 本文件比 minimal 更危险：外面套了 `coalesce(..., 0)` ⇒ 它报的是
         -- **`index = 0 bytes`** —— 一个**看起来完全正常、实为假的数字**。
         -- （minimal 同一处没有 coalesce ⇒ 整行塌成 NULL；空行至少还看得出不对，
         --   **加了保护的那一份反而更像一份真实测量**。）
         -- 改法：索引体积自己扫一张 relkind='i' 的表。
         coalesce((
           SELECT sum(pg_relation_size(i.oid))
           FROM pg_class i
           JOIN pg_namespace n2 ON n2.oid = i.relnamespace
           WHERE n2.nspname = 'public' AND i.relkind = 'i'
         ), 0)                                                              AS ix,
         coalesce(sum(pg_total_relation_size(coalesce(c.reltoastrelid, 0))), 0)       AS ts,
         count(*)                                                                       AS n
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
  WHERE n.nspname = 'public' AND c.relkind IN ('r','m','p')
) t;

\echo '===SECTION:CHECKPOINTS==='
SELECT 'num_timed = ' || num_timed
     || ' | num_requested = ' || num_requested
     || ' | restartpoints_timed = ' || restartpoints_timed
     || ' | restartpoints_req = ' || restartpoints_req
     || ' | write_time_ms = ' || write_time
     || ' | sync_time_ms = ' || sync_time
     || ' | buffers_written = ' || buffers_written
     || ' | stats_reset = ' || stats_reset
FROM pg_stat_checkpointer;

\echo '===SECTION:TOP_TABLES_SIZE==='
SELECT c.relname
     || ' | ' || pg_size_pretty(pg_total_relation_size(c.oid))
     || ' | heap=' || pg_size_pretty(pg_relation_size(c.oid))
     || ' | idx=' || pg_size_pretty(pg_indexes_size(c.oid))
     || ' | toast=' || pg_size_pretty(coalesce(pg_total_relation_size(c.reltoastrelid), 0))
     || ' | rows~' || greatest(c.reltuples, 0)::bigint
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind IN ('r','m')
  AND pg_total_relation_size(c.oid) > 50 * 1024 * 1024
ORDER BY pg_total_relation_size(c.oid) DESC
LIMIT 20;

\echo '===SECTION:PGSS_SUMMARY==='
SELECT 'statements = ' || count(*)
     || ' | total_exec_ms = ' || round(sum(total_exec_time)::numeric, 0)
     || ' | total_plan_ms = ' || round(sum(total_plan_time)::numeric, 0)
     || ' | calls = ' || sum(calls)
     || ' | rows = ' || sum(rows)
     || ' | shared_blks_hit = ' || sum(shared_blks_hit)
     || ' | shared_blks_read = ' || sum(shared_blks_read)
     || ' | temp_blks_written = ' || sum(temp_blks_written)
     || ' | wal_bytes = ' || sum(wal_bytes)
     || ' | jit_ms = ' || round(sum(jit_time)::numeric, 0)
     || ' | stats_reset = ' || stats_reset
FROM pg_stat_statements;

\echo '===SECTION:PGSS_TOP_TOTAL==='
SELECT 'calls=' || calls
     || ' | total_ms=' || round(total_exec_time::numeric, 0)
     || ' | mean_ms=' || round(mean_exec_time::numeric, 2)
     || ' | rows=' || rows
     || ' | read=' || shared_blks_read
     || ' | hit=' || shared_blks_hit
     || ' | temp=' || temp_blks_written
     || ' | ' || left(regexp_replace(query, '\s+', ' ', 'g'), 130)
FROM pg_stat_statements
ORDER BY total_exec_time DESC
LIMIT 15;

\echo '===SECTION:PGSS_TOP_MEAN==='
SELECT 'calls=' || calls
     || ' | total_ms=' || round(total_exec_time::numeric, 0)
     || ' | mean_ms=' || round(mean_exec_time::numeric, 2)
     || ' | rows=' || rows
     || ' | read=' || shared_blks_read
     || ' | ' || left(regexp_replace(query, '\s+', ' ', 'g'), 130)
FROM pg_stat_statements
WHERE calls > 0 AND mean_exec_time > 50
ORDER BY mean_exec_time DESC
LIMIT 15;

\echo '===SECTION:PGSS_TOP_CALLS==='
SELECT 'calls=' || calls
     || ' | total_ms=' || round(total_exec_time::numeric, 0)
     || ' | mean_ms=' || round(mean_exec_time::numeric, 2)
     || ' | ' || left(regexp_replace(query, '\s+', ' ', 'g'), 130)
FROM pg_stat_statements
ORDER BY calls DESC
LIMIT 15;

\echo '===SECTION:PGSS_TEMP_SPILL==='
SELECT 'temp_blks_written = ' || sum(temp_blks_written)
     || ' | stmts_with_spill = ' || count(*) FILTER (WHERE temp_blks_written > 0)
     || ' | top_s offender = ' || coalesce(max(temp_blks_written)::text, '0')
FROM pg_stat_statements;

\echo '===SECTION:SEQ_SCAN_HOTSPOTS==='
SELECT s.relname
     || ' | seq_scan = ' || s.seq_scan
     || ' | idx_scan = ' || s.idx_scan
     || ' | live_tuples~' || greatest(s.n_live_tup, 0)
     || ' | size = ' || pg_size_pretty(pg_total_relation_size(s.relid))
FROM pg_stat_user_tables s
WHERE s.seq_scan > 0
ORDER BY s.seq_tup_read DESC
LIMIT 15;

\echo '===SECTION:UNUSED_INDEXES==='
WITH unused AS (
  SELECT i.relid, i.indexrelid,
         pg_relation_size(i.indexrelid) AS bytes
  FROM pg_index i
  JOIN pg_class ic ON ic.oid = i.indexrelid
  JOIN pg_namespace n ON n.oid = ic.relnamespace
  WHERE n.nspname = 'public'
    AND NOT i.indisunique
    AND NOT i.indisprimary
    AND NOT EXISTS (
      SELECT 1 FROM pg_stat_user_indexes u
      WHERE u.indexrelid = i.indexrelid AND u.idx_scan > 0
    )
)
SELECT 'unused_nonunique_indexes >1MB = ' || count(*)
     || ' | total = ' || pg_size_pretty(coalesce(sum(bytes), 0))
FROM unused WHERE bytes > 1024 * 1024;

\echo '===SECTION:TOAST_COMPRESSION==='
SELECT coalesce(a.attcompression::text, '') || ' = ' || count(*)::text
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public'
  AND c.relkind IN ('r', 'p')
  AND a.attnum > 0 AND NOT a.attisdropped
  AND a.attstorage = 'x'
GROUP BY a.attcompression ORDER BY 1;

\echo '===SECTION:NEVER_ANALYZED==='
SELECT 'tables_never_analyzed = ' || count(*)::text
FROM pg_stat_user_tables
WHERE last_analyze IS NULL AND last_autoanalyze IS NULL;

\echo '===SECTION:DEAD_TUPLES==='
SELECT schemaname || '.' || relname
     || ' | n_live=' || n_live_tup
     || ' | n_dead=' || n_dead_tup
     || ' | dead_pct=' || CASE WHEN n_live_tup + n_dead_tup = 0 THEN '0'
                               ELSE round(100.0 * n_dead_tup / (n_live_tup + n_dead_tup), 1)::text END
     || ' | last_autovacuum=' || coalesce(last_autovacuum::text, 'never')
FROM pg_stat_user_tables
WHERE n_dead_tup > 10000
ORDER BY n_dead_tup DESC
LIMIT 15;

\echo '===SECTION:END==='
