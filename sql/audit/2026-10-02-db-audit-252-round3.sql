-- ============================================================================
-- 2026-10-02-252-round3.sql
--
-- 252 生产库第三轮采集:慢 SQL 与索引/膨胀,补齐前两轮未取到的部分。
--
-- 相比前两轮的两处结构性修正(都来自 round2 的实测报错):
--
--   1) 不再用 BEGIN READ ONLY 包整个脚本。
--      原因:一旦某条语句报错,整个事务进入 aborted 状态,后续所有语句
--      全部被拒(round2 里 jit_time 一处报错,导致 9 个 section 全空)。
--      改用 SET default_transaction_read_only = on —— 每条语句独立且都是只读,
--      一条失败不影响其余。
--   2) pg_stat_statements 没有 jit_time 列。JIT 相关列是
--      jit_functions / jit_generation_time / jit_emission_time 等。
--
-- 另修正 STORAGE_MIX:索引是独立 relkind='i',不能在 relkind IN ('r','m','p')
-- 的过滤结果里再 FILTER,否则恒为 0。
-- ============================================================================

\pset pager off
SET default_transaction_read_only = on;
SET statement_timeout = '100s';
SET lock_timeout = '5s';
SET client_min_messages = warning;

\echo '===SECTION:STORAGE_MIX==='
SELECT 'total = ' || pg_size_pretty(t.tot)
     || ' | heap = ' || pg_size_pretty(t.hp)
     || ' | index = ' || pg_size_pretty(t.ix)
     || ' | relations = ' || t.n
FROM (
  SELECT coalesce(sum(pg_relation_size(c.oid)), 0) AS tot,
         coalesce(sum(pg_relation_size(c.oid)) FILTER (WHERE c.relkind IN ('r','m')), 0) AS hp,
         coalesce(sum(pg_relation_size(c.oid)) FILTER (WHERE c.relkind = 'i'), 0)      AS ix,
         count(*)                                                                       AS n
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
  WHERE n.nspname = 'public' AND c.relkind IN ('r','m','p','i')
) t;

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
     || ' | jit_ms = ' || round(coalesce(sum(jit_emission_time), 0)::numeric, 0)
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
     || ' | ' || left(regexp_replace(query, '\s+', ' ', 'g'), 120)
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
     || ' | ' || left(regexp_replace(query, '\s+', ' ', 'g'), 120)
FROM pg_stat_statements
WHERE calls > 0 AND mean_exec_time > 50
ORDER BY mean_exec_time DESC
LIMIT 15;

\echo '===SECTION:PGSS_TOP_CALLS==='
SELECT 'calls=' || calls
     || ' | total_ms=' || round(total_exec_time::numeric, 0)
     || ' | mean_ms=' || round(mean_exec_time::numeric, 2)
     || ' | ' || left(regexp_replace(query, '\s+', ' ', 'g'), 120)
FROM pg_stat_statements
ORDER BY calls DESC
LIMIT 12;

\echo '===SECTION:PGSS_TEMP_SPILL==='
SELECT 'temp_blks_written = ' || sum(temp_blks_written)
     || ' | stmts_with_spill = ' || count(*) FILTER (WHERE temp_blks_written > 0)
     || ' | top_s = ' || coalesce(max(temp_blks_written)::text, '0')
     || ' | ' || left(regexp_replace(query, '\s+', ' ', 'g'), 110)
FROM pg_stat_statements
WHERE temp_blks_written > 0
ORDER BY temp_blks_written DESC
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
     || ' | def=' || left(regexp_replace(pg_get_indexdef(ic.oid), '\s+', ' ', 'g'), 90)
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
     || ' | last_autovacuum=' || coalesce(last_autovacuum::text, 'never')
FROM pg_stat_user_tables
WHERE n_dead_tup > 10000
ORDER BY n_dead_tup DESC
LIMIT 12;

\echo '===SECTION:TOAST_COMPRESSION==='
SELECT coalesce(a.attcompression::text, '(none)') || ' = ' || count(*)::text
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p')
  AND a.attnum > 0 AND NOT a.attisdropped AND a.attstorage = 'x'
GROUP BY a.attcompression ORDER BY 1;

\echo '===SECTION:DML_CHURN==='
SELECT relname
     || ' | ins=' || n_tup_ins
     || ' | upd=' || n_tup_upd
     || ' | del=' || n_tup_del
     || ' | hot_upd=' || n_tup_hot_upd
     || ' | live~' || n_live_tup
FROM pg_stat_user_tables
ORDER BY (n_tup_ins + n_tup_upd + n_tup_del) DESC
LIMIT 12;

\echo '===SECTION:END==='
