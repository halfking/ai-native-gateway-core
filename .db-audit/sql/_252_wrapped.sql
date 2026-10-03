\set ON_ERROR_STOP off
SET statement_timeout = '120s';
SET lock_timeout = '5s';
SET idle_in_transaction_session_timeout = '600s';
BEGIN READ ONLY;
-- ============================================================================
-- 2026-10-02-252-minimal.sql
--
-- 252 生产库最小化只读采集(第一批:实例身份 + 容量 + 关键参数)。
--
-- 为什么单独拆一份:252 是 4 核 / load 16 的生产机,实测单次 SSH 会话
-- 往返就要 2-4 分钟(裸 SSH 19 秒 + podman exec + psql 启动),
-- 完整的 38 节脚本在这台机器上跑不完。因此按"优化结论最需要什么"
-- 拆成多份小采集,每次一次会话拿一组,合并后做 diff。
--
-- 这一批回答的问题:252 是什么配置、存了多少数据、关键参数与 34 差在哪。
-- 纯 SELECT,只读事务,不建表、不取锁、不 ANALYZE、不 count(*) 业务表。
-- ============================================================================

\set ON_ERROR_STOP off
\pset pager off

SET statement_timeout = '60s';
SET idle_in_transaction_session_timeout = '30s';
SET lock_timeout = '5s';

\echo '===SECTION:META==='
SELECT 'collected_at'      AS k, now()::text AS v
UNION ALL SELECT 'db',            current_database()
UNION ALL SELECT 'server_version', current_setting('server_version')
UNION ALL SELECT 'version',        version()
UNION ALL SELECT 'uptime',         date_trunc('minute', now() - pg_postmaster_start_time())::text
UNION ALL SELECT 'postmaster_start', pg_postmaster_start_time()::text
UNION ALL SELECT 'host',           inet_server_addr()::text
UNION ALL SELECT 'port',           inet_server_port()::text
UNION ALL SELECT 'num_backends',   (SELECT count(*)::text FROM pg_stat_activity);

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
UNION ALL SELECT 'citus_dist_partitions', count(*)::text FROM pg_dist_partition;

\echo '===SECTION:DB_SIZE==='
SELECT 'db_size = ' || pg_size_pretty(pg_database_size(current_database()))
     || ' | server_encoding = ' || pg_encoding_to_char(encoding)
     || ' | lc_collate = ' || datcollate
     || ' | block_size = ' || current_setting('block_size')
FROM pg_database WHERE datname = current_database();

\echo '===SECTION:STORAGE_MIX==='
SELECT 'total = ' || pg_size_pretty(sum(pg_total_relation_size(c.oid)))
     || ' | heap = ' || pg_size_pretty(sum(pg_relation_size(c.oid)) FILTER (WHERE c.relkind IN ('r','m')))
     || ' | index = ' || pg_size_pretty(sum(pg_relation_size(c.oid)) FILTER (WHERE c.relkind='i'))
     || ' | toast = ' || pg_size_pretty(sum(pg_total_relation_size(c.reltoastrelid)) FILTER (WHERE c.reltoastrelid <> 0))
     || ' | relations = ' || count(*)
FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='public' AND c.relkind IN ('r','m','t','p');

\echo '===SECTION:KEY_SETTINGS==='
SELECT name || ' = ' || setting || COALESCE(unit, '')
FROM pg_settings
WHERE name IN ('shared_buffers','work_mem','maintenance_work_mem','effective_cache_size',
               'max_connections','max_worker_processes','max_parallel_workers',
               'max_parallel_workers_per_gather','effective_io_concurrency',
               'random_page_cost','seq_page_cost','default_statistics_target',
               'statement_timeout','idle_in_transaction_session_timeout',
               'autovacuum','autovacuum_vacuum_scale_factor','autovacuum_analyze_scale_factor',
               'wal_buffers','checkpoint_timeout','max_wal_size','min_wal_size',
               'synchronous_commit','commit_delay','jit','track_io_timing',
               'log_min_duration_statement','default_toast_compression','temp_file_limit',
               'max_worker_processes','vacuum_buffer_usage_limit','track_counts',
               'compute_query_id','enable_partition_pruning','enable_partitionwise_join',
               'enable_partitionwise_aggregate','hash_mem_multiplier','logical_decoding_work_mem')
   OR name LIKE 'columnar.%'
ORDER BY name;

\echo '===SECTION:DB_ACTIVITY==='
SELECT 'uptime = ' || date_trunc('minute', now() - pg_postmaster_start_time())::text
     || ' | xact_commit = ' || xact_commit
     || ' | xact_rollback = ' || xact_rollback
     || ' | blks_read = ' || blks_read
     || ' | blks_hit = ' || blks_hit
     || ' | tup_returned = ' || tup_returned
     || ' | tup_inserted = ' || tup_inserted
     || ' | tup_updated = ' || tup_updated
     || ' | tup_deleted = ' || tup_deleted
     || ' | conflicts = ' || conflicts
     || ' | temp_files = ' || temp_files
     || ' | temp_bytes = ' || pg_size_pretty(temp_bytes)
     || ' | deadlocks = ' || deadlocks
     || ' | stats_reset = ' || stats_reset
FROM pg_stat_database WHERE datname = current_database();

\echo '===SECTION:CONNECTIONS==='
SELECT usename::text || ' | ' || coalesce(state::text,'-') || ' | ' || count(*)::text
FROM pg_stat_activity
WHERE datname = current_database()
GROUP BY usename, state ORDER BY count(*) DESC;

\echo '===SECTION:WAIT_EVENTS==='
SELECT wait_event_type::text || '/' || coalesce(wait_event::text,'-') || ' = ' || count(*)::text
FROM pg_stat_activity
WHERE datname = current_database() AND wait_event_type IS NOT NULL
GROUP BY wait_event_type, wait_event ORDER BY count(*) DESC LIMIT 12;

\echo '===SECTION:CHECKPOINTS==='
SELECT 'num_timed = ' || num_timed
     || ' | num_requested = ' || num_requested
     || ' | restartpoints_timed = ' || restartpoints_timed
     || ' | restartpoints_req = ' || restartpoints_req
     || ' | write_time_ms = ' || write_time
     || ' | sync_time_ms = ' || sync_time
     || ' | buffers_checkpoint = ' || buffers_checkpoint
     || ' | stats_reset = ' || stats_reset
FROM pg_stat_checkpointer;

\echo '===SECTION:END==='

COMMIT;
