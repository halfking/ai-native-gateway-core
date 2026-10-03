\pset pager off
SET default_transaction_read_only = on;
SET statement_timeout = '30s';
-- 每条独立成句:一条出错不影响其余(这正是本轮修掉的事务连坐问题)
SELECT 'shared_buffers' AS p, 'pgst_setting' AS kind, setting AS v, unit AS u FROM pg_settings WHERE name='shared_buffers';
SELECT 'shared_buffers' AS p, 'bytes' AS kind, current_setting('shared_buffers')::bigint::text AS v, '' AS u;
SELECT 'shared_buffers' AS p, 'show' AS kind, current_setting('shared_buffers') AS v, '' AS u;
SELECT 'work_mem' AS p, 'bytes' AS kind, current_setting('work_mem')::bigint::text AS v, '' AS u;
SELECT 'work_mem' AS p, 'show' AS kind, current_setting('work_mem') AS v, '' AS u;
SELECT 'maintenance_work_mem' AS p, 'bytes' AS kind, current_setting('maintenance_work_mem')::bigint::text AS v, '' AS u;
SELECT 'maintenance_work_mem' AS p, 'show' AS kind, current_setting('maintenance_work_mem') AS v, '' AS u;
SELECT 'effective_cache_size' AS p, 'bytes' AS kind, current_setting('effective_cache_size')::bigint::text AS v, '' AS u;
SELECT 'effective_cache_size' AS p, 'show' AS kind, current_setting('effective_cache_size') AS v, '' AS u;
SELECT 'max_wal_size' AS p, 'bytes' AS kind, current_setting('max_wal_size')::bigint::text AS v, '' AS u;
SELECT 'wal_buffers' AS p, 'bytes' AS kind, current_setting('wal_buffers')::bigint::text AS v, '' AS u;