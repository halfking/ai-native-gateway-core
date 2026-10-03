-- batch 4/4  sections: 31..38 of 38
-- 自动切分自: F:\workspace\llm-gateway-go\sql\audit\2026-10-02-db-audit-collect.sql

-- ============================================================================
-- 2026-10-02-db-audit-collect.sql
--
-- llm_gateway 数据库只读采集脚本。对 252 (pg-252-pg17) 与 34 (llm-gateway-pg)
-- 两个实例跑同一份脚本，产出同一套分节报表，然后由 .db-audit/compare-instances.ps1
-- 做逐节 diff。这样「两库比较」是同一口径的，不受采集时间/工具差异影响。
--
-- 严格只读：不建表、不改数据、不 ANALYZE、不 VACUUM、不取锁。
--   - 全部走 pg_class / pg_stat_* / pg_stat_statements 的估算值，不做 count(*)
--   - 不设 transaction_timeout，但建议调用方加 -v ON_ERROR_STOP=1
--
-- 生产友好：
--   脚本开头设 statement_timeout，避免误连生产时一条慢查询拖住库。
--   采集本身是常数级开销（走系统目录），正常在秒级完成。
--
-- 用法（252，需先起隧道 TUNNEL=1）：
--   psql "postgres://llm_gateway@127.0.0.1:15432/llm_gateway?sslmode=disable" \
--        -v ON_ERROR_STOP=1 -f sql/audit/2026-10-02-db-audit-collect.sql > out/252.txt
--
-- 用法（34，走容器内 psql）：
--   docker exec -i llm-gateway-pg psql -U llm_gateway -d llm_gateway -A -F'|' -t \
--     < sql/audit/2026-10-02-db-audit-collect.sql > out/34.txt
--
-- 分节标记统一为 `===SECTION:<name>===`，供 diff 脚本切分。
-- ============================================================================

\pset pager off
\timing off
SET statement_timeout = '120s';
SET idle_in_transaction_session_timeout = '30s';
SET lock_timeout = '5s';
SET default_transaction_read_only = on;


\echo '===SECTION:EMPTY_PARTITIONS==='
SELECT c.relname || ' | parent=' || p.relname || ' | ' || pg_size_pretty(pg_total_relation_size(c.oid))
FROM pg_class c
JOIN pg_namespace n ON n.oid=c.relnamespace
JOIN pg_inherits i ON i.inhrelid=c.oid
JOIN pg_class p ON p.oid=i.inhparent
WHERE n.nspname='public' AND c.relkind='r' AND c.reltuples=0
  AND pg_total_relation_size(c.oid) > 1048576
ORDER BY pg_total_relation_size(c.oid) DESC;

\echo '===SECTION:ARCHIVE_COLUMNAR==='
SELECT 'archive_rows_total=' || coalesce(sum(greatest(c.reltuples,0)),0)::bigint ||
       ' | columnar_tables=' || count(*) FILTER (WHERE c.relkind='c') ||
       ' | archive_relations=' || count(*)
FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='public' AND c.relname LIKE '%archive%';

\echo '===SECTION:ARCHIVE_FUNCTIONS==='
SELECT p.proname || ' | ' || pg_get_function_identity_arguments(p.oid) ||
       ' | uses_columnar=' || (p.prosrc ILIKE '%USING columnar%')
FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
WHERE n.nspname='public' AND p.prosrc ILIKE '%USING columnar%'
ORDER BY 1;

\echo '===SECTION:COLUMNAR_GUCS==='
SELECT name || '=' || setting FROM pg_settings WHERE name LIKE 'columnar.%' ORDER BY 1;

\echo '===SECTION:ALL_NULL_COLUMNS==='
SELECT tablename || ' | total_cols=' || tc || ' | all_null=' || an
FROM (
  SELECT tablename, count(*) AS tc, count(*) FILTER (WHERE null_frac=1.0) AS an
  FROM pg_stats WHERE schemaname='public'
  GROUP BY tablename
) x
WHERE an > 0
ORDER BY an DESC, tc DESC
LIMIT 20;

\echo '===SECTION:WIDEST_COLUMNS==='
SELECT s.tablename || ' | ' || s.attname ||
       ' | null_frac=' || s.null_frac || ' | avg_width=' || s.avg_width ||
       ' | n_distinct=' || s.n_distinct
FROM pg_stats s
WHERE s.schemaname='public' AND s.avg_width > 40
  AND s.tablename IN ('request_logs_bodies','request_logs','session_bodies','session_turns',
                      'ursm_node_snapshot_min','session_turn_details','request_state_transitions')
ORDER BY s.avg_width DESC
LIMIT 40;

\echo '===SECTION:WAL_AND_REPLICA==='
SELECT 'slots=' || (SELECT count(*) FROM pg_replication_slots) ||
       ' | publications=' || (SELECT count(*) FROM pg_publication) ||
       ' | wal_level=' || current_setting('wal_level') ||
       ' | in_recovery=' || pg_is_in_recovery()::text;

\echo '===SECTION:END==='
SELECT 'ok' AS k, now()::text AS v;
