\pset pager off
SET default_transaction_read_only = on;
SET statement_timeout = '30s';
-- 用 pg_settings 换算成字节数,与 SHOW 交叉验证(不经字符串拼接)
SELECT 'shared_buffers_8kB_blocks' AS k, setting AS v, unit AS u FROM pg_settings WHERE name='shared_buffers';
SELECT 'shared_buffers_bytes_calc' AS k, (setting::bigint * 8 * 1024)::text AS v, 'B' AS u FROM pg_settings WHERE name='shared_buffers';
SELECT 'shared_buffers_MiB_calc'   AS k, (setting::bigint * 8 * 1024 / 1048576)::text AS v, 'MiB' AS u FROM pg_settings WHERE name='shared_buffers';
SELECT 'shared_buffers_SHOW'      AS k, current_setting('shared_buffers') AS v, '' AS u;
SELECT 'eff_cache_MiB_calc'       AS k, (setting::bigint * 8 * 1024 / 1048576)::text AS v, 'MiB' AS u FROM pg_settings WHERE name='effective_cache_size';
SELECT 'eff_cache_SHOW'           AS k, current_setting('effective_cache_size') AS v, '' AS u;