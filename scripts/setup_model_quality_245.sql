-- 模型质量监控配置 - 245测试环境
-- 用于在245测试环境中配置模型质量监控

-- ============================================================
-- 245测试环境配置
-- ============================================================
-- 使用方式: psql -h 10.177.48.245 -d llm_gateway -f setup_model_quality_245.sql

-- 启用模型质量监控
INSERT INTO settings_kv (key, value, scope, category, updated_at)
VALUES ('model_quality.enabled', 'true', 'platform', 'model_quality', NOW())
ON CONFLICT (key, scope, tenant_id) 
DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

-- 检测周期：测试环境24小时
INSERT INTO settings_kv (key, value, scope, category, updated_at)
VALUES ('model_quality.interval_hours', '24', 'platform', 'model_quality', NOW())
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
VALUES ('model_quality.data_dir', '/data/llm-gateway/model-quality', 'platform', 'model_quality', NOW())
ON CONFLICT (key, scope, tenant_id) 
DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

-- 245网关地址
INSERT INTO settings_kv (key, value, scope, category, updated_at)
VALUES ('model_quality.base_url', 'http://10.177.48.245:8787', 'platform', 'model_quality', NOW())
ON CONFLICT (key, scope, tenant_id) 
DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

-- API Key：测试环境使用专用key（请替换为实际的key）
-- 注意：这里需要创建一个专用的API key用于质量测试
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

-- 查看是否需要创建专用API key
SELECT 'WARNING: Please create a dedicated API key for model quality testing' AS reminder
WHERE (SELECT value FROM settings_kv WHERE key = 'model_quality.api_key') = '';
