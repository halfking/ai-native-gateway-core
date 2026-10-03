-- batch 1/4  sections: 1..10 of 38
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


\echo '===SECTION:META==='
SELECT 'collected_at'      AS k, now()::text AS v
UNION ALL SELECT 'db',            current_database()
UNION ALL SELECT 'server_version', current_setting('server_version')
UNION ALL SELECT 'version',        version()
UNION ALL SELECT 'uptime',         date_trunc('minute', now() - pg_postmaster_start_time())::text
UNION ALL SELECT 'postmaster_start', pg_postmaster_start_time()::text;

\echo '===SECTION:EXTENSIONS==='
SELECT extname || ' ' || extversion FROM pg_extension ORDER BY 1;

\echo '===SECTION:SCHEMA_COUNTS==='
SELECT 'public_relations'  AS k, count(*)::text AS v FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN ('r','p')
UNION ALL SELECT 'base_tables',     count(*)::text FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind='r' AND NOT EXISTS (SELECT 1 FROM pg_inherits i WHERE i.inhrelid=c.oid)
UNION ALL SELECT 'partitions',      count(*)::text FROM pg_inherits
UNION ALL SELECT 'partitioned_parents', count(*)::text FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind='p'
UNION ALL SELECT 'indexes',         count(*)::text FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind='i'
UNION ALL SELECT 'views',           count(*)::text FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind='v'
UNION ALL SELECT 'matviews',        count(*)::text FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind='m'
UNION ALL SELECT 'columnar_tables', count(*)::text FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind='c'
UNION ALL SELECT 'sequences',       count(*)::text FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind='S'
UNION ALL SELECT 'constraints',     count(*)::text FROM pg_constraint ch JOIN pg_class c ON c.oid=ch.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public'
UNION ALL SELECT 'functions',       count(*)::text FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public'
UNION ALL SELECT 'triggers',        count(*)::text FROM pg_trigger t WHERE NOT t.tgisinternal
UNION ALL SELECT 'rls_policies',    count(*)::text FROM pg_policies WHERE schemaname='public'
UNION ALL SELECT 'rls_tables',      count(*)::text FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relrowsecurity
UNION ALL SELECT 'citus_dist_partitions', count(*)::text FROM pg_dist_partition;

\echo '===SECTION:DB_SIZE==='
SELECT datname || ' ' || pg_size_pretty(pg_database_size(datname))
FROM pg_database WHERE datname = current_database();

\echo '===SECTION:STORAGE_MIX==='
SELECT 'heap='  || pg_size_pretty(coalesce(sum(pg_relation_size(c.oid)),0))          ||
       ' index='|| pg_size_pretty(coalesce(sum(pg_indexes_size(c.oid)),0))          ||
       ' toast='|| pg_size_pretty(coalesce(sum(pg_total_relation_size(c.reltoastrelid)),0))
FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='public' AND c.relkind IN ('r','p');

\echo '===SECTION:KEY_SETTINGS==='
SELECT name || '=' || setting || coalesce(unit,'') || ' [' || source || ']'
FROM pg_settings
WHERE name IN ('shared_buffers','work_mem','maintenance_work_mem','effective_cache_size',
               'max_connections','max_wal_size','min_wal_size','checkpoint_timeout',
               'checkpoint_completion_target','wal_compression','random_page_cost','seq_page_cost',
               'effective_io_concurrency','maintenance_io_concurrency','max_worker_processes',
               'max_parallel_workers','max_parallel_workers_per_gather','max_parallel_maintenance_workers',
               'jit','default_statistics_target','track_io_timing','log_min_duration_statement',
               'autovacuum','autovacuum_vacuum_scale_factor','autovacuum_analyze_scale_factor',
               'autovacuum_naptime','autovacuum_max_workers','autovacuum_vacuum_insert_threshold',
               'default_toast_compression','wal_level','synchronous_commit','fsync','full_page_writes',
               'plan_cache_mode','temp_file_limit','idle_in_transaction_session_timeout',
               'statement_timeout','shared_preload_libraries','io_combine_limit',
               'vacuum_buffer_usage_limit','vacuum_cost_limit','log_autovacuum_min_duration',
               'max_locks_per_transaction')
ORDER BY 1;

\echo '===SECTION:DB_ACTIVITY==='
SELECT 'commits='   || xact_commit   || ' rollbacks=' || xact_rollback ||
       ' blks_hit=' || blks_hit      || ' blks_read=' || blks_read ||
       ' cache_hit_pct=' || round(100.0*blks_hit/nullif(blks_hit+blks_read,0),2) ||
       ' tup_ret='   || tup_returned  || ' tup_ins='   || tup_inserted ||
       ' tup_upd='   || tup_updated   || ' tup_del='   || tup_deleted ||
       ' temp_files='|| temp_files    || ' temp_bytes='|| temp_bytes ||
       ' deadlocks=' || deadlocks     || ' stats_reset=' || stats_reset
FROM pg_stat_database WHERE datname = current_database();

\echo '===SECTION:CHECKPOINTS==='
SELECT 'timed=' || num_timed || ' requested=' || num_requested ||
       ' write_time_ms=' || write_time || ' sync_time_ms=' || sync_time ||
       ' buffers_written=' || buffers_written || ' stats_reset=' || stats_reset
FROM pg_stat_checkpointer;

\echo '===SECTION:TOP_TABLES_SIZE==='
SELECT c.relname || ' | ' || pg_size_pretty(pg_total_relation_size(c.oid)) ||
       ' | heap=' || pg_size_pretty(pg_relation_size(c.oid)) ||
       ' | idx='  || pg_size_pretty(pg_indexes_size(c.oid)) ||
       ' | toast='|| pg_size_pretty(coalesce(pg_total_relation_size(c.reltoastrelid),0)) ||
       ' | rows~' || greatest(c.reltuples,0)::bigint ||
       ' | parent=' || coalesce(p.relname,'-')
FROM pg_class c
JOIN pg_namespace n ON n.oid=c.relnamespace
LEFT JOIN pg_inherits i ON i.inhrelid=c.oid
LEFT JOIN pg_class p ON p.oid=i.inhparent
WHERE n.nspname='public' AND c.relkind IN ('r','p')
ORDER BY pg_total_relation_size(c.oid) DESC
LIMIT 60;

\echo '===SECTION:INDEX_OVERHEAD==='
SELECT c.relname || ' | idx=' || pg_size_pretty(pg_indexes_size(c.oid)) ||
       ' | heap=' || pg_size_pretty(pg_relation_size(c.oid)) ||
       ' | ratio=' || round(100.0*pg_indexes_size(c.oid)/nullif(pg_total_relation_size(c.oid),0),1) || '%'
FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='public' AND c.relkind IN ('r','p') AND pg_indexes_size(c.oid) > 0
ORDER BY pg_indexes_size(c.oid) DESC
LIMIT 30;

