-- 模型质量监控配置 - 环境初始化SQL
-- 用于在不同环境（本地、245、154）中配置模型质量监控

-- ============================================================
-- 本地开发环境配置
-- ============================================================
-- 使用方式: psql -d llm_gateway -f setup_model_quality_local.sql

-- 启用模型质量监控
INSERT INTO settings_kv (key, value, scope, category, updated_at)
VALUES ('model_quality.enabled', 'true', 'platform', 'model_quality', NOW())
ON CONFLICT (key, scope, tenant_id) 
DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

-- 检测周期：本地测试可以设置短一些，如6小时
INSERT INTO settings_kv (key, value, scope, category, updated_at)
VALUES ('model_quality.interval_hours', '6', 'platform', 'model_quality', NOW())
ON CONFLICT (key, scope, tenant_id) 
DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

-- 使用快速测试（50题）
INSERT INTO settings_kv (key, value, scope, category, updated_at)
VALUES ('model_quality.use_lite_benchmark', 'true', 'platform', 'model_quality', NOW())
ON CONFLICT (key, scope, tenant_id) 
DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

-- 告警阈值：5%
INSERT INTO settings_kv (key, value, scope, category, updated_at)
VALUES ('model_quality.alert_threshold', '5.0', 'platform', 'model_quality', NOW())
ON CONFLICT (key, scope, tenant_id) 
DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

-- 数据目录
INSERT INTO settings_kv (key, value, scope, category, updated_at)
VALUES ('model_quality.data_dir', './data/model-quality', 'platform', 'model_quality', NOW())
ON CONFLICT (key, scope, tenant_id) 
DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

-- 本地网关地址
INSERT INTO settings_kv (key, value, scope, category, updated_at)
VALUES ('model_quality.base_url', 'http://localhost:8787', 'platform', 'model_quality', NOW())
ON CONFLICT (key, scope, tenant_id) 
DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

-- API Key：本地测试留空，使用系统API key
INSERT INTO settings_kv (key, value, scope, category, updated_at)
VALUES ('model_quality.api_key', '', 'platform', 'model_quality', NOW())
ON CONFLICT (key, scope, tenant_id) 
DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

-- 测试超时时间：30秒
INSERT INTO settings_kv (key, value, scope, category, updated_at)
VALUES ('model_quality.test_timeout_seconds', '30', 'platform', 'model_quality', NOW())
ON CONFLICT (key, scope, tenant_id) 
DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

-- 验证配置
SELECT key, value, scope, category, updated_at 
FROM settings_kv 
WHERE category = 'model_quality'
ORDER BY key;
