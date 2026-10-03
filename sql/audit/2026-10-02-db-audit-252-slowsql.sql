-- ============================================================================
-- 2026-10-02-db-audit-252-slowsql.sql
--
-- 252 生产库:慢 SQL、索引、膨胀专项采集。
--
-- 前两轮踩过的坑(这里都绕开了,不要再改回去):
--   1) pg_stat_statements 没有 stats_reset 列 —— 它在 pg_stat_statements_info。
--      用错会导致整段作废(252 上一次报错,后续 9 个 section 全空)。
--   2) 不要把整个脚本包进 BEGIN ... COMMIT:一条语句报错会让事务进入
--      aborted 状态,后面所有语句被一并拒绝。改用
--      SET default_transaction_read_only = on,每条语句独立且只读。
--   3) 索引是独立 relkind='i',统计索引时不能沿用表的 relkind 过滤条件。
--   4) pg_stat_checkpointer 的列是 buffers_written,不是 buffers_checkpoint。
-- ============================================================================

\pset pager off
SET default_transaction_read_only = on;
SET statement_timeout = '90s';
SET lock_timeout = '5s';
SET client_min_messages = warning;

\echo '===SECTION:STORAGE_MIX==='
SELECT 'total = ' || pg_size_pretty(sum(pg_relation_size(c.oid)))
     || ' | heap = ' || pg_size_pretty(sum(pg_relation_size(c.oid)) FILTER (WHERE c.relkind IN ('r','m')))
     || ' | index = ' || pg_size_pretty(sum(pg_relation_size(c.oid)) FILTER (WHERE c.relkind = 'i'))
     || ' | relations = ' || count(*)
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind IN ('r','m','p','i');

\echo '===SECTION:TOAST_TOTAL==='
SELECT 'toast_total = ' || pg_size_pretty(coalesce(sum(pg_total_relation_size(c.reltoastrelid)), 0))
     || ' | tables_with_toast = ' || count(*)
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind IN ('r','m') AND c.reltoastrelid <> 0;

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
     || ' | jit_stmts = ' || count(*) FILTER (WHERE jit_functions > 0)
     || ' | jit_emission_ms = ' || round(coalesce(sum(jit_emission_time), 0)::numeric, 0)
FROM pg_stat_statements;

\echo '===SECTION:PGSS_INFO==='
SELECT 'pgss_deallocate = ' || deallocate
     || ' | stats_reset = ' || stats_reset
FROM pg_stat_statements_info;

\echo '===SECTION:PGSS_TOP_TOTAL==='
SELECT 'calls=' || calls
     || ' | total_ms=' || round(total_exec_time::numeric, 0)
     || ' | mean_ms=' || round(mean_exec_time::numeric, 2)
     || ' | rows=' || rows
     || ' | read=' || shared_blks_read
     || ' | hit=' || shared_blks_hit
     || ' | temp=' || temp_blks_written
     || ' | ' || left(regexp_replace(query, '\s+', ' ', 'g'), 110)
FROM pg_stat_statements
ORDER BY total_exec_time DESC
LIMIT 15;

\echo '===SECTION:PGSS_TOP_MEAN==='
SELECT 'calls=' || calls
     || ' | total_ms=' || round(total_exec_time::numeric, 0)
     || ' | mean_ms=' || round(mean_exec_time::numeric, 2)
     || ' | rows=' || rows
     || ' | read=' || shared_blks_read
     || ' | temp=' || temp_blks_written
     || ' | ' || left(regexp_replace(query, '\s+', ' ', 'g'), 110)
FROM pg_stat_statements
WHERE calls > 0 AND mean_exec_time > 50
ORDER BY mean_exec_time DESC
LIMIT 15;

\echo '===SECTION:PGSS_TOP_CALLS==='
SELECT 'calls=' || calls
     || ' | total_ms=' || round(total_exec_time::numeric, 0)
     || ' | mean_ms=' || round(mean_exec_time::numeric, 2)
     || ' | ' || left(regexp_replace(query, '\s+', ' ', 'g'), 110)
FROM pg_stat_statements
ORDER BY calls DESC
LIMIT 10;

\echo '===SECTION:PGSS_TEMP_SPILL==='
SELECT 'temp_blks_written = ' || sum(temp_blks_written)
     || ' | ' || left(regexp_replace(query, '\s+', ' ', 'g'), 100)
FROM pg_stat_statements
WHERE temp_blks_written > 0
GROUP BY query
ORDER BY sum(temp_blks_written) DESC
LIMIT 5;

\echo '===SECTION:UNUSED_INDEXES==='
WITH unused AS (
  SELECT i.indexrelid, pg_relation_size(i.indexrelid) AS bytes
  FROM pg_index i
  JOIN pg_class ic ON ic.oid = i.indexrelid
  JOIN pg_namespace n ON n.oid = ic.relnamespace
  WHERE n.nspname = 'public'
    AND NOT i.indisunique AND NOT i.indisprimary
    AND NOT EXISTS (SELECT 1 FROM pg_stat_user_indexes u
                    WHERE u.indexrelid = i.indexrelid AND u.idx_scan > 0)
)
SELECT 'unused_nonunique_gt1MB = ' || count(*)
     || ' | total = ' || pg_size_pretty(coalesce(sum(bytes), 0))
FROM unused WHERE bytes > 1024 * 1024;

\echo '===SECTION:UNUSED_INDEX_DETAIL==='
SELECT ic.relname
     || ' | ' || pg_size_pretty(pg_relation_size(ic.oid))
     || ' | on=' || t.relname
     || ' | ' || left(regexp_replace(pg_get_indexdef(ic.oid), '\s+', ' ', 'g'), 85)
FROM pg_index i
JOIN pg_class ic ON ic.oid = i.indexrelid
JOIN pg_class t  ON t.oid = i.indrelid
JOIN pg_namespace n ON n.oid = ic.relnamespace
WHERE n.nspname = 'public'
  AND NOT i.indisunique AND NOT i.indisprimary
  AND NOT EXISTS (SELECT 1 FROM pg_stat_user_indexes u
                  WHERE u.indexrelid = i.indexrelid AND u.idx_scan > 0)
  AND pg_relation_size(ic.oid) > 1024 * 1024
ORDER BY pg_relation_size(ic.oid) DESC
LIMIT 20;

\echo '===SECTION:NEVER_ANALYZED==='
SELECT 'tables_never_analyzed = ' || count(*)::text
FROM pg_stat_user_tables WHERE last_analyze IS NULL AND last_autoanalyze IS NULL;

\echo '===SECTION:DEAD_TUPLES==='
SELECT relname
     || ' | live=' || n_live_tup
     || ' | dead=' || n_dead_tup
     || ' | dead_pct=' || CASE WHEN n_live_tup + n_dead_tup = 0 THEN '0'
                               ELSE round(100.0 * n_dead_tup / (n_live_tup + n_dead_tup), 1)::text END
     || ' | autovac=' || coalesce(last_autovacuum::text, 'never')
FROM pg_stat_user_tables
WHERE n_dead_tup > 10000
ORDER BY n_dead_tup DESC
LIMIT 10;

\echo '===SECTION:DML_CHURN==='
SELECT relname
     || ' | ins=' || n_tup_ins
     || ' | upd=' || n_tup_upd
     || ' | del=' || n_tup_del
     || ' | hot_upd=' || n_tup_hot_upd
     || ' | live~' || n_live_tup
FROM pg_stat_user_tables
ORDER BY (n_tup_ins + n_tup_upd + n_tup_del) DESC
LIMIT 10;

\echo '===SECTION:TOAST_COMPRESSION==='
SELECT coalesce(a.attcompression::text, '(none)') || ' = ' || count(*)::text
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p')
  AND a.attnum > 0 AND NOT a.attisdropped AND a.attstorage = 'x'
GROUP BY a.attcompression ORDER BY 1;

\echo '===SECTION:SEQ_SCAN_HOTSPOTS==='
SELECT s.relname
     || ' | seq_scan=' || s.seq_scan
     || ' | idx_scan=' || s.idx_scan
     || ' | live~' || s.n_live_tup
     || ' | size=' || pg_size_pretty(pg_total_relation_size(s.relid))
FROM pg_stat_user_tables s
WHERE s.seq_scan > 0
ORDER BY s.seq_tup_read DESC
LIMIT 10;

\echo '===SECTION:END==='
