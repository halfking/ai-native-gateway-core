-- ===========================================================================
-- File:          sql/migrations/startup/616_provider_error_details_unique_constraint.sql
-- Database:      llm_gateway
-- Purpose:       为 provider_error_details 添加唯一约束以支持 UPSERT 聚合
--
-- Related:       bg/provider_error_aggregator.go
--                sql/migrations/startup/435_provider_quality_tables.sql
-- Status:        active
-- Idempotent:    YES
--
-- Context:
--   2026-08-29 审计发现 provider_error_details 表缺少业务写入逻辑。
--   添加唯一约束以支持从 candidate_failure_logs_hot 的 UPSERT 聚合。
--
-- Fingerprint:
--   错误指纹 = (provider_id, model_name, endpoint, error_type, error_code, error_message)
--   相同指纹的错误会合并，occurrences 递增
--
-- Changelog:
--   2026-08-29  v1.0  初始创建 - 添加唯一约束
-- ===========================================================================

\set ON_ERROR_STOP on

-- 添加唯一约束
-- 使用 COALESCE 处理 NULL 值，确保 (NULL, NULL) 与 (NULL, NULL) 被视为相同
-- 注意：PostgreSQL 认为 NULL != NULL，所以我们使用函数索引配合空字符串
CREATE UNIQUE INDEX IF NOT EXISTS idx_provider_error_details_fingerprint
ON provider_error_details (
    provider_id,
    COALESCE(model_name, ''),
    COALESCE(endpoint, ''),
    error_type,
    COALESCE(error_code, ''),
    COALESCE(LEFT(error_message, 200), '')
);

-- 注释
COMMENT ON INDEX idx_provider_error_details_fingerprint IS 
    '错误指纹唯一索引 - 用于 UPSERT 聚合，相同指纹的错误会合并';

-- 验证
DO $$
BEGIN
    ASSERT (SELECT COUNT(*) FROM pg_indexes 
            WHERE indexname = 'idx_provider_error_details_fingerprint') = 1,
        'Unique index not created';
    
    RAISE NOTICE '✅ Migration 616 completed: provider_error_details unique constraint added';
END $$;
