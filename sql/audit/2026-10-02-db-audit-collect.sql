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

\echo '===SECTION:EMPTY_PARTITIONS==='
-- R89-ED（214 号）更正。原写法是：
--     WHERE n.nspname='public' AND c.relkind='r' AND c.reltuples=0
--       AND pg_total_relation_size(c.oid) > 1048576
-- 即「reltuples=0 **且** 体积 > 1MB」就列为"空分区"。
--
-- 三个问题，逐条：
--
--   ① **`reltuples` 是规划器估算，是"上一次统计时刻的快照"，不是事实。**
--      `reltuples = 0` 的真实含义是「**上次 ANALYZE/VACUUM 时为空**」，
--      而本仓是 hot+分区架构，热分区持续被写入 ⇒ 只要 autoanalyze 还没追上，
--      一个**已经写进几百万行**的分区照样报 0，被列进"空分区"。
--      （⚠️ 边界要说清：PG14+ 从未统计的表报 **-1** 而不是 0，见 PG 官方
--      pg_class 文档与 commit 3d351d916b ⇒ "从未统计"那一类本来就被
--      `= 0` 排除在外，**原写法并没有漏掉它**，这里也不额外加条件。）
--   ② 与**本脚本自己**矛盾：`:168` 的 NEVER_ANALYZED 段专门统计
--      `last_analyze IS NULL AND last_autoanalyze IS NULL` 的表数，
--      说明作者**知道**统计可能缺失，却在另一处拿 `reltuples` 当"空"的判据。
--      （这正是 playbook §121「在同一个文件里找它自己怎么说这个字段不可信」。）
--   ③ 段名与内容自相矛盾：体积 > 1MB 的表**本来就不可能真的空** ——
--      未回收的死元组、TOAST、索引页都会撑起体积。所以这一段列出的
--      其实是「**膨胀/未回收**的分区」，不是"空分区"。
--
-- 改法：判据仍然只用 reltuples（**不依赖 pg_stat_* 视图**），但把
--   n_live_tup / n_dead_tup / last_analyze / last_autoanalyze 带出来，
--   让读的人能区分「真的空」「估算停留在旧值」「只是有死元组」。
--   真正的"空"要靠 EXISTS 探针，报表脚本（default_transaction_read_only=on）里不做。
--
-- ⚠️ 刻意**不**把 pg_stat_user_tables 的存在与否写进 WHERE：
--   pg_stat_user_tables 只收录当前角色有权限的关系；一旦把它做成
--   LEFT JOIN 后又在 WHERE 里要求 `last_analyze IS NOT NULL`，
--   缺统计行的分区会被**静默过滤掉**（漏报比误报更难发现 —— 这段
--   本来就是"少列一个分区"，加了这条只会让缺失更难被看到）。
--   所以判据留在 pg_class，诊断字段用 coalesce 显式标 NO_ROW。
SELECT c.relname || ' | parent=' || p.relname
       || ' | size=' || pg_size_pretty(pg_total_relation_size(c.oid))
       || ' | est_rows=' || c.reltuples          -- 估算快照，不是事实
       || ' | n_live_tup=' || coalesce(s.n_live_tup::text,  'NO_ROW')
       || ' | n_dead_tup=' || coalesce(s.n_dead_tup::text,  'NO_ROW')
       || ' | last_analyze=' || coalesce(s.last_analyze::text, 'NO_ROW')
       || ' | last_autoanalyze=' || coalesce(s.last_autoanalyze::text, 'NO_ROW')
FROM pg_class c
JOIN pg_namespace n ON n.oid=c.relnamespace
JOIN pg_inherits i ON i.inhrelid=c.oid
JOIN pg_class p ON p.oid=i.inhparent
LEFT JOIN pg_stat_user_tables s ON s.relid = c.oid
WHERE n.nspname='public' AND c.relkind='r'
  AND c.reltuples = 0        -- 含义仅为「上次统计时为空」，不是「现在为空」
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
