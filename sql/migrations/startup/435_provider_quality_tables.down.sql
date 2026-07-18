-- ===========================================================================
-- File:          sql/migrations/startup/435_provider_quality_tables.down.sql
-- Database:      llm_gateway
-- Purpose:       回滚供应商质量画像系统的所有表和视图
--
-- Related:       435_provider_quality_tables.sql
-- Status:        active
-- Idempotent:    YES (使用 IF EXISTS)
--
-- Changelog:
--   2026-07-19  v1.0  初始创建
-- ===========================================================================
--
-- 回滚顺序（与创建相反）：
--   1. 删除视图（依赖表）
--   2. 删除表（按依赖关系倒序）
--
-- 执行后验证:
--   SELECT COUNT(*) FROM information_schema.tables 
--   WHERE table_name LIKE 'provider_%quality%' OR table_name LIKE 'provider_%metrics%';
--   -- 应返回 0
-- ===========================================================================

\set ON_ERROR_STOP on

-- ============================================================================
-- 删除视图
-- ============================================================================

DROP VIEW IF EXISTS provider_error_distribution CASCADE;
DROP VIEW IF EXISTS provider_health_status CASCADE;

-- ============================================================================
-- 删除表（按依赖关系倒序）
-- ============================================================================

-- 配置表（被 provider_quality_profiles 引用）
DROP TABLE IF EXISTS provider_quality_configs CASCADE;

-- 事件表
DROP TABLE IF EXISTS provider_health_events CASCADE;

-- 错误表
DROP TABLE IF EXISTS provider_error_details CASCADE;

-- 聚合表
DROP TABLE IF EXISTS provider_metrics_hour CASCADE;
DROP TABLE IF EXISTS provider_metrics_minute CASCADE;

-- 主表
DROP TABLE IF EXISTS provider_quality_profiles CASCADE;

-- ============================================================================
-- 验证
-- ============================================================================

DO $$
DECLARE
    remaining_count INT;
BEGIN
    -- 验证所有表都已删除
    SELECT COUNT(*) INTO remaining_count
    FROM information_schema.tables 
    WHERE table_schema = 'public'
      AND table_name IN (
          'provider_quality_profiles',
          'provider_metrics_minute',
          'provider_metrics_hour',
          'provider_error_details',
          'provider_health_events',
          'provider_quality_configs'
      );

    IF remaining_count > 0 THEN
        RAISE EXCEPTION 'Rollback incomplete: % tables still exist', remaining_count;
    END IF;

    -- 验证所有视图都已删除
    SELECT COUNT(*) INTO remaining_count
    FROM information_schema.views
    WHERE table_schema = 'public'
      AND table_name IN (
          'provider_health_status',
          'provider_error_distribution'
      );

    IF remaining_count > 0 THEN
        RAISE EXCEPTION 'Rollback incomplete: % views still exist', remaining_count;
    END IF;

    RAISE NOTICE '✅ Rollback 435 completed: All tables and views dropped';
END $$;
