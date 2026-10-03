-- batch 3/4  sections: 21..30 of 38
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


\echo '===SECTION:PGSS_TOP_TOTAL==='
SELECT round((total_exec_time/1000.0)::numeric,1) || 's | calls=' || calls ||
       ' | mean=' || round(mean_exec_time::numeric,1) || 'ms | rows=' || rows ||
       ' | ' || left(regexp_replace(query, E'[\n\r\t ]+', ' ', 'g'), 150)
FROM pg_stat_statements
WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
ORDER BY total_exec_time DESC
LIMIT 30;

\echo '===SECTION:PGSS_TOP_MEAN==='
SELECT round(mean_exec_time::numeric,1) || 'ms | calls=' || calls ||
       ' | total=' || round((total_exec_time/1000.0)::numeric,1) || 's | rows=' || rows ||
       ' | ' || left(regexp_replace(query, E'[\n\r\t ]+', ' ', 'g'), 150)
FROM pg_stat_statements
WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database()) AND calls >= 20
ORDER BY mean_exec_time DESC
LIMIT 30;

\echo '===SECTION:PGSS_TOP_CALLS==='
SELECT calls || ' | total=' || round((total_exec_time/1000.0)::numeric,1) || 's' ||
       ' | mean=' || round(mean_exec_time::numeric,2) || 'ms' ||
       ' | ' || left(regexp_replace(query, E'[\n\r\t ]+', ' ', 'g'), 130)
FROM pg_stat_statements
WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
ORDER BY calls DESC
LIMIT 20;

\echo '===SECTION:PGSS_TEMP_SPILL==='
SELECT temp_blks_written || ' temp_blks | calls=' || calls ||
       ' | mean=' || round(mean_exec_time::numeric,1) || 'ms' ||
       ' | ' || left(regexp_replace(query, E'[\n\r\t ]+', ' ', 'g'), 130)
FROM pg_stat_statements
WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
ORDER BY temp_blks_written DESC
LIMIT 12;

\echo '===SECTION:PGSS_WAL_JIT==='
SELECT 'wal=' || pg_size_pretty(coalesce(sum(wal_bytes),0)) ||
       ' | wal_stmts=' || count(*) FILTER (WHERE wal_bytes > 0) ||
       ' | jit_compiles=' || coalesce(sum(jit_functions),0) ||
       ' | jit_stmts=' || count(*) FILTER (WHERE jit_functions > 0)
FROM pg_stat_statements
WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database());

\echo '===SECTION:CONNECTIONS==='
SELECT usename::text || ' | ' || coalesce(state::text,'-') || ' | ' || count(*)::text
FROM pg_stat_activity WHERE datname = current_database()
GROUP BY usename, state ORDER BY count(*) DESC;

\echo '===SECTION:WAIT_EVENTS==='
SELECT wait_event_type::text || '/' || coalesce(wait_event::text,'-') || ' = ' || count(*)::text
FROM pg_stat_activity
WHERE datname = current_database() AND wait_event_type IS NOT NULL
GROUP BY wait_event_type, wait_event ORDER BY count(*) DESC LIMIT 12;

\echo '===SECTION:TOAST_COMPRESSION==='
SELECT coalesce(a.attcompression::text,'') || ' = ' || count(*)::text
FROM pg_attribute a
JOIN pg_class c ON c.oid=a.attrelid
JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='public' AND a.attstorage='x' AND a.attnum>0
GROUP BY a.attcompression ORDER BY count(*) DESC;

\echo '===SECTION:PARTITION_INVENTORY==='
SELECT p.relname || ' | children=' || count(c.oid) || ' | key=' || pg_get_partkeydef(p.oid)
FROM pg_class p
JOIN pg_namespace n ON n.oid=p.relnamespace
JOIN pg_inherits i ON i.inhparent=p.oid
JOIN pg_class c ON c.oid=i.inhrelid
WHERE n.nspname='public' AND p.relkind='p'
GROUP BY p.relname, p.oid ORDER BY p.relname;

\echo '===SECTION:NO_DEFAULT_PARTITION==='
SELECT p.relname || ' | children=' || count(c.oid) ||
       ' | has_default=' || bool_or(c.relname LIKE '%\_default')
FROM pg_class p
JOIN pg_namespace n ON n.oid=p.relnamespace
JOIN pg_inherits i ON i.inhparent=p.oid
JOIN pg_class c ON c.oid=i.inhrelid
WHERE n.nspname='public' AND p.relkind='p'
GROUP BY p.relname
HAVING NOT bool_or(c.relname LIKE '%\_default')
ORDER BY p.relname;

