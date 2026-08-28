-- Enable model quality worker
-- This SQL can be used to enable/disable model quality worker without code changes

-- Enable model quality (default after this code change)
INSERT INTO settings_kv (key, value, value_type, scope, category, updated_by)
VALUES ('model_quality.enabled', 'true', 'boolean', 'platform', 'model_quality', 'system')
ON CONFLICT (key) DO UPDATE SET 
    value = 'true',
    updated_at = NOW(),
    prev_value = settings_kv.value,
    prev_updated_at = settings_kv.updated_at;

-- Optional: Configure base URL (default: http://localhost:8787)
INSERT INTO settings_kv (key, value, value_type, scope, category, updated_by)
VALUES ('model_quality.base_url', '"http://localhost:8787"', 'string', 'platform', 'model_quality', 'system')
ON CONFLICT (key) DO UPDATE SET 
    value = '"http://localhost:8787"',
    updated_at = NOW();

-- Optional: Configure data directory (default: ./data)
INSERT INTO settings_kv (key, value, value_type, scope, category, updated_by)
VALUES ('model_quality.data_dir', '"./data"', 'string', 'platform', 'model_quality', 'system')
ON CONFLICT (key) DO UPDATE SET 
    value = '"./data"',
    updated_at = NOW();

-- Optional: Configure test interval in hours (default: 24)
INSERT INTO settings_kv (key, value, value_type, scope, category, updated_by)
VALUES ('model_quality.interval_hours', '24', 'integer', 'platform', 'model_quality', 'system')
ON CONFLICT (key) DO UPDATE SET 
    value = '24',
    updated_at = NOW();

-- Optional: Configure test timeout in seconds (default: 30)
INSERT INTO settings_kv (key, value, value_type, scope, category, updated_by)
VALUES ('model_quality.test_timeout_seconds', '30', 'integer', 'platform', 'model_quality', 'system')
ON CONFLICT (key) DO UPDATE SET 
    value = '30',
    updated_at = NOW();

-- Optional: Enable per-node testing (default: false)
-- Requires database connection and credential decryption
INSERT INTO settings_kv (key, value, value_type, scope, category, updated_by)
VALUES ('model_quality.enable_per_node', 'false', 'boolean', 'platform', 'model_quality', 'system')
ON CONFLICT (key) DO UPDATE SET 
    value = 'false',
    updated_at = NOW();

-- Optional: Use lite benchmark (default: true)
INSERT INTO settings_kv (key, value, value_type, scope, category, updated_by)
VALUES ('model_quality.use_lite_benchmark', 'true', 'boolean', 'platform', 'model_quality', 'system')
ON CONFLICT (key) DO UPDATE SET 
    value = 'true',
    updated_at = NOW();

-- Optional: Alert threshold for quality drop (default: 5.0)
INSERT INTO settings_kv (key, value, value_type, scope, category, updated_by)
VALUES ('model_quality.alert_threshold', '5.0', 'number', 'platform', 'model_quality', 'system')
ON CONFLICT (key) DO UPDATE SET 
    value = '5.0',
    updated_at = NOW();

-- Verify settings
SELECT key, value, value_type, updated_at, updated_by
FROM settings_kv
WHERE key LIKE 'model_quality.%'
ORDER BY key;
