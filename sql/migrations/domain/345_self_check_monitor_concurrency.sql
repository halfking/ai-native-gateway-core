-- ============================================================================
-- Migration 345: self_check_settings 加 monitor_concurrency 列
-- Purpose:  系统监测模块的并发上限配置（默认 5，最大 32）
-- Object Type:  ALTER TABLE
-- Rollback: sql/migrations/domain/345_self_check_monitor_concurrency.down.sql
-- ============================================================================
--
-- 设计依据: docs/会话优化v2/32-系统监测模块设计.md §3.3 / §6.2
--           rule 38 §5.1 (幂等迁移)
--
-- Verification:
--   SELECT monitor_concurrency FROM self_check_settings WHERE id=1;
-- ===========================================================================

\set ON_ERROR_STOP on

BEGIN;

ALTER TABLE self_check_settings
    ADD COLUMN IF NOT EXISTS monitor_concurrency INT NOT NULL DEFAULT 5
        CHECK (monitor_concurrency BETWEEN 1 AND 32);

COMMENT ON COLUMN self_check_settings.monitor_concurrency IS
    '345: 系统监测模块全局并发上限（mandatory + automatic 任务总和），1-32，默认 5。';

COMMIT;