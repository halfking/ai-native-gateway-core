-- V370: Auto模型优化V2 - Phase 1: Tier配置表
-- 目标：创建任务类型到模型档位的映射配置表，并初始化10类编程任务的默认配置
-- 日期：2026-09-02
-- 作者：ZCode AI Assistant

-- 1. 创建配置表
CREATE TABLE IF NOT EXISTS task_type_tier_config (
    id BIGSERIAL PRIMARY KEY,
    task_type TEXT NOT NULL,           -- 任务类型标识（architecture/audit/debugging等）
    preferred_tier TEXT NOT NULL,      -- 首选档位 (tier-a/tier-b/tier-c)
    fallback_tiers TEXT[],            -- 回退档位顺序（当首选档位无可用模型时）
    tenant_id BIGINT,                 -- 租户ID，NULL表示全局默认配置
    enabled BOOLEAN DEFAULT TRUE,      -- 是否启用该配置
    description TEXT,                  -- 配置描述（可选）
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    CONSTRAINT task_type_tier_config_unique UNIQUE(task_type, COALESCE(tenant_id, 0))
);

-- 2. 添加表和字段注释
COMMENT ON TABLE task_type_tier_config IS '任务类型到模型档位的映射配置表，支持全局和租户级别配置';
COMMENT ON COLUMN task_type_tier_config.task_type IS '任务类型：architecture/audit/debugging/coding/refactoring/testing/devops/documentation/summary/dependency';
COMMENT ON COLUMN task_type_tier_config.preferred_tier IS '首选模型档位：tier-a(高性能$15-50/1M) / tier-b(标准$5-15/1M) / tier-c(经济$0.5-5/1M)';
COMMENT ON COLUMN task_type_tier_config.fallback_tiers IS '回退档位数组，按优先级排序。例如：{tier-b, tier-c}';
COMMENT ON COLUMN task_type_tier_config.tenant_id IS '租户ID，NULL表示全局默认。租户级配置优先于全局配置';

-- 3. 创建索引
CREATE INDEX IF NOT EXISTS idx_task_type_tier_config_lookup 
    ON task_type_tier_config(task_type, tenant_id, enabled) 
    WHERE enabled = TRUE;

CREATE INDEX IF NOT EXISTS idx_task_type_tier_config_tenant 
    ON task_type_tier_config(tenant_id, enabled) 
    WHERE enabled = TRUE;

-- 4. 初始化10类编程任务的全局默认配置
INSERT INTO task_type_tier_config (task_type, preferred_tier, fallback_tiers, description) VALUES
    ('architecture', 'tier-a', '{tier-b}', '架构设计：系统设计、技术选型、模块划分 → 使用高性能模型'),
    ('audit', 'tier-a', '{tier-b}', '代码审计：PR审查、安全扫描、合规检查 → 使用高性能模型'),
    ('debugging', 'tier-a', '{tier-b}', 'Bug分析：问题诊断、根因分析、修复方案 → 使用高性能模型'),
    ('coding', 'tier-b', '{tier-a,tier-c}', '功能编码：新功能开发、API实现、业务逻辑 → 使用标准模型'),
    ('refactoring', 'tier-b', '{tier-a,tier-c}', '代码重构：结构优化、技术债清理 → 使用标准模型'),
    ('testing', 'tier-b', '{tier-c}', '测试编写：单元测试、集成测试、测试用例 → 使用标准模型'),
    ('devops', 'tier-c', '{tier-b}', '运维部署：CI/CD、部署脚本、基础设施即代码 → 使用经济模型'),
    ('documentation', 'tier-c', '{}', '文档生成：注释、README、技术文档 → 使用经济模型'),
    ('summary', 'tier-c', '{}', '代码总结：代码解释、变更摘要、会话总结 → 使用经济模型'),
    ('dependency', 'tier-c', '{tier-b}', '依赖管理：依赖升级、版本管理、兼容性检查 → 使用经济模型')
ON CONFLICT (task_type, COALESCE(tenant_id, 0)) DO NOTHING;

-- 5. 更新 provider_models 表增加 tier 字段
ALTER TABLE provider_models 
    ADD COLUMN IF NOT EXISTS tier TEXT;

COMMENT ON COLUMN provider_models.tier IS '模型档位：tier-a(高性能)/tier-b(标准)/tier-c(经济)，根据价格自动计算或手动设置';

-- 6. 根据价格初始化模型的tier字段
UPDATE provider_models 
SET tier = CASE
    WHEN (COALESCE(unit_price_in_per_1m, 0) + COALESCE(unit_price_out_per_1m, 0)) > 15 THEN 'tier-a'
    WHEN (COALESCE(unit_price_in_per_1m, 0) + COALESCE(unit_price_out_per_1m, 0)) > 5 THEN 'tier-b'
    ELSE 'tier-c'
END
WHERE tier IS NULL;

-- 7. 创建provider_models的tier索引
CREATE INDEX IF NOT EXISTS idx_provider_models_tier 
    ON provider_models(tier, available) 
    WHERE available = TRUE;

-- 8. 创建更新时间触发器
CREATE OR REPLACE FUNCTION update_task_type_tier_config_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trigger_task_type_tier_config_updated_at
    BEFORE UPDATE ON task_type_tier_config
    FOR EACH ROW
    EXECUTE FUNCTION update_task_type_tier_config_updated_at();

-- 9. 验证初始化
DO $$
DECLARE
    config_count INT;
    tier_a_count INT;
    tier_b_count INT;
    tier_c_count INT;
BEGIN
    SELECT COUNT(*) INTO config_count FROM task_type_tier_config WHERE tenant_id IS NULL;
    SELECT COUNT(*) INTO tier_a_count FROM provider_models WHERE tier = 'tier-a';
    SELECT COUNT(*) INTO tier_b_count FROM provider_models WHERE tier = 'tier-b';
    SELECT COUNT(*) INTO tier_c_count FROM provider_models WHERE tier = 'tier-c';
    
    RAISE NOTICE '配置表初始化完成:';
    RAISE NOTICE '  任务类型配置数: %', config_count;
    RAISE NOTICE '  Tier-A模型数: %', tier_a_count;
    RAISE NOTICE '  Tier-B模型数: %', tier_b_count;
    RAISE NOTICE '  Tier-C模型数: %', tier_c_count;
    
    IF config_count != 10 THEN
        RAISE WARNING '任务类型配置数不正确，期望10个，实际%个', config_count;
    END IF;
END $$;
