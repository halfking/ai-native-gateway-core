-- Provider Profile System Database Fixes
-- Created: 2026-11-29
-- Purpose: 修正和优化供应商画像系统数据库
-- 依赖: 2026-07-26-provider-profile-system.sql 必须先执行
-- 
-- 本脚本修正原始迁移脚本的问题并添加优化

BEGIN;

-- ============================================================================
-- 修正1: 添加复合索引优化查询性能
-- ============================================================================

-- 优化凭据+供应商+时间的组合查询
CREATE INDEX IF NOT EXISTS idx_ppm_cred_provider_time 
    ON provider_profile_metrics(credential_id, provider_id, metric_time DESC);

-- 优化未解决告警的查询（部分索引）
CREATE INDEX IF NOT EXISTS idx_ppa_active_alerts 
    ON provider_profile_alerts(credential_id, created_at DESC) 
    WHERE resolved_at IS NULL;

COMMENT ON INDEX idx_ppm_cred_provider_time IS '支持按凭据和供应商查询时间序列指标';
COMMENT ON INDEX idx_ppa_active_alerts IS '支持快速查询未解决的告警（部分索引）';

-- ============================================================================
-- 修正2: 添加分数范围约束
-- ============================================================================

-- 确保天级画像分数在合理范围内（0-100）
ALTER TABLE provider_profile_daily 
    DROP CONSTRAINT IF EXISTS chk_network_score_range;
ALTER TABLE provider_profile_daily 
    ADD CONSTRAINT chk_network_score_range 
    CHECK (network_score IS NULL OR (network_score >= 0 AND network_score <= 100));

ALTER TABLE provider_profile_daily 
    DROP CONSTRAINT IF EXISTS chk_credibility_score_range;
ALTER TABLE provider_profile_daily 
    ADD CONSTRAINT chk_credibility_score_range 
    CHECK (credibility_score IS NULL OR (credibility_score >= 0 AND credibility_score <= 100));

ALTER TABLE provider_profile_daily 
    DROP CONSTRAINT IF EXISTS chk_availability_score_range;
ALTER TABLE provider_profile_daily 
    ADD CONSTRAINT chk_availability_score_range 
    CHECK (availability_score IS NULL OR (availability_score >= 0 AND availability_score <= 100));

ALTER TABLE provider_profile_daily 
    DROP CONSTRAINT IF EXISTS chk_stability_score_range;
ALTER TABLE provider_profile_daily 
    ADD CONSTRAINT chk_stability_score_range 
    CHECK (stability_score IS NULL OR (stability_score >= 0 AND stability_score <= 100));

ALTER TABLE provider_profile_daily 
    DROP CONSTRAINT IF EXISTS chk_total_score_range;
ALTER TABLE provider_profile_daily 
    ADD CONSTRAINT chk_total_score_range 
    CHECK (total_score IS NULL OR (total_score >= 0 AND total_score <= 100));

-- 确保可信度测试分数在合理范围内
ALTER TABLE provider_credibility_tests
    DROP CONSTRAINT IF EXISTS chk_authenticity_score_range;
ALTER TABLE provider_credibility_tests
    ADD CONSTRAINT chk_authenticity_score_range
    CHECK (authenticity_score IS NULL OR (authenticity_score >= 0 AND authenticity_score <= 100));

ALTER TABLE provider_credibility_tests
    DROP CONSTRAINT IF EXISTS chk_compliance_score_range;
ALTER TABLE provider_credibility_tests
    ADD CONSTRAINT chk_compliance_score_range
    CHECK (compliance_score IS NULL OR (compliance_score >= 0 AND compliance_score <= 100));

ALTER TABLE provider_credibility_tests
    DROP CONSTRAINT IF EXISTS chk_consistency_score_range;
ALTER TABLE provider_credibility_tests
    ADD CONSTRAINT chk_consistency_score_range
    CHECK (consistency_score IS NULL OR (consistency_score >= 0 AND consistency_score <= 100));

ALTER TABLE provider_credibility_tests
    DROP CONSTRAINT IF EXISTS chk_version_score_range;
ALTER TABLE provider_credibility_tests
    ADD CONSTRAINT chk_version_score_range
    CHECK (version_score IS NULL OR (version_score >= 0 AND version_score <= 100));

-- ============================================================================
-- 修正3: 添加唯一性约束避免重复数据
-- ============================================================================

-- 防止同一凭据在同一时间有多条指标记录
CREATE UNIQUE INDEX IF NOT EXISTS idx_ppm_unique_metric 
    ON provider_profile_metrics(credential_id, metric_time);

-- provider_profile_daily已有UNIQUE约束，无需添加

-- ============================================================================
-- 修正4: 添加枚举值约束
-- ============================================================================

-- 确保告警级别只使用预定义值
ALTER TABLE provider_profile_alerts 
    DROP CONSTRAINT IF EXISTS chk_alert_level;
ALTER TABLE provider_profile_alerts 
    ADD CONSTRAINT chk_alert_level 
    CHECK (alert_level IN ('critical', 'warning', 'info'));

-- 确保告警类型只使用预定义值
ALTER TABLE provider_profile_alerts 
    DROP CONSTRAINT IF EXISTS chk_alert_type;
ALTER TABLE provider_profile_alerts 
    ADD CONSTRAINT chk_alert_type 
    CHECK (alert_type IN ('score_drop', 'trend_drop', 'auto_disabled', 'auto_enabled', 'dimension_low'));

-- 确保测试类型只使用预定义值
ALTER TABLE provider_credibility_tests
    DROP CONSTRAINT IF EXISTS chk_test_type;
ALTER TABLE provider_credibility_tests
    ADD CONSTRAINT chk_test_type
    CHECK (test_type IN ('capability_probe', 'cost_analysis', 'standard_testset', 'fingerprint'));

-- ============================================================================
-- 修正5: 添加触发器自动更新时间戳
-- ============================================================================

-- 创建或替换通用的updated_at更新函数
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ language 'plpgsql';

-- 为provider_cost_reconciliation添加自动更新触发器
DROP TRIGGER IF EXISTS update_provider_cost_reconciliation_updated_at ON provider_cost_reconciliation;
CREATE TRIGGER update_provider_cost_reconciliation_updated_at
    BEFORE UPDATE ON provider_cost_reconciliation
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

COMMENT ON FUNCTION update_updated_at_column() IS '自动更新表的updated_at字段为当前时间';

-- ============================================================================
-- 修正6: 添加更详细的索引注释
-- ============================================================================

COMMENT ON INDEX idx_ppm_credential_time IS '支持按凭据查询最近指标（时间降序）';
COMMENT ON INDEX idx_ppm_provider_time IS '支持按供应商查询最近指标（时间降序）';
COMMENT ON INDEX idx_ppm_cleanup IS '支持按创建时间清理过期数据';

COMMENT ON INDEX idx_ppd_credential_date IS '支持按凭据查询每日画像（日期降序）';
COMMENT ON INDEX idx_ppd_provider_date IS '支持按供应商查询每日画像（日期降序）';
COMMENT ON INDEX idx_ppd_total_score IS '支持按总分排序和筛选';
COMMENT ON INDEX idx_ppd_date IS '支持按日期查询所有画像';

COMMENT ON INDEX idx_pct_credential_time IS '支持按凭据查询可信度测试（时间降序）';
COMMENT ON INDEX idx_pct_model IS '支持按模型名称查询测试记录';
COMMENT ON INDEX idx_pct_provider IS '支持按供应商查询测试记录';

COMMENT ON INDEX idx_ppa_credential IS '支持按凭据查询告警历史';
COMMENT ON INDEX idx_ppa_provider IS '支持按供应商查询告警历史';
COMMENT ON INDEX idx_ppa_type_level IS '支持按类型和级别筛选告警';
COMMENT ON INDEX idx_ppa_unresolved IS '支持快速查询未解决的告警';

-- ============================================================================
-- 验证修正结果
-- ============================================================================

DO $$
DECLARE
    constraint_count INTEGER;
    index_count INTEGER;
BEGIN
    -- 统计约束数量
    SELECT COUNT(*) INTO constraint_count
    FROM information_schema.table_constraints
    WHERE constraint_schema = 'public'
    AND (table_name LIKE 'provider_profile%' 
         OR table_name = 'provider_credibility_tests'
         OR table_name = 'provider_cost_reconciliation')
    AND constraint_type = 'CHECK';
    
    -- 统计索引数量
    SELECT COUNT(*) INTO index_count
    FROM pg_indexes
    WHERE schemaname = 'public'
    AND (indexname LIKE '%provider_profile%'
         OR indexname LIKE '%ppm_%'
         OR indexname LIKE '%ppd_%'
         OR indexname LIKE '%pct_%'
         OR indexname LIKE '%ppa_%'
         OR indexname LIKE '%pcr_%'
         OR indexname LIKE '%ppw_%');
    
    RAISE NOTICE '=================================================================';
    RAISE NOTICE '数据库修正已完成';
    RAISE NOTICE '=================================================================';
    RAISE NOTICE '已添加的改进:';
    RAISE NOTICE '  ✓ 复合索引: 2个（优化查询性能）';
    RAISE NOTICE '  ✓ 分数范围约束: 9个（确保0-100范围）';
    RAISE NOTICE '  ✓ 唯一性约束: 1个（防止重复数据）';
    RAISE NOTICE '  ✓ 枚举值约束: 3个（确保使用预定义值）';
    RAISE NOTICE '  ✓ 触发器: 1个（自动更新时间戳）';
    RAISE NOTICE '';
    RAISE NOTICE '当前统计:';
    RAISE NOTICE '  - CHECK约束数量: %', constraint_count;
    RAISE NOTICE '  - 索引数量: %', index_count;
    RAISE NOTICE '=================================================================';
END $$;

COMMIT;
