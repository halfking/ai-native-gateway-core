-- 模型质量监控配置 - 154生产环境
-- 用于在154生产环境中配置模型质量监控

-- ============================================================
-- 154生产环境配置
-- ============================================================
-- 使用方式: psql -h 10.177.48.154 -d llm_gateway -f setup_model_quality_154.sql

-- 启用模型质量监控
INSERT INTO settings_kv (key, value, scope, category, updated_at)
VALUES ('model_quality.enabled', 'true', 'platform', 'model_quality', NOW())
ON CONFLICT (key, scope, tenant_id) 
DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

-- 检测周期：生产环境24小时
INSERT INTO settings_kv (key, value, scope, category, updated_at)
VALUES ('model_quality.interval_hours', '24', 'platform', 'model_quality', NOW())
ON CONFLICT (key, scope, tenant_id) 
DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

-- 使用快速测试（50题）
INSERT INTO settings_kv (key, value, scope, category, updated_at)
VALUES ('model_quality.use_lite_benchmark', 'true', 'platform', 'model_quality', NOW())
ON CONFLICT (key, scope, tenant_id) 
DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

-- 告警阈值：生产环境更敏感，设置为3%
INSERT INTO settings_kv (key, value, scope, category, updated_at)
VALUES ('model_quality.alert_threshold', '3.0', 'platform', 'model_quality', NOW())
ON CONFLICT (key, scope, tenant_id) 
DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

-- 数据目录
INSERT INTO settings_kv (key, value, scope, category, updated_at)
VALUES ('model_quality.data_dir', '/data/llm-gateway/model-quality', 'platform', 'model_quality', NOW())
ON CONFLICT (key, scope, tenant_id) 
DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

-- 154生产网关地址
INSERT INTO settings_kv (key, value, scope, category, updated_at)
VALUES ('model_quality.base_url', 'http://10.177.48.154:8787', 'platform', 'model_quality', NOW())
ON CONFLICT (key, scope, tenant_id) 
DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

-- API Key：生产环境必须使用专用key（请替换为实际的key）
-- ⚠️ 重要：请创建一个专用的API key用于质量测试，便于单独追踪token消耗
INSERT INTO settings_kv (key, value, scope, category, updated_at)
VALUES ('model_quality.api_key', '', 'platform', 'model_quality', NOW())
ON CONFLICT (key, scope, tenant_id) 
DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

-- 测试超时时间：生产环境适当放宽，45秒
INSERT INTO settings_kv (key, value, scope, category, updated_at)
VALUES ('model_quality.test_timeout_seconds', '45', 'platform', 'model_quality', NOW())
ON CONFLICT (key, scope, tenant_id) 
DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

-- 验证配置
SELECT key, value, scope, category, updated_at 
FROM settings_kv 
WHERE category = 'model_quality'
ORDER BY key;

-- ⚠️ 生产环境检查清单
SELECT 
    CASE 
        WHEN (SELECT value FROM settings_kv WHERE key = 'model_quality.api_key') = '' 
        THEN '❌ ERROR: 生产环境必须配置专用API key'
        ELSE '✓ API key已配置'
    END AS api_key_check,
    CASE 
        WHEN (SELECT value FROM settings_kv WHERE key = 'model_quality.enabled') = 'true' 
        THEN '✓ 质量监控已启用'
        ELSE '⚠️ 质量监控未启用'
    END AS enabled_check,
    CASE 
        WHEN (SELECT value FROM settings_kv WHERE key = 'model_quality.base_url') LIKE '%154%' 
        THEN '✓ base_url配置正确'
        ELSE '❌ ERROR: base_url应该指向154地址'
    END AS url_check;
