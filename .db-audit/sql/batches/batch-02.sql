-- batch 2/4  sections: 11..20 of 38
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


\echo '===SECTION:UNUSED_INDEXES==='
SELECT pg_size_pretty(coalesce(sum(pg_relation_size(ic.oid)),0)) || ' across ' || count(*) || ' unused non-unique indexes >1MB'
FROM pg_stat_user_indexes ui
JOIN pg_index i ON i.indexrelid=ui.indexrelid
JOIN pg_class ic ON ic.oid=i.indexrelid
WHERE ui.idx_scan=0 AND NOT i.indisunique AND pg_relation_size(ic.oid) > 1048576;

\echo '===SECTION:UNUSED_INDEX_DETAIL==='
SELECT ic.relname || ' | ' || pg_size_pretty(pg_relation_size(ic.oid)) ||
       ' | table=' || tc.relname || ' | idx_scan=' || ui.idx_scan
FROM pg_stat_user_indexes ui
JOIN pg_index i ON i.indexrelid=ui.indexrelid
JOIN pg_class ic ON ic.oid=i.indexrelid
JOIN pg_class tc ON tc.oid=i.indrelid
WHERE ui.idx_scan=0 AND NOT i.indisunique AND pg_relation_size(ic.oid) > 1048576
ORDER BY pg_relation_size(ic.oid) DESC
LIMIT 40;

\echo '===SECTION:BAK_TABLES==='
SELECT c.relname || ' | rows~' || greatest(c.reltuples,0)::bigint ||
       ' | ' || pg_size_pretty(pg_total_relation_size(c.oid))
FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='public' AND c.relkind='r' AND c.relname LIKE 'bak\_%'
ORDER BY pg_total_relation_size(c.oid) DESC;

\echo '===SECTION:DEAD_TUPLES==='
SELECT s.relname || ' | n_live=' || s.n_live_tup || ' | n_dead=' || s.n_dead_tup ||
       ' | dead_pct=' || round(100.0*s.n_dead_tup/nullif(s.n_live_tup+s.n_dead_tup,0),1) ||
       ' | autovac=' || coalesce(s.autovacuum_count,0) ||
       ' | last_autovac=' || coalesce(s.last_autovacuum::text,'-') ||
       ' | ' || pg_size_pretty(pg_total_relation_size(s.relid))
FROM pg_stat_user_tables s
WHERE s.n_live_tup + s.n_dead_tup > 0
ORDER BY s.n_dead_tup DESC
LIMIT 25;

\echo '===SECTION:NEVER_ANALYZED==='
SELECT 'tables_never_analyzed=' || count(*)
FROM pg_stat_user_tables WHERE last_analyze IS NULL AND last_autoanalyze IS NULL;

\echo '===SECTION:NEVER_VACUUMED_BIG==='
SELECT relname || ' | n_live=' || n_live_tup || ' | ' || pg_size_pretty(pg_total_relation_size(relid))
FROM pg_stat_user_tables
WHERE (last_vacuum IS NULL AND last_autovacuum IS NULL)
  AND n_live_tup > 10000
ORDER BY pg_total_relation_size(relid) DESC
LIMIT 20;

\echo '===SECTION:SEQ_SCAN_HOTSPOTS==='
SELECT s.relname || ' | seq_scan=' || s.seq_scan || ' | seq_tup_read=' || s.seq_tup_read ||
       ' | idx_scan=' || s.idx_scan || ' | n_live=' || s.n_live_tup ||
       ' | ' || pg_size_pretty(pg_total_relation_size(s.relid))
FROM pg_stat_user_tables s
WHERE s.seq_scan > 0
ORDER BY s.seq_tup_read DESC
LIMIT 25;

\echo '===SECTION:INDEX_SELECTIVITY==='
SELECT s.relname || ' | idx_scan=' || s.idx_scan || ' | idx_tup_read=' || s.idx_tup_fetch ||
       ' | per_scan=' || round(s.idx_tup_fetch::numeric/nullif(s.idx_scan,0),1) ||
       ' | n_live=' || s.n_live_tup
FROM pg_stat_user_tables s
WHERE s.idx_scan > 1000
ORDER BY (s.idx_tup_fetch::numeric/nullif(s.idx_scan,0)) DESC
LIMIT 20;

\echo '===SECTION:DML_CHURN==='
SELECT relname || ' | ins=' || n_tup_ins || ' | upd=' || n_tup_upd ||
       ' | del=' || n_tup_del || ' | hot_upd=' || n_tup_hot_upd
FROM pg_stat_user_tables
WHERE n_tup_ins + n_tup_upd + n_tup_del > 200000
ORDER BY (n_tup_ins + n_tup_upd + n_tup_del) DESC
LIMIT 25;

\echo '===SECTION:PGSS_SUMMARY==='
SELECT 'statements=' || count(*) ||
       ' total_exec_s=' || round((sum(total_exec_time)/1000.0)::numeric,1) ||
       ' rows=' || sum(rows) || ' stats_reset=' || (SELECT stats_reset FROM pg_stat_statements_info)
FROM pg_stat_statements
WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database());

