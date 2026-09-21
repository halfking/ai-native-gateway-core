-- ===========================================================================
-- File:          sql/migrations/startup/616_provider_error_details_unique_constraint.down.sql
-- Database:      llm_gateway
-- Purpose:       回滚 provider_error_details 唯一约束
--
-- Related:       616_provider_error_details_unique_constraint.sql
-- ===========================================================================

\set ON_ERROR_STOP on

-- 删除唯一约束索引
DROP INDEX IF EXISTS idx_provider_error_details_fingerprint;

DO $$
BEGIN
    RAISE NOTICE 'Migration 616 rolled back: unique constraint removed';
END $$;
