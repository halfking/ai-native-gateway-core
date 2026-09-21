-- V370 回滚脚本: 删除Tier配置表
-- 日期：2026-09-02

-- 1. 删除触发器和函数
DROP TRIGGER IF EXISTS trigger_task_type_tier_config_updated_at ON task_type_tier_config;
DROP FUNCTION IF EXISTS update_task_type_tier_config_updated_at();

-- 2. 删除provider_models的tier索引
DROP INDEX IF EXISTS idx_provider_models_tier;

-- 3. 删除provider_models的tier字段
ALTER TABLE provider_models 
    DROP COLUMN IF EXISTS tier;

-- 4. 删除配置表的索引
DROP INDEX IF EXISTS idx_task_type_tier_config_tenant;
DROP INDEX IF EXISTS idx_task_type_tier_config_lookup;

-- 5. 删除配置表
DROP TABLE IF EXISTS task_type_tier_config;

-- 6. 确认回滚
DO $$
BEGIN
    RAISE NOTICE 'V370迁移已回滚：Tier配置表和相关字段已删除';
END $$;
