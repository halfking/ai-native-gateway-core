-- Provider Profile System Rollback
-- Created: 2026-07-26
-- Purpose: 回滚供应商画像系统数据表
-- Author: AI Assistant
--
-- 警告：此脚本会删除所有供应商画像相关的表和数据，请谨慎执行！

BEGIN;

-- 删除表（按依赖顺序反向删除）
DROP TABLE IF EXISTS provider_profile_whitelist CASCADE;
DROP TABLE IF EXISTS provider_cost_reconciliation CASCADE;
DROP TABLE IF EXISTS provider_profile_alerts CASCADE;
DROP TABLE IF EXISTS provider_credibility_tests CASCADE;
DROP TABLE IF EXISTS provider_profile_daily CASCADE;
DROP TABLE IF EXISTS provider_profile_metrics CASCADE;

-- 移除 credentials 表的扩展字段
ALTER TABLE credentials DROP COLUMN IF EXISTS auto_disabled_at;
ALTER TABLE credentials DROP COLUMN IF EXISTS auto_disabled_reason;
ALTER TABLE credentials DROP COLUMN IF EXISTS auto_enabled_at;
ALTER TABLE credentials DROP COLUMN IF EXISTS auto_enabled_reason;

COMMIT;

-- 回滚完成提示
DO $$
BEGIN
    RAISE NOTICE '=================================================================';
    RAISE NOTICE '供应商画像系统已回滚';
    RAISE NOTICE '=================================================================';
    RAISE NOTICE '已删除的表：';
    RAISE NOTICE '  - provider_profile_metrics';
    RAISE NOTICE '  - provider_profile_daily';
    RAISE NOTICE '  - provider_credibility_tests';
    RAISE NOTICE '  - provider_profile_alerts';
    RAISE NOTICE '  - provider_cost_reconciliation';
    RAISE NOTICE '  - provider_profile_whitelist';
    RAISE NOTICE '';
    RAISE NOTICE '已移除的字段：';
    RAISE NOTICE '  - credentials.auto_disabled_at';
    RAISE NOTICE '  - credentials.auto_disabled_reason';
    RAISE NOTICE '  - credentials.auto_enabled_at';
    RAISE NOTICE '  - credentials.auto_enabled_reason';
    RAISE NOTICE '=================================================================';
END $$;
