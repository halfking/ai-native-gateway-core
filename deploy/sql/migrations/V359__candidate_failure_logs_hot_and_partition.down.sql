-- =============================================================================
-- V359__candidate_failure_logs_hot_and_partition.down.sql
-- 反向：把 hot + 月度分区合并回单一 columnar 表（复杂度高，建议仅做紧急回滚）
--
-- 回滚策略：
--   1. DROP partitioned parent + 所有 ATTACHED 月度分区（含月度数据）
--   2. DROP hot 表
--   3. DROP 视图
--   4. DROP promote 函数
--   5. RENAME _columnar_old → candidate_failure_logs（恢复原 columnar 表）
--
-- 注意：父表的月度分区数据（12,328 行已迁 + 未来 hot promote 进的行）将丢失！
-- 仅在 V359 上线后 < 7 天内做回滚才安全。
-- =============================================================================

\set ON_ERROR_STOP on

\echo '=== V359 DOWN: 紧急回滚（仅 < 7 天内可执行，月度分区数据将丢失） ==='

-- 1. DROP 视图
DROP VIEW IF EXISTS candidate_failure_logs_with_current_month;

-- 2. DROP 索引（分区父表的索引在 DROP TABLE 时自动消失）
DROP INDEX IF EXISTS idx_cfl_cred_ts;
DROP INDEX IF EXISTS idx_cfl_provider_ts;
DROP INDEX IF EXISTS idx_cfl_model_ts;
DROP INDEX IF EXISTS idx_cfl_request_id;
DROP INDEX IF EXISTS idx_cfl_session_ts;

-- 3. DROP partitioned 父表（CASCADE 同时 DROP 月度分区）
DROP TABLE IF EXISTS candidate_failure_logs CASCADE;

-- 4. DROP RLS policy（如未跟随 DROP TABLE）
DROP POLICY IF EXISTS tenant_isolation_candidate_failure_logs ON candidate_failure_logs;

-- 5. DROP promote 函数
DROP FUNCTION IF EXISTS promote_candidate_failure_logs_hot_to_partition(interval, int);

-- 6. DROP ensure_partition 函数
DROP FUNCTION IF EXISTS ensure_candidate_failure_logs_partition(timestamp with time zone);

-- 7. DROP hot 表（含 RLS policy + 索引）
DROP TABLE IF EXISTS candidate_failure_logs_hot CASCADE;
DROP POLICY IF EXISTS tenant_isolation_candidate_failure_logs_hot ON candidate_failure_logs_hot;

-- 8. 恢复原 columnar 表名（如果 _columnar_old 还在）
DO $do$
DECLARE
    has_old boolean;
BEGIN
    SELECT EXISTS (
        SELECT 1 FROM pg_class
        WHERE relname = 'candidate_failure_logs_columnar_old'
          AND relnamespace = 'public'::regnamespace
    ) INTO has_old;

    IF has_old THEN
        ALTER TABLE candidate_failure_logs_columnar_old RENAME TO candidate_failure_logs;
        RAISE NOTICE 'V359 DOWN: restored original columnar candidate_failure_logs';
    ELSE
        RAISE WARNING 'V359 DOWN: candidate_failure_logs_columnar_old not found, cannot restore';
    END IF;
END
$do$;

\echo '=== V359 DOWN 完成 ==='