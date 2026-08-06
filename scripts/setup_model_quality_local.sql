-- 模型质量监控配置 - 本地开发环境
-- 使用方式: psql -d llm_gateway -f scripts/setup_model_quality_local.sql
--
-- settings_kv.value 是 jsonb，value_type 用于记录设置类型。

INSERT INTO settings_kv (key, value, value_type, scope, category, updated_at)
VALUES ('model_quality.enabled', 'true', 'bool', 'platform', 'model_quality', NOW())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, value_type = EXCLUDED.value_type, scope = EXCLUDED.scope, category = EXCLUDED.category, updated_at = NOW();

INSERT INTO settings_kv (key, value, value_type, scope, category, updated_at)
VALUES ('model_quality.interval_hours', '6', 'int', 'platform', 'model_quality', NOW())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, value_type = EXCLUDED.value_type, scope = EXCLUDED.scope, category = EXCLUDED.category, updated_at = NOW();

INSERT INTO settings_kv (key, value, value_type, scope, category, updated_at)
VALUES ('model_quality.use_lite_benchmark', 'true', 'bool', 'platform', 'model_quality', NOW())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, value_type = EXCLUDED.value_type, scope = EXCLUDED.scope, category = EXCLUDED.category, updated_at = NOW();

INSERT INTO settings_kv (key, value, value_type, scope, category, updated_at)
VALUES ('model_quality.alert_threshold', '5.0', 'float', 'platform', 'model_quality', NOW())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, value_type = EXCLUDED.value_type, scope = EXCLUDED.scope, category = EXCLUDED.category, updated_at = NOW();

INSERT INTO settings_kv (key, value, value_type, scope, category, updated_at)
VALUES ('model_quality.data_dir', '"./data"', 'string', 'platform', 'model_quality', NOW())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, value_type = EXCLUDED.value_type, scope = EXCLUDED.scope, category = EXCLUDED.category, updated_at = NOW();

INSERT INTO settings_kv (key, value, value_type, scope, category, updated_at)
VALUES ('model_quality.base_url', '"http://localhost:8787"', 'string', 'platform', 'model_quality', NOW())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, value_type = EXCLUDED.value_type, scope = EXCLUDED.scope, category = EXCLUDED.category, updated_at = NOW();

INSERT INTO settings_kv (key, value, value_type, scope, category, updated_at)
VALUES ('model_quality.api_key', '""', 'string', 'platform', 'model_quality', NOW())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, value_type = EXCLUDED.value_type, scope = EXCLUDED.scope, category = EXCLUDED.category, updated_at = NOW();

INSERT INTO settings_kv (key, value, value_type, scope, category, updated_at)
VALUES ('model_quality.test_timeout_seconds', '30', 'int', 'platform', 'model_quality', NOW())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, value_type = EXCLUDED.value_type, scope = EXCLUDED.scope, category = EXCLUDED.category, updated_at = NOW();

SELECT key, value, value_type, scope, category, updated_at
FROM settings_kv
WHERE category = 'model_quality'
ORDER BY key;
